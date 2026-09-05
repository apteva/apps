CREATE TABLE repo_imports_new (
 id INTEGER PRIMARY KEY,
 repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
 source TEXT NOT NULL,
 imported_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO repo_imports_new SELECT i.* FROM repo_imports i JOIN repositories r ON r.id=i.repo_id;
DROP TABLE repo_imports;
ALTER TABLE repo_imports_new RENAME TO repo_imports;
CREATE INDEX ix_imports_repo ON repo_imports(repo_id, imported_at DESC);
