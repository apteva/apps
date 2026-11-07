-- Locally verified provenance must survive replacement of provider metadata.
ALTER TABLE ad_resources ADD COLUMN audience_provenance_json TEXT NOT NULL DEFAULT '{}';
