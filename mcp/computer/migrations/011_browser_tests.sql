CREATE TABLE computer_test_sessions (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 owner_install_id INTEGER NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('starting','active','closed','interrupted')),
 profile TEXT NOT NULL,
 pid INTEGER NOT NULL DEFAULT 0,
 engine TEXT NOT NULL DEFAULT '',
 expires_at TEXT NOT NULL,
 diagnostics TEXT NOT NULL DEFAULT '{}',
 created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX computer_test_slot ON computer_test_sessions((1)) WHERE state IN ('starting','active');
