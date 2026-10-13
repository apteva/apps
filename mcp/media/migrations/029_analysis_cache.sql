CREATE TABLE analysis_cache (
 cache_key TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 file_id TEXT NOT NULL,
 source_sha256 TEXT NOT NULL,
 algorithm_version TEXT NOT NULL,
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_analysis_cache_project ON analysis_cache(project_id,file_id);
