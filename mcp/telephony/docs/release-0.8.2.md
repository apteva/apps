# Telephony 0.8.2

This patch hardens direct SIP TLS certificate renewal. Telephony now fingerprints
the certificate and private-key contents on each TLS handshake, so a rapid
replacement is detected even when the filesystem keeps the same inode, size,
and modification time. Invalid or partially written renewals continue using the
last known-good certificate and expose the renewal error in diagnostics.

## Verification

The same-metadata replacement regression passes, along with the full standalone
Go test suite, Go vet/build, frontend typecheck, frontend tests, and headless
client build.

No installation was upgraded and no staging, production, carrier, or live-call
configuration was changed by this release.
