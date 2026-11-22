# Using the Security Scanner

The Security Scanner finds open ports, identifies services and versions,
matches them to known CVEs and flags risky exposures. It only scans
addresses on the project's allowlist, which only people can edit in the
Security panel. Scans detect only: no exploits, no password guessing.

## Workflow

1. Run `security_targets_list` to see what you may scan. If the allowlist is
   empty, stop and ask a person to add targets in the Security panel. Never
   try to scan systems that are not on the list.
2. Run `security_scope_check` with the targets you plan to scan. It sends
   nothing and shows which addresses are allowed or dropped and why.
3. Pick a profile:
   - `quick`: 100 common TCP ports. Use for a first look.
   - `standard` (default): ports 1-1024, common high ports, DNS/NTP/SNMP.
   - `full`: all 65,535 TCP ports. Slow; use on a few key hosts.
   - `custom`: your own `tcp_ports` like "22,80,8000-8100".
4. Start with `security_scan_start`. Scans run in the background; poll
   `security_scan_status` every 15-30 seconds rather than in a tight loop.
   Only one scan runs per project at a time. `security_scan_cancel` stops it.
5. Read results with `security_findings_search` (most severe first) and
   `security_hosts_search` (open ports, products, versions).
6. Triage with `security_findings_update`. Version-based CVE findings can be
   false positives when a Linux distribution backports fixes without changing
   the version; say so instead of overstating the risk.
7. Summarise with `security_report_generate` and, after a rescan,
   `security_diff` to show new ports, closed ports, new and fixed findings.

## Severity

- critical: CVSS 9.0+, any CVE on the CISA Known Exploited Vulnerabilities
  list, or an unauthenticated Docker API.
- high: CVSS 7.0-8.9, or an unauthenticated data service such as Redis or
  Elasticsearch.
- medium: CVSS 4.0-6.9, exposed databases, RDP, SMB, VNC, weak TLS.
- low: CVSS below 4, version disclosure, self-signed certificates.
- info: context such as missing HTTP security headers.

## Rules

- Never scan anything not returned as allowed by `security_scope_check`.
- Do not raise speed or range limits; they are set by people.
- Report findings with their evidence; do not claim a system is compromised.
