package main

import "sort"

// References contain existing tool names and exact typed arguments. They are
// recovery instructions, never summaries or replacements for saved evidence.
type RereadReference struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

func procedureReread(process string, version int) RereadReference {
	return RereadReference{Tool: "processes_get", Args: map[string]any{"process_id": process, "version": version}}
}
func runReread(process, run string) RereadReference {
	return RereadReference{Tool: "processes_run_get", Args: map[string]any{"process_id": process, "run_id": run}}
}
func stepReread(process, run, step string) RereadReference {
	return RereadReference{Tool: "processes_step_get", Args: map[string]any{"process_id": process, "run_id": run, "step_id": step, "include_context": true}}
}

type ProcessMetadata struct {
	ID              string          `json:"id"`
	ProjectID       string          `json:"project_id"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Status          string          `json:"status"`
	Version         int             `json:"version"`
	Category        string          `json:"category,omitempty"`
	Tags            []string        `json:"tags,omitempty"`
	OwnerAgentIDs   []int64         `json:"owner_agent_ids"`
	AssignmentCount int             `json:"assignment_count"`
	StepCount       int             `json:"step_count"`
	NextRunAt       string          `json:"next_run_at,omitempty"`
	SyncPending     bool            `json:"sync_pending"`
	SyncError       string          `json:"sync_error,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
	Readiness       Readiness       `json:"readiness"`
	Reread          RereadReference `json:"reread"`
}

func processMetadata(p Process) ProcessMetadata {
	owners := []int64{}
	seen := map[int64]bool{}
	for _, x := range p.Assignments {
		if !seen[x.OwnerAgentID] {
			owners = append(owners, x.OwnerAgentID)
			seen[x.OwnerAgentID] = true
		}
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i] < owners[j] })
	return ProcessMetadata{ID: p.ID, ProjectID: p.ProjectID, Name: p.Name, Description: p.Description,
		Status: p.Status, Version: p.Version, Category: p.Category, Tags: p.Tags, OwnerAgentIDs: owners,
		AssignmentCount: len(p.Assignments), StepCount: len(p.Steps), NextRunAt: p.NextRunAt,
		SyncPending: p.SyncPending, SyncError: p.SyncError, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		Readiness: p.Readiness, Reread: procedureReread(p.ID, p.Version)}
}

type VersionMetadata struct {
	Version   int             `json:"version"`
	CreatedBy string          `json:"created_by"`
	CreatedAt string          `json:"created_at"`
	Reread    RereadReference `json:"reread"`
}

func (a *App) getMCP(project, id string, args map[string]any) (any, error) {
	p, err := a.get(project, id)
	if err != nil {
		return nil, err
	}
	// Historical definitions are not loaded at all until explicitly requested.
	rows, err := a.db.Query(`SELECT version,created_by,created_at FROM process_versions WHERE process_id=? ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	versions := []VersionMetadata{}
	for rows.Next() {
		var v VersionMetadata
		if err = rows.Scan(&v.Version, &v.CreatedBy, &v.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		v.Reread = procedureReread(id, v.Version)
		versions = append(versions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := map[string]any{"process": p, "versions": versions, "reread": procedureReread(id, p.Version)}
	if version := number(args, "version"); version > 0 {
		d, e := a.definition(id, version)
		if e != nil {
			return nil, errNotFound
		}
		result["process"], result["definition"], result["reread"] = processMetadata(*p), d, procedureReread(id, version)
	}
	return result, nil
}

// Full procedure instructions occur once in run_get.definition. All other step
// fields, including human approval receipts and attribution, remain verbatim.
type MCPRunStep struct {
	StepRun
	Definition *Step           `json:"definition,omitempty"`
	Reread     RereadReference `json:"reread"`
}

func mcpRunRead(process string, r Run, d Definition, steps []StepRun) map[string]any {
	r.Steps = nil
	result := map[string]any{"run": r, "definition": d, "reread": runReread(process, r.ID)}
	if r.Workflow {
		states := make([]MCPRunStep, 0, len(steps))
		for _, s := range steps {
			states = append(states, MCPRunStep{StepRun: s, Reread: stepReread(process, r.ID, s.ID)})
		}
		result["steps"] = states
	}
	return result
}
func mcpRunAcknowledgement(process string, r Run) map[string]any {
	result := map[string]any{"process_id": process, "run_id": r.ID, "procedure_version": r.Version,
		"state": r.State, "progress": r.Progress, "done": terminal(r.State), "reread": runReread(process, r.ID)}
	if r.Error != "" {
		result["error"] = r.Error
	}
	return result
}
