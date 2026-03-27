package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/garett/mailr/internal/relay"
	"github.com/garett/mailr/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

type Server struct {
	Router   *chi.Mux
	store    *store.Store
	upgrader websocket.Upgrader
	subs     map[string]map[*websocket.Conn]bool // domainID → connections
	mu       sync.RWMutex
}

func NewServer(s *store.Store) *Server {
	srv := &Server{
		Router: chi.NewRouter(),
		store:  s,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		subs: make(map[string]map[*websocket.Conn]bool),
	}

	// Register the inbound callback for WS push
	s.OnInbound = srv.pushInbound

	srv.Router.Route("/api", func(r chi.Router) {
		// Admin endpoints
		r.Post("/domains", srv.requireAdmin(srv.handleDomainCreate))
		r.Get("/domains", srv.requireAdmin(srv.handleDomainList))
		r.Get("/domains/{id}", srv.requireAdmin(srv.handleDomainGet))
		r.Delete("/domains/{id}", srv.requireAdmin(srv.handleDomainDelete))
		r.Post("/domains/{id}/dkim/generate", srv.requireAdmin(srv.handleDKIMGenerate))

		// Client endpoints (domain token auth)
		r.Get("/domains/{id}/messages/poll", srv.requireDomainToken(srv.handlePoll))
		r.Post("/domains/{id}/messages/ack", srv.requireDomainToken(srv.handleAck))
		r.Post("/domains/{id}/send", srv.requireDomainToken(srv.handleSend))
		r.Get("/domains/{id}/messages", srv.requireDomainToken(srv.handleMessageList))
		r.Get("/domains/{id}/messages/{mid}", srv.requireDomainToken(srv.handleMessageGet))
	})

	srv.Router.Get("/ws", srv.handleWS)
	srv.Router.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})

	return srv
}

// --- Auth Middleware ---

func extractToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && h[:7] == "Bearer " {
		return h[7:]
	}
	return ""
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminToken := os.Getenv("MAILR_ADMIN_TOKEN")
		if adminToken == "" {
			next(w, r) // no admin token configured, allow (local dev)
			return
		}
		provided := extractToken(r)
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(adminToken)) != 1 {
			writeError(w, 401, "Unauthorized — admin token required")
			return
		}
		next(w, r)
	}
}

func (s *Server) requireDomainToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		domainID := chi.URLParam(r, "id")
		dom, err := s.store.GetDomain(domainID)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if dom == nil {
			writeError(w, 404, "Domain not found")
			return
		}
		provided := extractToken(r)
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(dom.AuthToken)) != 1 {
			writeError(w, 401, "Unauthorized")
			return
		}
		next(w, r)
	}
}

// --- Domain Handlers ---

