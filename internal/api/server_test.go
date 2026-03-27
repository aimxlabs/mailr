package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/garett/mailr/internal/db"
	"github.com/garett/mailr/internal/store"
)

func testServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	os.Unsetenv("MAILR_ADMIN_TOKEN")
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	st, err := store.NewStore(database)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(st)
	return srv, st
}

func doJSON(t *testing.T, srv *Server, method, path string, body interface{}, token string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	return result
}

// --- Health ---

func TestHealth(t *testing.T) {
	srv, _ := testServer(t)
	w := doJSON(t, srv, "GET", "/health", nil, "")
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// --- Domain CRUD ---

func TestCreateDomain(t *testing.T) {
	srv, _ := testServer(t)
	w := doJSON(t, srv, "POST", "/api/domains", map[string]string{"name": "test.com"}, "")
	if w.Code != 201 {
		t.Errorf("status = %d, want 201", w.Code)
	}
	body := decodeBody(t, w)
	if body["name"] != "test.com" {
		t.Errorf("name = %v", body["name"])
	}
	if body["id"] == nil || body["auth_token"] == nil {
		t.Error("expected id and auth_token in response")
	}
}

func TestCreateDomainMissingName(t *testing.T) {
	srv, _ := testServer(t)
	w := doJSON(t, srv, "POST", "/api/domains", map[string]string{}, "")
	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestListDomains(t *testing.T) {
	srv, st := testServer(t)
	st.CreateDomain("a.com")
	st.CreateDomain("b.com")

	w := doJSON(t, srv, "GET", "/api/domains", nil, "")
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var domains []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&domains)
	if len(domains) != 2 {
		t.Errorf("len = %d, want 2", len(domains))
	}
}

func TestGetDomain(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("get.com")

	w := doJSON(t, srv, "GET", "/api/domains/"+dom.ID, nil, "")
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestGetDomainNotFound(t *testing.T) {
	srv, _ := testServer(t)
	w := doJSON(t, srv, "GET", "/api/domains/dom_nope", nil, "")
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDeleteDomain(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("delete.com")

	w := doJSON(t, srv, "DELETE", "/api/domains/"+dom.ID, nil, "")
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}

	got, _ := st.GetDomain(dom.ID)
	if got != nil {
		t.Error("domain should be deleted")
	}
}

// --- Admin Auth ---

func TestAdminTokenRequired(t *testing.T) {
	srv, _ := testServer(t)

	os.Setenv("MAILR_ADMIN_TOKEN", "secret-admin-token")
	defer os.Unsetenv("MAILR_ADMIN_TOKEN")

	// No token
	w := doJSON(t, srv, "GET", "/api/domains", nil, "")
	if w.Code != 401 {
		t.Errorf("no token: status = %d, want 401", w.Code)
	}

	// Wrong token
	w = doJSON(t, srv, "GET", "/api/domains", nil, "wrong")
	if w.Code != 401 {
		t.Errorf("wrong token: status = %d, want 401", w.Code)
	}

	// Correct token
	w = doJSON(t, srv, "GET", "/api/domains", nil, "secret-admin-token")
	if w.Code != 200 {
		t.Errorf("correct token: status = %d, want 200", w.Code)
	}
}

// --- Domain Token Auth ---

func TestDomainTokenRequired(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("auth.test")

	// No token → 401
	w := doJSON(t, srv, "GET", "/api/domains/"+dom.ID+"/messages/poll", nil, "")
	if w.Code != 401 {
		t.Errorf("no token: status = %d, want 401", w.Code)
	}

	// Wrong token → 401
	w = doJSON(t, srv, "GET", "/api/domains/"+dom.ID+"/messages/poll", nil, "wrong-token")
	if w.Code != 401 {
		t.Errorf("wrong token: status = %d, want 401", w.Code)
	}

	// Correct token → 200
	w = doJSON(t, srv, "GET", "/api/domains/"+dom.ID+"/messages/poll", nil, dom.AuthToken)
	if w.Code != 200 {
		t.Errorf("correct token: status = %d, want 200", w.Code)
	}
}

func TestDomainTokenNotFound(t *testing.T) {
	srv, _ := testServer(t)
	w := doJSON(t, srv, "GET", "/api/domains/dom_nope/messages/poll", nil, "any")
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- Send ---

func TestSend(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("send.test")

	w := doJSON(t, srv, "POST", "/api/domains/"+dom.ID+"/send", map[string]interface{}{
		"from":      "agent@send.test",
		"to":        []string{"bob@gmail.com"},
		"subject":   "Hello",
		"body_text": "Hi Bob",
	}, dom.AuthToken)

	if w.Code != 201 {
		t.Errorf("status = %d, want 201", w.Code)
	}

	body := decodeBody(t, w)
	if body["direction"] != "outbound" {
		t.Errorf("direction = %v", body["direction"])
	}
	if body["status"] != "queued" {
		t.Errorf("status = %v", body["status"])
	}
}

func TestSendMissingFields(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("sendval.test")

	w := doJSON(t, srv, "POST", "/api/domains/"+dom.ID+"/send", map[string]interface{}{
		"from": "agent@sendval.test",
	}, dom.AuthToken)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- Poll + Ack ---

func TestPollAndAck(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("poll.test")

	st.StoreInbound(&store.Message{DomainID: dom.ID, From: "a@b.com", To: []string{"x@poll.test"}, Subject: "Msg 1", BodyText: "1"})
	st.StoreInbound(&store.Message{DomainID: dom.ID, From: "a@b.com", To: []string{"x@poll.test"}, Subject: "Msg 2", BodyText: "2"})

	// Poll
	w := doJSON(t, srv, "GET", "/api/domains/"+dom.ID+"/messages/poll", nil, dom.AuthToken)
	if w.Code != 200 {
		t.Fatalf("poll status = %d", w.Code)
	}
	body := decodeBody(t, w)
	msgs := body["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Errorf("poll returned %d messages, want 2", len(msgs))
	}

	// Ack first message
	msg1 := msgs[0].(map[string]interface{})
	w = doJSON(t, srv, "POST", "/api/domains/"+dom.ID+"/messages/ack", map[string]interface{}{
		"message_ids": []string{msg1["id"].(string)},
	}, dom.AuthToken)
	if w.Code != 200 {
		t.Fatalf("ack status = %d", w.Code)
	}

	ackBody := decodeBody(t, w)
	if ackBody["acknowledged"] != float64(1) {
		t.Errorf("acknowledged = %v, want 1", ackBody["acknowledged"])
	}

	// Poll again — only 1 message left
	w = doJSON(t, srv, "GET", "/api/domains/"+dom.ID+"/messages/poll", nil, dom.AuthToken)
	body = decodeBody(t, w)
	msgs = body["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Errorf("poll after ack returned %d messages, want 1", len(msgs))
	}
}

func TestAckMissingIDs(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("ackval.test")

	w := doJSON(t, srv, "POST", "/api/domains/"+dom.ID+"/messages/ack", map[string]interface{}{}, dom.AuthToken)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- Messages List + Get ---

func TestMessageList(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("list.test")

	st.StoreInbound(&store.Message{DomainID: dom.ID, From: "a@b.com", To: []string{"x@list.test"}, Subject: "In", BodyText: "1"})
	st.StoreOutbound(&store.Message{DomainID: dom.ID, From: "x@list.test", To: []string{"a@b.com"}, Subject: "Out", BodyText: "2"})

	// All
	w := doJSON(t, srv, "GET", "/api/domains/"+dom.ID+"/messages", nil, dom.AuthToken)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var msgs []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&msgs)
	if len(msgs) != 2 {
		t.Errorf("len = %d, want 2", len(msgs))
	}

	// Filter by direction
	req := httptest.NewRequest("GET", "/api/domains/"+dom.ID+"/messages?direction=inbound", nil)
	req.Header.Set("Authorization", "Bearer "+dom.AuthToken)
	w = httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)
	json.NewDecoder(w.Body).Decode(&msgs)
	if len(msgs) != 1 {
		t.Errorf("inbound = %d, want 1", len(msgs))
	}
}

func TestMessageGet(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("getmsg.test")

	msg, _ := st.StoreInbound(&store.Message{DomainID: dom.ID, From: "a@b.com", To: []string{"x@getmsg.test"}, Subject: "Find me", BodyText: "here"})

	w := doJSON(t, srv, "GET", "/api/domains/"+dom.ID+"/messages/"+msg.ID, nil, dom.AuthToken)
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	body := decodeBody(t, w)
	if body["subject"] != "Find me" {
		t.Errorf("subject = %v", body["subject"])
	}
}

func TestMessageGetNotFound(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("getmsg404.test")

	w := doJSON(t, srv, "GET", "/api/domains/"+dom.ID+"/messages/msg_nope", nil, dom.AuthToken)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- Addresses ---

func TestAddressCreate(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("addr.test")

	w := doJSON(t, srv, "POST", "/api/domains/"+dom.ID+"/addresses", map[string]interface{}{
		"local_part": "agent",
		"label":      "My Agent",
	}, dom.AuthToken)

	if w.Code != 201 {
		t.Errorf("status = %d, want 201", w.Code)
	}
	body := decodeBody(t, w)
	if body["local_part"] != "agent" {
		t.Errorf("local_part = %v", body["local_part"])
	}
	if body["address"] != "agent@addr.test" {
		t.Errorf("address = %v", body["address"])
	}
}

func TestAddressCreateMissingLocalPart(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("addrval.test")

	w := doJSON(t, srv, "POST", "/api/domains/"+dom.ID+"/addresses", map[string]interface{}{}, dom.AuthToken)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestAddressList(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("addrlist.test")
	st.CreateAddress(dom.ID, "alice", "")
	st.CreateAddress(dom.ID, "bob", "")

	w := doJSON(t, srv, "GET", "/api/domains/"+dom.ID+"/addresses", nil, dom.AuthToken)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var addrs []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&addrs)
	if len(addrs) != 2 {
		t.Errorf("len = %d, want 2", len(addrs))
	}
}

func TestAddressDelete(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("addrdel.test")
	addr, _ := st.CreateAddress(dom.ID, "temp", "")

	w := doJSON(t, srv, "DELETE", "/api/domains/"+dom.ID+"/addresses/"+addr.ID, nil, dom.AuthToken)
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}

	got, _ := st.GetAddress(addr.ID)
	if got != nil {
		t.Error("expected address to be deleted")
	}
}

func TestPollByAddress(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("polladdr.test")
	addr, _ := st.CreateAddress(dom.ID, "agent", "")

	st.StoreInbound(&store.Message{DomainID: dom.ID, AddressID: addr.ID, From: "x@y.com", To: []string{"agent@polladdr.test"}, Subject: "For agent", BodyText: "1"})
	st.StoreInbound(&store.Message{DomainID: dom.ID, From: "x@y.com", To: []string{"other@polladdr.test"}, Subject: "For other", BodyText: "2"})

	// Poll with address filter
	req := httptest.NewRequest("GET", "/api/domains/"+dom.ID+"/messages/poll?address_id="+addr.ID, nil)
	req.Header.Set("Authorization", "Bearer "+dom.AuthToken)
	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	body := decodeBody(t, w)
	msgs := body["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Errorf("filtered poll = %d, want 1", len(msgs))
	}
}

// --- DKIM Generate ---

func TestDKIMGenerate(t *testing.T) {
	srv, st := testServer(t)
	dom, _ := st.CreateDomain("dkim.test")

	w := doJSON(t, srv, "POST", "/api/domains/"+dom.ID+"/dkim/generate", nil, "")
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}

	body := decodeBody(t, w)
	if body["selector"] != "default" {
		t.Errorf("selector = %v", body["selector"])
	}
	if body["dns_record"] == nil || body["dns_value"] == nil {
		t.Error("expected dns_record and dns_value")
	}

	// Verify key was stored
	got, _ := st.GetDomain(dom.ID)
	if got.DKIMPrivateKey == "" {
		t.Error("DKIM key should be stored")
	}
}

func TestDKIMGenerateNotFound(t *testing.T) {
	srv, _ := testServer(t)
	w := doJSON(t, srv, "POST", "/api/domains/dom_nope/dkim/generate", nil, "")
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
