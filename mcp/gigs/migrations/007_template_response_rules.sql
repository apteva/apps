-- Generic, versioned worker-response policies applied by instruction kind.
-- Rules are frozen into gig instruction bodies at dispatch time.
ALTER TABLE template_versions ADD COLUMN response_rules_json TEXT;
