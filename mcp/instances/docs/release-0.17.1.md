# Instances 0.17.1

SSH private keys now live in a per-install AES-256-GCM credential vault. SQLite
contains only opaque references, including pending OVH order credentials.
Credentials are authenticated to the owning record. Missing keys, unavailable
vaults, tampering and unsafe file permissions fail closed. Struct formatting and
JSON serialization exclude private keys; SQL guards reject plaintext instance
credential writes.

Before migrating any existing credential, startup creates, syncs and verifies an
encrypted snapshot of the complete database, including WAL contents. Credential
updates and the snapshot reference commit together. A failure leaves the old
rows intact. Migration preserves existing SSH identities, hosts, metadata,
volumes and pending orders; it does not regenerate keys or modify remote hosts.
Restarting is idempotent. The master key and credential directory are in the
persistent per-install data directory, outside versioned source/binary folders.

An external master key can be mounted using `APTEVA_INSTANCES_SSH_KEY_FILE`.
Backups must retain the database, vault files and the correct master key. Older
binaries cannot read migrated credential references: restore a consistent backup
for rollback. See [storage and recovery](ssh-credential-storage.md).

Uses app-sdk v0.115.2, the latest descendant release by commit topology.
This release includes v0.17.0 host profiles and v0.16.x runtime recovery fixes.
Exposed-key rotation, account restrictions and historical evidence retention
remain incident-response tasks; encrypting storage does not revoke old keys.
