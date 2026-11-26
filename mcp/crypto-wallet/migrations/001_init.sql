-- Crypto Wallet v0.1 schema.
--
-- wallets       one row per address
-- wallet_keys   envelope-encrypted private keys (built-in provider only)
-- policies      versioned; the current policy is the highest version
-- allowlist     recipient addresses; agent-proposed ones need approval
-- proposals     every outgoing transfer an agent asked for, and its fate
-- approvals     who decided a proposal, and where
-- transactions  on-chain transfers in and out
-- ledger        every event above, append-only (triggers refuse edits)
-- watch_state   last block scanned for incoming transfers per wallet

CREATE TABLE IF NOT EXISTS wallets (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    TEXT    NOT NULL DEFAULT '',
  name          TEXT    NOT NULL,
  chain         TEXT    NOT NULL,
  address       TEXT    NOT NULL,
  provider      TEXT    NOT NULL DEFAULT 'builtin',
  frozen        INTEGER NOT NULL DEFAULT 0,
  frozen_reason TEXT    NOT NULL DEFAULT '',
  created_by    INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT    NOT NULL,
  UNIQUE (chain, address)
);

CREATE TABLE IF NOT EXISTS wallet_keys (
  wallet_id     INTEGER PRIMARY KEY REFERENCES wallets(id),
  key_nonce     BLOB    NOT NULL,
  key_cipher    BLOB    NOT NULL,
  dek_nonce     BLOB    NOT NULL,
  dek_cipher    BLOB    NOT NULL,
  master_fp     TEXT    NOT NULL,
  created_at    TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS policies (
  id                        INTEGER PRIMARY KEY AUTOINCREMENT,
  wallet_id                 INTEGER NOT NULL REFERENCES wallets(id),
  version                   INTEGER NOT NULL,
  per_tx_usd_micros         INTEGER NOT NULL,
  daily_usd_micros          INTEGER NOT NULL,
  approval_usd_micros       INTEGER NOT NULL,
  tokens_json               TEXT    NOT NULL,
  new_recipient_delay_hours INTEGER NOT NULL,
  updated_by                INTEGER NOT NULL DEFAULT 0,
  created_at                TEXT    NOT NULL,
  UNIQUE (wallet_id, version)
);

CREATE TABLE IF NOT EXISTS allowlist (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  wallet_id           INTEGER NOT NULL REFERENCES wallets(id),
  address             TEXT    NOT NULL,
  label               TEXT    NOT NULL DEFAULT '',
  reason              TEXT    NOT NULL DEFAULT '',
  status              TEXT    NOT NULL,          -- pending | approved | rejected | removed
  proposed_by_agent   INTEGER NOT NULL DEFAULT 0,
  added_by_user       INTEGER NOT NULL DEFAULT 0,
  decided_by          INTEGER NOT NULL DEFAULT 0,
  decided_at          TEXT    NOT NULL DEFAULT '',
  usable_after        TEXT    NOT NULL DEFAULT '',
  approval_message_id INTEGER NOT NULL DEFAULT 0,
  created_at          TEXT    NOT NULL,
  UNIQUE (wallet_id, address)
);
CREATE INDEX IF NOT EXISTS allowlist_message ON allowlist(approval_message_id);

CREATE TABLE IF NOT EXISTS proposals (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  wallet_id           INTEGER NOT NULL REFERENCES wallets(id),
  agent_id            INTEGER NOT NULL DEFAULT 0,
  caller_app          TEXT    NOT NULL DEFAULT '',
  tool_call_id        TEXT    NOT NULL DEFAULT '',
  token               TEXT    NOT NULL,
  to_address          TEXT    NOT NULL,
  amount_base         TEXT    NOT NULL,          -- integer in the token's smallest unit
  amount_display      TEXT    NOT NULL,
  usd_micros          INTEGER NOT NULL,
  memo                TEXT    NOT NULL DEFAULT '',
  state               TEXT    NOT NULL,          -- pending_approval | approved | rejected | refused | sending | sent | confirmed | failed
  refusal_rule        TEXT    NOT NULL DEFAULT '',
  refusal_detail      TEXT    NOT NULL DEFAULT '',
  approval_message_id INTEGER NOT NULL DEFAULT 0,
  tx_hash             TEXT    NOT NULL DEFAULT '',
  nonce               INTEGER NOT NULL DEFAULT -1,
  error               TEXT    NOT NULL DEFAULT '',
  executed_at         TEXT    NOT NULL DEFAULT '',  -- set when the transfer starts counting against caps
  created_at          TEXT    NOT NULL,
  updated_at          TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS proposals_tool_call ON proposals(tool_call_id) WHERE tool_call_id <> '';
CREATE INDEX IF NOT EXISTS proposals_wallet_state ON proposals(wallet_id, state);
CREATE INDEX IF NOT EXISTS proposals_message ON proposals(approval_message_id);

CREATE TABLE IF NOT EXISTS approvals (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  subject     TEXT    NOT NULL,                  -- proposal | recipient
  subject_id  INTEGER NOT NULL,
  decision    TEXT    NOT NULL,                  -- approve | reject
  decided_by  INTEGER NOT NULL DEFAULT 0,
  via         TEXT    NOT NULL,                  -- panel | conversations
  note        TEXT    NOT NULL DEFAULT '',
  created_at  TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS transactions (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  wallet_id    INTEGER NOT NULL REFERENCES wallets(id),
  proposal_id  INTEGER NOT NULL DEFAULT 0,
  direction    TEXT    NOT NULL,                 -- in | out
  token        TEXT    NOT NULL,
  hash         TEXT    NOT NULL,
  log_index    INTEGER NOT NULL DEFAULT -1,
  from_address TEXT    NOT NULL,
  to_address   TEXT    NOT NULL,
  amount_base  TEXT    NOT NULL,
  block_number INTEGER NOT NULL DEFAULT 0,
  status       TEXT    NOT NULL,                 -- pending | confirmed | failed
  fee_wei      TEXT    NOT NULL DEFAULT '',
  created_at   TEXT    NOT NULL,
  confirmed_at TEXT    NOT NULL DEFAULT '',
  UNIQUE (hash, log_index, direction)
);
CREATE INDEX IF NOT EXISTS transactions_wallet ON transactions(wallet_id, id);

CREATE TABLE IF NOT EXISTS ledger (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  wallet_id   INTEGER NOT NULL DEFAULT 0,
  kind        TEXT    NOT NULL,
  actor       TEXT    NOT NULL,                  -- agent:<id> | user:<id> | app:<name> | system
  proposal_id INTEGER NOT NULL DEFAULT 0,
  data_json   TEXT    NOT NULL DEFAULT '{}',
  created_at  TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS ledger_wallet ON ledger(wallet_id, id);

CREATE TRIGGER IF NOT EXISTS ledger_no_update BEFORE UPDATE ON ledger
BEGIN SELECT RAISE(ABORT, 'ledger is append-only'); END;
CREATE TRIGGER IF NOT EXISTS ledger_no_delete BEFORE DELETE ON ledger
BEGIN SELECT RAISE(ABORT, 'ledger is append-only'); END;

CREATE TABLE IF NOT EXISTS watch_state (
  wallet_id  INTEGER PRIMARY KEY REFERENCES wallets(id),
  last_block INTEGER NOT NULL
);
