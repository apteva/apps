CREATE TABLE prepared_contexts (
 id TEXT PRIMARY KEY,
 source_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 agent_id INTEGER NOT NULL,
 thread_id TEXT NOT NULL
);
CREATE TABLE operations (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 context_id TEXT NOT NULL REFERENCES prepared_contexts(id),
 name TEXT NOT NULL,
 artifact_id TEXT NOT NULL DEFAULT '',
 digest TEXT NOT NULL DEFAULT '',
 receipt_json TEXT NOT NULL,
 tool_call_id TEXT NOT NULL,
 UNIQUE(context_id,name,artifact_id)
);
