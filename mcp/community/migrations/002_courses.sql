-- community v0.2: courses.
--
-- A "course" is a space with kind='course'. The original CHECK
-- constraint on spaces.kind allowed only feed|forum|chat, so we
-- rebuild the table to widen it. SQLite can't ALTER CHECK directly;
-- the recommended dance is:
--
--   1. PRAGMA foreign_keys=OFF (FKs from threads/space_members
--      reference spaces.id; OFF lets us swap the table).
--   2. CREATE spaces_new with widened CHECK.
--   3. INSERT SELECT to copy rows.
--   4. DROP old spaces; RENAME new.
--   5. Recreate the indexes the original migration declared.
--   6. PRAGMA foreign_keys=ON.
--
-- Then add the per-course tables: sections (sequenced inside a course
-- space), lessons (sequenced inside a section), per-member progress
-- (state machine: not_started → in_progress → complete with the
-- video's last-known position recorded), and lesson comments.

PRAGMA foreign_keys=OFF;

CREATE TABLE spaces_new (
    id            TEXT    PRIMARY KEY,
    community_id  TEXT    NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    slug          TEXT    NOT NULL,
    name          TEXT    NOT NULL,
    kind          TEXT    NOT NULL DEFAULT 'feed'
                  CHECK(kind IN ('feed','forum','chat','course')),
    visibility    TEXT    NOT NULL DEFAULT 'members'
                  CHECK(visibility IN ('public','members')),
    sort_order    INTEGER NOT NULL DEFAULT 0,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at   TIMESTAMP,
    UNIQUE(community_id, slug)
);
INSERT INTO spaces_new
  (id, community_id, slug, name, kind, visibility, sort_order, created_at, archived_at)
SELECT id, community_id, slug, name, kind, visibility, sort_order, created_at, archived_at
FROM spaces;
DROP TABLE spaces;
ALTER TABLE spaces_new RENAME TO spaces;
CREATE INDEX IF NOT EXISTS idx_spaces_community ON spaces(community_id, archived_at);

PRAGMA foreign_keys=ON;

-- ─── Course content ─────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS sections (
    id          TEXT    PRIMARY KEY,                   -- "sec_<random>"
    space_id    TEXT    NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    title       TEXT    NOT NULL,
    position    INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_sections_space ON sections(space_id, position);

CREATE TABLE IF NOT EXISTS lessons (
    id                       TEXT    PRIMARY KEY,      -- "les_<random>"
    community_id             TEXT    NOT NULL,         -- denormalised for bus filter
    section_id               TEXT    NOT NULL REFERENCES sections(id) ON DELETE CASCADE,
    title                    TEXT    NOT NULL,
    body                     TEXT    NOT NULL DEFAULT '',  -- markdown
    video_storage_key        TEXT,                     -- the storage app's file id (string)
    video_duration_seconds   INTEGER,
    position                 INTEGER NOT NULL DEFAULT 0,
    published_at             TIMESTAMP,                -- null = draft
    created_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_lessons_section ON lessons(section_id, position);
CREATE INDEX IF NOT EXISTS idx_lessons_community ON lessons(community_id, created_at DESC);

CREATE TABLE IF NOT EXISTS lesson_progress (
    lesson_id              TEXT    NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
    member_id              TEXT    NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    status                 TEXT    NOT NULL DEFAULT 'not_started'
                            CHECK(status IN ('not_started','in_progress','complete')),
    completed_at           TIMESTAMP,
    last_position_seconds  INTEGER,
    updated_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(lesson_id, member_id)
);
CREATE INDEX IF NOT EXISTS idx_lesson_progress_member ON lesson_progress(member_id, status);

CREATE TABLE IF NOT EXISTS lesson_comments (
    id           TEXT    PRIMARY KEY,                  -- "lcom_<random>"
    lesson_id    TEXT    NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
    member_id    TEXT    NOT NULL REFERENCES members(id),
    body         TEXT    NOT NULL,                     -- markdown
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_lesson_comments_lesson ON lesson_comments(lesson_id, created_at);
