-- Existing plaintext rows are migrated by OnMount before workers start.
-- Protect future direct SQL writes too: the historical column holds references.
CREATE TABLE ssh_credential_migrations (
    id INTEGER PRIMARY KEY,
    snapshot_ref TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TRIGGER instances_ssh_credential_insert
BEFORE INSERT ON instances
WHEN NEW.ssh_private_key != '' AND (
    length(NEW.ssh_private_key) != 77 OR
    substr(NEW.ssh_private_key, 1, 13) != 'ssh-vault:v1:' OR
    substr(NEW.ssh_private_key, 14) GLOB '*[^0-9a-f]*'
)
BEGIN
    SELECT RAISE(ABORT, 'SSH private keys must be stored in the credential vault');
END;

CREATE TRIGGER instances_ssh_credential_update
BEFORE UPDATE OF ssh_private_key ON instances
WHEN NEW.ssh_private_key != '' AND (
    length(NEW.ssh_private_key) != 77 OR
    substr(NEW.ssh_private_key, 1, 13) != 'ssh-vault:v1:' OR
    substr(NEW.ssh_private_key, 14) GLOB '*[^0-9a-f]*'
)
BEGIN
    SELECT RAISE(ABORT, 'SSH private keys must be stored in the credential vault');
END;
