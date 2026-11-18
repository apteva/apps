-- Credits ledger. Every change happens in one transaction that writes an
-- entry row and updates the wallet's stored balances, so a balance read is
-- one row and history can always rebuild it.
--
-- Amounts are integers in the wallet's unit. Currency units are micro-units
-- (usd = 1/1,000,000 dollar). Units never mix.
--
-- Lot accounting: `remaining` is credit that has not been spent, expired or
-- voided. It INCLUDES money currently held. `held` is the part of `remaining`
-- reserved by active holds. So for every wallet:
--   available + held = SUM(lots.remaining)
--   held             = SUM(lots.held) = SUM(active holds.amount)

CREATE TABLE IF NOT EXISTS credit_wallets (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id          TEXT    NOT NULL,
  subject_type        TEXT    NOT NULL,
  subject_id          TEXT    NOT NULL,
  unit                TEXT    NOT NULL,
  available           INTEGER NOT NULL DEFAULT 0 CHECK (available >= 0),
  held                INTEGER NOT NULL DEFAULT 0 CHECK (held >= 0),
  status              TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active','frozen','closed')),
  billing_customer_id INTEGER NOT NULL DEFAULT 0,
  low_threshold       INTEGER NOT NULL DEFAULT 0,
  low_state           TEXT    NOT NULL DEFAULT 'empty' CHECK (low_state IN ('ok','low','empty')),
  metadata            TEXT    NOT NULL DEFAULT '{}',
  created_at          TEXT    NOT NULL,
  updated_at          TEXT    NOT NULL,
  UNIQUE (project_id, subject_type, subject_id, unit)
);

