# SSH credential encryption

Instances stores SSH private keys in a per-install encrypted file vault, not in
SQLite. The historical `instances.ssh_private_key` column now holds an opaque
`ssh-vault:v1:` reference. OVH order state holds `ssh_credential_ref`; neither
order JSON nor serialized create inputs include private keys. Inserts and updates
through the store encrypt first; SQL triggers reject direct plaintext writes to
the instance column.

Each credential uses AES-256-GCM with a fresh random nonce. Its reference and
owning instance/order ID are authenticated, preventing reference substitution
between records. Tampering, missing credentials, missing keys and unavailable
vaults fail closed. Private keys are decrypted only in process memory for
internal operations and excluded from API JSON and struct formatting.

The default master key is 32 random bytes at
`<APTEVA_DATA_DIR>/ssh-credentials.key` (0600). Encrypted credential files live in
`<APTEVA_DATA_DIR>/ssh-credentials/` (0700 directory, 0600 files). Losing the master
key makes the vault unrecoverable; Instances refuses to generate a replacement
when existing encrypted files or database references are present.

For a separately managed secret, provision a 32-byte raw master-key file and set
`APTEVA_INSTANCES_SSH_KEY_FILE` to its absolute path. The file must already exist,
be a regular file and have no group/other access (0400 or 0600). Instances never
creates the override file. This supports a read-only secret mounted by the host
or deployment's secret manager. The SDK currently provides no secrets-store API.

Back up the database and encrypted credential directory consistently. Store the
master-key backup separately with restricted access; a copy of the entire data
directory including the default master key is sufficient to decrypt the vault.
This protects database-only leaks, not compromise of the running app or access
to both ciphertext and its master key. Changing the environment variable does
not rotate the master key: the mounted file must contain the original bytes.

On mount, before any workers or reconcilers start, Instances migrates existing
instance keys and legacy OVH order keys. Before changing any live value it
serializes the complete SQLite database from the migration's read transaction
(including committed WAL data), encrypts it in the vault, syncs the file and
authenticates it. Its reference is recorded in
`ssh_credential_migrations.snapshot_ref`, in the same transaction as the live
credential changes. Recovery decrypts that reference with owner
`migration:pre-ssh-vault:v1`; its payload is a complete SQLite database image.
No plaintext snapshot is written to disk. If snapshot creation or verification
fails, migration stops without changing existing rows.

Encrypted files are synced before all live database rows are replaced in a
transaction. Existing host IDs, public keys, SSH users, ports, provider metadata,
volumes and pending orders are preserved. No SSH key is regenerated and no host
is modified by migration. Every existing reference is
authenticated on each startup. A failed migration leaves original rows intact;
any unreferenced files created before a failure remain encrypted. No automatic
garbage collection or evidence destruction occurs.

Keep the entire persistent install directory and the master key during upgrades.
Do not downgrade an older binary against a migrated database: it cannot read
vault references. An incident rollback must restore a consistent data-directory
backup, or decrypt the pre-migration snapshot to a separate recovery database and
verify it before replacing anything. That snapshot includes additive migration
014's empty metadata table and guards; remove only the
`instances_ssh_credential_insert` and `instances_ssh_credential_update` triggers
in a recovery copy if an old binary must write plaintext again. Returning to an
older binary also returns to its unsafe credential storage. Preserve the current
database, WAL and vault before recovery to retain changes made after migration.

This migration does not revoke or replace existing SSH keys, prove their origin,
reject legacy key reuse, change SSH accounts, or erase old SQLite pages, WALs,
journals and backups. Preserve incident evidence before maintenance. Rotate any
exposed key on every host still trusting it; handle historical plaintext copies
under the incident's retention and disposal process. New registrations already
generate their own automation keypairs. Restricting root access and enforcing
key provenance across imports require separate enrollment/account changes.
