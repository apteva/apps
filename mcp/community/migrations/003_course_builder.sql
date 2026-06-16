-- community v0.4: full course builder metadata and learning objects.

CREATE TABLE IF NOT EXISTS course_details (
    space_id                TEXT    PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
    summary                 TEXT    NOT NULL DEFAULT '',
    description             TEXT    NOT NULL DEFAULT '',
    instructor_member_id    TEXT    REFERENCES members(id),
    instructor_name         TEXT    NOT NULL DEFAULT '',
    level                   TEXT    NOT NULL DEFAULT '',
    tags_json               TEXT    NOT NULL DEFAULT '[]',
    price_cents             INTEGER NOT NULL DEFAULT 0,
    currency                TEXT    NOT NULL DEFAULT 'USD',
    prerequisites_json      TEXT    NOT NULL DEFAULT '[]',
    outcomes_json           TEXT    NOT NULL DEFAULT '[]',
    cover_storage_file_id   TEXT,
    updated_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS lesson_resources (
    id                   TEXT    PRIMARY KEY,
    lesson_id             TEXT    NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
    storage_file_id       TEXT    NOT NULL,
    name                  TEXT    NOT NULL DEFAULT '',
    kind                  TEXT    NOT NULL DEFAULT 'file',
    content_type          TEXT    NOT NULL DEFAULT '',
    size_bytes            INTEGER,
    position              INTEGER NOT NULL DEFAULT 0,
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_lesson_resources_lesson ON lesson_resources(lesson_id, position);

CREATE TABLE IF NOT EXISTS quizzes (
    id             TEXT    PRIMARY KEY,
    lesson_id       TEXT    NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
    title           TEXT    NOT NULL,
    questions_json  TEXT    NOT NULL DEFAULT '[]',
    passing_score   INTEGER NOT NULL DEFAULT 70,
    position        INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_quizzes_lesson ON quizzes(lesson_id, position);

CREATE TABLE IF NOT EXISTS assignments (
    id                         TEXT    PRIMARY KEY,
    lesson_id                   TEXT    NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
    title                       TEXT    NOT NULL,
    instructions                TEXT    NOT NULL DEFAULT '',
    due_after_days              INTEGER,
    attachment_storage_file_id  TEXT,
    created_at                  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_assignments_lesson ON assignments(lesson_id, created_at);

CREATE TABLE IF NOT EXISTS course_certificates (
    space_id                  TEXT    PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
    enabled                   INTEGER NOT NULL DEFAULT 0,
    title                     TEXT    NOT NULL DEFAULT '',
    body                      TEXT    NOT NULL DEFAULT '',
    template_storage_file_id  TEXT,
    issue_on_completion       INTEGER NOT NULL DEFAULT 1,
    updated_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS drip_schedules (
    id                  TEXT    PRIMARY KEY,
    space_id            TEXT    NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    lesson_id            TEXT    NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
    release_at           TIMESTAMP,
    release_after_days   INTEGER,
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(lesson_id)
);
CREATE INDEX IF NOT EXISTS idx_drip_schedules_space ON drip_schedules(space_id, release_at);

CREATE TABLE IF NOT EXISTS enrollment_rules (
    space_id            TEXT    PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
    access_mode         TEXT    NOT NULL DEFAULT 'free'
                        CHECK(access_mode IN ('free','paid','invite','manual')),
    requires_approval   INTEGER NOT NULL DEFAULT 0,
    max_enrollments     INTEGER,
    starts_at           TIMESTAMP,
    ends_at             TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS course_enrollments (
    space_id       TEXT    NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    member_id      TEXT    NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    status         TEXT    NOT NULL DEFAULT 'active'
                   CHECK(status IN ('pending','active','rejected','cancelled','completed')),
    enrolled_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at   TIMESTAMP,
    PRIMARY KEY(space_id, member_id)
);
CREATE INDEX IF NOT EXISTS idx_course_enrollments_member ON course_enrollments(member_id, status);
