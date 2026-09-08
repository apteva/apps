-- Stable source identities survive bill voiding and prevent replay duplication.
CREATE TABLE bill_sources (
 project_id TEXT NOT NULL, source_key TEXT NOT NULL, payload_hash TEXT NOT NULL,
 bill_id INTEGER NOT NULL REFERENCES bills(id), created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(project_id,source_key), UNIQUE(bill_id)
);
CREATE TABLE bill_payment_requests (
 project_id TEXT NOT NULL, request_key TEXT NOT NULL, payload_hash TEXT NOT NULL,
 payment_id INTEGER NOT NULL REFERENCES bill_payments(id), PRIMARY KEY(project_id,request_key)
);
CREATE TABLE bill_adjustments (
 id INTEGER PRIMARY KEY, project_id TEXT NOT NULL, bill_id INTEGER NOT NULL REFERENCES bills(id),
 request_key TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('credit','refund','reversal')),
 amount_minor INTEGER NOT NULL CHECK(amount_minor > 0), reason TEXT NOT NULL, actor TEXT NOT NULL,
 payment_id INTEGER REFERENCES bill_payments(id), created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(project_id,request_key)
);
CREATE TABLE bill_documents (
 bill_id INTEGER NOT NULL REFERENCES bills(id), file_id INTEGER NOT NULL, label TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(bill_id,file_id)
);

CREATE INDEX bill_adjustments_bill ON bill_adjustments(bill_id,kind);
INSERT OR IGNORE INTO bill_documents(bill_id,file_id,label) SELECT id,attached_file_id,'Original attachment' FROM bills WHERE attached_file_id IS NOT NULL;
