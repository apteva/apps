-- Additive only: legacy messages remain untouched. Never reconstruct their
-- historical content using a mutable current template.
ALTER TABLE messages ADD COLUMN template_snapshot TEXT;
