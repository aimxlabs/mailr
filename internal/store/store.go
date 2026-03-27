package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/garett/mailr/internal/db"
)

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS domains (
		id               TEXT PRIMARY KEY,
		name             TEXT NOT NULL UNIQUE,
		dkim_private_key TEXT DEFAULT '',
		dkim_selector    TEXT DEFAULT 'default',
		auth_token       TEXT NOT NULL,
		created_at       TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS messages (
		id           TEXT PRIMARY KEY,
		domain_id    TEXT NOT NULL REFERENCES domains(id),
		message_id   TEXT DEFAULT '',
		direction    TEXT NOT NULL,
		from_addr    TEXT NOT NULL,
		to_addrs     TEXT DEFAULT '[]',
		cc_addrs     TEXT DEFAULT '[]',
		subject      TEXT DEFAULT '',
		body_text    TEXT DEFAULT '',
		body_html    TEXT DEFAULT '',
		raw_data     TEXT DEFAULT '',
		status       TEXT DEFAULT 'received',
		received_at  TEXT NOT NULL,
		delivered_at TEXT DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_msg_domain ON messages(domain_id, direction, status);
	CREATE INDEX IF NOT EXISTS idx_msg_msgid ON messages(message_id) WHERE message_id != '';

	CREATE TABLE IF NOT EXISTS outbound_queue (
		id          TEXT PRIMARY KEY,
		message_id  TEXT NOT NULL REFERENCES messages(id),
		recipient   TEXT NOT NULL,
		attempts    INTEGER DEFAULT 0,
		next_retry  TEXT NOT NULL,
		last_error  TEXT DEFAULT '',
		status      TEXT DEFAULT 'pending'
	);
	CREATE INDEX IF NOT EXISTS idx_queue_pending ON outbound_queue(status, next_retry);`,
}

type Store struct {
	DB        *sql.DB
	OnInbound func(*Message) // called after storing inbound message
	mu        sync.RWMutex
}

type Domain struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DKIMPrivateKey string `json:"-"`
	DKIMSelector   string `json:"dkim_selector"`
	AuthToken      string `json:"auth_token,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type Message struct {
	ID          string   `json:"id"`
	DomainID    string   `json:"domain_id"`
	MessageID   string   `json:"message_id,omitempty"`
	Direction   string   `json:"direction"`
	From        string   `json:"from"`
	To          []string `json:"to"`
	Cc          []string `json:"cc,omitempty"`
	Subject     string   `json:"subject"`
	BodyText    string   `json:"body_text"`
	BodyHTML    string   `json:"body_html,omitempty"`
	RawData     string   `json:"-"`
	Status      string   `json:"status"`
	ReceivedAt  string   `json:"received_at"`
	DeliveredAt string   `json:"delivered_at,omitempty"`
}

type QueueEntry struct {
	ID        string `json:"id"`
	MessageID string `json:"message_id"`
	Recipient string `json:"recipient"`
	Attempts  int    `json:"attempts"`
	NextRetry string `json:"next_retry"`
	LastError string `json:"last_error,omitempty"`
	Status    string `json:"status"`
}

func newID(prefix string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func NewStore(database *sql.DB) (*Store, error) {
	if err := db.Migrate(database, "mailr", migrations); err != nil {
		return nil, fmt.Errorf("migrating mailr schema: %w", err)
	}
	return &Store{DB: database}, nil
}

// --- Domains ---

func (s *Store) CreateDomain(name string) (*Domain, error) {
	d := &Domain{
		ID:           newID("dom_"),
		Name:         name,
		DKIMSelector: "default",
		AuthToken:    newID("tok_"),
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	_, err := s.DB.Exec(
		`INSERT INTO domains (id,name,dkim_private_key,dkim_selector,auth_token,created_at)
		 VALUES (?,?,?,?,?,?)`,
		d.ID, d.Name, "", d.DKIMSelector, d.AuthToken, d.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("creating domain: %w", err)
	}
	return d, nil
}

func (s *Store) GetDomain(id string) (*Domain, error) {
	d := &Domain{}
	err := s.DB.QueryRow(
		`SELECT id,name,dkim_private_key,dkim_selector,auth_token,created_at FROM domains WHERE id=?`, id,
	).Scan(&d.ID, &d.Name, &d.DKIMPrivateKey, &d.DKIMSelector, &d.AuthToken, &d.CreatedAt)
	if err == sql.ErrNoRows { return nil, nil }
	if err != nil { return nil, err }
	return d, nil
}

func (s *Store) GetDomainByName(name string) (*Domain, error) {
	d := &Domain{}
	err := s.DB.QueryRow(
		`SELECT id,name,dkim_private_key,dkim_selector,auth_token,created_at FROM domains WHERE name=?`, name,
	).Scan(&d.ID, &d.Name, &d.DKIMPrivateKey, &d.DKIMSelector, &d.AuthToken, &d.CreatedAt)
	if err == sql.ErrNoRows { return nil, nil }
	if err != nil { return nil, err }
	return d, nil
}

func (s *Store) ListDomains() ([]Domain, error) {
	rows, err := s.DB.Query(
		`SELECT id,name,dkim_selector,created_at FROM domains ORDER BY created_at DESC`)
	if err != nil { return nil, err }
	defer rows.Close()
	var result []Domain
	for rows.Next() {
		var d Domain
		rows.Scan(&d.ID, &d.Name, &d.DKIMSelector, &d.CreatedAt)
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *Store) DeleteDomain(id string) error {
	_, err := s.DB.Exec("DELETE FROM domains WHERE id=?", id)
	return err
}

func (s *Store) SetDKIM(id, privateKey, selector string) error {
	_, err := s.DB.Exec(
		"UPDATE domains SET dkim_private_key=?, dkim_selector=? WHERE id=?",
		privateKey, selector, id,
	)
	return err
}

// --- Messages ---

func (s *Store) StoreInbound(msg *Message) (*Message, error) {
	msg.ID = newID("msg_")
	msg.Direction = "inbound"
	msg.Status = "received"
	if msg.ReceivedAt == "" { msg.ReceivedAt = time.Now().UTC().Format(time.RFC3339) }
	if msg.To == nil { msg.To = []string{} }
	if msg.Cc == nil { msg.Cc = []string{} }

	toJSON, _ := json.Marshal(msg.To)
	ccJSON, _ := json.Marshal(msg.Cc)
	_, err := s.DB.Exec(
		`INSERT INTO messages (id,domain_id,message_id,direction,from_addr,to_addrs,cc_addrs,subject,body_text,body_html,raw_data,status,received_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		msg.ID, msg.DomainID, msg.MessageID, msg.Direction, msg.From,
		string(toJSON), string(ccJSON), msg.Subject, msg.BodyText, msg.BodyHTML,
		msg.RawData, msg.Status, msg.ReceivedAt,
	)
	if err != nil { return nil, fmt.Errorf("storing inbound message: %w", err) }

	s.mu.RLock()
	fn := s.OnInbound
	s.mu.RUnlock()
	if fn != nil { go fn(msg) }

	return msg, nil
}

func (s *Store) StoreOutbound(msg *Message) (*Message, error) {
	msg.ID = newID("msg_")
	msg.Direction = "outbound"
	msg.Status = "queued"
	msg.ReceivedAt = time.Now().UTC().Format(time.RFC3339)
	if msg.MessageID == "" { msg.MessageID = newID("") + "@" + "mailr" }
	if msg.To == nil { msg.To = []string{} }
	if msg.Cc == nil { msg.Cc = []string{} }

	toJSON, _ := json.Marshal(msg.To)
	ccJSON, _ := json.Marshal(msg.Cc)
	_, err := s.DB.Exec(
		`INSERT INTO messages (id,domain_id,message_id,direction,from_addr,to_addrs,cc_addrs,subject,body_text,body_html,raw_data,status,received_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		msg.ID, msg.DomainID, msg.MessageID, msg.Direction, msg.From,
		string(toJSON), string(ccJSON), msg.Subject, msg.BodyText, msg.BodyHTML,
		msg.RawData, msg.Status, msg.ReceivedAt,
	)
	if err != nil { return nil, fmt.Errorf("storing outbound message: %w", err) }

	// Enqueue for each recipient
	now := time.Now().UTC().Format(time.RFC3339)
	for _, rcpt := range append(msg.To, msg.Cc...) {
		s.DB.Exec(
			`INSERT INTO outbound_queue (id,message_id,recipient,next_retry,status) VALUES (?,?,?,?,?)`,
			newID("q_"), msg.ID, rcpt, now, "pending",
		)
	}

	return msg, nil
}

func (s *Store) GetMessage(id string) (*Message, error) {
	m := &Message{}
	var toJSON, ccJSON string
	err := s.DB.QueryRow(
		`SELECT id,domain_id,message_id,direction,from_addr,to_addrs,cc_addrs,subject,body_text,body_html,status,received_at,COALESCE(delivered_at,'')
		 FROM messages WHERE id=?`, id,
	).Scan(&m.ID, &m.DomainID, &m.MessageID, &m.Direction, &m.From,
		&toJSON, &ccJSON, &m.Subject, &m.BodyText, &m.BodyHTML,
		&m.Status, &m.ReceivedAt, &m.DeliveredAt)
	if err == sql.ErrNoRows { return nil, nil }
	if err != nil { return nil, err }
	json.Unmarshal([]byte(toJSON), &m.To)
	json.Unmarshal([]byte(ccJSON), &m.Cc)
	if m.To == nil { m.To = []string{} }
	if m.Cc == nil { m.Cc = []string{} }
	return m, nil
}

func (s *Store) PollInbound(domainID string, limit int) ([]Message, error) {
	if limit <= 0 { limit = 50 }
	rows, err := s.DB.Query(
		`SELECT id,domain_id,message_id,direction,from_addr,to_addrs,cc_addrs,subject,body_text,body_html,status,received_at,COALESCE(delivered_at,'')
		 FROM messages WHERE domain_id=? AND direction='inbound' AND status='received'
		 ORDER BY received_at ASC LIMIT ?`, domainID, limit,
	)
	if err != nil { return nil, err }
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) AckMessages(domainID string, ids []string) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	count := 0
	for _, id := range ids {
		res, err := s.DB.Exec(
			"UPDATE messages SET status='delivered', delivered_at=? WHERE id=? AND domain_id=? AND direction='inbound'",
			now, id, domainID,
		)
		if err != nil { continue }
		n, _ := res.RowsAffected()
		count += int(n)
	}
	return count, nil
}

type MessageListOpts struct {
	DomainID  string
	Direction string
	Status    string
	Limit     int
}

func (s *Store) ListMessages(opts MessageListOpts) ([]Message, error) {
	q := `SELECT id,domain_id,message_id,direction,from_addr,to_addrs,cc_addrs,subject,body_text,body_html,status,received_at,COALESCE(delivered_at,'')
	      FROM messages WHERE 1=1`
	var args []interface{}
	if opts.DomainID != "" { q += " AND domain_id=?"; args = append(args, opts.DomainID) }
	if opts.Direction != "" { q += " AND direction=?"; args = append(args, opts.Direction) }
	if opts.Status != "" { q += " AND status=?"; args = append(args, opts.Status) }
	q += " ORDER BY received_at DESC"
	if opts.Limit > 0 { q += fmt.Sprintf(" LIMIT %d", opts.Limit) } else { q += " LIMIT 50" }

	rows, err := s.DB.Query(q, args...)
	if err != nil { return nil, err }
	defer rows.Close()
	return scanMessages(rows)
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	var result []Message
	for rows.Next() {
		var m Message
		var toJSON, ccJSON string
		rows.Scan(&m.ID, &m.DomainID, &m.MessageID, &m.Direction, &m.From,
			&toJSON, &ccJSON, &m.Subject, &m.BodyText, &m.BodyHTML,
			&m.Status, &m.ReceivedAt, &m.DeliveredAt)
		json.Unmarshal([]byte(toJSON), &m.To)
		json.Unmarshal([]byte(ccJSON), &m.Cc)
		if m.To == nil { m.To = []string{} }
		if m.Cc == nil { m.Cc = []string{} }
		result = append(result, m)
	}
	return result, rows.Err()
}

// --- Queue ---

func (s *Store) PendingQueue(limit int) ([]QueueEntry, error) {
	if limit <= 0 { limit = 10 }
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := s.DB.Query(
		`SELECT id,message_id,recipient,attempts,next_retry,COALESCE(last_error,''),status
		 FROM outbound_queue WHERE status='pending' AND next_retry<=?
		 ORDER BY next_retry ASC LIMIT ?`, now, limit,
	)
	if err != nil { return nil, err }
	defer rows.Close()
	var result []QueueEntry
	for rows.Next() {
		var q QueueEntry
		rows.Scan(&q.ID, &q.MessageID, &q.Recipient, &q.Attempts, &q.NextRetry, &q.LastError, &q.Status)
		result = append(result, q)
	}
	return result, rows.Err()
}

func (s *Store) UpdateQueue(id string, attempts int, nextRetry, lastError, status string) error {
	_, err := s.DB.Exec(
		"UPDATE outbound_queue SET attempts=?, next_retry=?, last_error=?, status=? WHERE id=?",
		attempts, nextRetry, lastError, status, id,
	)
	return err
}

func (s *Store) UpdateMessageStatus(id, status string) error {
	_, err := s.DB.Exec("UPDATE messages SET status=? WHERE id=?", status, id)
	return err
}

func (s *Store) SetMessageDelivered(id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.DB.Exec("UPDATE messages SET status='sent', delivered_at=? WHERE id=?", now, id)
	return err
}

func (s *Store) GetRawMessage(id string) (string, error) {
	var raw string
	err := s.DB.QueryRow("SELECT raw_data FROM messages WHERE id=?", id).Scan(&raw)
	if err == sql.ErrNoRows { return "", nil }
	return raw, err
}

func (s *Store) SetRawMessage(id, raw string) error {
	_, err := s.DB.Exec("UPDATE messages SET raw_data=? WHERE id=?", raw, id)
	return err
}

// AllQueueDone checks if all queue entries for a message are terminal.
func (s *Store) AllQueueDone(messageID string) (allSent bool, anyFailed bool, err error) {
	var pending, sent, failed int
	err = s.DB.QueryRow(
		`SELECT
			SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),
			SUM(CASE WHEN status='sent' THEN 1 ELSE 0 END),
			SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END)
		 FROM outbound_queue WHERE message_id=?`, messageID,
	).Scan(&pending, &sent, &failed)
	if err != nil { return false, false, err }
	return pending == 0 && failed == 0, failed > 0, nil
}
