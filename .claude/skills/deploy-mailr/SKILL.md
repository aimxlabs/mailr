---
name: deploy-mailr
description: >-
  Deploys a fully working mailr mail relay instance to the cloud (AWS or DigitalOcean),
  configures DNS (A + MX records), generates DKIM keys, sets up a mail domain, and
  verifies inbound/outbound email — all autonomously. Use when the user asks to deploy
  mailr, set up a mail relay, or get email working for their AI agents.
disable-model-invocation: true
argument-hint: "[provider]"
allowed-tools: Bash(mailr *), Bash(go *), Bash(aws *), Bash(doctl *), Bash(curl *), Bash(dig *), Bash(ssh *), Bash(gh *), Bash(openssl *), Bash(nslookup *)
---

# Autonomous mailr Deployment

You are an autonomous deployment agent. Your job is to get mailr fully operational with minimal user input. Follow these phases in order. At each phase, do the work — don't just describe it.

If the user passed a provider argument (e.g. `/deploy-mailr aws`), skip the provider detection and use that provider directly: $ARGUMENTS

---

## Phase 1: Gather information and detect environment

Ask the user only what you cannot determine yourself:

1. **Domain**: "What domain should mailr use for email? (e.g. `mail.yourdomain.com`)"
2. **Cloud provider**: Check which credentials are available:
   - Run `aws sts get-caller-identity` to detect AWS
   - Run `doctl account get` to detect DigitalOcean
   - If both or neither are available, ask the user which to use

Do NOT ask about:
- Region (default to `us-east-1` for AWS, `nyc1` for DO)
- Instance size (default to `t3.small` / `s-1vcpu-1gb`)
- Any other configuration — use sensible defaults

---

## Phase 2: Build and deploy the server

First, ensure the mailr binary is built:
```bash
cd /path/to/mailr && go build -o mailr ./cmd/mailr
```

**For AWS:**
```bash
mailr deploy aws <DOMAIN> <REGION>
```

**For DigitalOcean:**
```bash
mailr deploy digitalocean <DOMAIN> <REGION>
```

Capture the output — extract the **public IP** from the command output.

### If the deploy fails

**No default VPC:** Look up available VPCs and subnets:
```bash
aws ec2 describe-vpcs --region <REGION> --query "Vpcs[*].[VpcId,Tags[?Key=='Name']|[0].Value]" --output text
aws ec2 describe-subnets --region <REGION> --filters "Name=vpc-id,Values=<VPC_ID>" --query "Subnets[*].[SubnetId,AvailabilityZone,MapPublicIpOnLaunch]" --output text
```
Pick a VPC and a **public** subnet, then retry:
```bash
mailr deploy aws <DOMAIN> <REGION> --vpc-id <VPC_ID> --subnet-id <SUBNET_ID>
```

**Network issues:** Tell the user to check their credentials and network access, and abort.

---

## Phase 3: Configure DNS

Mail requires both A and MX records. Check if you can manage DNS programmatically:

**AWS Route 53:**
```bash
# Extract the base domain
BASE_DOMAIN=$(echo "<DOMAIN>" | awk -F. '{print $(NF-1)"."$NF}')

# Find the hosted zone
ZONE_ID=$(aws route53 list-hosted-zones-by-name \
  --dns-name "$BASE_DOMAIN" \
  --query "HostedZones[0].Id" \
  --output text | sed 's|/hostedzone/||')

# Create A record
aws route53 change-resource-record-sets \
  --hosted-zone-id "$ZONE_ID" \
  --change-batch '{
    "Changes": [{
      "Action": "UPSERT",
      "ResourceRecordSet": {
        "Name": "<DOMAIN>",
        "Type": "A",
        "TTL": 300,
        "ResourceRecords": [{"Value": "<PUBLIC_IP>"}]
      }
    }]
  }'

# Create MX record
aws route53 change-resource-record-sets \
  --hosted-zone-id "$ZONE_ID" \
  --change-batch '{
    "Changes": [{
      "Action": "UPSERT",
      "ResourceRecordSet": {
        "Name": "<DOMAIN>",
        "Type": "MX",
        "TTL": 300,
        "ResourceRecords": [{"Value": "10 <DOMAIN>."}]
      }
    }]
  }'
```

**If DNS cannot be managed programmatically**, tell the user:
> Add these DNS records for `<DOMAIN>` and tell me when they're done:
> - A record → `<PUBLIC_IP>`
> - MX record → `10 <DOMAIN>.`

Wait for DNS propagation:
```bash
dig <DOMAIN> A +short     # Should return the public IP
dig <DOMAIN> MX +short    # Should return the MX record
```

---

## Phase 4: Wait for health + HTTPS

```bash
# Poll until the HTTP API is healthy (up to 5 minutes)
for i in $(seq 1 60); do
  curl -sf "http://<PUBLIC_IP>:4802/health" && break
  sleep 5
done

# Then wait for HTTPS (Let's Encrypt via Caddy)
for i in $(seq 1 30); do
  curl -sf "https://<DOMAIN>/health" && break
  sleep 10
done
```

If health checks time out, SSH in and check logs:
```bash
ssh -i ~/.ssh/mailr-deploy-key.pem ubuntu@<PUBLIC_IP> 'tail -50 /var/log/cloud-init-output.log'
```

---

## Phase 5: Retrieve admin token and create domain

### Retrieve the admin token

```bash
MAILR_ADMIN_TOKEN=$(ssh -i ~/.ssh/mailr-deploy-key.pem ubuntu@<PUBLIC_IP> 'sudo cat /opt/mailr/.admin-token')
```

If SSH is not available, tell the user:
> SSH into your server and run: `sudo cat /opt/mailr/.admin-token`

### Create a mail domain

