package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func requestProjectID(r *http.Request) string {
	if r == nil {
		return ""
	}
	if value := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID")); value != "" {
		return value
	}
	return strings.TrimSpace(r.URL.Query().Get("project_id"))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeTaskError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, errTaskNotFound) {
		status = http.StatusNotFound
	}
	http.Error(w, err.Error(), status)
}

func splitStates(value string) []string {
	out := []string{}
	for _, state := range strings.Split(value, ",") {
		if state = strings.TrimSpace(state); state != "" {
			out = append(out, state)
		}
	}
	return out
}

func (a *App) handleTasks(w http.ResponseWriter, r *http.Request) {
	projectID := requestProjectID(r)
	if projectID == "" {
		http.Error(w, "project_id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		agentID, _ := strconv.ParseInt(r.URL.Query().Get("agent_id"), 10, 64)
		filter := TaskFilter{ProjectID: projectID, AgentID: agentID, States: splitStates(r.URL.Query().Get("states")), AssignedThread: strings.TrimSpace(r.URL.Query().Get("assigned_thread_id")), AssociatedThread: strings.TrimSpace(r.URL.Query().Get("thread_id"))}
		if limit, _ := strconv.Atoi(r.URL.Query().Get("limit")); limit > 0 {
			filter.Limit = limit
		}
		if r.URL.Query().Get("include_runs") != "1" {
			empty := ""
			filter.ParentTaskID = &empty
		}
		tasks, err := a.store.List(filter)
		if err != nil {
			writeTaskError(w, err)
			return
		}
		counts, _ := a.store.Counts(projectID)
		writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "counts": counts, "enabled": true, "scheduling_enabled": true})
	case http.MethodPost:
		var body struct {
			AgentID          int64          `json:"agent_id"`
			Title            string         `json:"title"`
			Description      string         `json:"description"`
			AssignedThreadID string         `json:"assigned_thread_id"`
			IdempotencyKey   string         `json:"idempotency_key"`
			Schedule         *ScheduleInput `json:"schedule"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		agent, err := a.ctx.GetAgent(body.AgentID)
		if err != nil {
			http.Error(w, "agent not found", http.StatusNotFound)
			return
		}
		if agent.ProjectID != projectID {
			http.Error(w, "agent is outside this project", http.StatusForbidden)
			return
		}
		assigned := strings.TrimSpace(body.AssignedThreadID)
		if assigned == "" {
			assigned = strings.TrimSpace(agent.DefaultThreadID)
		}
		if assigned == "" {
			http.Error(w, "agent has no default thread", http.StatusConflict)
			return
		}
		task, created, err := a.store.Create(CreateTaskInput{AgentID: body.AgentID, ProjectID: projectID, Title: body.Title, Description: body.Description, State: stateQueued, AssignedThreadID: assigned, IdempotencyKey: body.IdempotencyKey, Schedule: body.Schedule})
		if err != nil {
			writeTaskError(w, err)
			return
		}
		if created && body.Schedule == nil {
			_ = a.notifyAssigned(task.ID, "task.assigned")
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, map[string]any{"task": task, "created": created})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTask(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/tasks/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		http.NotFound(w, r)
		return
	}
	task, err := a.store.Get(parts[0])
	if err != nil {
		writeTaskError(w, err)
		return
	}
	if projectID := requestProjectID(r); projectID == "" || projectID != task.ProjectID {
		http.Error(w, "task is outside this project", http.StatusForbidden)
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "events":
			if r.Method != http.MethodGet {
				http.Error(w, "GET only", http.StatusMethodNotAllowed)
				return
			}
			events, err := a.store.Events(task.ID)
			if err != nil {
				writeTaskError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"events": events})
			return
		case "runs":
			if r.Method != http.MethodGet {
				http.Error(w, "GET only", http.StatusMethodNotAllowed)
				return
			}
			parent := task.ID
			runs, err := a.store.List(TaskFilter{ProjectID: task.ProjectID, AgentID: task.AgentID, ParentTaskID: &parent, Limit: 200})
			if err != nil {
				writeTaskError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
			return
		case "pause", "resume", "run-now":
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", http.StatusMethodNotAllowed)
				return
			}
			var updated *Task
			switch parts[1] {
			case "pause":
				updated, err = a.store.Pause(task.ID, "api")
			case "resume":
				updated, err = a.store.Resume(task.ID, "api")
			case "run-now":
				updated, err = a.store.RunNow(task.ID, "api")
			}
			if err != nil {
				writeTaskError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"task": updated})
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		events, _ := a.store.Events(task.ID)
		writeJSON(w, http.StatusOK, map[string]any{"task": task, "events": events})
	case http.MethodPatch, http.MethodPut:
		var body struct {
			State            *string        `json:"state"`
			Progress         *int           `json:"progress"`
			ClearProgress    bool           `json:"clear_progress"`
			CurrentStep      *string        `json:"current_step"`
			AssignedThreadID *string        `json:"assigned_thread_id"`
			Result           *string        `json:"result"`
			Error            *string        `json:"error"`
			Schedule         *ScheduleInput `json:"schedule"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.Schedule != nil {
			updated, err := a.store.SetSchedule(task.ID, "api", *body.Schedule)
			if err != nil {
				writeTaskError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"task": updated})
			return
		}
		updated, _, err := a.store.Update(task.ID, "api", UpdateTaskInput{State: body.State, Progress: body.Progress, ClearProgress: body.ClearProgress, CurrentStep: body.CurrentStep, AssignedThreadID: body.AssignedThreadID, Result: body.Result, Error: body.Error})
		if err != nil {
			writeTaskError(w, err)
			return
		}
		if terminalState(updated.State) {
			_ = a.notifyCreator(updated)
		}
		writeJSON(w, http.StatusOK, map[string]any{"task": updated})
	case http.MethodDelete:
		state, reason := stateCancelled, "Cancelled by operator"
		updated, _, err := a.store.Update(task.ID, "api", UpdateTaskInput{State: &state, Error: &reason})
		if err != nil {
			writeTaskError(w, err)
			return
		}
		_, _ = a.store.db.Exec(`UPDATE tasks SET schedule_enabled=0, next_run_at=NULL WHERE id=?`, task.ID)
		_ = a.notifyCreator(updated)
		writeJSON(w, http.StatusOK, map[string]any{"task": updated})
	default:
		http.Error(w, "GET, PATCH, or DELETE", http.StatusMethodNotAllowed)
	}
}
