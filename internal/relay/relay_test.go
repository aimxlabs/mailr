package relay

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/garett/mailr/internal/store"
)

func TestGenerateDKIMKey(t *testing.T) {
	privPEM, dnsValue, err := GenerateDKIMKey()
	if err != nil {
		t.Fatal(err)
	}

	// Verify private key is valid PEM
	block, _ := pem.Decode([]byte(privPEM))
	if block == nil {
		t.Fatal("failed to decode private key PEM")
	}
	if block.Type != "RSA PRIVATE KEY" {
		t.Errorf("PEM type = %q, want RSA PRIVATE KEY", block.Type)
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("invalid private key: %v", err)
	}
	if key.N.BitLen() != 2048 {
		t.Errorf("key size = %d bits, want 2048", key.N.BitLen())
	}

	// Verify DNS value format
	if !strings.HasPrefix(dnsValue, "v=DKIM1; k=rsa; p=") {
		t.Errorf("unexpected DNS value format: %q", dnsValue)
	}

	// Verify public key is valid base64
	pubB64 := strings.TrimPrefix(dnsValue, "v=DKIM1; k=rsa; p=")
	pubBytes, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		t.Fatalf("invalid base64 in DNS value: %v", err)
	}
	pubKey, err := x509.ParsePKIXPublicKey(pubBytes)
	if err != nil {
		t.Fatalf("invalid public key: %v", err)
	}

	// Verify key pair matches
	rsaPub, ok := pubKey.(*rsa.PublicKey)
	if !ok {
		t.Fatal("public key is not RSA")
	}
	if rsaPub.N.Cmp(key.PublicKey.N) != 0 {
		t.Error("public key does not match private key")
	}
}

func TestSignDKIM(t *testing.T) {
	privPEM, _, err := GenerateDKIMKey()
	if err != nil {
		t.Fatal(err)
	}

	message := "From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: Test\r\n" +
		"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
		"Message-ID: <test123@example.com>\r\n" +
		"\r\n" +
		"Hello, world!\r\n"

	signed, err := signDKIM(message, "example.com", "default", privPEM)
	if err != nil {
		t.Fatal(err)
	}

	// Should have DKIM-Signature prepended
	if !strings.HasPrefix(signed, "DKIM-Signature: ") {
		t.Error("signed message should start with DKIM-Signature")
	}

	// Should contain original message
	if !strings.Contains(signed, "Hello, world!") {
		t.Error("signed message should contain original body")
	}

	// Parse the DKIM-Signature header
	sigLine := strings.SplitN(signed, "\r\n", 2)[0]
	if !strings.Contains(sigLine, "a=rsa-sha256") {
		t.Error("expected rsa-sha256 algorithm")
	}
	if !strings.Contains(sigLine, "d=example.com") {
		t.Error("expected d=example.com")
	}
	if !strings.Contains(sigLine, "s=default") {
		t.Error("expected s=default")
	}
	if !strings.Contains(sigLine, "bh=") {
		t.Error("expected body hash (bh=)")
	}
	if !strings.Contains(sigLine, "b=") {
		t.Error("expected signature (b=)")
	}
}

func TestSignDKIMVerifiesWithPublicKey(t *testing.T) {
	privPEM, _, err := GenerateDKIMKey()
	if err != nil {
		t.Fatal(err)
	}

	message := "From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: Verify me\r\n" +
		"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
		"Message-ID: <verify@example.com>\r\n" +
		"\r\n" +
		"Verifiable body\r\n"

	signed, err := signDKIM(message, "example.com", "default", privPEM)
	if err != nil {
		t.Fatal(err)
	}

	// Extract signature value
	sigLine := strings.SplitN(signed, "\r\n", 2)[0]
	sigValue := extractDKIMField(sigLine, "b=")
	bhValue := extractDKIMField(sigLine, "bh=")

	// Verify body hash
	parts := strings.SplitN(signed, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatal("could not split signed message")
	}
	canonBody := canonicalizeBody(parts[1])
	bodyHash := sha256.Sum256([]byte(canonBody))
	expectedBH := base64.StdEncoding.EncodeToString(bodyHash[:])
	if bhValue != expectedBH {
		t.Errorf("body hash mismatch: got %q, want %q", bhValue, expectedBH)
	}

	// Verify RSA signature
	block, _ := pem.Decode([]byte(privPEM))
	key, _ := x509.ParsePKCS1PrivateKey(block.Bytes)

	// Reconstruct data that was signed
	headerMap := parseHeaders(strings.SplitN(message, "\r\n\r\n", 2)[0])
	signedHeaders := []string{"from", "to", "subject", "date", "message-id"}
	var dataToSign strings.Builder
	for _, h := range signedHeaders {
		if val, ok := headerMap[h]; ok {
			dataToSign.WriteString(h + ":" + relaxHeaderValue(val) + "\r\n")
		}
	}
	// Reconstruct DKIM-Signature without b= value
	dkimWithoutSig := sigLine[:strings.Index(sigLine, "b=")+2]
	dkimVal := strings.TrimPrefix(dkimWithoutSig, "DKIM-Signature: ")
	dataToSign.WriteString("dkim-signature:" + relaxHeaderValue(dkimVal))

	hashed := sha256.Sum256([]byte(dataToSign.String()))
	sigBytes, err := base64.StdEncoding.DecodeString(sigValue)
	if err != nil {
		t.Fatalf("failed to decode signature: %v", err)
	}

	err = rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hashed[:], sigBytes)
	if err != nil {
		t.Errorf("RSA signature verification failed: %v", err)
	}
}

