package main

// Local access is target-owned. A missing rule deliberately preserves the
// trusted-project behavior from earlier A2A releases; an explicit wildcard
// rule or subject allowlist opts a target into managed access.
import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	sdk "github.com/apteva/app-sdk"
)

const wildcardSubjectID int64 = 0

var localAccessActions = map[string]bool{
	"discover": true,
	"invoke":   true,
	"message":  true,
	"continue": true,
}

type accessAgent struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Attached bool   `json:"attached"`
}

type accessPolicy struct {
	TargetAgentID int64   `json:"target_agent_id"`
	Action        string  `json:"action"`
	Mode          string  `json:"mode"` // compatibility, all, selected, none
	SubjectIDs    []int64 `json:"subject_ids,omitempty"`
}

type accessEdge struct {
	FromAgentID int64  `json:"from_agent_id"`
	ToAgentID   int64  `json:"to_agent_id"`
	Action      string `json:"action"`
	Allowed     bool   `json:"allowed"`
}

func localAccessAllowed(app *sdk.AppCtx, projectID string, subjectID, targetID int64, action string) (bool, error) {
	if !localAccessActions[action] {
		return false, fmt.Errorf("unsupported local access action %q", action)
	}
	rows, err := app.AppDB().Query(`SELECT subject_agent_id, effect
		FROM a2a_local_access WHERE project_id=? AND target_agent_id=? AND action=?`, projectID, targetID, action)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	hasRules := false
	matched := false
	allowed := false
	for rows.Next() {
		var subject int64
		var effect string
		if err := rows.Scan(&subject, &effect); err != nil {
			return false, err
		}
		hasRules = true
		if subject != wildcardSubjectID && subject != subjectID {
			continue
		}
		matched = true
		if effect == "deny" {
			return false, nil
		}
		if effect == "allow" {
			allowed = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if !matched {
		// A target with explicit rules is closed to subjects that do not match.
		return !hasRules, nil
	}
	return allowed, nil
}

func localAccessPolicies(db *sql.DB, projectID string) ([]accessPolicy, error) {
	rows, err := db.Query(`SELECT target_agent_id, action, subject_agent_id, effect
		FROM a2a_local_access WHERE project_id=? ORDER BY target_agent_id, action, subject_agent_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type bucket struct {
		target, subject int64
		action, effect  string
	}
	var raw []bucket
	for rows.Next() {
		var item bucket
		if err := rows.Scan(&item.target, &item.action, &item.subject, &item.effect); err != nil {
			return nil, err
		}
		raw = append(raw, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	policies := make([]accessPolicy, 0)
	for _, item := range raw {
		idx := -1
		for i := range policies {
			if policies[i].TargetAgentID == item.target && policies[i].Action == item.action {
				idx = i
				break
			}
		}
		if idx < 0 {
			policies = append(policies, accessPolicy{TargetAgentID: item.target, Action: item.action})
			idx = len(policies) - 1
		}
		if item.subject == wildcardSubjectID {
			if item.effect == "deny" {
				policies[idx].Mode = "none"
			} else {
				policies[idx].Mode = "all"
			}
		} else if item.effect == "allow" {
			policies[idx].SubjectIDs = append(policies[idx].SubjectIDs, item.subject)
			if policies[idx].Mode == "" {
				policies[idx].Mode = "selected"
			}
		}
	}
	for i := range policies {
		if policies[i].Mode == "" {
			policies[i].Mode = "selected"
		}
	}
	return policies, nil
}

func localAccessAgents(app *sdk.AppCtx, projectID string) ([]sdk.PlatformAgent, error) {
	agents, err := sdk.ListAgentsVia(app.PlatformAPI(), projectID)
	if err != nil {
		return nil, err
	}
	out := make([]sdk.PlatformAgent, 0, len(agents))
	for _, agent := range agents {
		if agent.ProjectID == projectID {
			out = append(out, agent)
		}
	}
	return out, nil
}

func (a *App) handleAccess(w http.ResponseWriter, r *http.Request) {
	app := panelContext(w, r)
	if app == nil {
		return
	}
	projectID := app.CurrentProject()
	switch r.Method {
	case http.MethodGet:
		agents, err := localAccessAgents(app, projectID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		policies, err := localAccessPolicies(app.AppDB(), projectID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		outAgents := make([]accessAgent, 0, len(agents))
		for _, agent := range agents {
			outAgents = append(outAgents, accessAgent{ID: agent.ID, Name: agent.Name, Status: agent.Status, Attached: agent.AttachedToCaller})
		}
		edges := make([]accessEdge, 0)
		for _, from := range agents {
			if !from.AttachedToCaller {
				continue
			}
			for _, to := range agents {
				if from.ID == to.ID || !to.AttachedToCaller {
					continue
				}
				allowed, err := localAccessAllowed(app, projectID, from.ID, to.ID, "invoke")
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				edges = append(edges, accessEdge{FromAgentID: from.ID, ToAgentID: to.ID, Action: "invoke", Allowed: allowed})
			}
		}
		writeJSON(w, map[string]any{"agents": outAgents, "policies": policies, "edges": edges, "default": "all_attached"})
	case http.MethodPatch:
		var input struct {
			TargetAgentID int64   `json:"target_agent_id"`
			Action        string  `json:"action"`
			Mode          string  `json:"mode"`
			SubjectIDs    []int64 `json:"subject_ids"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if input.TargetAgentID <= 0 || !localAccessActions[input.Action] {
			http.Error(w, "target_agent_id and a valid action are required", http.StatusBadRequest)
			return
		}
		if input.Mode != "compatibility" && input.Mode != "all" && input.Mode != "selected" && input.Mode != "none" {
			http.Error(w, "mode must be compatibility, all, selected, or none", http.StatusBadRequest)
			return
		}
		agents, err := localAccessAgents(app, projectID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		valid := map[int64]bool{}
		for _, agent := range agents {
			if agent.AttachedToCaller {
				valid[agent.ID] = true
			}
		}
		if !valid[input.TargetAgentID] {
			http.Error(w, "target must be an attached local agent", http.StatusBadRequest)
			return
		}
		for _, subject := range input.SubjectIDs {
			if !valid[subject] || subject == input.TargetAgentID {
				http.Error(w, "subject_ids must name other attached local agents", http.StatusBadRequest)
				return
			}
		}
		if input.Mode == "selected" && len(input.SubjectIDs) == 0 {
			input.Mode = "none"
		}
		tx, err := app.AppDB().Begin()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rollback := func() { _ = tx.Rollback() }
		if _, err = tx.Exec(`DELETE FROM a2a_local_access WHERE project_id=? AND target_agent_id=? AND action=?`, projectID, input.TargetAgentID, input.Action); err != nil {
			rollback()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if input.Mode != "compatibility" {
			now := nowUTC()
			if input.Mode == "all" || input.Mode == "none" {
				effect := "allow"
				if input.Mode == "none" {
					effect = "deny"
				}
				_, err = tx.Exec(`INSERT INTO a2a_local_access(project_id,target_agent_id,subject_agent_id,action,effect,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, projectID, input.TargetAgentID, wildcardSubjectID, input.Action, effect, now, now)
			} else {
				for _, subject := range input.SubjectIDs {
					if _, err = tx.Exec(`INSERT INTO a2a_local_access(project_id,target_agent_id,subject_agent_id,action,effect,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, projectID, input.TargetAgentID, subject, input.Action, "allow", now, now); err != nil {
						break
					}
				}
			}
		}
		if err != nil {
			rollback()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err = tx.Commit(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"updated": true, "target_agent_id": input.TargetAgentID, "action": input.Action, "mode": input.Mode, "subject_ids": input.SubjectIDs})
	default:
		http.Error(w, "GET or PATCH only", http.StatusMethodNotAllowed)
	}
}
