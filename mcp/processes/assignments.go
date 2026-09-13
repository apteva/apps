package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Parameter struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Default  any      `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
}
type AssignmentConfig struct {
	Roles            map[string]Executor `json:"roles,omitempty"`
	FollowLatest     bool                `json:"follow_latest"`
	Name             string              `json:"name"`
	Target           string              `json:"target"`
	OwnerAgentID     int64               `json:"owner_agent_id"`
	ExecutionMode    string              `json:"execution_mode"`
	Schedule         *Schedule           `json:"schedule,omitempty"`
	ProcedureVersion int                 `json:"procedure_version"`
	Parameters       map[string]any      `json:"parameters"`
}
type Assignment struct {
	ID               string `json:"id"`
	ProcessID        string `json:"process_id"`
	Revision         int    `json:"revision"`
	Status           string `json:"status"`
	SyncPending      bool   `json:"sync_pending"`
	SyncError        string `json:"sync_error"`
	NextRunAt        string `json:"next_run_at"`
	ScheduledVersion int    `json:"scheduled_version"`
	LastScheduleNote string `json:"last_schedule_note"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	AssignmentConfig
}

func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }
func params(v map[string]any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	return v
}
func validateParameters(schema []Parameter, values map[string]any, require bool) (map[string]any, error) {
	result := map[string]any{}
	known := map[string]bool{}
	for _, f := range schema {
		known[f.Key] = true
		v, ok := values[f.Key]
		if !ok || v == nil {
			v = f.Default
		}
		if v == nil || (f.Type == "string" && v == "") {
			if f.Required && require {
				return nil, fmt.Errorf("parameter %s is required", f.Key)
			}
			continue
		}
		valid := false
		switch f.Type {
		case "string":
			_, valid = v.(string)
		case "number":
			_, valid = v.(float64)
		case "boolean":
			_, valid = v.(bool)
		}
		if !valid {
			return nil, fmt.Errorf("parameter %s must be %s", f.Key, f.Type)
		}
		if len(f.Options) > 0 {
			found := false
			for _, option := range f.Options {
				if option == v {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("parameter %s must use an allowed option", f.Key)
			}
		}
		result[f.Key] = v
	}
	for k := range values {
		if !known[k] {
			return nil, fmt.Errorf("unknown parameter %s", k)
		}
	}
	if len(jsonText(result)) > 32000 {
		return nil, errors.New("parameters exceed 32 KB")
	}
	return result, nil
}
func validateSchema(fields []Parameter) error {
	if len(fields) > 50 {
		return errors.New("at most 50 parameters")
	}
	seen := map[string]bool{}
	for _, f := range fields {
		if !regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`).MatchString(f.Key) || seen[f.Key] {
			return errors.New("parameter keys must be unique identifiers, starting with a letter")
		}
		seen[f.Key] = true
		if f.Type != "string" && f.Type != "number" && f.Type != "boolean" {
			return errors.New("parameter type must be string, number, or boolean")
		}
		if len(f.Options) > 0 && f.Type != "string" {
			return errors.New("options only apply to string parameters")
		}
		if _, e := validateParameters([]Parameter{f}, nil, false); e != nil {
			return e
		}
	}
	return nil
}

const assignmentColumns = `id,process_id,revision,body_json,status,sync_pending,sync_error,next_run_at,scheduled_version,last_schedule_note,created_at,updated_at`

func scanAssignment(row scanner) (Assignment, error) {
	var x Assignment
	var body string
	e := row.Scan(&x.ID, &x.ProcessID, &x.Revision, &body, &x.Status, &x.SyncPending, &x.SyncError, &x.NextRunAt, &x.ScheduledVersion, &x.LastScheduleNote, &x.CreatedAt, &x.UpdatedAt)
	if e == nil {
		e = json.Unmarshal([]byte(body), &x.AssignmentConfig)
	}
	x.Parameters = params(x.Parameters)
	return x, e
}
func (a *App) assignments(id string) ([]Assignment, error) {
	rows, e := a.db.Query(`SELECT `+assignmentColumns+` FROM process_assignments WHERE process_id=? ORDER BY created_at,id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Assignment{}
	for rows.Next() {
		x, e := scanAssignment(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (a *App) assignment(project, process, id string) (Assignment, error) {
	if _, e := a.get(project, process); e != nil {
		return Assignment{}, e
	}
	x, e := scanAssignment(a.db.QueryRow(`SELECT `+assignmentColumns+` FROM process_assignments WHERE process_id=? AND id=?`, process, id))
	if errors.Is(e, sql.ErrNoRows) {
		e = errNotFound
	}
	return x, e
}
func (a *App) assigned(p *Process, x Assignment) (*Process, error) {
	d, e := a.definition(p.ID, x.ProcedureVersion)
	if e != nil {
		return nil, e
	}
	v := *p
	v.Definition = d
	v.Version = x.ProcedureVersion
	v.Assignment = &x
	v.OwnerAgentID = x.OwnerAgentID
	v.ExecutionMode = x.ExecutionMode
	v.Schedule = x.Schedule
	v.NextRunAt = x.NextRunAt
	v.ScheduledVersion = x.ScheduledVersion
	v.LastScheduleNote = x.LastScheduleNote
	v.SyncPending = x.SyncPending
	v.SyncError = x.SyncError
	if p.Status == "active" {
		v.Status = x.Status
	}
	return &v, nil
}
func (a *App) runDefinition(r Run) (Definition, error) {
	d, e := a.definition(r.ProcessID, r.Version)
	if e == nil {
		d.OwnerAgentID = r.Binding.OwnerAgentID
		d.ExecutionMode = r.Backend
		d.Schedule = r.Binding.Schedule
	}
	return d, e
}
func (a *App) saveAssignment(project, process, id string, expected int, c AssignmentConfig) (*Assignment, error) {
	p, e := a.get(project, process)
	if e != nil {
		return nil, e
	}
	if p.Status == "archived" {
		return nil, errors.New("process is archived")
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || len(c.Name) > 160 || len(c.Target) > 500 {
		return nil, errors.New("assignment name is required (max 160); target max 500")
	}
	if c.FollowLatest || c.ProcedureVersion == 0 {
		c.ProcedureVersion = p.Version
	}
	d, e := a.definition(process, c.ProcedureVersion)
	if e != nil {
		return nil, errNotFound
	}
	if c.ExecutionMode == "" {
		c.ExecutionMode = "agent"
	}
	d.OwnerAgentID = c.OwnerAgentID
	d.ExecutionMode = c.ExecutionMode
	d.Schedule = c.Schedule
	if e = d.validate(); e != nil {
		return nil, e
	}
	agent, e := a.ctx.GetAgent(c.OwnerAgentID)
	if e != nil {
		return nil, e
	}
	if agent.ProjectID != project {
		return nil, errors.New("owner is outside this project")
	}
	if e = a.validateRoles(project, d, c); e != nil {
		return nil, e
	}
	c.Parameters, e = validateParameters(d.Parameters, c.Parameters, false)
	if e != nil {
		return nil, e
	}
	now := timestamp()
	if id == "" {
		id = newID("assignment-")
		_, e = a.db.Exec(`INSERT INTO process_assignments(id,process_id,body_json,created_at,updated_at) VALUES(?,?,?,?,?)`, id, process, jsonText(c), now, now)
	} else {
		x, e2 := a.assignment(project, process, id)
		if e2 != nil {
			return nil, e2
		}
		if x.Revision != expected {
			return nil, errConflict
		}
		if x.Status == "archived" || x.SyncPending || (x.Status == "active" && p.Status == "active") || p.SyncPending {
			return nil, errors.New("pause and synchronize the assignment before editing")
		}
		_, e = a.db.Exec(`UPDATE process_assignments SET revision=revision+1,body_json=?,next_run_at='',scheduled_version=0,last_schedule_note='',updated_at=? WHERE id=?`, jsonText(c), now, id)
	}
	if e != nil {
		return nil, e
	}
	x, e := a.assignment(project, process, id)
	return &x, e
}
func (a *App) assignmentStatus(project, process, id, status string) (*Assignment, error) {
	p, e := a.get(project, process)
	if e != nil {
		return nil, e
	}
	x, e := a.assignment(project, process, id)
	if e != nil {
		return nil, e
	}
	if (x.Status == "archived" || p.Status == "archived") && status != "archived" {
		return nil, errors.New("archived assignments cannot reactivate")
	}
	if status == "active" {
		if p.Status != "active" {
			return nil, errors.New("activate the process first")
		}
		if e = a.checkAssignment(project, x); e != nil {
			return nil, e
		}
	}
	_, e = a.db.Exec(`UPDATE process_assignments SET status=?,sync_pending=1,sync_error='',updated_at=? WHERE id=?`, status, timestamp(), id)
	if e != nil {
		return nil, e
	}
	x.Status = status
	v, e := a.assigned(p, x)
	if e != nil {
		return nil, e
	}
	_ = a.synchronize(v)
	x, e = a.assignment(project, process, id)
	return &x, e
}
func (a *App) checkAssignment(project string, x Assignment) error {
	d, e := a.definition(x.ProcessID, x.ProcedureVersion)
	if e != nil {
		return e
	}
	if e = a.validateRoles(project, d, x.AssignmentConfig); e != nil {
		return e
	}
	if e = a.assignmentParametersReady(d, x); e != nil {
		return e
	}
	agent, e := a.ctx.GetAgent(x.OwnerAgentID)
	if e != nil {
		return e
	}
	if agent.ProjectID != project || strings.TrimSpace(agent.DefaultThreadID) == "" {
		return errors.New("owner needs a default thread in this project")
	}
	return nil
}
func (a *App) synchronizeProcess(p *Process) error {
	xs, e := a.assignments(p.ID)
	if e != nil {
		return e
	}
	var failures []error
	for _, x := range xs {
		v, e := a.assigned(p, x)
		if e == nil {
			e = a.synchronize(v)
		}
		if e != nil {
			failures = append(failures, e)
		}
	}
	e = errors.Join(failures...)
	message := ""
	if e != nil {
		message = e.Error()
	}
	_, saveErr := a.db.Exec(`UPDATE processes SET sync_pending=?,sync_error=? WHERE id=?`, e != nil, message, p.ID)
	return errors.Join(e, saveErr)
}
