# mailr — Claude Code project context

Mail relay for AI agents. Receives inbound email via SMTP, delivers outbound email via MX lookup with DKIM signing, and exposes an HTTP API + WebSocket for aiprod clients to send/receive messages. Supports hello-message (Ethereum signature) authentication for agent-direct sends.

## Quick reference

- **Language**: Go 1.26
- **Build**: `go build ./cmd/mailr`
- **Run**: `mailr serve --domain mail.example.com`
- **Deploy**: `mailr deploy aws <domain>` or `mailr deploy digitalocean <domain>`

## Architecture

```
cmd/mailr/main.go             CLI entrypoint
internal/
  cli/                         Cobra CLI (serve, setup, deploy, manage)
    cloud-init.sh              Server provisioning script (embedded, runs on remote VM)
  store/store.go               SQLite persistence (domains, messages, queue)
  smtp/server.go               Inbound SMTP server (go-smtp)
  relay/relay.go               Outbound delivery (MX lookup, DKIM signing, retry queue)
  api/server.go                HTTP API + WebSocket (chi router)
  db/                          SQLite open + migration utilities
Dockerfile                     Multi-stage Go build
docker-compose.yml             mailr + Caddy (automatic HTTPS)
Caddyfile                      Reverse proxy for HTTPS API
```

## Send authentication

Two auth paths for outbound email:

- **Domain token** (`POST /api/domains/{id}/send`): Bearer token auth. Validates that the `from` address domain matches the authenticated domain, and if the domain has registered addresses, the `from` address must be one of them. Used by aiprod-to-mailr relay calls.
- **Hello-message** (`POST /api/send`): Ethereum signature auth via `Authorization: Hello <base64>`. Verifies the hello-message signature (see `hello-message-go`), recovers the signer's Ethereum address, and checks it matches the `ethereum_address` bound to the `from` email address in the `addresses` table. No domain token needed — agents authenticate directly.

Addresses are registered via `POST /api/domains/{id}/addresses` with an optional `ethereum_address` field to bind an Ethereum identity to an email address.

## Skills

- `/deploy-mailr` — Autonomous end-to-end deployment: provisions cloud server, configures DNS (A, MX, SPF, DKIM), creates mail domain, and verifies the full pipeline.
