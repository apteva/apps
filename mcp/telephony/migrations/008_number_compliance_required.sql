ALTER TABLE number_purchase_intents
    ADD COLUMN compliance_required INTEGER NOT NULL DEFAULT 0;
