package main

import (
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
	ProjectID   string   `json:"project_id,omitempty"`
	ProjectName string   `json:"project_name,omitempty"`
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
	ProjectID      string         `json:"project_id,omitempty"`
	ProjectName    string         `json:"project_name,omitempty"`
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
	Scope       string            `json:"scope,omitempty"`
	Projects    []overviewProject `json:"projects,omitempty"`
	Coverage    string            `json:"coverage"`
	Counts      overviewCounts    `json:"counts"`
	Active      []overviewItem    `json:"active"`
	Upcoming    []overviewItem    `json:"upcoming"`
	Recent      []overviewItem    `json:"recent"`
	Attention   []overviewItem    `json:"attention"`
	LiveSteps   []overviewStep    `json:"live_steps"`
	Warnings    []string          `json:"warnings"`
	Partial     bool              `json:"partial"`
	GeneratedAt string            `json:"generated_at"`
}

type overviewProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Reads never dispatch work or acquire the worker mutex. Direct-run counts cover
// the full project; payloads and optional inter-app history requests are bounded.
func (a *App) overview(project string) (*processOverview, error) {
	out := &processOverview{Active: []overviewItem{}, Upcoming: []overviewItem{}, Recent: []overviewItem{}, Attention: []overviewItem{}, LiveSteps: []overviewStep{}, Warnings: []string{}, GeneratedAt: timestamp()}
	const direct = `(r.backend='agent' OR r.workflow=1)`
	const active = `r.state NOT IN ('completed','failed','cancelled')`
	const attention = `(r.state IN ('waiting','blocked') OR r.delivery_warning<>'' OR EXISTS (SELECT 1 FROM process_step_runs s WHERE s.run_id=r.id AND s.state NOT IN ('completed','cancelled') AND (s.state IN ('waiting','blocked','failed') OR (s.due_at<>'' AND julianday(s.due_at)<=julianday('now') AND s.state NOT IN ('completed','failed','cancelled')) OR s.delivery_warning<>'' OR (json_extract(s.definition_json,'$.kind')='approval' AND s.state IN ('ready','running')))))`
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

// overviewGlobal reads the global installation's own database for every
// project the platform says this install may see. A global install has its own
// app storage; project-scoped installations are intentionally not opened or
// merged here. This keeps the read boundary explicit and prevents a global
// widget from discovering data outside the platform's project projection.
func (a *App) overviewGlobal(selector string) (*processOverview, error) {
	api := a.ctx.PlatformAPI()
	if api == nil {
		return nil, errors.New("platform project directory unavailable")
	}
	projects, err := api.ListProjects()
	if err != nil {
		return nil, err
	}
	visible := make([]overviewProject, 0, len(projects))
	seen := map[string]bool{}
	for _, project := range projects {
		id := strings.TrimSpace(project.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		visible = append(visible, overviewProject{ID: id, Name: project.Name})
	}
	sort.Slice(visible, func(i, j int) bool {
		if visible[i].Name != visible[j].Name {
			return visible[i].Name < visible[j].Name
		}
		return visible[i].ID < visible[j].ID
	})
	selector = strings.TrimSpace(selector)
	if selector != "" && !seen[selector] {
		return nil, errProjectNotVisible
	}
	out := &processOverview{
		Scope:       "global",
		Projects:    visible,
		Active:      []overviewItem{},
		Upcoming:    []overviewItem{},
		Recent:      []overviewItem{},
		Attention:   []overviewItem{},
		LiveSteps:   []overviewStep{},
		Warnings:    []string{},
		GeneratedAt: timestamp(),
	}
	for _, project := range visible {
		if selector != "" && selector != project.ID {
			continue
		}
		part, err := a.overview(project.ID)
		if err != nil {
			return nil, err
		}
		out.Counts.Active += part.Counts.Active
		out.Counts.Scheduled += part.Counts.Scheduled
		out.Counts.Attention += part.Counts.Attention
		out.Counts.Recent += part.Counts.Recent
		out.Partial = out.Partial || part.Partial
		out.Warnings = append(out.Warnings, part.Warnings...)
		annotate := func(items []overviewItem) {
			for i := range items {
				items[i].ProjectID = project.ID
				items[i].ProjectName = project.Name
			}
		}
		annotate(part.Active)
		annotate(part.Upcoming)
		annotate(part.Recent)
		annotate(part.Attention)
		for i := range part.LiveSteps {
			part.LiveSteps[i].ProjectID = project.ID
			part.LiveSteps[i].ProjectName = project.Name
			part.LiveSteps[i].Name = project.Name + " · " + part.LiveSteps[i].Name
		}
		out.Active = append(out.Active, part.Active...)
		out.Upcoming = append(out.Upcoming, part.Upcoming...)
		out.Recent = append(out.Recent, part.Recent...)
		out.Attention = append(out.Attention, part.Attention...)
		out.LiveSteps = append(out.LiveSteps, part.LiveSteps...)
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
	out.Active = capOverview(out.Active)
	out.Recent = capOverview(out.Recent)
	out.Upcoming = capOverview(out.Upcoming)
	out.Attention = capOverview(out.Attention)
	if len(out.LiveSteps) > 24 {
		out.LiveSteps = out.LiveSteps[:24]
	}
	if out.Partial {
		out.Coverage = "Partial overview: " + strings.Join(out.Warnings, " ")
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
