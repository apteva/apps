CREATE INDEX test_runs_dashboard_time ON test_runs(project_id,created_at DESC,id DESC);
CREATE INDEX test_runs_dashboard_suite ON test_runs(project_id,suite_id,created_at DESC,id DESC);
CREATE INDEX test_runs_dashboard_environment ON test_runs(project_id,environment,created_at DESC,id DESC);
