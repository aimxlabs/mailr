package smtp

import (
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/garett/mailr/internal/store"
)

type Server struct {
	server *smtp.Server
	store  *store.Store
	domain string
}

func NewServer(s *store.Store, domain, addr string) *Server {
	srv := &Server{store: s, domain: domain}

	smtpSrv := smtp.NewServer(srv)
	smtpSrv.Addr = addr
	smtpSrv.Domain = domain
	smtpSrv.MaxMessageBytes = 25 * 1024 * 1024
	smtpSrv.MaxRecipients = 50
	smtpSrv.ReadTimeout = 30 * time.Second
	smtpSrv.WriteTimeout = 30 * time.Second
	smtpSrv.AllowInsecureAuth = true

	srv.server = smtpSrv
	return srv
}

func (s *Server) ListenAndServe() error { return s.server.ListenAndServe() }
func (s *Server) Close() error          { return s.server.Close() }

// smtp.Backend
func (s *Server) NewSession(_ *smtp.Conn) (smtp.Session, error) {
	return &session{store: s.store, domain: s.domain}, nil
}

type session struct {
	store  *store.Store
	domain string
	from   string
	to     []string
}

func (s *session) AuthPlain(username, password string) error { return nil }

func (s *session) Mail(from string, opts *smtp.MailOptions) error {
	s.from = from
	return nil
}

func (s *session) Rcpt(to string, opts *smtp.RcptOptions) error {
	addr, dom, err := s.store.ValidateAddress(to)
	if err != nil {
		return fmt.Errorf("lookup failed: %s", to)
	}
	if dom == nil {
		return fmt.Errorf("domain not handled: %s", to)
	}

	// If domain has registered addresses, require a match
	hasAddrs, _ := s.store.HasAddresses(dom.ID)
	if hasAddrs && addr == nil {
		return fmt.Errorf("address not registered: %s", to)
	}

	s.to = append(s.to, to)
	return nil
}

func (s *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	parsed, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		log.Printf("smtp: failed to parse message: %v", err)
		return s.storeRaw(raw)
	}

	subject := decodeHeader(parsed.Header.Get("Subject"))
	from := s.from
	if parsed.Header.Get("From") != "" {
		from = parsed.Header.Get("From")
	}

	var cc []string
	if parsed.Header.Get("Cc") != "" {
		cc = parseAddressList(parsed.Header.Get("Cc"))
	}

	bodyText, bodyHTML := extractBodies(parsed)

	// Store per recipient, resolving address
	seen := map[string]bool{} // dedupe by domain
	for _, rcpt := range s.to {
		addr, dom, _ := s.store.ValidateAddress(rcpt)
		if dom == nil { continue }
		if seen[dom.ID] { continue }
		seen[dom.ID] = true

		msg := &store.Message{
			DomainID:  dom.ID,
			MessageID: parsed.Header.Get("Message-Id"),
			From:      from,
			To:        s.to,
			Cc:        cc,
			Subject:   subject,
			BodyText:  bodyText,
			BodyHTML:  bodyHTML,
			RawData:   string(raw),
		}
		if addr != nil {
			msg.AddressID = addr.ID
		}
		if _, err := s.store.StoreInbound(msg); err != nil {
			log.Printf("smtp: failed to store message for %s: %v", rcpt, err)
		}
	}

	return nil
}

func (s *session) storeRaw(raw []byte) error {
	if len(s.to) == 0 { return nil }
	parts := strings.SplitN(s.to[0], "@", 2)
	dom, _ := s.store.GetDomainByName(parts[1])
	if dom == nil { return nil }

	msg := &store.Message{
		DomainID: dom.ID,
		From:     s.from,
		To:       s.to,
		Subject:  "(unparseable)",
		BodyText: string(raw),
		RawData:  string(raw),
	}
	_, err := s.store.StoreInbound(msg)
	return err
}

func (s *session) Reset() {
	s.from = ""
	s.to = nil
}

func (s *session) Logout() error { return nil }

// --- Helpers ---

func decodeHeader(s string) string {
	dec := new(mime.WordDecoder)
	decoded, err := dec.DecodeHeader(s)
	if err != nil { return s }
	return decoded
}

func parseAddressList(raw string) []string {
	addrs, err := mail.ParseAddressList(raw)
	if err != nil { return []string{raw} }
	var result []string
	for _, a := range addrs {
		result = append(result, a.Address)
	}
	return result
}

func extractBodies(msg *mail.Message) (text, html string) {
	ct := msg.Header.Get("Content-Type")
	if ct == "" { ct = "text/plain" }

	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		body, _ := io.ReadAll(msg.Body)
		return string(body), ""
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		reader := multipart.NewReader(msg.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err != nil { break }
			partCT := part.Header.Get("Content-Type")
			partBody, _ := io.ReadAll(part)
			if strings.HasPrefix(partCT, "text/plain") {
				text = string(partBody)
			} else if strings.HasPrefix(partCT, "text/html") {
				html = string(partBody)
			}
		}
		return text, html
	}

	body, _ := io.ReadAll(msg.Body)
	if strings.HasPrefix(mediaType, "text/html") {
		return "", string(body)
	}
	return string(body), ""
}
