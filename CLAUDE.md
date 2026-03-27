# mailr — Claude Code project context

Mail relay for AI agents. Receives inbound email via SMTP, delivers outbound email via MX lookup with DKIM signing, and exposes an HTTP API + WebSocket for aiprod clients to send/receive messages.

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

## Skills

- `/deploy-mailr` — Autonomous end-to-end deployment: provisions cloud server, configures DNS (A, MX, SPF, DKIM), creates mail domain, and verifies the full pipeline.
