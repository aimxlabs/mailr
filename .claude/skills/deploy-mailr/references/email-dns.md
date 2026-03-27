# Email DNS Reference

Detailed DNS configuration for mailr deployments. Read this when configuring DNS in Phase 3 and Phase 6.

## Required DNS Records

Email delivery requires four DNS record types. All are critical for production use.

### A Record (Phase 3)

Points the domain to the server's IP address.

```
<DOMAIN>    A    <PUBLIC_IP>    TTL: 300
```

Required for: HTTPS API access, MX resolution.

### MX Record (Phase 3)

Tells other mail servers where to deliver email for this domain.

```
<DOMAIN>    MX    10 <DOMAIN>.    TTL: 300
```

- Priority `10` is standard for a single server
- The trailing dot after the domain is required in DNS zone files
- Without this record, no external mail server will deliver to your domain

### SPF Record (Phase 6)

Tells receiving servers which IPs are authorized to send email for your domain.

```
<DOMAIN>    TXT    "v=spf1 ip4:<PUBLIC_IP> -all"    TTL: 300
```

- `ip4:<PUBLIC_IP>` authorizes your server
- `-all` means reject email from any other IP (strict)
- Without SPF, outbound email is likely to be marked as spam

### DKIM Record (Phase 6)

Publishes the public key used to verify DKIM signatures on outbound email.

```
default._domainkey.<DOMAIN>    TXT    "<DNS_VALUE>"    TTL: 300
```

The `<DNS_VALUE>` is returned by the DKIM generate endpoint:
```bash
curl -X POST https://<DOMAIN>/api/domains/<DOMAIN_ID>/dkim/generate \
  -H "Authorization: Bearer $MAILR_ADMIN_TOKEN"
```

Response:
```json
{
  "selector": "default",
  "dns_record": "default._domainkey.<DOMAIN>",
  "dns_value": "v=DKIM1; k=rsa; p=<BASE64_PUBLIC_KEY>"
}
```

## Optional: DMARC Record

DMARC tells receiving servers what to do when SPF or DKIM fail.

```
_dmarc.<DOMAIN>    TXT    "v=DMARC1; p=none; rua=mailto:dmarc@<DOMAIN>"    TTL: 300
```

Start with `p=none` (monitoring only), then move to `p=quarantine` or `p=reject` once you confirm everything works.

## Verification

After adding all records, verify propagation:

```bash
# A record
dig <DOMAIN> A +short
# Expected: <PUBLIC_IP>

# MX record
dig <DOMAIN> MX +short
# Expected: 10 <DOMAIN>.

# SPF
dig <DOMAIN> TXT +short
# Expected: "v=spf1 ip4:<PUBLIC_IP> -all"

# DKIM
dig default._domainkey.<DOMAIN> TXT +short
# Expected: "v=DKIM1; k=rsa; p=..."
```

DNS propagation typically takes 1-5 minutes but can take up to 48 hours in some cases.

## AWS Route 53 Specifics

When using Route 53, find the hosted zone first:

```bash
BASE_DOMAIN=$(echo "<DOMAIN>" | awk -F. '{print $(NF-1)"."$NF}')
ZONE_ID=$(aws route53 list-hosted-zones-by-name \
  --dns-name "$BASE_DOMAIN" \
  --query "HostedZones[0].Id" \
  --output text | sed 's|/hostedzone/||')
```

Then use `change-resource-record-sets` with `Action: UPSERT` for each record.

Note: TXT record values in Route 53 must be wrapped in escaped quotes:
```json
{"Value": "\"v=spf1 ip4:1.2.3.4 -all\""}
```

## Security model

mailr has two levels of authentication:

| Token | Env var | Used for | Scope |
|-------|---------|----------|-------|
| **Admin token** | `MAILR_ADMIN_TOKEN` | Domain create/delete, DKIM generation | Server-wide |
| **Domain token** | Returned on domain creation | Send, poll, ack, message list | Per-domain |

- If `MAILR_ADMIN_TOKEN` is not set on the server, admin endpoints are unrestricted (safe for local dev).
- Domain tokens authenticate API clients (aiprod instances) per domain.
- Inbound SMTP has no token auth — it validates that the recipient domain is registered.

## Error recovery

- **MX lookup fails for outbound**: Check that the recipient domain has MX records (`dig recipient.com MX`)
- **Inbound email not arriving**: Verify MX record points to your server, check port 25 is open (`telnet <IP> 25`)
- **Outbound marked as spam**: Verify SPF and DKIM records are propagated, check with [mail-tester.com](https://www.mail-tester.com)
- **DKIM verification fails**: Ensure the TXT record value matches exactly (no truncation, proper quoting)
- **Port 25 blocked**: Some cloud providers block outbound port 25 by default — check your provider's docs
