-- Preserve ambiguous existing identities; do not merge financial histories.
ALTER TABLE customers ADD COLUMN email_conflict INTEGER NOT NULL DEFAULT 0;
UPDATE customers SET email=lower(trim(email)) WHERE email IS NOT NULL;
UPDATE customers SET email_conflict=1,
 metadata=json_set(CASE WHEN json_valid(metadata) THEN metadata ELSE '{}' END,'$.billing_email_conflict',json('true'))
WHERE deleted_at IS NULL AND email IS NOT NULL AND email<>'' AND id NOT IN
 (SELECT min(id) FROM customers WHERE deleted_at IS NULL AND email IS NOT NULL AND email<>'' GROUP BY project_id,email);
CREATE UNIQUE INDEX ux_customer_active_email ON customers(project_id,email)
 WHERE deleted_at IS NULL AND email IS NOT NULL AND email<>'' AND email_conflict=0;
CREATE TABLE billing_invoice_sequences(project_id TEXT NOT NULL, series TEXT NOT NULL, next_value INTEGER NOT NULL, PRIMARY KEY(project_id,series));
CREATE TABLE billing_invoice_snapshots(invoice_id INTEGER PRIMARY KEY REFERENCES invoices(id), customer_json TEXT NOT NULL, issuer_json TEXT NOT NULL, document_json TEXT NOT NULL, provenance TEXT NOT NULL DEFAULT 'finalized');
ALTER TABLE invoices ADD COLUMN tax_treatment TEXT NOT NULL DEFAULT 'standard';
ALTER TABLE invoices ADD COLUMN collection_hold INTEGER NOT NULL DEFAULT 0;
ALTER TABLE payments ADD COLUMN source_payment_id INTEGER REFERENCES payments(id);
CREATE INDEX ix_payments_source ON payments(source_payment_id);
CREATE TABLE billing_provider_operations(
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL, invoice_id INTEGER REFERENCES invoices(id), kind TEXT NOT NULL,
 caller_key TEXT NOT NULL, connection_id INTEGER NOT NULL, request_json TEXT NOT NULL,
 provider_id TEXT NOT NULL DEFAULT '', response_json TEXT NOT NULL DEFAULT '{}',
 state TEXT NOT NULL DEFAULT 'pending', lease_until INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, error TEXT NOT NULL DEFAULT '',
 UNIQUE(project_id,kind,caller_key));
CREATE TABLE billing_payment_locks(invoice_id INTEGER PRIMARY KEY REFERENCES invoices(id), operation_id TEXT NOT NULL);
CREATE TABLE billing_webhook_inbox(id TEXT PRIMARY KEY, connection_id INTEGER NOT NULL, event_type TEXT NOT NULL, object_json TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
CREATE INDEX ix_billing_inbox_pending ON billing_webhook_inbox(state,created_at);
CREATE TABLE billing_outbox(id TEXT PRIMARY KEY, project_id TEXT NOT NULL, topic TEXT NOT NULL, payload TEXT NOT NULL, delivered_at TEXT, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE INDEX ix_billing_outbox_pending ON billing_outbox(delivered_at,created_at);
CREATE TABLE billing_refund_reconciliation(provider_payment_id TEXT PRIMARY KEY, project_id TEXT NOT NULL, invoice_id INTEGER NOT NULL, recorded_cents INTEGER NOT NULL DEFAULT 0, observed_cents INTEGER NOT NULL DEFAULT 0);
ALTER TABLE billing_refund_requests ADD COLUMN reopen_invoice INTEGER NOT NULL DEFAULT 0;
CREATE TABLE billing_customer_provider_ids(customer_id INTEGER NOT NULL REFERENCES customers(id), connection_id INTEGER NOT NULL, provider_id TEXT NOT NULL, PRIMARY KEY(customer_id,connection_id), UNIQUE(connection_id,provider_id));
CREATE TABLE billing_method_connections(method_id INTEGER PRIMARY KEY REFERENCES billing_payment_methods(id), connection_id INTEGER NOT NULL);
CREATE INDEX ix_billing_invoice_created ON invoices(project_id,created_at DESC,id DESC);
CREATE INDEX ix_billing_customer_updated ON customers(project_id,updated_at DESC,id DESC);
CREATE INDEX ix_billing_payment_customer ON payments(project_id,customer_id,received_at DESC,id DESC);
-- Existing untracked provider sessions must block overlapping new charges until reconciled.
INSERT OR IGNORE INTO billing_payment_locks SELECT invoice_id,'legacy-checkout:'||id FROM billing_checkout_sessions WHERE status='pending';
INSERT OR IGNORE INTO billing_payment_locks SELECT invoice_id,'legacy-collection:'||id FROM billing_collection_attempts WHERE status NOT IN ('succeeded','failed','canceled');

CREATE TABLE billing_provider_refunds (
 id TEXT PRIMARY KEY, provider_payment_id TEXT NOT NULL, amount_cents INTEGER NOT NULL,
 status TEXT NOT NULL, received_at TEXT NOT NULL
);

CREATE TABLE billing_detached_methods(provider_id TEXT PRIMARY KEY,detached_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
INSERT OR IGNORE INTO billing_detached_methods(provider_id) SELECT provider_payment_method_id FROM billing_payment_methods WHERE detached_at IS NOT NULL;
CREATE TABLE billing_payment_connections(payment_id INTEGER PRIMARY KEY REFERENCES payments(id),connection_id INTEGER NOT NULL);

-- Match normalized timestamp ORDER BY expressions used for historical offsets.
CREATE INDEX ix_billing_invoice_instant ON invoices(project_id,julianday(created_at) DESC,id DESC) WHERE deleted_at IS NULL;
CREATE INDEX ix_billing_payment_instant ON payments(project_id,julianday(received_at) DESC,id DESC);
CREATE INDEX ix_billing_payment_invoice_instant ON payments(project_id,invoice_id,julianday(received_at) DESC,id DESC);
CREATE INDEX ix_billing_payment_customer_instant ON payments(project_id,customer_id,julianday(received_at) DESC,id DESC);
