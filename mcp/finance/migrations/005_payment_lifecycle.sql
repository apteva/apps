ALTER TABLE bank_payments ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bank_payments ADD COLUMN callback_state TEXT NOT NULL DEFAULT '';
ALTER TABLE bank_payments ADD COLUMN callback_verified INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bank_payments ADD COLUMN continued INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bank_payments ADD COLUMN last_checked_at TEXT NOT NULL DEFAULT '';
CREATE TABLE bank_payment_event_cursors (
 project_id TEXT NOT NULL,
 connection_id INTEGER NOT NULL,
 after_id INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(project_id,connection_id)
);
