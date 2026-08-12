PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS ticket_areas (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  slug TEXT NOT NULL,
  name TEXT NOT NULL,
  color TEXT NOT NULL DEFAULT '#6b7280',
  sort_order INTEGER NOT NULL DEFAULT 0,
  archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  UNIQUE(project_id, slug)
);

CREATE TABLE IF NOT EXISTS ticket_portals (
  project_id TEXT PRIMARY KEY,
  token TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL DEFAULT 'Client feedback',
  welcome_text TEXT NOT NULL DEFAULT 'Share feedback, report a problem, or request a change.',
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE TABLE IF NOT EXISTS tickets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  area_id INTEGER REFERENCES ticket_areas(id) ON DELETE SET NULL,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  type TEXT NOT NULL DEFAULT 'feedback'
    CHECK (type IN ('feedback','bug','feature','change_request','question','support')),
  status TEXT NOT NULL DEFAULT 'new'
    CHECK (status IN ('new','acknowledged','planned','in_progress','waiting_client','resolved','closed')),
  priority TEXT NOT NULL DEFAULT 'normal'
    CHECK (priority IN ('low','normal','high','urgent')),
  source TEXT NOT NULL DEFAULT 'internal',
  requester_name TEXT NOT NULL DEFAULT '',
  requester_email TEXT NOT NULL DEFAULT '',
  requester_organization TEXT NOT NULL DEFAULT '',
  requester_crm_contact_id INTEGER,
  assignee_kind TEXT NOT NULL DEFAULT '',
  assignee_ref TEXT NOT NULL DEFAULT '',
  assignee_name TEXT NOT NULL DEFAULT '',
  due_at TEXT,
  portal_token TEXT NOT NULL UNIQUE,
  created_by_kind TEXT NOT NULL DEFAULT 'human',
  created_by_ref TEXT NOT NULL DEFAULT '',
  created_by_name TEXT NOT NULL DEFAULT '',
  resolved_at TEXT,
  closed_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_tickets_project_status_updated
  ON tickets(project_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_tickets_project_area_updated
  ON tickets(project_id, area_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_tickets_project_requester
  ON tickets(project_id, requester_email, requester_crm_contact_id);

CREATE TABLE IF NOT EXISTS ticket_comments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','internal')),
  body TEXT NOT NULL,
  author_kind TEXT NOT NULL DEFAULT 'human',
  author_ref TEXT NOT NULL DEFAULT '',
  author_name TEXT NOT NULL DEFAULT '',
  edited_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_ticket_comments_ticket_created
  ON ticket_comments(ticket_id, created_at, id);

CREATE TABLE IF NOT EXISTS ticket_comment_revisions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  comment_id INTEGER NOT NULL REFERENCES ticket_comments(id) ON DELETE CASCADE,
  body TEXT NOT NULL,
  edited_by_kind TEXT NOT NULL DEFAULT 'human',
  edited_by_ref TEXT NOT NULL DEFAULT '',
  edited_by_name TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE TABLE IF NOT EXISTS ticket_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','internal')),
  actor_kind TEXT NOT NULL DEFAULT 'system',
  actor_ref TEXT NOT NULL DEFAULT '',
  actor_name TEXT NOT NULL DEFAULT '',
  data_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_ticket_events_ticket_created
  ON ticket_events(ticket_id, created_at, id);

CREATE TABLE IF NOT EXISTS ticket_attachments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  comment_id INTEGER REFERENCES ticket_comments(id) ON DELETE SET NULL,
  storage_file_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
  size_bytes INTEGER NOT NULL DEFAULT 0,
  url TEXT NOT NULL DEFAULT '',
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','internal')),
  uploaded_by_kind TEXT NOT NULL DEFAULT 'human',
  uploaded_by_ref TEXT NOT NULL DEFAULT '',
  uploaded_by_name TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_ticket_attachments_ticket
  ON ticket_attachments(ticket_id, created_at, id);

CREATE TABLE IF NOT EXISTS ticket_links (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  app_name TEXT NOT NULL DEFAULT '',
  external_id TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_by_kind TEXT NOT NULL DEFAULT 'human',
  created_by_ref TEXT NOT NULL DEFAULT '',
  created_by_name TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_ticket_links_ticket
  ON ticket_links(ticket_id, created_at, id);