CREATE TABLE IF NOT EXISTS credit_lots (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  wallet_id   INTEGER NOT NULL REFERENCES credit_wallets(id),
  source      TEXT    NOT NULL CHECK (source IN ('purchase','promo','refund','reward','adjustment','transfer')),
  source_ref  TEXT    NOT NULL DEFAULT '',
  original    INTEGER NOT NULL CHECK (original > 0),
  remaining   INTEGER NOT NULL CHECK (remaining >= 0),
  held        INTEGER NOT NULL DEFAULT 0 CHECK (held >= 0 AND held <= remaining),
  refunded    INTEGER NOT NULL DEFAULT 0 CHECK (refunded >= 0),
  priority    INTEGER NOT NULL DEFAULT 100,
  expires_at  TEXT,
  refundable  INTEGER NOT NULL DEFAULT 0,
  paid        INTEGER NOT NULL DEFAULT 0, -- prepaid customer money (purchase, or transferred from purchase)
  status      TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active','depleted','expired','voided')),
  metadata    TEXT    NOT NULL DEFAULT '{}',
  created_at  TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_credit_lots_source_ref ON credit_lots(wallet_id, source_ref) WHERE source_ref != '';
CREATE INDEX IF NOT EXISTS ix_credit_lots_spend ON credit_lots(wallet_id, status, priority, expires_at, id);
CREATE INDEX IF NOT EXISTS ix_credit_lots_expiry ON credit_lots(status, expires_at);

CREATE TABLE IF NOT EXISTS credit_holds (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  wallet_id       INTEGER NOT NULL REFERENCES credit_wallets(id),
  amount          INTEGER NOT NULL CHECK (amount > 0),
  committed       INTEGER NOT NULL DEFAULT 0,
  shortfall       INTEGER NOT NULL DEFAULT 0,
  status          TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active','committed','released','expired')),
  reference_app   TEXT    NOT NULL,
  reference_type  TEXT    NOT NULL,
  reference_id    TEXT    NOT NULL,
  scope           TEXT    NOT NULL DEFAULT '',
  expires_at      TEXT    NOT NULL,
  metadata        TEXT    NOT NULL DEFAULT '{}',
  created_at      TEXT    NOT NULL,
  settled_at      TEXT,
  UNIQUE (wallet_id, reference_app, reference_type, reference_id)
);
CREATE INDEX IF NOT EXISTS ix_credit_holds_expiry ON credit_holds(status, expires_at);
CREATE INDEX IF NOT EXISTS ix_credit_holds_wallet ON credit_holds(wallet_id, status, id);

-- Which lots a hold drew from, in spend order.
CREATE TABLE IF NOT EXISTS credit_hold_lots (
  hold_id INTEGER NOT NULL REFERENCES credit_holds(id),
  lot_id  INTEGER NOT NULL REFERENCES credit_lots(id),
  seq     INTEGER NOT NULL,
  amount  INTEGER NOT NULL CHECK (amount > 0),
  PRIMARY KEY (hold_id, lot_id)
);

-- Append-only ledger. delta_available / delta_held are the entry's effect
-- on the wallet; summing them rebuilds the stored balances.
CREATE TABLE IF NOT EXISTS credit_entries (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  wallet_id        INTEGER NOT NULL REFERENCES credit_wallets(id),
  kind             TEXT    NOT NULL CHECK (kind IN ('credit','hold','commit','release','expire','void','adjust','transfer')),
  amount           INTEGER NOT NULL,
  delta_available  INTEGER NOT NULL,
  delta_held       INTEGER NOT NULL,
  available_after  INTEGER NOT NULL,
  held_after       INTEGER NOT NULL,
  lot_id           INTEGER,
  hold_id          INTEGER,
  idempotency_key  TEXT    NOT NULL DEFAULT '',
  actor            TEXT    NOT NULL DEFAULT '',
  metadata         TEXT    NOT NULL DEFAULT '{}',
  created_at       TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_credit_entries_idem ON credit_entries(wallet_id, idempotency_key) WHERE idempotency_key != '';
CREATE INDEX IF NOT EXISTS ix_credit_entries_wallet ON credit_entries(wallet_id, id);

-- Per-lot changes to `remaining` (spend, expiry, void, negative adjust,
-- transfer out). With lot originals this answers "unspent paid credit at
-- date D" for the year-end liability report.
CREATE TABLE IF NOT EXISTS credit_lot_moves (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  entry_id   INTEGER NOT NULL REFERENCES credit_entries(id),
  lot_id     INTEGER NOT NULL REFERENCES credit_lots(id),
  delta      INTEGER NOT NULL,
  created_at TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_credit_lot_moves_lot ON credit_lot_moves(lot_id, created_at);

CREATE TABLE IF NOT EXISTS credit_caps (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  wallet_id    INTEGER NOT NULL REFERENCES credit_wallets(id),
  scope        TEXT    NOT NULL DEFAULT '',
  cap_window   TEXT    NOT NULL CHECK (cap_window IN ('day','week','month','lifetime')),
  limit_amount INTEGER NOT NULL CHECK (limit_amount >= 0),
  metadata     TEXT    NOT NULL DEFAULT '{}',
  created_at   TEXT    NOT NULL,
  updated_at   TEXT    NOT NULL,
  UNIQUE (wallet_id, scope, cap_window)
);

-- One row per cap per window period ('2026-11-18', '2026-W47', '2026-11',
-- 'lifetime'). A hold counts against the period it was created in.
CREATE TABLE IF NOT EXISTS credit_cap_usage (
  cap_id     INTEGER NOT NULL REFERENCES credit_caps(id),
  period     TEXT    NOT NULL,
  spent      INTEGER NOT NULL DEFAULT 0,
  held       INTEGER NOT NULL DEFAULT 0 CHECK (held >= 0),
  reached_at TEXT,
  PRIMARY KEY (cap_id, period)
);

CREATE TABLE IF NOT EXISTS credit_hold_caps (
  hold_id INTEGER NOT NULL REFERENCES credit_holds(id),
  cap_id  INTEGER NOT NULL REFERENCES credit_caps(id),
  period  TEXT    NOT NULL,
  PRIMARY KEY (hold_id, cap_id)
);

-- Paid top-ups. A purchase lot exists only once billing reports the
-- invoice paid.
CREATE TABLE IF NOT EXISTS credit_topups (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  wallet_id        INTEGER NOT NULL REFERENCES credit_wallets(id),
  kind             TEXT    NOT NULL DEFAULT 'manual' CHECK (kind IN ('manual','auto')),
  amount           INTEGER NOT NULL CHECK (amount > 0),
  price_cents      INTEGER NOT NULL CHECK (price_cents > 0),
  currency         TEXT    NOT NULL,
  invoice_id       INTEGER NOT NULL DEFAULT 0,
  status           TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','paid','failed')),
  lot_id           INTEGER,
  idempotency_key  TEXT    NOT NULL DEFAULT '',
  error            TEXT    NOT NULL DEFAULT '',
  created_at       TEXT    NOT NULL,
  updated_at       TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_credit_topups_idem ON credit_topups(wallet_id, idempotency_key) WHERE idempotency_key != '';
CREATE INDEX IF NOT EXISTS ix_credit_topups_invoice ON credit_topups(invoice_id);
-- At most one auto-reload in flight per wallet.
CREATE UNIQUE INDEX IF NOT EXISTS ux_credit_topups_auto_inflight ON credit_topups(wallet_id) WHERE kind = 'auto' AND status = 'pending';

-- Money the ledger could not take: a commit larger than the hold and the
-- balance, or a refund of credit that was already spent. For a person to
-- review; balances never go negative.
CREATE TABLE IF NOT EXISTS credit_shortfalls (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  wallet_id   INTEGER NOT NULL REFERENCES credit_wallets(id),
  kind        TEXT    NOT NULL CHECK (kind IN ('commit_overrun','refund_spent')),
  amount      INTEGER NOT NULL CHECK (amount > 0),
  reference   TEXT    NOT NULL DEFAULT '',
  status      TEXT    NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
  note        TEXT    NOT NULL DEFAULT '',
  created_at  TEXT    NOT NULL,
  resolved_at TEXT,
  UNIQUE (wallet_id, kind, reference)
);

-- Processed billing deliveries, so a retried event never applies twice.
CREATE TABLE IF NOT EXISTS credit_billing_events (
  event_key  TEXT PRIMARY KEY,
  created_at TEXT NOT NULL
);
