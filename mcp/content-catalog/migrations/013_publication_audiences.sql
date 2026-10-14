-- Existing labels remain legacy text. Never infer a structured audience.
ALTER TABLE posts ADD COLUMN audience_type TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE posts ADD COLUMN access_policy TEXT NOT NULL DEFAULT '';
ALTER TABLE posts ADD COLUMN group_scope TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE posts ADD COLUMN group_ids_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE posts ADD COLUMN group_labels_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE posts ADD COLUMN publisher_ref_json TEXT NOT NULL DEFAULT 'null';
ALTER TABLE posts ADD COLUMN observed_at TEXT NOT NULL DEFAULT '';
ALTER TABLE posts ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE post_events ADD COLUMN audience TEXT NOT NULL DEFAULT '';
ALTER TABLE post_events ADD COLUMN audience_type TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE post_events ADD COLUMN access_policy TEXT NOT NULL DEFAULT '';
ALTER TABLE post_events ADD COLUMN group_scope TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE post_events ADD COLUMN group_ids_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE post_events ADD COLUMN group_labels_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE post_events ADD COLUMN publisher_ref_json TEXT NOT NULL DEFAULT 'null';
ALTER TABLE post_events ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE post_events ADD COLUMN before_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE post_events ADD COLUMN after_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE post_events ADD COLUMN recorded_at TEXT NOT NULL DEFAULT '';
UPDATE post_events SET recorded_at=observed_at;
CREATE INDEX ix_posts_audience ON posts(project_id,destination,account_ref,audience_type,access_policy,group_scope);
-- A separate identity index avoids deleting or silently merging legacy duplicates.
CREATE TABLE post_external_identities (
 project_id TEXT NOT NULL,destination TEXT NOT NULL,account_ref TEXT NOT NULL,
 external_post_id TEXT NOT NULL,post_id TEXT NOT NULL REFERENCES posts(id),
 PRIMARY KEY(project_id,destination,account_ref,external_post_id)
);
INSERT INTO post_external_identities(project_id,destination,account_ref,external_post_id,post_id)
SELECT project_id,LOWER(destination),account_ref,external_post_id,MIN(id) FROM posts
WHERE account_ref<>'' AND external_post_id<>''
GROUP BY project_id,LOWER(destination),account_ref,external_post_id HAVING COUNT(*)=1;
