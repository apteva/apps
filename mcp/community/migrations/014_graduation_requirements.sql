ALTER TABLE course_certificates ADD COLUMN require_quizzes_passed INTEGER NOT NULL DEFAULT 0 CHECK(require_quizzes_passed IN (0,1));
ALTER TABLE course_certificates ADD COLUMN require_assignments_approved INTEGER NOT NULL DEFAULT 0 CHECK(require_assignments_approved IN (0,1));
ALTER TABLE course_certificates ADD COLUMN require_milestones_approved INTEGER NOT NULL DEFAULT 0 CHECK(require_milestones_approved IN (0,1));
