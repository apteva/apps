-- Outbound payment requests are separate from booked ledger transactions.
-- Reserve submission durably BEFORE a provider call; never replay an uncertain write.
CREATE TABLE bank_payments (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    request_key TEXT NOT NULL,
    request_json TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'draft',
    provider_id TEXT NOT NULL DEFAULT '',
    provider_status TEXT NOT NULL DEFAULT '',
    response_json TEXT NOT NULL DEFAULT '{}',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, request_key)
);
CREATE INDEX idx_bank_payments_project ON bank_payments(project_id, created_at);
-- A provider operation may only reconcile to one local request per connection.
CREATE UNIQUE INDEX idx_bank_payments_provider
 ON bank_payments(project_id, json_extract(request_json,'$.connection_id'), provider_id)
 WHERE provider_id != '';
