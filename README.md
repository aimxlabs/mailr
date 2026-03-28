# mailr

Mail relay for AI agents. Send and receive real email through a simple API.

AI agents need email — to send notifications, receive inbound messages, and communicate with the outside world. But running a mail server requires public infrastructure, DNS configuration, DKIM signing, and reputation management. Your agent can't do that from a laptop behind a firewall.

**mailr** is a lightweight, self-hosted mail relay that handles all of that. Deploy it once, point your DNS at it, and your agents get full email capabilities through a clean HTTP API.

## Why mailr?

Agents are building and operating software now. The apps they create need to send transactional email. The workflows they run need to receive inbound messages. The systems they monitor need to alert via email.

The existing options don't fit:

- **SaaS email APIs** (SendGrid, Postmark) require accounts, billing, API keys, and vendor lock-in. They're designed for businesses, not agents spinning up infrastructure on the fly.
- **Running your own SMTP server** means managing DNS, TLS, DKIM, SPF, DMARC, sender reputation, bounce handling, and retry queues. That's a full-time job, not a side task for an agent.
- **SMTP libraries in your agent** still need a public IP, port 25 open, and proper DNS — which you don't have on your laptop.

**mailr solves this with one deployment:**

```bash
# Send email via API
curl -X POST https://mail.example.com/api/domains/dom_abc123/send \
  -H "Authorization: Bearer tok_..." \
  -H "Content-Type: application/json" \
  -d '{
    "from": "agent@example.com",
    "to": ["alice@gmail.com"],
    "subject": "Build complete",
    "body_text": "Your deployment finished successfully."
  }'

# Poll for inbound email
curl https://mail.example.com/api/domains/dom_abc123/messages/poll \
  -H "Authorization: Bearer tok_..."
```

Deploy the relay to a cloud server, configure DNS, and your agents get email. No vendor accounts, no complex setup, no ongoing management beyond the single relay instance.

**This matters because:**

- **Agent notifications** — Your deployment agent finishes a build and needs to email the team. POST to mailr.
- **Inbound processing** — Customers reply to an email and your support agent needs to see it. Poll mailr or connect via WebSocket.
- **Agent-to-human communication** — Email is the universal protocol. Every person has an inbox. Your agents should be able to reach them.
- **Self-hosted control** — Your email, your server, your data. No third-party reading your messages or rate-limiting your agents.

## Quick Start

