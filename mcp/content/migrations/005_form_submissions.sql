-- core/form block submissions.
--
-- Submissions are kept even when an external app (CRM, auth) is wired
-- as an action target — they're the source of truth for "what did the
-- visitor type", whereas action outputs are summaries of "what we did
-- with it". The two diverge: an upsert action might succeed without
-- creating anything, or an auth signup might mint tokens that we
-- don't store back here. Keeping payload + results side-by-side is
-- what lets the dashboard's submissions inbox say "Ada signed up;
-- here's the row CRM created and the auth user_id, side-by-side".
--
-- payload + results are JSON text columns rather than EAV; query
-- patterns are "list submissions for one form" and "show one row in
-- full", neither of which benefits from indexing inside the JSON.
--
-- ip_hash is salted SHA-256 (matching the page-view path) — we never
-- store the raw IP, so the table is GDPR-clean by default.

CREATE TABLE form_submissions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  TEXT    NOT NULL,
  site_id     INTEGER,
  post_id     INTEGER NOT NULL,
  block_id    TEXT    NOT NULL,
  payload     TEXT    NOT NULL DEFAULT '{}',
  ip_hash     TEXT,
  user_agent  TEXT,
  status      TEXT    NOT NULL,             -- ok | partial | rejected_honeypot | rejected_validation
  results     TEXT    NOT NULL DEFAULT '[]', -- JSON [{app, tool, ok, error?, output?}]
  error       TEXT,
  created_at  INTEGER NOT NULL
);

CREATE INDEX form_submissions_project_idx
  ON form_submissions (project_id, created_at DESC);
CREATE INDEX form_submissions_block_idx
  ON form_submissions (project_id, block_id, created_at DESC);
CREATE INDEX form_submissions_post_idx
  ON form_submissions (project_id, post_id, created_at DESC);