func TestSignDKIMBadPEM(t *testing.T) {
	_, err := signDKIM("From: a\r\n\r\nbody", "d.com", "s", "not-pem")
	if err == nil {
		t.Error("expected error for bad PEM")
	}
}

func TestSignDKIMMalformedMessage(t *testing.T) {
	privPEM, _, _ := GenerateDKIMKey()
	_, err := signDKIM("no separator here", "d.com", "s", privPEM)
	if err == nil {
		t.Error("expected error for malformed message")
	}
}

func TestCanonicalizeBody(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"trailing whitespace", "hello   \r\nworld\t\r\n", "hello\r\nworld\r\n"},
		{"trailing empty lines", "hello\r\n\r\n\r\n", "hello\r\n"},
		{"single line", "hello\r\n", "hello\r\n"},
		{"empty body", "\r\n", "\r\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canonicalizeBody(tt.input)
			if got != tt.want {
				t.Errorf("canonicalizeBody(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseHeaders(t *testing.T) {
	input := "From: alice@example.com\r\nTo: bob@example.com\r\nSubject: Hello\r\n  World\r\nDate: Mon, 01 Jan 2024"

	headers := parseHeaders(input)

	if headers["from"] != "alice@example.com" {
		t.Errorf("from = %q", headers["from"])
	}
	if headers["to"] != "bob@example.com" {
		t.Errorf("to = %q", headers["to"])
	}
	// Continuation line
	if headers["subject"] != "Hello World" {
		t.Errorf("subject = %q, want %q", headers["subject"], "Hello World")
	}
}

func TestRelaxHeaderValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"  hello   world  ", "hello world"},
		{"no\textra\tspace", "no extra space"},
		{"already clean", "already clean"},
	}

	for _, tt := range tests {
		got := relaxHeaderValue(tt.input)
		if got != tt.want {
			t.Errorf("relaxHeaderValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestComposeMessage(t *testing.T) {
	r := &Relay{domain: "test.com"}

	t.Run("plain text", func(t *testing.T) {
		msg := &store.Message{
			From:      "alice@test.com",
			To:        []string{"bob@other.com"},
			Subject:   "Hello",
			BodyText:  "Plain text body",
			MessageID: "test123@test.com",
		}
		composed := r.composeMessage(msg)

		if !strings.Contains(composed, "From: alice@test.com") {
			t.Error("missing From header")
		}
		if !strings.Contains(composed, "To: bob@other.com") {
			t.Error("missing To header")
		}
		if !strings.Contains(composed, "Subject: Hello") {
			t.Error("missing Subject header")
		}
		if !strings.Contains(composed, "Content-Type: text/plain") {
			t.Error("expected text/plain content type")
		}
		if !strings.Contains(composed, "Plain text body") {
			t.Error("missing body text")
		}
		if strings.Contains(composed, "multipart") {
			t.Error("plain text message should not be multipart")
		}
	})

	t.Run("multipart html", func(t *testing.T) {
		msg := &store.Message{
			From:      "alice@test.com",
			To:        []string{"bob@other.com"},
			Subject:   "HTML Test",
			BodyText:  "Plain version",
			BodyHTML:  "<h1>HTML version</h1>",
			MessageID: "html123@test.com",
		}
		composed := r.composeMessage(msg)

		if !strings.Contains(composed, "multipart/alternative") {
			t.Error("expected multipart/alternative")
		}
		if !strings.Contains(composed, "Plain version") {
			t.Error("missing plain text part")
		}
		if !strings.Contains(composed, "<h1>HTML version</h1>") {
			t.Error("missing HTML part")
		}
	})

	t.Run("with cc", func(t *testing.T) {
		msg := &store.Message{
			From:      "alice@test.com",
			To:        []string{"bob@other.com"},
			Cc:        []string{"carol@other.com"},
			Subject:   "CC Test",
			BodyText:  "body",
			MessageID: "cc123@test.com",
		}
		composed := r.composeMessage(msg)

		if !strings.Contains(composed, "Cc: carol@other.com") {
			t.Error("missing Cc header")
		}
	})
}

// --- Helpers ---

func extractDKIMField(sigLine, field string) string {
	idx := strings.Index(sigLine, field)
	if idx < 0 {
		return ""
	}
	rest := sigLine[idx+len(field):]
	if semi := strings.Index(rest, ";"); semi >= 0 {
		return rest[:semi]
	}
	return rest
}
