-- Equality filters lead the timestamp/id cursor suffix. These indexes also
-- cover history lookup without scanning unrelated projects or older pages.
CREATE INDEX read_diagnostics_project_outcome_time ON read_diagnostics(project_id,outcome,recorded_at_ms DESC,id DESC);
CREATE INDEX read_diagnostics_project_request_time ON read_diagnostics(project_id,request_id,recorded_at_ms DESC,id DESC);
CREATE INDEX read_diagnostics_project_query_time ON read_diagnostics(project_id,query_id,recorded_at_ms DESC,id DESC);
CREATE INDEX read_diagnostics_project_operation_time ON read_diagnostics(project_id,operation,recorded_at_ms DESC,id DESC);
CREATE INDEX read_diagnostics_project_call_time ON read_diagnostics(project_id,call_id,recorded_at_ms DESC,id DESC);
