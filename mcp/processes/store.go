package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/robfig/cron/v3"
	"strings"
	"time"
)

var errNotFound = errors.New("process not found in this project")
var errConflict = errors.New("procedure changed; reload before saving")

type Schedule struct {
	Kind     string `json:"kind"`
	Every    string `json:"every,omitempty"`
	Cron     string `json:"cron,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}
type Definition struct {
	Parameters           []Parameter `json:"parameters,omitempty"`
	ExecutionMode        string      `json:"execution_mode"`
	Name                 string      `json:"name"`
	Description          string      `json:"description"`
	Instructions         string      `json:"instructions"`
	RequiredInputs       string      `json:"required_inputs"`
	DefaultInputs        string      `json:"default_inputs"`
	CompletionCriteria   string      `json:"completion_criteria"`
	ApprovalRequirements string      `json:"approval_requirements"`
	OwnerAgentID         int64       `json:"owner_agent_id"`
	Schedule             *Schedule   `json:"schedule,omitempty"`
}
type Process struct {
	Assignment       *Assignment  `json:"-"`
	Assignments      []Assignment `json:"assignments"`
	NextRunAt        string       `json:"next_run_at,omitempty"`
	ScheduledVersion int          `json:"scheduled_version"`
	LastScheduleNote string       `json:"last_schedule_note,omitempty"`
	ID               string       `json:"id"`
	ProjectID        string       `json:"project_id"`
	Status           string       `json:"status"`
	Version          int          `json:"version"`
	SyncPending      bool         `json:"sync_pending"`
	SyncError        string       `json:"sync_error"`
	CreatedAt        string       `json:"created_at"`
	UpdatedAt        string       `json:"updated_at"`
	Definition
}
type Version struct {
	Version    int        `json:"version"`
	Definition Definition `json:"definition"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  string     `json:"created_at"`
}
type Run struct {
	AssignmentID       string           `json:"assignment_id"`
	AssignmentRevision int              `json:"assignment_revision"`
	Binding            AssignmentConfig `json:"assignment"`
	Overrides          map[string]any   `json:"parameter_overrides"`
	Backend            string           `json:"backend"`
	State              string           `json:"state"`
	Progress           int              `json:"progress"`
	CurrentStep        string           `json:"current_step"`
	Result             string           `json:"result"`
	Error              string           `json:"error"`
	ExecutionID        string           `json:"execution_id,omitempty"`
	TargetThreadID     string           `json:"target_thread_id,omitempty"`
	DeliveredAt        string           `json:"delivered_at,omitempty"`
	DeliveryAttempts   int              `json:"delivery_attempts"`
	NextAttemptAt      string           `json:"next_attempt_at,omitempty"`
	ScheduledFor       string           `json:"scheduled_for,omitempty"`
	SchedulePaused     bool             `json:"schedule_paused"`
	LifecycleSequence  int64            `json:"-"`
	ExecutionState     string           `json:"execution_state,omitempty"`

	ID              string `json:"id"`
	ProcessID       string `json:"process_id"`
	Version         int    `json:"version"`
	Kind            string `json:"kind"`
	RequestKey      string `json:"request_key"`
	Inputs          string `json:"inputs"`
	TaskID          string `json:"task_id"`
	DeliveryWarning string `json:"delivery_warning"`
	CreatedAt       string `json:"created_at"`
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func newID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
func (d *Definition) validate() error {
	if e := validateSchema(d.Parameters); e != nil {
		return e
	}
	if d.ExecutionMode == "" {
		d.ExecutionMode = "agent"
	}
	if d.ExecutionMode != "agent" && d.ExecutionMode != "tasks" {
		return errors.New("execution_mode must be agent or tasks")
	}
	d.Name = strings.TrimSpace(d.Name)
	d.Instructions = strings.TrimSpace(d.Instructions)
	if d.Name == "" || len(d.Name) > 160 {
		return errors.New("name must contain 1–160 characters")
	}
	if d.Instructions == "" || strings.TrimSpace(d.CompletionCriteria) == "" {
		return errors.New("instructions and completion criteria are required")
	}
	if d.OwnerAgentID <= 0 {
		return errors.New("choose an owner agent")
	}
	raw, _ := json.Marshal(d)
	if len(raw) > 128*1024 {
		return errors.New("procedure exceeds 128 KB")
	}
	if s := d.Schedule; s != nil {
		if s.Timezone == "" {
			s.Timezone = "UTC"
		}
		loc, err := time.LoadLocation(s.Timezone)
		if err != nil {
			return errors.New("invalid IANA timezone")
		}
		switch s.Kind {
		case "interval":
			duration, err := time.ParseDuration(s.Every)
			if err != nil || duration < time.Minute {
				return errors.New("interval must be at least 1m")
			}
		case "cron":
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			schedule, err := parser.Parse(s.Cron)
			if err != nil || schedule.Next(time.Now().In(loc)).IsZero() {
				return errors.New("invalid five-field cron schedule")
			}
		default:
			return errors.New("schedule kind must be interval or cron")
		}
	}
	return nil
}
func (a *App) get(project, id string) (*Process, error) {
	var p Process
	var body string
	err := a.db.QueryRow(`SELECT p.id,p.project_id,p.status,p.current_version,p.sync_pending,p.sync_error,p.created_at,p.updated_at,p.next_run_at,p.scheduled_version,p.last_schedule_note,v.body_json FROM processes p JOIN process_versions v ON v.process_id=p.id AND v.version=p.current_version WHERE p.id=? AND p.project_id=?`, id, project).Scan(&p.ID, &p.ProjectID, &p.Status, &p.Version, &p.SyncPending, &p.SyncError, &p.CreatedAt, &p.UpdatedAt, &p.NextRunAt, &p.ScheduledVersion, &p.LastScheduleNote, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(body), &p.Definition); err != nil {
		return nil, err
	}
	if p.ExecutionMode == "" {
		p.ExecutionMode = "tasks"
	}
	p.Assignments, err = a.assignments(p.ID)
	if err != nil {
		return nil, err
	}
	for _, x := range p.Assignments {
		if x.SyncPending {
			p.SyncPending = true
			if p.SyncError == "" {
				p.SyncError = x.SyncError
			}
		}
		if x.ID == "assignment-"+p.ID {
			p.NextRunAt = x.NextRunAt
			p.LastScheduleNote = x.LastScheduleNote
		}
	}
	return &p, nil
}
func (a *App) list(project string) ([]Process, error) {
	rows, err := a.db.Query(`SELECT id FROM processes WHERE project_id=? ORDER BY updated_at DESC,id DESC`, project)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Process{}
	for _, id := range ids {
		p, e := a.get(project, id)
		if e != nil {
			return nil, e
		}
		out = append(out, *p)
	}
	return out, nil
}
func (a *App) versions(id string) ([]Version, error) {
	rows, err := a.db.Query(`SELECT version,body_json,created_by,created_at FROM process_versions WHERE process_id=? ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		var v Version
		var body string
		if err = rows.Scan(&v.Version, &body, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(body), &v.Definition); err != nil {
			return nil, err
		}
		if v.Definition.ExecutionMode == "" {
			v.Definition.ExecutionMode = "tasks"
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (a *App) definition(id string, version int) (Definition, error) {
	var d Definition
	var raw string
	err := a.db.QueryRow(`SELECT body_json FROM process_versions WHERE process_id=? AND version=?`, id, version).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &d)
	}
	if d.ExecutionMode == "" {
		d.ExecutionMode = "tasks"
	}
	return d, err
}
func (a *App) save(project, id, actor string, expected int, d Definition) (*Process, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	agent, err := a.ctx.GetAgent(d.OwnerAgentID)
	if err != nil {
		return nil, fmt.Errorf("resolve owner: %w", err)
	}
	if agent.ProjectID != project {
		return nil, errors.New("owner is outside this project")
	}
	var previous *Process
	version := 1
	now := timestamp()
	create := id == ""
	if create {
		id = newID("process-")
	} else {
		p, err := a.get(project, id)
		if err != nil {
			return nil, err
		}
		previous = p
		if p.Version != expected {
			return nil, errConflict
		}
		if p.SyncPending || p.Status == "active" || p.Status == "archived" {
			return nil, errors.New("pause and synchronize the process before editing")
		}
		version = p.Version + 1
	}
	body, _ := json.Marshal(d)
	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if create {
		_, err = tx.Exec(`INSERT INTO processes(id,project_id,created_at,updated_at) VALUES(?,?,?,?)`, id, project, now, now)
	} else {
		_, err = tx.Exec(`UPDATE processes SET current_version=?,status='draft',sync_error='',next_run_at='',scheduled_version=0,last_schedule_note='',updated_at=? WHERE id=? AND project_id=?`, version, now, id, project)
	}
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`INSERT INTO process_versions(process_id,version,body_json,created_by,created_at) VALUES(?,?,?,?,?)`, id, version, string(body), actor, now)
	if err != nil {
		return nil, err
	}
	if create {
		c := AssignmentConfig{FollowLatest: true, Name: "Default assignment", OwnerAgentID: d.OwnerAgentID, ExecutionMode: d.ExecutionMode, Schedule: d.Schedule, ProcedureVersion: version, Parameters: map[string]any{}}
		_, err = tx.Exec(`INSERT INTO process_assignments(id,process_id,body_json,status,created_at,updated_at) VALUES(?,?,?,'active',?,?)`, "assignment-"+id, id, jsonText(c), now, now)
	} else {
		for _, x := range previous.Assignments {
			c := x.AssignmentConfig
			changed := false
			if c.FollowLatest {
				c.ProcedureVersion = version
				changed = true
			}
			if x.ID == "assignment-"+id && (previous.OwnerAgentID != d.OwnerAgentID || previous.ExecutionMode != d.ExecutionMode || jsonText(previous.Schedule) != jsonText(d.Schedule)) {
				c.OwnerAgentID = d.OwnerAgentID
				c.ExecutionMode = d.ExecutionMode
				c.Schedule = d.Schedule
				changed = true
			}
			if changed {
				_, err = tx.Exec(`UPDATE process_assignments SET revision=revision+1,body_json=?,next_run_at='',scheduled_version=0,updated_at=? WHERE id=?`, jsonText(c), now, x.ID)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return a.get(project, id)
}
func (a *App) dispatches(id string) ([]Run, error) {
	rows, err := a.db.Query(`SELECT `+runColumns+` FROM process_runs WHERE process_id=? ORDER BY created_at DESC,id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, scanErr := scanRun(rows)
		if scanErr != nil {
			err = scanErr
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (a *App) reserveRun(p *Process, kind, key, inputs string) (Run, error) {
	return a.reserveAssignedRun(p, kind, key, inputs, nil)
}
func (a *App) reserveAssignedRun(p *Process, kind, key, inputs string, overrides map[string]any) (Run, error) {
	if p.Assignment == nil {
		x, e := a.assignment(p.ProjectID, p.ID, "assignment-"+p.ID)
		if e != nil {
			return Run{}, e
		}
		p, e = a.assigned(p, x)
		if e != nil {
			return Run{}, e
		}
	}
	// Preserve legacy default keys; other assignments get independent namespaces.
	if p.Assignment.ID != "assignment-"+p.ID {
		key = p.Assignment.ID + ":" + key
	}
	r, e := scanRun(a.db.QueryRow(`SELECT `+runColumns+` FROM process_runs WHERE process_id=? AND request_key=?`, p.ID, key))
	if e == nil {
		if r.AssignmentID != p.Assignment.ID || r.Inputs != inputs || r.Kind != kind || jsonText(params(r.Overrides)) != jsonText(params(overrides)) {
			return Run{}, errors.New("idempotency key already used with different input")
		}
		return r, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Run{}, e
	}
	c := p.Assignment.AssignmentConfig
	values := map[string]any{}
	for k, v := range c.Parameters {
		values[k] = v
	}
	for k, v := range overrides {
		values[k] = v
	}
	c.Parameters, e = validateParameters(p.Parameters, values, true)
	if e != nil {
		return Run{}, e
	}
	r = Run{ID: newID("run-"), ProcessID: p.ID, Version: p.Version, Kind: kind, RequestKey: key, Inputs: inputs, CreatedAt: timestamp(), Backend: p.ExecutionMode, State: "queued", AssignmentID: p.Assignment.ID, AssignmentRevision: p.Assignment.Revision, Binding: c, Overrides: params(overrides)}
	_, e = a.db.Exec(`INSERT INTO process_runs(id,process_id,version,kind,request_key,inputs,created_at,backend,assignment_id,assignment_revision,assignment_json,overrides_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, r.ID, r.ProcessID, r.Version, r.Kind, r.RequestKey, r.Inputs, r.CreatedAt, r.Backend, r.AssignmentID, r.AssignmentRevision, jsonText(r.Binding), jsonText(r.Overrides))
	return r, e
}

const runColumns = `id,process_id,version,kind,request_key,inputs,task_id,delivery_warning,created_at,backend,state,progress,current_step,result,error,execution_id,target_thread_id,delivered_at,delivery_attempts,next_attempt_at,scheduled_for,schedule_paused,lifecycle_sequence,execution_state,assignment_id,assignment_revision,assignment_json,overrides_json`

type scanner interface{ Scan(...any) error }

func scanRun(row scanner) (Run, error) {
	var r Run
	var binding, overrides string
	err := row.Scan(&r.ID, &r.ProcessID, &r.Version, &r.Kind, &r.RequestKey, &r.Inputs, &r.TaskID, &r.DeliveryWarning, &r.CreatedAt, &r.Backend, &r.State, &r.Progress, &r.CurrentStep, &r.Result, &r.Error, &r.ExecutionID, &r.TargetThreadID, &r.DeliveredAt, &r.DeliveryAttempts, &r.NextAttemptAt, &r.ScheduledFor, &r.SchedulePaused, &r.LifecycleSequence, &r.ExecutionState, &r.AssignmentID, &r.AssignmentRevision, &binding, &overrides)
	if err == nil {
		err = json.Unmarshal([]byte(binding), &r.Binding)
	}
	if err == nil {
		err = json.Unmarshal([]byte(overrides), &r.Overrides)
	}
	return r, err
}
func (a *App) getRun(project, process, id string) (Run, error) {
	if _, err := a.get(project, process); err != nil {
		return Run{}, err
	}
	r, err := scanRun(a.db.QueryRow(`SELECT `+runColumns+` FROM process_runs WHERE process_id=? AND id=?`, process, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = errNotFound
	}
	return r, err
}
