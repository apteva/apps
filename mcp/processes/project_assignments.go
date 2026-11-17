package main

import "strings"

// projectAssignment is one assignment with the identity of its process and a
// compact view of its run activity, for the project-wide Assignments view.
type projectAssignment struct {
	Assignment
	ProcessName   string `json:"process_name"`
	ProcessStatus string `json:"process_status"`
	RunCount      int    `json:"run_count"`
	OngoingCount  int    `json:"ongoing_count"`
	LastRunID     string `json:"last_run_id,omitempty"`
	LastRunState  string `json:"last_run_state,omitempty"`
	LastRunAt     string `json:"last_run_at,omitempty"`
}

// projectAssignments lists every assignment in a project. Run statistics use
// the assignment_runs index; no procedure body or run output is loaded.
func (a *App) projectAssignments(project string) (map[string]any, error) {
	rows, e := a.db.Query(`SELECT x.id,x.process_id,x.revision,x.body_json,x.status,x.sync_pending,x.sync_error,x.next_run_at,x.scheduled_version,x.last_schedule_note,x.created_at,x.updated_at,
		coalesce(json_extract(v.body_json,'$.name'),''),p.status,
		(SELECT count(*) FROM process_runs r WHERE r.assignment_id=x.id),
		(SELECT count(*) FROM process_runs r WHERE r.assignment_id=x.id AND r.state NOT IN ('completed','failed','cancelled')),
		coalesce((SELECT r.id||char(31)||r.state||char(31)||r.created_at FROM process_runs r WHERE r.assignment_id=x.id ORDER BY r.created_at DESC,r.id DESC LIMIT 1),'')
		FROM process_assignments x JOIN processes p ON p.id=x.process_id
		JOIN process_versions v ON v.process_id=p.id AND v.version=p.current_version
		WHERE p.project_id=? ORDER BY CASE x.status WHEN 'active' THEN 0 WHEN 'paused' THEN 1 ELSE 2 END,x.updated_at DESC,x.id`, project)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []projectAssignment{}
	for rows.Next() {
		var y projectAssignment
		var last string
		x, e := scanAssignment(multiScanner{rows, []any{&y.ProcessName, &y.ProcessStatus, &y.RunCount, &y.OngoingCount, &last}})
		if e != nil {
			return nil, e
		}
		y.Assignment = x
		if parts := strings.Split(last, "\x1f"); len(parts) == 3 {
			y.LastRunID, y.LastRunState, y.LastRunAt = parts[0], parts[1], parts[2]
		}
		out = append(out, y)
	}
	return map[string]any{"assignments": out}, rows.Err()
}

// multiScanner lets scanAssignment read its fixed columns while extra trailing
// columns land in the supplied destinations.
type multiScanner struct {
	row   scanner
	extra []any
}

func (m multiScanner) Scan(dest ...any) error { return m.row.Scan(append(dest, m.extra...)...) }