```bash
curl -s -X POST "https://<DOMAIN>/api/domains" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $MAILR_ADMIN_TOKEN" \
  -d '{"name": "<DOMAIN>"}'
```

Capture the **domain ID** and **auth token** from the response.

---

## Phase 6: Generate DKIM keys and configure DNS

### Generate DKIM key pair

```bash
DKIM_RESPONSE=$(curl -s -X POST "https://<DOMAIN>/api/domains/<DOMAIN_ID>/dkim/generate" \
  -H "Authorization: Bearer $MAILR_ADMIN_TOKEN")
echo "$DKIM_RESPONSE"
```

Extract the `dns_record` and `dns_value` from the response.

### Add DKIM DNS record

**AWS Route 53:**
```bash
aws route53 change-resource-record-sets \
  --hosted-zone-id "$ZONE_ID" \
  --change-batch '{
    "Changes": [{
      "Action": "UPSERT",
      "ResourceRecordSet": {
        "Name": "<DNS_RECORD>",
        "Type": "TXT",
        "TTL": 300,
        "ResourceRecords": [{"Value": "\"<DNS_VALUE>\""}]
      }
    }]
  }'
```

**If DNS cannot be managed programmatically**, tell the user:
> Add this TXT record and tell me when it's done:
> - `<DNS_RECORD>` → `<DNS_VALUE>`

### Add SPF record

```bash
aws route53 change-resource-record-sets \
  --hosted-zone-id "$ZONE_ID" \
  --change-batch '{
    "Changes": [{
      "Action": "UPSERT",
      "ResourceRecordSet": {
        "Name": "<DOMAIN>",
        "Type": "TXT",
        "TTL": 300,
        "ResourceRecords": [{"Value": "\"v=spf1 ip4:<PUBLIC_IP> -all\""}]
      }
    }]
  }'
```

Or tell the user to add: `TXT <DOMAIN> → "v=spf1 ip4:<PUBLIC_IP> -all"`

Verify DKIM record propagation:
```bash
dig default._domainkey.<DOMAIN> TXT +short
```

---

## Phase 7: Verify end-to-end

### Test inbound (SMTP receiving)

Send a test email to the server via SMTP:
```bash
# Use the server's SMTP port to send a test
curl --url "smtp://<PUBLIC_IP>:25" \
  --mail-from "test@example.com" \
  --mail-rcpt "test@<DOMAIN>" \
  -T - <<EOF
From: test@example.com
To: test@<DOMAIN>
Subject: mailr deployment test
Date: $(date -R)

This is a test email sent during mailr deployment.
EOF
```

Then check if it was received:
```bash
curl -s "https://<DOMAIN>/api/domains/<DOMAIN_ID>/messages/poll" \
  -H "Authorization: Bearer <AUTH_TOKEN>" | python3 -m json.tool
```

### Test outbound (API send)

```bash
curl -s -X POST "https://<DOMAIN>/api/domains/<DOMAIN_ID>/send" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <AUTH_TOKEN>" \
  -d '{
    "from": "test@<DOMAIN>",
    "to": ["test@<DOMAIN>"],
    "subject": "mailr outbound test",
    "body_text": "This is a test of outbound delivery."
  }'
```

---

## Phase 8: Report to user

Present a clear summary:

```
mailr is deployed and ready.

  Server:       https://<DOMAIN>
  Health:       https://<DOMAIN>/health
  SMTP:         <PUBLIC_IP>:25
  Instance:     <INSTANCE_ID> (<REGION>)
  IP:           <PUBLIC_IP>
  SSH:          ssh -i ~/.ssh/mailr-deploy-key.pem ubuntu@<PUBLIC_IP>

  Domain:       <DOMAIN>
  Domain ID:    <DOMAIN_ID>
  Auth Token:   <AUTH_TOKEN>
  DKIM:         ✓ configured (selector: default)
  SPF:          ✓ configured

  DNS Records:
    A      <DOMAIN>                      → <PUBLIC_IP>
    MX     <DOMAIN>                      → 10 <DOMAIN>.
    TXT    default._domainkey.<DOMAIN>   → v=DKIM1; k=rsa; p=...
    TXT    <DOMAIN>                      → v=spf1 ip4:<PUBLIC_IP> -all

  Send email via API:
    curl -X POST https://<DOMAIN>/api/domains/<DOMAIN_ID>/send \
      -H "Authorization: Bearer <AUTH_TOKEN>" \
      -H "Content-Type: application/json" \
      -d '{"from":"you@<DOMAIN>","to":["recipient@example.com"],"subject":"Hello","body_text":"..."}'

  Poll for inbound email:
    curl https://<DOMAIN>/api/domains/<DOMAIN_ID>/messages/poll \
      -H "Authorization: Bearer <AUTH_TOKEN>"

  Management:
    mailr manage status
    mailr manage update
    mailr manage logs
    mailr manage backup
```

---

## Key files reference

| File | Purpose |
|------|---------|
| `internal/cli/deploy.go` | `mailr deploy` — AWS/DigitalOcean provisioning + teardown |
| `internal/cli/cloud-init.sh` | Server provisioning (Docker, mailr, Caddy) — runs on remote VM |
| `internal/cli/manage.go` | `mailr manage` — remote server management via SSH |
| `internal/smtp/server.go` | Inbound SMTP server |
| `internal/relay/relay.go` | Outbound delivery + DKIM signing + queue |
| `internal/api/server.go` | HTTP API + WebSocket |
| `internal/store/store.go` | Domain, message, queue persistence |
| `internal/cli/setup.go` | Interactive setup wizard |
| `Dockerfile` | Multi-stage Go build |
| `docker-compose.yml` | mailr + Caddy (HTTPS) |
