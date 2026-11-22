# Changelog

## 0.1.2

- Bare-IP scans now send a server name (SNI and HTTP Host), so TLS and
  certificate checks work on virtual-hosting servers (Caddy, nginx,
  Traefik, CDNs) that refuse handshakes without one. The name is an
  allowlisted hostname that resolves to the address, else the reverse-DNS
  name if it resolves back. Excluded names are never used, and the name
  never widens scope. It is recorded as the host's hostname.
- A plain-HTTP "request sent to an HTTPS port" 400 reply no longer stops
  fingerprinting; the port is probed as TLS instead.

## 0.1.1

- Allowlist, exclusion and limit edits are now gated on the server-verified
  operator identity (`X-Apteva-Operator-ID`), not on the presence of a
  `session` cookie, which any caller could supply. The operator's own API
  key now works; agent, app-to-app and delegated-user calls are refused.

## 0.1.0

- First release: allowlist-gated host discovery, TCP connect scans, UDP
  DNS/NTP/SNMP probes, service fingerprinting, NVD/KEV/EPSS CVE matching,
  exposure rules, findings triage, Markdown reports, scan diffs and a panel.
