-- Install-local evidence; never stores API keys or SIP passwords. Fresh reads
-- remain authoritative. Expired evidence cannot authorize an outage fallback.
CREATE TABLE IF NOT EXISTS carrier_verification_evidence (
  project_id TEXT NOT NULL,
  connection_id INTEGER NOT NULL,
  provider TEXT NOT NULL,
  credential_hash TEXT NOT NULL,
  kind TEXT NOT NULL,
  resource_key TEXT NOT NULL,
  value_json TEXT NOT NULL,
  verified_at_ms INTEGER NOT NULL,
  observed_at_ns INTEGER NOT NULL,
  PRIMARY KEY(project_id,connection_id,provider,credential_hash,kind,resource_key)
);

CREATE INDEX IF NOT EXISTS idx_carrier_verification_age ON carrier_verification_evidence(verified_at_ms);