func (s *Server) handleDomainCreate(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string `json:"name"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "Invalid JSON")
		return
	}
	if req.Name == "" {
		writeError(w, 400, "name is required")
		return
	}
	dom, err := s.store.CreateDomain(req.Name)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, dom)
}

func (s *Server) handleDomainList(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.ListDomains()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) handleDomainGet(w http.ResponseWriter, r *http.Request) {
	dom, err := s.store.GetDomain(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if dom == nil {
		writeError(w, 404, "Domain not found")
		return
	}
	writeJSON(w, 200, dom)
}

func (s *Server) handleDomainDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteDomain(chi.URLParam(r, "id")); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) handleDKIMGenerate(w http.ResponseWriter, r *http.Request) {
	domainID := chi.URLParam(r, "id")
	dom, err := s.store.GetDomain(domainID)
	if err != nil || dom == nil {
		writeError(w, 404, "Domain not found")
		return
	}

	privatePEM, dnsValue, err := relay.GenerateDKIMKey()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	if err := s.store.SetDKIM(domainID, privatePEM, "default"); err != nil {
		writeError(w, 500, err.Error())
		return
	}

	writeJSON(w, 200, map[string]string{
		"selector":   "default",
		"dns_record": "default._domainkey." + dom.Name,
		"dns_value":  dnsValue,
	})
}

// --- Message Handlers ---

func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs, err := s.store.PollInbound(chi.URLParam(r, "id"), limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"messages": msgs,
		"count":    len(msgs),
	})
}

func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	var req struct{ MessageIDs []string `json:"message_ids"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "Invalid JSON")
		return
	}
	if len(req.MessageIDs) == 0 {
		writeError(w, 400, "message_ids array is required")
		return
	}
	count, err := s.store.AckMessages(chi.URLParam(r, "id"), req.MessageIDs)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]int{"acknowledged": count})
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From     string   `json:"from"`
		To       []string `json:"to"`
		Cc       []string `json:"cc"`
		Subject  string   `json:"subject"`
		BodyText string   `json:"body_text"`
		BodyHTML string   `json:"body_html"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "Invalid JSON")
		return
	}
	if req.From == "" || len(req.To) == 0 || req.Subject == "" {
		writeError(w, 400, "from, to, and subject are required")
		return
	}

	msg := &store.Message{
		DomainID: chi.URLParam(r, "id"),
		From:     req.From,
		To:       req.To,
		Cc:       req.Cc,
		Subject:  req.Subject,
		BodyText: req.BodyText,
		BodyHTML: req.BodyHTML,
	}
	result, err := s.store.StoreOutbound(msg)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, result)
}

func (s *Server) handleMessageList(w http.ResponseWriter, r *http.Request) {
	opts := store.MessageListOpts{
		DomainID:  chi.URLParam(r, "id"),
		Direction: r.URL.Query().Get("direction"),
		Status:    r.URL.Query().Get("status"),
	}
	if v := r.URL.Query().Get("limit"); v != "" { opts.Limit, _ = strconv.Atoi(v) }
	result, err := s.store.ListMessages(opts)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) handleMessageGet(w http.ResponseWriter, r *http.Request) {
	msg, err := s.store.GetMessage(chi.URLParam(r, "mid"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if msg == nil {
		writeError(w, 404, "Message not found")
		return
	}
	writeJSON(w, 200, msg)
}

// --- WebSocket ---

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil { return }
	defer conn.Close()

	// Auth
	var authMsg struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if err := conn.ReadJSON(&authMsg); err != nil { return }
	if authMsg.Type != "auth" {
		conn.WriteJSON(map[string]string{"type": "error", "message": "expected auth message"})
		return
	}

	// Validate token against any domain
	domains, _ := s.store.ListDomains()
	validToken := false
	for _, d := range domains {
		full, _ := s.store.GetDomain(d.ID)
		if full != nil && subtle.ConstantTimeCompare([]byte(authMsg.Token), []byte(full.AuthToken)) == 1 {
			validToken = true
			break
		}
	}
	if !validToken {
		conn.WriteJSON(map[string]string{"type": "auth_error", "message": "invalid token"})
		return
	}
	conn.WriteJSON(map[string]string{"type": "auth_ok"})

	// Subscribe
	var subMsg struct {
		Type     string `json:"type"`
		DomainID string `json:"domainId"`
	}
	if err := conn.ReadJSON(&subMsg); err != nil { return }
	if subMsg.Type != "subscribe" || subMsg.DomainID == "" {
		conn.WriteJSON(map[string]string{"type": "error", "message": "expected subscribe message"})
		return
	}

	s.mu.Lock()
	if s.subs[subMsg.DomainID] == nil {
		s.subs[subMsg.DomainID] = make(map[*websocket.Conn]bool)
	}
	s.subs[subMsg.DomainID][conn] = true
	s.mu.Unlock()

	conn.WriteJSON(map[string]string{"type": "subscribed", "domainId": subMsg.DomainID})

	defer func() {
		s.mu.Lock()
		delete(s.subs[subMsg.DomainID], conn)
		s.mu.Unlock()
	}()

	// Read loop (acks + pings)
	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil { return }
		msgType, _ := msg["type"].(string)
		switch msgType {
		case "ack":
			if id, ok := msg["messageId"].(string); ok {
				s.store.AckMessages(subMsg.DomainID, []string{id})
			}
		case "ping":
			conn.WriteJSON(map[string]string{"type": "pong"})
		}
	}
}

func (s *Server) pushInbound(msg *store.Message) {
	s.mu.RLock()
	conns := s.subs[msg.DomainID]
	s.mu.RUnlock()

	payload := map[string]interface{}{
		"type":      "message",
		"messageId": msg.ID,
		"from":      msg.From,
		"to":        msg.To,
		"subject":   msg.Subject,
		"body_text": msg.BodyText,
		"body_html": msg.BodyHTML,
	}

	for conn := range conns {
		conn.WriteJSON(payload)
	}
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
