ALTER TABLE books ADD COLUMN publication_json TEXT NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS book_assets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  node_id INTEGER REFERENCES book_nodes(id) ON DELETE SET NULL,
  kind TEXT NOT NULL DEFAULT 'interior_image',
  filename TEXT NOT NULL,
  content_type TEXT NOT NULL,
  content_blob BLOB NOT NULL,
  alt_text TEXT NOT NULL DEFAULT '',
  caption TEXT NOT NULL DEFAULT '',
  sha256 TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  width_px INTEGER NOT NULL DEFAULT 0,
  height_px INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  deleted_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_book_assets_book_kind
  ON book_assets(book_id, kind, updated_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_book_assets_one_ebook_cover
  ON book_assets(book_id, kind)
  WHERE kind = 'cover' AND deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_book_assets_one_print_cover
  ON book_assets(book_id, kind)
  WHERE kind = 'print_cover' AND deleted_at IS NULL;
