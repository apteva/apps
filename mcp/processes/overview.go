package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const overviewLimit = 12

type overviewStep struct {
	StartAt     string   `json:"start_at,omitempty"`
	DueAt       string   `json:"due_at,omitempty"`
	CompletedAt string   `json:"completed_at,omitempty"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	State       string   `json:"state"`
	Kind        string   `json:"kind"`
	Origin      string   `json:"origin"`
	Executor    Executor `json:"executor"`
	Progress    int      `json:"progress"`
	UpdatedAt   string   `json:"updated_at"`
	Warning     string   `json:"warning,omitempty"`
}
type overviewItem struct {
	ID             string         `json:"id"`
	ProcessID      string         `json:"process_id"`
	ProcessName    string         `json:"process_name"`
	AssignmentID   string         `json:"assignment_id"`
	AssignmentName string         `json:"assignment_name"`
	Target         string         `json:"target"`
	AgentID        int64          `json:"agent_id"`
	Version        int            `json:"version"`
	State          string         `json:"state"`
	Backend        string         `json:"backend"`
	Progress       int            `json:"progress"`
	CurrentStep    string         `json:"current_step"`
	CreatedAt      string         `json:"created_at"`
	NextRunAt      string         `json:"next_run_at,omitempty"`
	Schedule       *Schedule      `json:"schedule,omitempty"`
	Warning        string         `json:"warning,omitempty"`
	NeedsAttention bool           `json:"needs_attention"`
	Steps          []overviewStep `json:"steps"`
	StepsTotal     int            `json:"steps_total"`
	StepsCompleted int            `json:"steps_completed"`
}
type overviewCounts struct {
	Active    int `json:"active"`
	Scheduled int `json:"scheduled"`
	Attention int `json:"attention"`
	Recent    int `json:"recent"`
}
type processOverview struct {
	Coverage    string         `json:"coverage"`
	Counts      overviewCounts `json:"counts"`
	Active      []overviewItem `json:"active"`
	Upcoming    []overviewItem `json:"upcoming"`
	Recent      []overviewItem `json:"recent"`
	Attention   []overviewItem `json:"attention"`
	LiveSteps   []overviewStep `json:"live_steps"`
	Warnings    []string       `json:"warnings"`
	Partial     bool           `json:"partial"`
	GeneratedAt string         `json:"generated_at"`
}

// Reads never dispatch work or acquire the worker mutex. Direct-run counts cover
// the full project; payloads and optional inter-app history requests are bounded.
func (a *App) overview(project string) (*processOverview, error) {
	out := &processOverview{Active: []overviewItem{}, Upcoming: []overviewItem{}, Recent: []overviewItem{}, Attention: []overviewItem{}, LiveSteps: []overviewStep{}, Warnings: []string{}, GeneratedAt: timestamp()}
	const direct = `(r.backend='agent' OR r.workflow=1)`
	const active = `r.state NOT IN ('completed','failed','cancelled')`
	const attention = `(r.state IN ('waiting','blocked') OR r.delivery_warning<>'' OR EXISTS (SELECT 1 FROM process_step_runs s WHERE s.run_id=r.id AND s.state NOT IN ('completed','cancelled') AND (s.state IN ('waiting','blocked','failed') OR (s.due_at<>'' AND julianday(s.due_at)<julianday('now') AND s.state NOT IN ('completed','failed','cancelled')) OR s.delivery_warning<>'' OR (json_extract(s.definition_json,'$.kind')='approval' AND s.state IN ('ready','running')))))`
	base := ` FROM process_runs r JOIN processes p ON p.id=r.process_id WHERE p.project_id=? AND ` + direct
	if e := a.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN `+attention+` THEN 1 ELSE 0 END),0)`+base+` AND `+active, project).Scan(&out.Counts.Active, &out.Counts.Attention); e != nil {
		return nil, e
	}
	if e := a.db.QueryRow(`SELECT COUNT(*)`+base+` AND NOT (`+active+`)`, project).Scan(&out.Counts.Recent); e != nil {
		return nil, e
	}
	for _, group := range []string{"active", "recent"} {
		condition, order := active, `CASE WHEN r.state='running' THEN 0 WHEN r.state IN ('ready','queued','pending') THEN 1 ELSE 2 END,r.created_at DESC,r.id DESC`
		if group == "recent" {
			condition = `NOT (` + active + `)`
			order = `r.created_at DESC,r.id DESC`
		}
		rows, e := a.db.Query(`SELECT r.id,p.id,COALESCE(json_extract(v.body_json,'$.name'),''),r.assignment_id,r.assignment_json,r.version,r.state,r.backend,r.progress,substr(r.current_step,1,500),r.created_at,substr(CASE WHEN r.delivery_warning<>'' THEN r.delivery_warning ELSE r.error END,1,500),`+attention+` FROM process_runs r JOIN processes p ON p.id=r.process_id JOIN process_versions v ON v.process_id=r.process_id AND v.version=r.version WHERE p.project_id=? AND `+direct+` AND `+condition+` ORDER BY `+order+` LIMIT ?`, project, overviewLimit)
		if e != nil {
			return nil, e
		}
		items := []overviewItem{}
		for rows.Next() {
			var x overviewItem
			var binding string
			if e = rows.Scan(&x.ID, &x.ProcessID, &x.ProcessName, &x.AssignmentID, &binding, &x.Version, &x.State, &x.Backend, &x.Progress, &x.CurrentStep, &x.CreatedAt, &x.Warning, &x.NeedsAttention); e != nil {
				rows.Close()
				return nil, e
			}
			var b AssignmentConfig
			if e = json.Unmarshal([]byte(binding), &b); e != nil {
				rows.Close()
				return nil, e
			}
			x.AssignmentName = b.Name
			x.Target = b.Target
			x.AgentID = b.OwnerAgentID
			x.Steps = []overviewStep{}
			items = append(items, x)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		for i := range items {
			if e = a.overviewSteps(&items[i]); e != nil {
				return nil, e
			}
		}
		if group == "active" {
			out.Active = items
		} else {
			out.Recent = items
		}
	}
	// Only active assignments on active procedures are scheduled. A sync problem
	// belongs in attention, never in a misleading "next run" count.
	rows, e := a.db.Query(`SELECT x.id,p.id,COALESCE(json_extract(v.body_json,'$.name'),''),x.body_json,x.next_run_at,x.sync_pending,substr(x.sync_error,1,500),x.scheduled_version FROM process_assignments x JOIN processes p ON p.id=x.process_id JOIN process_versions v ON v.process_id=p.id AND v.version=p.current_version WHERE p.project_id=? AND p.status='active' AND x.status='active'`, project)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var x overviewItem
		var body string
		var pending bool
		if e = rows.Scan(&x.ID, &x.ProcessID, &x.ProcessName, &body, &x.NextRunAt, &pending, &x.Warning, &x.Version); e != nil {
			rows.Close()
			return nil, e
		}
		var b AssignmentConfig
		if e = json.Unmarshal([]byte(body), &b); e != nil {
			rows.Close()
			return nil, e
		}
		x.AssignmentID = x.ID
		x.AssignmentName = b.Name
		x.Target = b.Target
		x.AgentID = b.OwnerAgentID
		x.Backend = b.ExecutionMode
		x.Schedule = b.Schedule
		x.Steps = []overviewStep{}
		if pending || x.Warning != "" {
			x.State = "sync pending"
			x.NeedsAttention = true
			if x.Warning == "" {
				x.Warning = "Assignment synchronization pending"
			}
			out.Counts.Attention++
			out.Attention = append(out.Attention, x)
		} else if b.Schedule != nil && x.NextRunAt != "" {
			x.State = "scheduled"
			out.Counts.Scheduled++
			out.Upcoming = append(out.Upcoming, x)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if e = a.overviewTasks(project, out); e != nil {
		return nil, e
	}
	sort.SliceStable(out.Active, func(i, j int) bool {
		rank := func(state string) int {
			if state == "running" {
				return 0
			}
			if state == "ready" || state == "queued" || state == "pending" {
				return 1
			}
			return 2
		}
		if rank(out.Active[i].State) != rank(out.Active[j].State) {
			return rank(out.Active[i].State) < rank(out.Active[j].State)
		}
		return newer(out.Active[i].CreatedAt, out.Active[j].CreatedAt)
	})
	sort.SliceStable(out.Recent, func(i, j int) bool { return newer(out.Recent[i].CreatedAt, out.Recent[j].CreatedAt) })
	sort.SliceStable(out.Upcoming, func(i, j int) bool { return newer(out.Upcoming[j].NextRunAt, out.Upcoming[i].NextRunAt) })
	if out.Partial {
		out.Coverage = "Partial overview: " + strings.Join(out.Warnings, " ")
	}
	out.Active = capOverview(out.Active)
	out.Recent = capOverview(out.Recent)
	out.Upcoming = capOverview(out.Upcoming)
	out.Attention = capOverview(out.Attention)
	for _, x := range out.Active {
		for _, s := range x.Steps {
			if s.State == "running" || s.State == "ready" || s.State == "waiting" || s.State == "blocked" || s.State == "scheduled" {
				s.Name = x.ProcessName + " · " + s.Name
				out.LiveSteps = append(out.LiveSteps, s)
			}
		}
	}
	if len(out.LiveSteps) > 24 {
		out.LiveSteps = out.LiveSteps[:24]
	}
	return out, nil
}
func newer(a, b string) bool {
	ta, _ := time.Parse(time.RFC3339Nano, a)
	tb, _ := time.Parse(time.RFC3339Nano, b)
	return ta.After(tb)
}
func capOverview(items []overviewItem) []overviewItem {
	if len(items) > overviewLimit {
		return items[:overviewLimit]
	}
	return items
}
func (a *App) overviewSteps(x *overviewItem) error {
	if e := a.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN state='completed' THEN 1 ELSE 0 END),0) FROM process_step_runs WHERE run_id=?`, x.ID).Scan(&x.StepsTotal, &x.StepsCompleted); e != nil {
		return e
	}
	rows, e := a.db.Query(`SELECT id,COALESCE(json_extract(definition_json,'$.name'),''),state,COALESCE(json_extract(definition_json,'$.kind'),'work'),origin,executor_json,progress,updated_at,substr(delivery_warning,1,500),start_at,due_at,completed_at FROM process_step_runs WHERE run_id=? ORDER BY position,id LIMIT 60`, x.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var s overviewStep
		var executor string
		if e = rows.Scan(&s.ID, &s.Name, &s.State, &s.Kind, &s.Origin, &executor, &s.Progress, &s.UpdatedAt, &s.Warning, &s.StartAt, &s.DueAt, &s.CompletedAt); e != nil {
			return e
		}
		if e = json.Unmarshal([]byte(executor), &s.Executor); e != nil {
			return e
		}
		x.Steps = append(x.Steps, s)
	}
	return rows.Err()
}

// Tasks keeps authoritative execution state. Never count its persisted dispatch
// placeholders as running, or a recurring schedule definition as an execution.
func (a *App) overviewTasks(project string, out *processOverview) error {
	rows, e := a.db.Query(`SELECT DISTINCT p.id FROM processes p JOIN process_runs r ON r.process_id=p.id WHERE p.project_id=? AND r.backend='tasks' AND r.workflow=0 ORDER BY p.id LIMIT 9`, project)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(ids) > 8 {
		ids = ids[:8]
		out.Partial = true
		out.Warnings = append(out.Warnings, "Tasks history covers the first 8 procedures. Open Processes for other Tasks-backed procedures.")
	}
	for _, id := range ids {
		var history struct {
			HasMore bool `json:"has_more"`
			Runs    []struct {
				RunKey  string `json:"run_key"`
				Version int    `json:"version"`
				Task    struct {
					ID              string `json:"id"`
					AgentID         int64  `json:"agent_id"`
					State           string `json:"state"`
					Progress        int    `json:"progress"`
					CurrentStep     string `json:"current_step"`
					CreatedAt       string `json:"created_at"`
					NextRunAt       string `json:"next_run_at"`
					ScheduleKind    string `json:"schedule_kind"`
					ScheduleEnabled bool   `json:"schedule_enabled"`
					Error           string `json:"error"`
				} `json:"task"`
			} `json:"runs"`
		}
		if e = a.callTasks(project, id, "list", nil, &history); e != nil {
			out.Partial = true
			out.Warnings = append(out.Warnings, "Tasks history is unavailable. Counts exclude unverified Tasks executions.")
			continue
		}
		if history.HasMore {
			out.Partial = true
			out.Warnings = append(out.Warnings, "Tasks supplies at most 200 records per procedure. Counts may exclude older Tasks work.")
		}
		for _, h := range history.Runs {
			if strings.HasPrefix(h.RunKey, "step-") {
				continue
			}
			var x overviewItem
			var body string
			var activeAssignment bool
			e = a.db.QueryRow(`SELECT r.process_id,COALESCE(json_extract(v.body_json,'$.name'),''),r.assignment_id,r.assignment_json,(p.status='active' AND COALESCE(x.status,'')='active' AND COALESCE(x.sync_pending,1)=0) FROM process_runs r JOIN processes p ON p.id=r.process_id JOIN process_versions v ON v.process_id=r.process_id AND v.version=r.version LEFT JOIN process_assignments x ON x.id=r.assignment_id WHERE p.project_id=? AND r.process_id=? AND r.id=? AND r.workflow=0 AND r.backend='tasks'`, project, id, h.RunKey).Scan(&x.ProcessID, &x.ProcessName, &x.AssignmentID, &body, &activeAssignment)
			if errors.Is(e, sql.ErrNoRows) {
				continue
			} // A stale/foreign link is never admitted into this project.
			if e != nil {
				return e
			}
			var b AssignmentConfig
			if e = json.Unmarshal([]byte(body), &b); e != nil {
				return e
			}
			x.ID = h.Task.ID
			x.AssignmentName = b.Name
			x.Target = b.Target
			x.AgentID = h.Task.AgentID
			x.Backend = "tasks"
			x.Version = h.Version
			x.State = h.Task.State
			x.Progress = h.Task.Progress
			x.CurrentStep = h.Task.CurrentStep
			x.CreatedAt = h.Task.CreatedAt
			x.Warning = h.Task.Error
			x.Steps = []overviewStep{}
			if h.Task.ScheduleKind == "interval" || h.Task.ScheduleKind == "cron" {
				if activeAssignment && h.Task.ScheduleEnabled {
					x.State = "scheduled"
					x.NextRunAt = h.Task.NextRunAt
					x.Schedule = b.Schedule
					out.Counts.Scheduled++
					out.Upcoming = append(out.Upcoming, x)
				}
			} else if terminal(x.State) {
				out.Counts.Recent++
				out.Recent = append(out.Recent, x)
			} else {
				x.NeedsAttention = x.State == "waiting" || x.State == "blocked"
				out.Counts.Active++
				if x.NeedsAttention {
					out.Counts.Attention++
				}
				out.Active = append(out.Active, x)
			}
		}
	}
	return nil
}
