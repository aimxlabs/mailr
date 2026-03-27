package store

import (
	"database/sql"
	"testing"

	"github.com/garett/mailr/internal/db"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(testDB(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// --- Domains ---

func TestCreateDomain(t *testing.T) {
	s := testStore(t)

	dom, err := s.CreateDomain("example.com")
	if err != nil {
		t.Fatal(err)
	}
	if dom.Name != "example.com" {
		t.Errorf("name = %q, want %q", dom.Name, "example.com")
	}
	if dom.ID == "" || dom.AuthToken == "" || dom.CreatedAt == "" {
		t.Error("expected ID, AuthToken, and CreatedAt to be set")
	}
}

func TestCreateDomainDuplicate(t *testing.T) {
	s := testStore(t)

	s.CreateDomain("example.com")
	_, err := s.CreateDomain("example.com")
	if err == nil {
		t.Error("expected error for duplicate domain")
	}
}

func TestGetDomain(t *testing.T) {
	s := testStore(t)

	created, _ := s.CreateDomain("example.com")
	got, err := s.GetDomain(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "example.com" {
		t.Errorf("name = %q, want %q", got.Name, "example.com")
	}
}

func TestGetDomainNotFound(t *testing.T) {
	s := testStore(t)

	got, err := s.GetDomain("dom_nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for missing domain")
	}
}

func TestGetDomainByName(t *testing.T) {
	s := testStore(t)

	s.CreateDomain("test.org")
	got, err := s.GetDomainByName("test.org")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "test.org" {
		t.Error("expected to find domain by name")
	}

	got, err = s.GetDomainByName("nope.org")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for missing domain name")
	}
}

func TestListDomains(t *testing.T) {
	s := testStore(t)

	s.CreateDomain("a.com")
	s.CreateDomain("b.com")

	domains, err := s.ListDomains()
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 2 {
		t.Errorf("len = %d, want 2", len(domains))
	}
}

func TestDeleteDomain(t *testing.T) {
	s := testStore(t)

	dom, _ := s.CreateDomain("delete.me")
	s.DeleteDomain(dom.ID)

	got, _ := s.GetDomain(dom.ID)
	if got != nil {
		t.Error("expected domain to be deleted")
	}
}

func TestSetDKIM(t *testing.T) {
	s := testStore(t)

	dom, _ := s.CreateDomain("dkim.test")
	s.SetDKIM(dom.ID, "-----BEGIN RSA PRIVATE KEY-----\nfake\n-----END RSA PRIVATE KEY-----", "selector1")

	got, _ := s.GetDomain(dom.ID)
	if got.DKIMPrivateKey == "" {
		t.Error("expected DKIM key to be set")
	}
	if got.DKIMSelector != "selector1" {
		t.Errorf("selector = %q, want %q", got.DKIMSelector, "selector1")
	}
}

// --- Messages ---

func TestStoreInbound(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("in.test")

	var notified bool
	s.OnInbound = func(m *Message) { notified = true }

	msg, err := s.StoreInbound(&Message{
		DomainID: dom.ID,
		From:     "sender@external.com",
		To:       []string{"user@in.test"},
		Subject:  "Hello",
		BodyText: "Hi there",
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Direction != "inbound" || msg.Status != "received" {
		t.Errorf("direction=%q status=%q, want inbound/received", msg.Direction, msg.Status)
	}
	if msg.ID == "" || msg.ReceivedAt == "" {
		t.Error("expected ID and ReceivedAt to be set")
	}

	// Give goroutine a moment
	for i := 0; i < 100 && !notified; i++ {
		// spin
	}
}

func TestStoreOutbound(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("out.test")

	msg, err := s.StoreOutbound(&Message{
		DomainID: dom.ID,
		From:     "agent@out.test",
		To:       []string{"alice@gmail.com", "bob@gmail.com"},
		Subject:  "Report",
		BodyText: "Build passed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Direction != "outbound" || msg.Status != "queued" {
		t.Errorf("direction=%q status=%q, want outbound/queued", msg.Direction, msg.Status)
	}

	// Should have 2 queue entries
	entries, err := s.PendingQueue(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("queue entries = %d, want 2", len(entries))
	}
}

func TestGetMessage(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("get.test")

	created, _ := s.StoreInbound(&Message{
		DomainID: dom.ID,
		From:     "a@b.com",
		To:       []string{"c@get.test"},
		Subject:  "Test",
		BodyText: "Body",
	})

	got, err := s.GetMessage(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "Test" || got.BodyText != "Body" {
		t.Errorf("unexpected message content: %+v", got)
	}
	if len(got.To) != 1 || got.To[0] != "c@get.test" {
		t.Errorf("To = %v, want [c@get.test]", got.To)
	}
}

func TestGetMessageNotFound(t *testing.T) {
	s := testStore(t)

	got, err := s.GetMessage("msg_nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for missing message")
	}
}

func TestPollInbound(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("poll.test")

	s.StoreInbound(&Message{DomainID: dom.ID, From: "a@b.com", To: []string{"x@poll.test"}, Subject: "First", BodyText: "1"})
	s.StoreInbound(&Message{DomainID: dom.ID, From: "a@b.com", To: []string{"x@poll.test"}, Subject: "Second", BodyText: "2"})

	msgs, err := s.PollInbound(dom.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Errorf("poll returned %d messages, want 2", len(msgs))
	}
}

func TestPollInboundOnlyUndelivered(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("pollfilter.test")

	msg1, _ := s.StoreInbound(&Message{DomainID: dom.ID, From: "a@b.com", To: []string{"x@pollfilter.test"}, Subject: "Ack me", BodyText: "1"})
	s.StoreInbound(&Message{DomainID: dom.ID, From: "a@b.com", To: []string{"x@pollfilter.test"}, Subject: "Keep me", BodyText: "2"})

	// Ack the first
	s.AckMessages(dom.ID, []string{msg1.ID})

	msgs, err := s.PollInbound(dom.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Errorf("poll returned %d messages after ack, want 1", len(msgs))
	}
	if msgs[0].Subject != "Keep me" {
		t.Errorf("wrong message returned: %q", msgs[0].Subject)
	}
}

func TestAckMessages(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("ack.test")

	msg, _ := s.StoreInbound(&Message{DomainID: dom.ID, From: "a@b.com", To: []string{"x@ack.test"}, Subject: "Ack", BodyText: "body"})

	count, err := s.AckMessages(dom.ID, []string{msg.ID})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("ack count = %d, want 1", count)
	}

	// Verify status changed
	got, _ := s.GetMessage(msg.ID)
	if got.Status != "delivered" {
		t.Errorf("status = %q, want delivered", got.Status)
	}
	if got.DeliveredAt == "" {
		t.Error("expected delivered_at to be set")
	}
}

func TestAckWrongDomain(t *testing.T) {
	s := testStore(t)
	dom1, _ := s.CreateDomain("dom1.test")
	dom2, _ := s.CreateDomain("dom2.test")

	msg, _ := s.StoreInbound(&Message{DomainID: dom1.ID, From: "a@b.com", To: []string{"x@dom1.test"}, Subject: "X", BodyText: "x"})

	// Try to ack with wrong domain
	count, _ := s.AckMessages(dom2.ID, []string{msg.ID})
	if count != 0 {
		t.Error("should not ack message from different domain")
	}
}

func TestListMessages(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("list.test")

	s.StoreInbound(&Message{DomainID: dom.ID, From: "a@b.com", To: []string{"x@list.test"}, Subject: "In", BodyText: "1"})
	s.StoreOutbound(&Message{DomainID: dom.ID, From: "x@list.test", To: []string{"a@b.com"}, Subject: "Out", BodyText: "2"})

	// All
	all, _ := s.ListMessages(MessageListOpts{DomainID: dom.ID})
	if len(all) != 2 {
		t.Errorf("all = %d, want 2", len(all))
	}

	// Filter by direction
	inbound, _ := s.ListMessages(MessageListOpts{DomainID: dom.ID, Direction: "inbound"})
	if len(inbound) != 1 {
		t.Errorf("inbound = %d, want 1", len(inbound))
	}

	outbound, _ := s.ListMessages(MessageListOpts{DomainID: dom.ID, Direction: "outbound"})
	if len(outbound) != 1 {
		t.Errorf("outbound = %d, want 1", len(outbound))
	}
}

func TestListMessagesLimit(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("limit.test")

	for i := 0; i < 5; i++ {
		s.StoreInbound(&Message{DomainID: dom.ID, From: "a@b.com", To: []string{"x@limit.test"}, Subject: "msg", BodyText: "body"})
	}

	msgs, _ := s.ListMessages(MessageListOpts{DomainID: dom.ID, Limit: 2})
	if len(msgs) != 2 {
		t.Errorf("len = %d, want 2", len(msgs))
	}
}

// --- Queue ---

func TestPendingQueue(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("queue.test")

	s.StoreOutbound(&Message{DomainID: dom.ID, From: "a@queue.test", To: []string{"b@c.com"}, Subject: "Q", BodyText: "q"})

	entries, err := s.PendingQueue(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("pending = %d, want 1", len(entries))
	}
	if entries[0].Status != "pending" {
		t.Errorf("status = %q, want pending", entries[0].Status)
	}
}

func TestUpdateQueue(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("upq.test")

	s.StoreOutbound(&Message{DomainID: dom.ID, From: "a@upq.test", To: []string{"b@c.com"}, Subject: "Q", BodyText: "q"})
	entries, _ := s.PendingQueue(10)

	s.UpdateQueue(entries[0].ID, 1, "2099-01-01T00:00:00Z", "connection refused", "pending")

	// Should not show up in pending (next_retry is in the future)
	entries, _ = s.PendingQueue(10)
	if len(entries) != 0 {
		t.Errorf("pending = %d after retry delay, want 0", len(entries))
	}
}

func TestAllQueueDone(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("done.test")

	msg, _ := s.StoreOutbound(&Message{DomainID: dom.ID, From: "a@done.test", To: []string{"b@c.com"}, Subject: "Q", BodyText: "q"})
	entries, _ := s.PendingQueue(10)

	// Mark as sent
	s.UpdateQueue(entries[0].ID, 1, "", "", "sent")

	allSent, anyFailed, err := s.AllQueueDone(msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !allSent {
		t.Error("expected allSent=true")
	}
	if anyFailed {
		t.Error("expected anyFailed=false")
	}
}

func TestAllQueueDoneWithFailure(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("fail.test")

	msg, _ := s.StoreOutbound(&Message{DomainID: dom.ID, From: "a@fail.test", To: []string{"b@c.com", "d@e.com"}, Subject: "Q", BodyText: "q"})
	entries, _ := s.PendingQueue(10)

	s.UpdateQueue(entries[0].ID, 1, "", "", "sent")
	s.UpdateQueue(entries[1].ID, 5, "", "timeout", "failed")

	allSent, anyFailed, _ := s.AllQueueDone(msg.ID)
	if allSent {
		t.Error("expected allSent=false when one failed")
	}
	if !anyFailed {
		t.Error("expected anyFailed=true")
	}
}

func TestSetMessageDelivered(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("delivered.test")

	msg, _ := s.StoreOutbound(&Message{DomainID: dom.ID, From: "a@delivered.test", To: []string{"b@c.com"}, Subject: "D", BodyText: "d"})
	s.SetMessageDelivered(msg.ID)

	got, _ := s.GetMessage(msg.ID)
	if got.Status != "sent" {
		t.Errorf("status = %q, want sent", got.Status)
	}
	if got.DeliveredAt == "" {
		t.Error("expected delivered_at to be set")
	}
}

func TestRawMessage(t *testing.T) {
	s := testStore(t)
	dom, _ := s.CreateDomain("raw.test")

	msg, _ := s.StoreOutbound(&Message{DomainID: dom.ID, From: "a@raw.test", To: []string{"b@c.com"}, Subject: "R", BodyText: "r"})

	raw := "From: a@raw.test\r\nTo: b@c.com\r\nSubject: R\r\n\r\nr\r\n"
	s.SetRawMessage(msg.ID, raw)

	got, _ := s.GetRawMessage(msg.ID)
	if got != raw {
		t.Errorf("raw message mismatch")
	}
}