The fastest way to get mailr running is with [Claude Code](https://docs.anthropic.com/en/docs/claude-code) and the built-in `/deploy-mailr` skill. It handles everything end-to-end — server provisioning, DNS (A, MX, SPF, DKIM), domain creation, and verification.

**Prerequisites:** Cloud credentials configured ([AWS](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html) `aws configure` or [DigitalOcean](https://docs.digitalocean.com/reference/doctl/how-to/install/) `doctl auth init`) and a domain name.

```bash
git clone https://github.com/aimxlabs/mailr.git && cd mailr
go build -o mailr ./cmd/mailr
```

Then open Claude Code in the repo folder and type:

```
/deploy-mailr
```

The skill will autonomously:

- Detect your cloud credentials and ask for a domain
- Deploy the server (AWS EC2 or DigitalOcean Droplet)
- Configure all DNS records (A, MX, SPF, DKIM)
- Wait for HTTPS (Let's Encrypt via Caddy)
- Create a mail domain and generate DKIM keys
- Verify inbound and outbound email end-to-end

## How It Works

```
                    ┌─────────────────────┐
                    │   External World    │
                    │  (Gmail, Outlook,   │
                    │   other agents)     │
                    └──────┬──────┬───────┘
                      SMTP │      │ SMTP
                    inbound│      │outbound
                           ▼      │
                    ┌──────────────┴──────┐
                    │   mailr (cloud)     │
                    │                     │
                    │  • SMTP server :25  │
                    │  • HTTP API  :443   │
                    │  • DKIM signing     │
                    │  • MX delivery      │
                    │  • Retry queue      │
                    │  • SQLite storage   │
                    └──────┬──────────────┘
                           │
                     API / WebSocket
                           │
                    ┌──────▼──────────────┐
                    │   aiprod (local)    │
                    │                     │
                    │  Your AI agents     │
                    │  poll for inbound,  │
                    │  send via API       │
                    └─────────────────────┘
```

1. **Register a domain** — mailr gives you a domain token for API access
2. **Configure DNS** — point A, MX, SPF, and DKIM records at your mailr server
3. **Send email** — POST to `/api/domains/:id/send` with from, to, subject, and body
4. **Receive email** — inbound SMTP arrives at port 25, stored and available via poll or WebSocket
5. **Outbound delivery** — mailr resolves MX records, signs with DKIM, and delivers via SMTP with automatic retry

## Features

- **Full SMTP server** for receiving inbound email from anywhere
- **Direct MX delivery** for outbound — no third-party relay required
- **DKIM signing** with automatic key generation and DNS record output
- **Retry queue** with exponential backoff (up to 5 attempts)
- **Real-time delivery** via WebSocket push for inbound messages
- **HTTP polling** for agents that can't maintain persistent connections
- **RFC 5322 parsing** with MIME multipart support (text + HTML)
- **Per-domain auth tokens** for multi-tenant isolation
- **Self-hosted** — single Go binary, SQLite storage, zero external dependencies
- **One-command deploy** to AWS or DigitalOcean with automatic HTTPS

## Commands

```
mailr serve                        Start the mail relay server
mailr setup                        Guided setup — connect to server, create domain
mailr deploy aws|digitalocean      Deploy to cloud
mailr deploy teardown <provider>   Destroy cloud resources
mailr manage init                  Configure SSH connection to remote server
mailr manage status                Show server health and disk usage
mailr manage start|stop|restart    Container lifecycle
mailr manage logs                  View container logs (follow mode)
mailr manage update                Pull latest code, rebuild, restart
mailr manage backup                Download database backup
mailr manage restore <file>        Restore database from backup
mailr manage domain <new-domain>   Update server domain name
mailr manage cleanup               Prune Docker resources
mailr manage ssh                   Interactive SSH session
mailr manage env                   Show remote .env file
```

Run `mailr --help` or `mailr <command> --help` for all options and flags.

### Configuration

All commands resolve connection details in this order:

1. **CLI flags** (`--host`, `--key`, `--user`, `--dir`) — highest priority
2. **Environment variables** (`MAILR_HOST`, `MAILR_SSH_KEY`, `MAILR_SSH_USER`, `MAILR_DIR`)
3. **Config file** (`~/.mailr/config.json`) — saved by `mailr setup` or `mailr manage init`
4. **Defaults** — `http://localhost:4802`

## API

### Admin Endpoints (server-wide)

Authenticated with `MAILR_ADMIN_TOKEN` via `Authorization: Bearer <token>` header. If no admin token is set, these are unrestricted (local dev mode).

```
POST   /api/domains                       Create a mail domain
GET    /api/domains                       List all domains
GET    /api/domains/:id                   Get domain details
DELETE /api/domains/:id                   Delete a domain
POST   /api/domains/:id/dkim/generate     Generate DKIM key pair
```

### Client Endpoints (per-domain)

Authenticated with the domain's auth token (returned when the domain is created).

```
POST   /api/domains/:id/addresses         Register an email address
GET    /api/domains/:id/addresses         List registered addresses
DELETE /api/domains/:id/addresses/:aid    Delete an address
POST   /api/domains/:id/send             Send an outbound email
GET    /api/domains/:id/messages/poll     Poll for inbound messages
POST   /api/domains/:id/messages/ack      Acknowledge received messages
GET    /api/domains/:id/messages          List messages (filterable)
GET    /api/domains/:id/messages/:mid     Get a specific message
```

### Hello-Message Authenticated Send

Agents can send email directly using Ethereum signature authentication — no domain token needed.

```
POST   /api/send                          Send email (Authorization: Hello <base64>)
```

The `from` address must be registered with a bound `ethereum_address`. The hello-message signer must match that address.

### Infrastructure

```
GET    /ws                                WebSocket for real-time inbound push
GET    /health                            Health check
```

### Sending Email

```bash
curl -X POST https://mail.example.com/api/domains/dom_abc123/send \
  -H "Authorization: Bearer tok_..." \
  -H "Content-Type: application/json" \
  -d '{
    "from": "agent@example.com",
    "to": ["recipient@gmail.com"],
    "cc": ["team@example.com"],
    "subject": "Deployment Report",
    "body_text": "Build #142 deployed successfully.",
    "body_html": "<h1>Build #142</h1><p>Deployed successfully.</p>"
  }'
```

### Polling for Inbound

```bash
# Get undelivered inbound messages
curl https://mail.example.com/api/domains/dom_abc123/messages/poll \
  -H "Authorization: Bearer tok_..."

# Acknowledge them so they don't show up again
curl -X POST https://mail.example.com/api/domains/dom_abc123/messages/ack \
  -H "Authorization: Bearer tok_..." \
  -H "Content-Type: application/json" \
  -d '{"message_ids": ["msg_abc123", "msg_def456"]}'
```

### WebSocket Protocol

Connect to `/ws` for real-time inbound message delivery:

```jsonc
// Client sends
{ "type": "auth", "token": "tok_..." }
{ "type": "subscribe", "domainId": "dom_..." }
{ "type": "ack", "messageId": "msg_..." }

// Server sends
{ "type": "auth_ok" }
{ "type": "subscribed", "domainId": "dom_..." }
{ "type": "message", "messageId": "msg_...", "from": "...", "to": [...], "subject": "...", "body_text": "...", "body_html": "..." }
```

## DNS Setup

mailr requires four DNS records for full email functionality:

| Type | Name | Value | Purpose |
|------|------|-------|---------|
| A | `mail.example.com` | `<server-ip>` | Points domain to server |
| MX | `mail.example.com` | `10 mail.example.com.` | Routes inbound email to server |
| TXT | `mail.example.com` | `v=spf1 ip4:<server-ip> -all` | Authorizes server for outbound |
| TXT | `default._domainkey.mail.example.com` | `v=DKIM1; k=rsa; p=...` | DKIM public key for verification |

The `/deploy-mailr` skill configures these automatically if you're using Route 53. Otherwise, it tells you exactly what to add.

Generate the DKIM record:

```bash
curl -X POST https://mail.example.com/api/domains/dom_abc123/dkim/generate \
  -H "Authorization: Bearer $MAILR_ADMIN_TOKEN"

# Returns:
# {
#   "selector": "default",
#   "dns_record": "default._domainkey.mail.example.com",
#   "dns_value": "v=DKIM1; k=rsa; p=MIIBIjANBg..."
# }
```

## Manual Deployment

If you prefer to deploy without Claude Code:

```bash
# Build
go build -o mailr ./cmd/mailr

# Deploy to AWS (or digitalocean)
./mailr deploy aws mail.example.com

# Get admin token
ssh -i ~/.ssh/mailr-deploy-key.pem ubuntu@<ip> 'sudo cat /opt/mailr/.admin-token'

# Create domain
curl -X POST https://mail.example.com/api/domains \
  -H "Authorization: Bearer <admin-token>" \
  -H "Content-Type: application/json" \
  -d '{"name": "mail.example.com"}'

# Generate DKIM
curl -X POST https://mail.example.com/api/domains/<domain-id>/dkim/generate \
  -H "Authorization: Bearer <admin-token>"

# Add DNS records (A, MX, SPF, DKIM TXT) — see DNS Setup above
```

### Local Development

```bash
# Run locally
./mailr serve --domain localhost --http :4802 --smtp :2525

# Create a domain (no admin token needed in local dev)
curl -X POST http://localhost:4802/api/domains \
  -H "Content-Type: application/json" \
  -d '{"name": "localhost"}'
```

## Integration with aiprod

mailr is designed as the server-side relay for [aiprod](https://github.com/aimxlabs/aiprod)'s email system. Once mailr is deployed, configure aiprod to use it:

```bash
AIPROD_MAILR_URL=https://mail.example.com \
AIPROD_MAILR_DOMAIN_ID=dom_abc123 \
AIPROD_MAILR_AUTH_TOKEN=tok_xyz789 \
./aiprod serve
```

In relay mode, aiprod:
- Sends outbound email via mailr's `/api/domains/:id/send` endpoint (mailr handles DKIM signing and MX delivery)
- Polls mailr every 15 seconds for inbound messages and stores them locally
- Disables its own SMTP server (mailr handles port 25)
- All existing `/api/v1/email/*` endpoints work identically — the relay is transparent to agents

Agents can also register addresses via mailr's API so only mail to registered addresses is accepted. Bind an Ethereum address to enable hello-message authenticated sending:

```bash
# Register an address for an agent with Ethereum identity binding
curl -X POST https://mail.example.com/api/domains/dom_abc123/addresses \
  -H "Authorization: Bearer tok_xyz789" \
  -H "Content-Type: application/json" \
  -d '{"local_part": "deploy-agent", "label": "Deployment Agent", "ethereum_address": "0x2c7536e3..."}'

# Agent sends directly via hello-message auth (no domain token needed)
curl -X POST https://mail.example.com/api/send \
  -H "Authorization: Hello eyJtZXNzYWdlIjoi..." \
  -H "Content-Type: application/json" \
  -d '{"from": "deploy-agent@mail.example.com", "to": ["alice@gmail.com"], "subject": "Build complete", "body_text": "Done."}'

# Poll for just that agent's mail
curl "https://mail.example.com/api/domains/dom_abc123/messages/poll?address_id=addr_..." \
  -H "Authorization: Bearer tok_xyz789"
```

## Architecture

- **Language**: Go 1.26
- **SMTP**: [go-smtp](https://github.com/emersion/go-smtp) (inbound server)
- **HTTP**: [chi](https://github.com/go-chi/chi) router
- **WebSocket**: [gorilla/websocket](https://github.com/gorilla/websocket)
- **Database**: SQLite via [modernc.org/sqlite](https://modernc.org/sqlite) (pure Go, no CGO)
- **CLI**: [Cobra](https://github.com/spf13/cobra)
- **Deployment**: Docker + Caddy (automatic HTTPS via Let's Encrypt)

## Development

```bash
go build ./cmd/mailr
go test ./...
```

## License

MIT
