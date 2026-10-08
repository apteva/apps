package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

type HistoryRow struct {
	ExecutorAgentIDs   []int64          `json:"executor_agent_ids"`
	Error              string           `json:"error,omitempty"`
	ErrorAvailable     bool             `json:"error_available"`
	DeliveryWarning    string           `json:"delivery_warning,omitempty"`
	DeliveryAttention  bool             `json:"delivery_attention"`
	MetadataOnly       bool             `json:"metadata_only"`
	ID                 string           `json:"id"`
	ProcessID          string           `json:"process_id"`
	ProcessName        string           `json:"process_name"`
	Version            int              `json:"version"`
	AssignmentID       string           `json:"assignment_id"`
	AssignmentRevision int              `json:"assignment_revision"`
	Assignment         WorkerAssignment `json:"assignment"`
	CreatedAt          string           `json:"created_at"`
	Kind               string           `json:"kind"`
	Workflow           bool             `json:"workflow"`
	State              string           `json:"state"`
	Progress           int              `json:"progress"`
	CurrentStep        string           `json:"current_step"`
	SummaryAvailable   bool             `json:"summary_available"`
	Summary            *SavedSummary    `json:"summary,omitempty"`
	Reread             RereadReference  `json:"reread"`
}

func (a *App) historyPage(project, process string, args map[string]any) (any, error) {
	// Scope through SQL, without loading any procedure body, run output or steps.
	if process != "" {
		var found string
		if e := a.db.QueryRow(`SELECT id FROM processes WHERE id=? AND project_id=?`, process, project).Scan(&found); e != nil {
			return nil, errNotFound
		}
	}
	limit, e := pageLimit(args)
	if e != nil {
		return nil, e
	}
	assignment, status := str(args, "assignment_id"), str(args, "status")
	if status != "" && !strings.Contains("|queued|running|waiting|scheduled|blocked|completed|failed|cancelled|ongoing|attention|", "|"+status+"|") {
		return nil, errors.New("invalid history status")
	}
	after, e := validateDate(str(args, "after"))
	if e != nil {
		return nil, e
	}
	before, e := validateDate(str(args, "before"))
	if e != nil {
		return nil, e
	}
	if after != "" && before != "" && after > before {
		return nil, errors.New("after must precede before")
	}
	owner := number(args, "owner_agent_id")
	qid := jsonText([]string{project, process, assignment, status, after, before, strconv.Itoa(owner)})
	c, e := parseCursor(str(args, "cursor"), qid)
	if e != nil {
		return nil, e
	}
	q := `SELECT r.id,r.process_id,r.version,r.assignment_id,r.assignment_revision,r.created_at,r.kind,r.workflow,r.state,r.progress,r.current_step,json_extract(r.assignment_json,'$.name'),coalesce(json_extract(r.assignment_json,'$.owner_agent_id'),0),json_extract(v.body_json,'$.name'),s.revision,s.body_json,s.author,s.created_at,CASE WHEN length(cast(r.error AS BLOB))<=512 THEN r.error ELSE '' END,r.error!='',CASE WHEN length(cast(r.delivery_warning AS BLOB))<=512 THEN r.delivery_warning ELSE '' END,r.delivery_warning!='',(SELECT json_group_array(json_extract(t.executor_json,'$.agent_id')) FROM process_step_runs t WHERE t.run_id=r.id AND json_extract(t.executor_json,'$.kind')='agent') FROM process_runs r JOIN processes p ON p.id=r.process_id JOIN process_versions v ON v.process_id=r.process_id AND v.version=r.version LEFT JOIN process_run_summaries s ON s.run_id=r.id AND s.revision=(SELECT max(revision) FROM process_run_summaries WHERE run_id=r.id) WHERE p.project_id=?`
	values := []any{project}
	if process != "" {
		q += ` AND r.process_id=?`
		values = append(values, process)
	}
	if owner > 0 {
		q += ` AND (json_extract(r.assignment_json,'$.owner_agent_id')=? OR EXISTS(SELECT 1 FROM process_step_runs t WHERE t.run_id=r.id AND json_extract(t.executor_json,'$.kind')='agent' AND json_extract(t.executor_json,'$.agent_id')=?))`
		values = append(values, owner, owner)
	}
	if assignment != "" {
		q += ` AND r.assignment_id=?`
		values = append(values, assignment)
	}
	if status == "ongoing" {
		q += ` AND r.state NOT IN ('completed','failed','cancelled')`
	} else if status == "attention" {
		q += ` AND (r.state IN ('waiting','blocked','failed') OR r.delivery_warning!='')`
	} else if status != "" {
		q += ` AND r.state=?`
		values = append(values, status)
	}
	if after != "" {
		q += ` AND r.history_at>=?`
		values = append(values, after)
	}
	if before != "" {
		q += ` AND r.history_at<=?`
		values = append(values, before)
	}
	if c.ID != "" {
		q += ` AND (r.history_at<? OR (r.history_at=? AND r.id<?))`
		values = append(values, c.Date, c.Date, c.ID)
	}
	q += ` ORDER BY r.history_at DESC,r.id DESC LIMIT ?`
	values = append(values, limit+1)
	rows, e := a.db.Query(q, values...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []HistoryRow{}
	next := ""
	size := 1024
	for rows.Next() {
		r := HistoryRow{MetadataOnly: true}
		var rev *int
		var body, author, created *string
		var name *string
		var executors string
		if e = rows.Scan(&r.ID, &r.ProcessID, &r.Version, &r.AssignmentID, &r.AssignmentRevision, &r.CreatedAt, &r.Kind, &r.Workflow, &r.State, &r.Progress, &r.CurrentStep, &name, &r.Assignment.OwnerAgentID, &r.ProcessName, &rev, &body, &author, &created, &r.Error, &r.ErrorAvailable, &r.DeliveryWarning, &r.DeliveryAttention, &executors); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(executors), &r.ExecutorAgentIDs); e != nil {
			return nil, e
		}
		r.Assignment.ID = r.AssignmentID
		r.Assignment.Revision = r.AssignmentRevision
		if name != nil {
			r.Assignment.Name = *name
		}
		r.Reread = runReread(r.ProcessID, r.ID)
		if rev != nil {
			s := SavedSummary{RunID: r.ID, Revision: *rev, Author: *author, CreatedAt: *created}
			if e = json.Unmarshal([]byte(*body), &s.RunSummary); e != nil {
				return nil, e
			}
			r.Summary = &s
			r.SummaryAvailable = true
		}
		// An old unbounded current_step/name can be recovered exactly by run_get.
		if len(r.CurrentStep) > 256 {
			r.CurrentStep = ""
		}
		if len(r.ProcessName) > 160 {
			r.ProcessName = ""
		}
		if len(r.Assignment.Name) > 160 {
			r.Assignment.Name = ""
		}
		b, _ := json.Marshal(r)
		if len(out) >= limit || size+len(b) > 14*1024 {
			if len(out) == 0 {
				return nil, errors.New("history metadata too large; retrieve run directly")
			}
			last := out[len(out)-1]
			date, e := validateDate(last.CreatedAt)
			if e != nil {
				return nil, e
			}
			next = cursorToken(qid, date, last.ID)
			break
		}
		size += len(b)
		out = append(out, r)
	}
	return map[string]any{"runs": out, "has_more": next != "", "next_cursor": next, "detail_view": "run_get", "summary_notice": "Agent-authored checkpoints; missing summaries are unavailable. Use exact evidence before relying on findings."}, rows.Err()
}

// Explicit paged export is for HTTP/UI clients. Workers cannot request it via
// MCP runs. Full inspection objects are loaded only for this selected page.
func (a *App) exportHistory(project, process string, args map[string]any) (any, error) {
	page, e := a.historyPage(project, process, args)
	if e != nil {
		return nil, e
	}
	result := page.(map[string]any)
	out := []any{}
	for _, row := range result["runs"].([]HistoryRow) {
		detail, e := a.directRunResponse(project, "operator", row.ProcessID, row.ID, "run_get", map[string]any{}, false)
		if e != nil {
			return nil, e
		}
		out = append(out, detail)
	}
	result["runs"] = out
	result["view"] = "export"
	return result, nil
}
