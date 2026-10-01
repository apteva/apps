PRAGMA foreign_keys = ON;

CREATE TABLE events (
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  sport TEXT NOT NULL,
  competition TEXT NOT NULL,
  home TEXT NOT NULL,
  away TEXT NOT NULL,
  starts_at INTEGER NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('scheduled','live','finished','cancelled')),
  home_score INTEGER,
  away_score INTEGER,
  source TEXT NOT NULL,
  external_id TEXT NOT NULL,
  connection_id INTEGER NOT NULL,
  received_at INTEGER NOT NULL,
  example INTEGER NOT NULL DEFAULT 0 CHECK(example IN (0,1)),
  PRIMARY KEY(project_id,id),
  UNIQUE(project_id,connection_id,source,external_id)
);
CREATE INDEX event_schedule ON events(project_id,example,sport,starts_at);
CREATE TABLE markets (
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  type TEXT NOT NULL CHECK(type='match_winner'),
  rules TEXT NOT NULL CHECK(rules IN ('regulation','match_completed')),
  PRIMARY KEY(project_id,id),
  UNIQUE(project_id,event_id,type,rules),
  FOREIGN KEY(project_id,event_id) REFERENCES events(project_id,id)
);
CREATE TABLE odds_observations (
  id INTEGER PRIMARY KEY,
  project_id TEXT NOT NULL,
  market_id TEXT NOT NULL,
  selection TEXT NOT NULL CHECK(selection IN ('home','draw','away')),
  bookmaker TEXT NOT NULL,
  odds_micros INTEGER NOT NULL CHECK(odds_micros>1000000 AND odds_micros<=1000000000),
  source TEXT NOT NULL,
  connection_id INTEGER NOT NULL,
  observed_at INTEGER NOT NULL,
  received_at INTEGER NOT NULL,
  snapshot_id TEXT NOT NULL,
  FOREIGN KEY(project_id,market_id) REFERENCES markets(project_id,id),
  UNIQUE(project_id,id),
  UNIQUE(project_id,snapshot_id,selection,bookmaker)
);
CREATE TABLE event_aliases (
  project_id TEXT NOT NULL,
  source TEXT NOT NULL,
  connection_id INTEGER NOT NULL,
  external_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  PRIMARY KEY(project_id,source,connection_id,external_id),
  FOREIGN KEY(project_id,event_id) REFERENCES events(project_id,id)
);
CREATE INDEX odds_latest ON odds_observations(project_id,market_id,selection,observed_at DESC);
CREATE TABLE quote_heads (
  project_id TEXT NOT NULL,
  market_id TEXT NOT NULL,
  source TEXT NOT NULL,
  connection_id INTEGER NOT NULL,
  snapshot_id TEXT NOT NULL,
  PRIMARY KEY(project_id,market_id,source,connection_id),
  FOREIGN KEY(project_id,market_id) REFERENCES markets(project_id,id)
);
CREATE TABLE predictions (
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  market_id TEXT NOT NULL,
  model TEXT NOT NULL,
  probabilities TEXT NOT NULL,
  features TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,id),
  FOREIGN KEY(project_id,market_id) REFERENCES markets(project_id,id)
);
CREATE TABLE explanations (
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  prediction_id TEXT NOT NULL,
  connection_id INTEGER NOT NULL,
  model TEXT NOT NULL,
  text TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,id),
  FOREIGN KEY(project_id,prediction_id) REFERENCES predictions(project_id,id)
);
CREATE TABLE provider_routes (
  project_id TEXT NOT NULL,
  role TEXT NOT NULL CHECK(role IN ('sports_data','odds','llm','execution')),
  sport TEXT NOT NULL,
  connection_id INTEGER NOT NULL CHECK(connection_id>0),
  model TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(project_id,role,sport)
);
CREATE TABLE bankrolls (
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  name TEXT NOT NULL,
  currency TEXT NOT NULL CHECK(currency IN ('EUR','USD','GBP')),
  initial_minor INTEGER NOT NULL CHECK(initial_minor>0),
  max_stake_bps INTEGER NOT NULL CHECK(max_stake_bps BETWEEN 1 AND 1000),
  max_exposure_bps INTEGER NOT NULL CHECK(max_exposure_bps BETWEEN 1 AND 5000),
  example INTEGER NOT NULL CHECK(example IN (0,1)),
  created_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,id)
);
CREATE TABLE proposals (
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  bankroll_id TEXT NOT NULL,
  prediction_id TEXT NOT NULL,
  quote_id INTEGER NOT NULL,
  stake_minor INTEGER NOT NULL CHECK(stake_minor>0),
  probability REAL NOT NULL CHECK(probability>0 AND probability<1),
  expected_value REAL NOT NULL,
  rationale TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('proposed','accepted')),
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,id),
  FOREIGN KEY(project_id,bankroll_id) REFERENCES bankrolls(project_id,id),
  FOREIGN KEY(project_id,prediction_id) REFERENCES predictions(project_id,id),
  FOREIGN KEY(project_id,quote_id) REFERENCES odds_observations(project_id,id)
);
CREATE TABLE bets (
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  proposal_id TEXT NOT NULL,
  bankroll_id TEXT NOT NULL,
  stake_minor INTEGER NOT NULL,
  odds_micros INTEGER NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('open','won','lost','void')),
  accepted_at INTEGER NOT NULL,
  settled_at INTEGER,
  settlement_note TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(project_id,id),
  UNIQUE(project_id,proposal_id),
  FOREIGN KEY(project_id,proposal_id) REFERENCES proposals(project_id,id),
  FOREIGN KEY(project_id,bankroll_id) REFERENCES bankrolls(project_id,id)
);
CREATE TABLE ledger_entries (
  id INTEGER PRIMARY KEY,
  project_id TEXT NOT NULL,
  bankroll_id TEXT NOT NULL,
  transaction_id TEXT NOT NULL,
  account TEXT NOT NULL CHECK(account IN ('cash','locked','equity','pnl')),
  amount_minor INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  FOREIGN KEY(project_id,bankroll_id) REFERENCES bankrolls(project_id,id),
  UNIQUE(project_id,transaction_id,account)
);
CREATE INDEX ledger_balance ON ledger_entries(project_id,bankroll_id,account);
CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY,
  project_id TEXT NOT NULL,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
