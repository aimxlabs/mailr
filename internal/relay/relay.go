package relay

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log"
	"net"
	gosmtp "net/smtp"
	"strings"
	"time"

	"github.com/garett/mailr/internal/store"
)

const maxAttempts = 5

type Relay struct {
	store  *store.Store
	domain string
}

func New(s *store.Store, domain string) *Relay {
	return &Relay{store: s, domain: domain}
}

// StartQueueProcessor processes the outbound queue on a timer.
func (r *Relay) StartQueueProcessor(stop chan struct{}) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			r.processQueue()
		}
	}
}

func (r *Relay) processQueue() {
	entries, err := r.store.PendingQueue(10)
	if err != nil {
		log.Printf("relay: queue fetch error: %v", err)
		return
	}

	for _, entry := range entries {
		msg, err := r.store.GetMessage(entry.MessageID)
		if err != nil || msg == nil {
			r.store.UpdateQueue(entry.ID, entry.Attempts+1, "", "message not found", "failed")
			continue
		}

		raw, _ := r.store.GetRawMessage(entry.MessageID)
		if raw == "" {
			composed := r.composeMessage(msg)

			// Sign with DKIM if configured
			dom, _ := r.store.GetDomain(msg.DomainID)
			if dom != nil && dom.DKIMPrivateKey != "" {
				signed, err := signDKIM(composed, dom.Name, dom.DKIMSelector, dom.DKIMPrivateKey)
				if err != nil {
					log.Printf("relay: DKIM signing failed for %s: %v", entry.MessageID, err)
				} else {
					composed = signed
				}
			}

			raw = composed
			r.store.SetRawMessage(entry.MessageID, raw)
		}

		err = r.deliver(msg.From, entry.Recipient, raw)
		if err != nil {
			attempts := entry.Attempts + 1
			if attempts >= maxAttempts {
				r.store.UpdateQueue(entry.ID, attempts, "", err.Error(), "failed")
				r.checkMessageDone(entry.MessageID)
				log.Printf("relay: permanently failed %s→%s: %v", entry.MessageID, entry.Recipient, err)
			} else {
				backoff := time.Duration(attempts*attempts) * time.Minute
				nextRetry := time.Now().UTC().Add(backoff).Format(time.RFC3339)
				r.store.UpdateQueue(entry.ID, attempts, nextRetry, err.Error(), "pending")
				log.Printf("relay: retry %d for %s→%s: %v", attempts, entry.MessageID, entry.Recipient, err)
			}
		} else {
			r.store.UpdateQueue(entry.ID, entry.Attempts+1, "", "", "sent")
			r.checkMessageDone(entry.MessageID)
			log.Printf("relay: delivered %s→%s", entry.MessageID, entry.Recipient)
		}
	}
}

func (r *Relay) checkMessageDone(messageID string) {
	allSent, anyFailed, err := r.store.AllQueueDone(messageID)
	if err != nil { return }
	if allSent {
		r.store.SetMessageDelivered(messageID)
	} else if anyFailed {
		r.store.UpdateMessageStatus(messageID, "failed")
	}
}

func (r *Relay) deliver(from, to, raw string) error {
	parts := strings.SplitN(to, "@", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid recipient: %s", to)
	}
	domain := parts[1]

	mxRecords, err := net.LookupMX(domain)
	if err != nil {
		return fmt.Errorf("MX lookup for %s: %w", domain, err)
	}
	if len(mxRecords) == 0 {
		return fmt.Errorf("no MX records for %s", domain)
	}

	var lastErr error
	for _, mx := range mxRecords {
		host := strings.TrimRight(mx.Host, ".")
		addr := host + ":25"
		err := gosmtp.SendMail(addr, nil, from, []string{to}, []byte(raw))
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("all MX hosts failed for %s: %w", domain, lastErr)
}

// --- Message Composition ---

func (r *Relay) composeMessage(msg *store.Message) string {
	var b strings.Builder

	b.WriteString("From: " + msg.From + "\r\n")
	b.WriteString("To: " + strings.Join(msg.To, ", ") + "\r\n")
	if len(msg.Cc) > 0 {
		b.WriteString("Cc: " + strings.Join(msg.Cc, ", ") + "\r\n")
	}
	b.WriteString("Subject: " + msg.Subject + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: <" + msg.MessageID + ">\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")

	if msg.BodyHTML != "" {
		boundary := "mailr-" + fmt.Sprintf("%x", time.Now().UnixNano())
		b.WriteString("Content-Type: multipart/alternative; boundary=" + boundary + "\r\n")
		b.WriteString("\r\n")
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(msg.BodyText + "\r\n")
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
		b.WriteString(msg.BodyHTML + "\r\n")
		b.WriteString("--" + boundary + "--\r\n")
	} else {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		b.WriteString("\r\n")
		b.WriteString(msg.BodyText + "\r\n")
	}

	return b.String()
}

// --- DKIM Signing ---

func GenerateDKIMKey() (privatePEM string, publicDNS string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("generating RSA key: %w", err)
	}

	privBytes := x509.MarshalPKCS1PrivateKey(key)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes})

	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("marshaling public key: %w", err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pubBytes)

	dnsValue := "v=DKIM1; k=rsa; p=" + pubB64

	return string(privPEM), dnsValue, nil
}

