-- Interactive lessons: ordered content blocks per lesson, reusable
-- sandboxed elements (versioned HTML/JS programs), per-member element
-- state, recorded steps, and per-community runner bindings.
--
-- Lessons with no blocks keep rendering the classic way (body, video,
-- resources, quizzes, assignments), so no backfill is needed.

ALTER TABLE lessons ADD COLUMN layout TEXT NOT NULL DEFAULT 'article';

CREATE TABLE IF NOT EXISTS course_elements (
    id                          TEXT PRIMARY KEY,           -- "elm_<random>"
    community_id                TEXT NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    slug                        TEXT NOT NULL,
    name                        TEXT NOT NULL,
    description                 TEXT NOT NULL DEFAULT '',
    draft_source                TEXT NOT NULL DEFAULT '',
    draft_runners_json          TEXT NOT NULL DEFAULT '[]',
    draft_settings_schema_json  TEXT NOT NULL DEFAULT '{}',
    current_version             INTEGER NOT NULL DEFAULT 0, -- 0 = never published
    archived_at                 TIMESTAMP,
    created_at                  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (community_id, slug)
);

-- Published versions are immutable: lessons pin a version so an element
-- edit never changes a live lesson until the block is moved forward.
CREATE TABLE IF NOT EXISTS course_element_versions (
    element_id            TEXT    NOT NULL REFERENCES course_elements(id) ON DELETE CASCADE,
    version               INTEGER NOT NULL,
    source                TEXT    NOT NULL,
    checksum              TEXT    NOT NULL,
    runners_json          TEXT    NOT NULL DEFAULT '[]',
    settings_schema_json  TEXT    NOT NULL DEFAULT '{}',
    published_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (element_id, version)
);

CREATE TABLE IF NOT EXISTS lesson_blocks (
    id                   TEXT    PRIMARY KEY,              -- "blk_<random>"
    lesson_id            TEXT    NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
    position             INTEGER NOT NULL DEFAULT 0,
    slot                 TEXT    NOT NULL DEFAULT 'main',  -- main | stage | side
    type                 TEXT    NOT NULL,
    title                TEXT    NOT NULL DEFAULT '',
    element_id           TEXT    REFERENCES course_elements(id),
    element_version      INTEGER,
    settings_json        TEXT    NOT NULL DEFAULT '{}',
    required_steps_json  TEXT    NOT NULL DEFAULT '[]',
    height               INTEGER,
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_lesson_blocks_lesson ON lesson_blocks(lesson_id, position);
CREATE INDEX IF NOT EXISTS idx_lesson_blocks_element ON lesson_blocks(element_id, element_version);

CREATE TABLE IF NOT EXISTS element_state (
    block_id    TEXT NOT NULL REFERENCES lesson_blocks(id) ON DELETE CASCADE,
    member_id   TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    lesson_id   TEXT NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
    state_json  TEXT NOT NULL DEFAULT '{}',
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (block_id, member_id)
);

CREATE TABLE IF NOT EXISTS lesson_steps (
    block_id    TEXT NOT NULL REFERENCES lesson_blocks(id) ON DELETE CASCADE,
    member_id   TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    lesson_id   TEXT NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
    step        TEXT NOT NULL,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (block_id, member_id, step)
);
CREATE INDEX IF NOT EXISTS idx_lesson_steps_member ON lesson_steps(lesson_id, member_id);

-- A runner name an element may ask for, bound to an app tool the
-- community operator trusts (e.g. "js" -> functions/functions_invoke).
CREATE TABLE IF NOT EXISTS element_runners (
    community_id  TEXT NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    app           TEXT NOT NULL,
    tool          TEXT NOT NULL,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (community_id, name)
);
