ALTER TABLE instructor_profiles ADD COLUMN persona_id INTEGER NOT NULL DEFAULT 0 CHECK (persona_id >= 0);
