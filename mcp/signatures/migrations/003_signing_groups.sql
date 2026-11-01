-- Parallel signing. signing_order stays the unique position within the
-- envelope (table-level UNIQUE, which SQLite cannot drop without a
-- table rebuild that would cascade into fields/field_values);
-- signing_group is the round: recipients sharing it sign at the same
-- time. Existing envelopes keep sequential behavior (group = order).
ALTER TABLE recipients ADD COLUMN signing_group INTEGER;
UPDATE recipients SET signing_group = signing_order WHERE signing_group IS NULL;
