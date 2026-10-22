# TURN 0.2.1

- Domains appears once in setup and declares its minimum version.
- Managed DNS and public TLS: bind Domains, choose a relay subdomain and zone, and accept the Let's Encrypt subscriber agreement. TURN creates a direct A record and uses ACME DNS-01 for issuance and automatic renewal on the execution host.
- Renewed certificates load atomically without restarting connections. Only the active execution can mutate DNS; passive starts use persisted certificates.
- Account/private keys are encrypted in the app database with a stable install-config storage key. Preserve app data and this key for migration/restore.
- DNS writes preserve existing records. Challenge cleanup uses exact record IDs and snapshots, with a durable receipt for retry after interruption.
- The themed native panel adds automatic/manual certificate selection, DNS zone discovery, terms acceptance, issuance/renewal status and scheduled checks.
- Adds `db.write.app` and `platform.apps.call` permissions plus an optional Domains binding. Existing manual TLS configuration remains supported.

No other apps or platform internals are modified. Raw TURN listeners remain UDP/TCP 3478, TCP 5349, and UDP 49160–49223.
