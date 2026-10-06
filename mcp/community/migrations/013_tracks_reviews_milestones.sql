-- Learning tracks, assignment review workflow, and student milestones.
CREATE TABLE IF NOT EXISTS course_tracks (
    id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    slug TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL DEFAULT 0,
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(space_id, slug)
);
CREATE INDEX IF NOT EXISTS idx_course_tracks_space ON course_tracks(space_id, active, position);

CREATE TABLE IF NOT EXISTS lesson_track_assignments (
    lesson_id TEXT NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
    track_id TEXT NOT NULL REFERENCES course_tracks(id) ON DELETE CASCADE,
    PRIMARY KEY(lesson_id, track_id)
);
CREATE INDEX IF NOT EXISTS idx_lesson_track_assignments_track ON lesson_track_assignments(track_id, lesson_id);

CREATE TABLE IF NOT EXISTS member_course_tracks (
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    track_id TEXT NOT NULL REFERENCES course_tracks(id) ON DELETE CASCADE,
    selected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(space_id, member_id)
);
CREATE INDEX IF NOT EXISTS idx_member_course_tracks_member ON member_course_tracks(member_id, space_id);

ALTER TABLE assignment_submissions ADD COLUMN status TEXT NOT NULL DEFAULT 'submitted'
  CHECK(status IN ('submitted','needs_changes','approved'));
ALTER TABLE assignment_submissions ADD COLUMN feedback TEXT NOT NULL DEFAULT '';
ALTER TABLE assignment_submissions ADD COLUMN reviewed_by TEXT REFERENCES members(id);
ALTER TABLE assignment_submissions ADD COLUMN reviewed_at TIMESTAMP;
ALTER TABLE assignment_submissions ADD COLUMN links_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE assignment_submissions ADD COLUMN files_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE assignment_submissions ADD COLUMN version INTEGER NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_assignment_submissions_review ON assignment_submissions(status, updated_at);

CREATE TABLE IF NOT EXISTS assignment_submission_versions (
    id TEXT PRIMARY KEY,
    assignment_id TEXT NOT NULL REFERENCES assignments(id) ON DELETE CASCADE,
    member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    links_json TEXT NOT NULL DEFAULT '[]',
    files_json TEXT NOT NULL DEFAULT '[]',
    submitted_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(assignment_id, member_id, version)
);
CREATE INDEX IF NOT EXISTS idx_assignment_submission_versions_assignment ON assignment_submission_versions(assignment_id, submitted_at DESC);
INSERT OR IGNORE INTO assignment_submission_versions(id, assignment_id, member_id, version, body, submitted_at)
SELECT 'asv_' || assignment_id || '_' || member_id, assignment_id, member_id, version, body, updated_at
FROM assignment_submissions;

CREATE TABLE IF NOT EXISTS milestone_definitions (
    id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    track_id TEXT REFERENCES course_tracks(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL DEFAULT 0,
    requires_approval INTEGER NOT NULL DEFAULT 0 CHECK(requires_approval IN (0,1)),
    evidence_type TEXT NOT NULL DEFAULT 'text' CHECK(evidence_type IN ('text','link','file','any')),
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_milestone_definitions_course ON milestone_definitions(space_id, track_id, active, position);

CREATE TABLE IF NOT EXISTS member_milestones (
    definition_id TEXT NOT NULL REFERENCES milestone_definitions(id) ON DELETE CASCADE,
    member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'not_started' CHECK(status IN ('not_started','submitted','needs_changes','approved')),
    evidence_text TEXT NOT NULL DEFAULT '',
    evidence_links_json TEXT NOT NULL DEFAULT '[]',
    evidence_files_json TEXT NOT NULL DEFAULT '[]',
    feedback TEXT NOT NULL DEFAULT '',
    approved_by TEXT REFERENCES members(id),
    submitted_at TIMESTAMP,
    approved_at TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(definition_id, member_id)
);
CREATE INDEX IF NOT EXISTS idx_member_milestones_member ON member_milestones(member_id, status, updated_at);

CREATE TABLE learning_files (
 file_id TEXT PRIMARY KEY,
 space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
 member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_learning_files_member ON learning_files(space_id,member_id);
CREATE TABLE assignment_reviews (
 id TEXT PRIMARY KEY,
 assignment_id TEXT NOT NULL REFERENCES assignments(id) ON DELETE CASCADE,
 member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
 version INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('needs_changes','approved')),
 feedback TEXT NOT NULL DEFAULT '',
 reviewer_id TEXT REFERENCES members(id),
 created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