func signDKIM(message, domain, selector, privatePEM string) (string, error) {
	block, _ := pem.Decode([]byte(privatePEM))
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parsing private key: %w", err)
	}

	// Split headers and body
	parts := strings.SplitN(message, "\r\n\r\n", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("malformed message: no header/body separator")
	}
	headerSection := parts[0]
	body := parts[1]

	// Body hash (relaxed canonicalization: trim trailing whitespace, ensure trailing CRLF)
	canonBody := canonicalizeBody(body)
	bodyHash := sha256.Sum256([]byte(canonBody))
	bh := base64.StdEncoding.EncodeToString(bodyHash[:])

	// Determine which headers to sign
	signedHeaders := []string{"from", "to", "subject", "date", "message-id"}
	if strings.Contains(strings.ToLower(headerSection), "cc:") {
		signedHeaders = append(signedHeaders, "cc")
	}

	// Build DKIM-Signature header (without b= value)
	now := time.Now().UTC()
	dkimHeader := fmt.Sprintf(
		"DKIM-Signature: v=1; a=rsa-sha256; c=relaxed/relaxed; d=%s; s=%s; t=%d; h=%s; bh=%s; b=",
		domain, selector, now.Unix(), strings.Join(signedHeaders, ":"), bh,
	)

	// Canonicalize signed headers
	headerMap := parseHeaders(headerSection)
	var dataToSign strings.Builder
	for _, h := range signedHeaders {
		if val, ok := headerMap[h]; ok {
			dataToSign.WriteString(h + ":" + relaxHeaderValue(val) + "\r\n")
		}
	}
	// Add DKIM-Signature header itself (without trailing CRLF, without b= value)
	dataToSign.WriteString("dkim-signature:" + relaxHeaderValue(
		strings.TrimPrefix(dkimHeader, "DKIM-Signature: "),
	))

	// Sign
	hashed := sha256.Sum256([]byte(dataToSign.String()))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("signing: %w", err)
	}

	dkimHeader += base64.StdEncoding.EncodeToString(sig)

	// Prepend DKIM-Signature to message
	return dkimHeader + "\r\n" + message, nil
}

func canonicalizeBody(body string) string {
	lines := strings.Split(body, "\r\n")
	var result []string
	for _, line := range lines {
		// Trim trailing whitespace
		result = append(result, strings.TrimRight(line, " \t"))
	}
	// Remove trailing empty lines
	for len(result) > 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	return strings.Join(result, "\r\n") + "\r\n"
}

func parseHeaders(headerSection string) map[string]string {
	headers := make(map[string]string)
	var currentKey, currentVal string

	for _, line := range strings.Split(headerSection, "\r\n") {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			// Continuation
			currentVal += " " + strings.TrimSpace(line)
		} else if idx := strings.Index(line, ":"); idx > 0 {
			if currentKey != "" {
				headers[currentKey] = currentVal
			}
			currentKey = strings.ToLower(strings.TrimSpace(line[:idx]))
			currentVal = strings.TrimSpace(line[idx+1:])
		}
	}
	if currentKey != "" {
		headers[currentKey] = currentVal
	}
	return headers
}

func relaxHeaderValue(v string) string {
	// Collapse whitespace
	fields := strings.Fields(v)
	return strings.Join(fields, " ")
}
