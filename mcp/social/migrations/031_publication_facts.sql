-- Preserve ambiguous historical values verbatim. They are not evidence of
-- native publication; new published_at values come only from native inspection.
ALTER TABLE post_targets RENAME COLUMN published_at TO legacy_published_at;
ALTER TABLE post_targets ADD COLUMN published_at TEXT;
ALTER TABLE post_targets ADD COLUMN submitted_at TEXT;
ALTER TABLE post_targets ADD COLUMN provider_accepted_at TEXT;
ALTER TABLE post_targets ADD COLUMN provider_reported_published_at TEXT;
ALTER TABLE post_targets ADD COLUMN delivery_recorded_at TEXT;
ALTER TABLE post_targets ADD COLUMN native_uploaded_at TEXT;
ALTER TABLE post_targets ADD COLUMN publication_observed_at TEXT;
ALTER TABLE post_targets ADD COLUMN publication_time_source TEXT NOT NULL DEFAULT '';
ALTER TABLE post_targets ADD COLUMN publication_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE posts RENAME COLUMN published_at TO legacy_published_at;
ALTER TABLE posts ADD COLUMN published_at TEXT;
ALTER TABLE posts ADD COLUMN delivery_recorded_at TEXT;
UPDATE post_targets SET delivery_recorded_at=legacy_published_at WHERE EXISTS(SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.source NOT IN ('history','provider') AND p.imported_at IS NULL);
UPDATE posts SET delivery_recorded_at=legacy_published_at WHERE source NOT IN ('history','provider') AND imported_at IS NULL;
CREATE TABLE social_publication_corrections (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 social_account_id INTEGER NOT NULL,
 target_id INTEGER NOT NULL REFERENCES post_targets(id) ON DELETE CASCADE,
 post_id INTEGER NOT NULL,
 native_post_id TEXT NOT NULL,
 account_binding TEXT NOT NULL,
 content_binding TEXT NOT NULL,
 post_revision INTEGER NOT NULL,
 source_attempt INTEGER NOT NULL,
 before_facts TEXT NOT NULL,
 after_facts TEXT NOT NULL,
 evidence TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TABLE social_publication_record_repairs (
 correction_id TEXT NOT NULL REFERENCES social_publication_corrections(id) ON DELETE CASCADE,
 app TEXT NOT NULL,
 record_id TEXT NOT NULL,
 expected_revision INTEGER NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending',
 result TEXT NOT NULL DEFAULT '{}',
 PRIMARY KEY(correction_id,app,record_id)
);
ALTER TABLE delivery_upload_operations ADD COLUMN account_binding TEXT NOT NULL DEFAULT '';
ALTER TABLE delivery_upload_operations ADD COLUMN source_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE delivery_upload_operations ADD COLUMN content_binding TEXT NOT NULL DEFAULT '';
-- Unbounded durable history, independent of the recent provider-poll ring.
CREATE TABLE social_delivery_recordings (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 target_id INTEGER NOT NULL REFERENCES post_targets(id) ON DELETE CASCADE,
 social_account_id INTEGER NOT NULL,
 source_attempt INTEGER NOT NULL,
 operation_id TEXT NOT NULL,
 status TEXT NOT NULL,
 facts TEXT NOT NULL,
 recorded_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX social_delivery_recordings_target ON social_delivery_recordings(project_id,target_id,id);
INSERT INTO social_delivery_recordings(project_id,target_id,social_account_id,source_attempt,operation_id,status,facts)
SELECT p.project_id,t.id,t.social_account_id,t.attempts,COALESCE(t.publish_operation_id,''),t.status,
 json_object('kind','legacy_snapshot','native_post_id',t.platform_post_id,'url',t.platform_url,'legacy_published_at',t.legacy_published_at,'native_published_at',t.published_at,'delivery_recorded_at',t.delivery_recorded_at)
FROM post_targets t JOIN posts p ON p.id=t.post_id;
CREATE TRIGGER social_delivery_insert AFTER INSERT ON post_targets BEGIN
 INSERT INTO social_delivery_recordings(project_id,target_id,social_account_id,source_attempt,operation_id,status,facts)
 SELECT p.project_id,NEW.id,NEW.social_account_id,NEW.attempts,COALESCE(NEW.publish_operation_id,''),NEW.status,
 json_object('kind','initial_record','native_post_id',NEW.platform_post_id,'url',NEW.platform_url,'native_published_at',NEW.published_at,'native_uploaded_at',NEW.native_uploaded_at,'observed_at',NEW.publication_observed_at,'submitted_at',NEW.submitted_at,'provider_accepted_at',NEW.provider_accepted_at,'delivery_recorded_at',NEW.delivery_recorded_at)
 FROM posts p WHERE p.id=NEW.post_id;
END;
CREATE TRIGGER social_delivery_update AFTER UPDATE ON post_targets
WHEN OLD.status IS NOT NEW.status OR OLD.attempts IS NOT NEW.attempts OR OLD.social_account_id IS NOT NEW.social_account_id OR OLD.publish_operation_id IS NOT NEW.publish_operation_id OR OLD.platform_post_id IS NOT NEW.platform_post_id OR OLD.platform_url IS NOT NEW.platform_url OR OLD.submitted_at IS NOT NEW.submitted_at OR OLD.provider_accepted_at IS NOT NEW.provider_accepted_at OR OLD.delivery_recorded_at IS NOT NEW.delivery_recorded_at OR OLD.published_at IS NOT NEW.published_at OR OLD.native_uploaded_at IS NOT NEW.native_uploaded_at
BEGIN
 UPDATE post_targets SET publication_revision=publication_revision+1 WHERE id=NEW.id AND publication_revision=OLD.publication_revision AND NEW.publication_revision=OLD.publication_revision;
 INSERT INTO social_delivery_recordings(project_id,target_id,social_account_id,source_attempt,operation_id,status,facts)
 SELECT p.project_id,NEW.id,NEW.social_account_id,NEW.attempts,COALESCE(NEW.publish_operation_id,''),NEW.status,
 json_object('kind','recording','native_post_id',NEW.platform_post_id,'url',NEW.platform_url,'native_published_at',NEW.published_at,'native_uploaded_at',NEW.native_uploaded_at,'observed_at',NEW.publication_observed_at,'submitted_at',NEW.submitted_at,'provider_accepted_at',NEW.provider_accepted_at,'delivery_recorded_at',NEW.delivery_recorded_at,'post_revision',p.revision)
 FROM posts p WHERE p.id=NEW.post_id;
END;
