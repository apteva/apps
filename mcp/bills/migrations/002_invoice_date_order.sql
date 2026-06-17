CREATE INDEX IF NOT EXISTS ix_bills_invoice_date
ON bills(project_id, vendor_invoice_date DESC, created_at DESC, id DESC)
WHERE deleted_at IS NULL;
