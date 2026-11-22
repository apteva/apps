# Changelog

## 0.1.1

- Allowlist, exclusion and limit edits are now gated on the server-verified
  operator identity (`X-Apteva-Operator-ID`), not on the presence of a
  `session` cookie, which any caller could supply. The operator's own API
  key now works; agent, app-to-app and delegated-user calls are refused.

## 0.1.0

- First release: allowlist-gated host discovery, TCP connect scans, UDP
  DNS/NTP/SNMP probes, service fingerprinting, NVD/KEV/EPSS CVE matching,
  exposure rules, findings triage, Markdown reports, scan diffs and a panel.
