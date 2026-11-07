-- Short ownership reservations replace holding a per-call mutex over carrier I/O.
CREATE TABLE call_carrier_commands (
 call_id TEXT PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 token TEXT NOT NULL,
 owner TEXT NOT NULL,
 operation TEXT NOT NULL,
 lease_until TEXT NOT NULL
);
