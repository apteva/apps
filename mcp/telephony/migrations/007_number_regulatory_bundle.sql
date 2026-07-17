ALTER TABLE number_purchase_intents
    ADD COLUMN selected_address_sid TEXT NOT NULL DEFAULT '';

ALTER TABLE number_purchase_intents
    ADD COLUMN selected_bundle_sid TEXT NOT NULL DEFAULT '';
