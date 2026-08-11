CREATE TABLE IF NOT EXISTS instructor_profiles (
    id                       TEXT      PRIMARY KEY,
    community_id             TEXT      NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    member_id                TEXT      REFERENCES members(id),
    display_name             TEXT      NOT NULL,
    avatar_storage_file_id   TEXT,
    professional_title       TEXT      NOT NULL DEFAULT '',
    sales_bio                TEXT      NOT NULL DEFAULT '',
    credentials_json         TEXT      NOT NULL DEFAULT '[]',
    links_json               TEXT      NOT NULL DEFAULT '[]',
    accomplishments_json     TEXT      NOT NULL DEFAULT '[]',
    public_visible           INTEGER   NOT NULL DEFAULT 1 CHECK(public_visible IN (0, 1)),
    archived_at              TIMESTAMP,
    created_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_instructor_profiles_community
ON instructor_profiles(community_id, archived_at, display_name);

ALTER TABLE course_details
ADD COLUMN instructor_ids_json TEXT NOT NULL DEFAULT '[]';

ALTER TABLE course_details
ADD COLUMN primary_instructor_id TEXT;
