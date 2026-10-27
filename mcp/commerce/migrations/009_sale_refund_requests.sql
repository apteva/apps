-- Keep refund intents durable independently of processor execution and ledger reconciliation.
CREATE TABLE commerce_sale_refund_requests (
 id INTEGER PRIMARY KEY,
 project_id TEXT NOT NULL,
 sale_id INTEGER NOT NULL REFERENCES commerce_sales(id),
 invoice_id INTEGER NOT NULL,
 amount_cents INTEGER NOT NULL CHECK(amount_cents > 0),
 currency TEXT NOT NULL,
 reason TEXT NOT NULL,
 idempotency_key TEXT NOT NULL,
 billing_key TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending',
 refund_json TEXT NOT NULL DEFAULT '{}',
 execution_error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
 updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(project_id,idempotency_key),
 UNIQUE(billing_key)
);
CREATE INDEX ix_commerce_sale_refunds ON commerce_sale_refund_requests(project_id,sale_id,id);
