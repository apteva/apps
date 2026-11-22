# Security Scanner

Network exposure scanning for Apteva agents and analysts. Scans only
targets on a per-project allowlist that people edit in the panel.

## What it does

- Expands IPs, CIDRs and hostnames, and drops anything not on the
  allowlist or on the exclusion list. Hostnames are resolved at scan time;
  each resolved address must also be in scope (allowlisted hostname or
  covering IP/CIDR).
- Discovers live hosts with TCP probes (no root needed), connect-scans TCP
  ports, and sends read-only DNS, NTP and SNMPv3 discovery probes on UDP.
- Fingerprints services from banners and handshakes: SSH, FTP, SMTP,
  POP3/IMAP, MySQL/MariaDB, PostgreSQL, Redis, Memcached, HTTP(S),
  Elasticsearch, CouchDB, Docker API, VNC, Telnet, TLS certificates.
- Matches product versions to CVEs through the NVD API, flags entries on the
  CISA KEV list and adds EPSS scores. Results are cached for 24 hours. Only
  product names and versions leave the app, never target addresses.
- Applies exposure rules (unauthenticated Redis/Elasticsearch/Docker,
  exposed databases, RDP, SMB, VNC, Telnet, cleartext FTP, TLS hygiene).
- Deduplicates findings, marks them fixed when a rescan no longer sees them,
  and produces severity-grouped Markdown reports and scan diffs.

## Safety

- Allowlist, exclusions and limits can only be changed from a signed-in
  dashboard session (the panel). MCP tools are read-only for scope.
- Rate limit (default 100 probes/s, ceiling 1,000), parallel hosts
  (default 10, ceiling 64), max addresses per scan (default 1,024).
- One scan per project at a time; "Stop all scans" in the panel.
- Detection only: no exploit payloads, no credential guessing, no SNMP
  community strings.

## Configuration

- `nvd_api_key` (config) or `NVD_API_KEY` (env): optional NVD API key.
  Without it, NVD lookups are spaced 6.5 s apart (public limit).

## Development

```sh
GOWORK=off go test ./...
cd ../.. && bun run scripts/build-panels.ts --app security-scanner
```
