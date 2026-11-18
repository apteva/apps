-- Presale: a machine_pool profile may take a limited number of orders ahead
-- of hardware. NULL keeps the old behaviour (an unbounded waiting line with
-- nothing sold ahead in availability); 0 sells only free machines.
ALTER TABLE hosting_profiles ADD COLUMN presale_limit INTEGER;
ALTER TABLE hosting_profiles ADD COLUMN presale_note TEXT NOT NULL DEFAULT '';

-- A presale hold has no machine yet, so machine_id becomes nullable. NULLs
-- are distinct in the unique index, so many presale holds can coexist.
CREATE TABLE hosting_reservations_007 (
  id                  TEXT PRIMARY KEY,
  profile_key         TEXT    NOT NULL REFERENCES hosting_profiles(key),
  machine_id          INTEGER REFERENCES hosting_machines(id),
  external_ref        TEXT    NOT NULL,
  status              TEXT    NOT NULL DEFAULT 'held'
                      CHECK (status IN ('held', 'confirmed', 'released', 'expired')),
  expires_at          TIMESTAMP NOT NULL,
  tenant_id           TEXT,
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO hosting_reservations_007 (id, profile_key, machine_id, external_ref, status, expires_at, tenant_id, created_at, updated_at)
  SELECT id, profile_key, machine_id, external_ref, status, expires_at, tenant_id, created_at, updated_at
  FROM hosting_reservations
  WHERE machine_id IN (SELECT id FROM hosting_machines)
    AND profile_key IN (SELECT key FROM hosting_profiles);
DROP TABLE hosting_reservations;
ALTER TABLE hosting_reservations_007 RENAME TO hosting_reservations;
CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_reservations_machine_held
  ON hosting_reservations(machine_id) WHERE status = 'held';
CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_reservations_ref_held
  ON hosting_reservations(profile_key, external_ref) WHERE status = 'held';
