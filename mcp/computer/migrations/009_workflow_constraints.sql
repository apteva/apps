CREATE TABLE computer_workflow_constraints (
    id TEXT PRIMARY KEY,
    context_id TEXT NOT NULL,
    origin TEXT NOT NULL,
    resource_url TEXT NOT NULL,
    policy_json TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(context_id)
);

CREATE INDEX computer_workflow_resource_url ON computer_workflow_constraints(resource_url);
