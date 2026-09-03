package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMobileRecentLimit = 4
	maxMobileRecentLimit     = 12
	maxMobileSummaryTasks    = 500
)

type mobileSummaryCounts struct {
	Active    int `json:"active"`
	Scheduled int `json:"scheduled"`
	Blocked   int `json:"blocked"`
}

type mobileTaskSummary struct {
	Counts   mobileSummaryCounts `json:"counts"`
	Active   []Task              `json:"active"`
	Upcoming []Task              `json:"upcoming"`
	Recent   []Task              `json:"recent"`
}

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

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
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

func mobileRecentLimit(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultMobileRecentLimit
	}
	limit, err := strconv.Atoi(value)
	if err != nil {
		return defaultMobileRecentLimit
	}
	return clampMobileRecentLimit(limit)
}

func clampMobileRecentLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > maxMobileRecentLimit {
		return maxMobileRecentLimit
	}
	return limit
}

func mobileActiveRank(state string) int {
	switch state {
	case stateBlocked:
		return 0
	case stateRunning:
		return 1
	case stateQueued:
		return 2
	case stateWaiting:
		return 3
	default:
		return 4
	}
}

func taskRecentTime(task Task) time.Time {
	if task.CompletedAt != nil {
		return task.CompletedAt.UTC()
	}
	return task.UpdatedAt.UTC()
}

func buildMobileTaskSummary(tasks []Task, recentLimit int) mobileTaskSummary {
	summary := mobileTaskSummary{Active: []Task{}, Upcoming: []Task{}, Recent: []Task{}}
	for _, task := range tasks {
		isScheduleDefinition := task.ScheduleKind != "" && task.State == stateWaiting
		if isScheduleDefinition && task.ScheduleEnabled && task.NextRunAt != nil {
			summary.Upcoming = append(summary.Upcoming, task)
		}
		if !isScheduleDefinition {
			switch task.State {
			case stateQueued, stateRunning, stateWaiting, stateBlocked:
				summary.Active = append(summary.Active, task)
				if task.State == stateBlocked {
					summary.Counts.Blocked++
				}
			}
		}
		if terminalState(task.State) {
			summary.Recent = append(summary.Recent, task)
		}
	}
	summary.Counts.Active = len(summary.Active)
	summary.Counts.Scheduled = len(summary.Upcoming)

	sort.SliceStable(summary.Active, func(i, j int) bool {
		left, right := summary.Active[i], summary.Active[j]
		if mobileActiveRank(left.State) != mobileActiveRank(right.State) {
			return mobileActiveRank(left.State) < mobileActiveRank(right.State)
		}
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		return left.ID > right.ID
	})
	sort.SliceStable(summary.Upcoming, func(i, j int) bool {
		left, right := summary.Upcoming[i], summary.Upcoming[j]
		if !left.NextRunAt.Equal(*right.NextRunAt) {
			return left.NextRunAt.Before(*right.NextRunAt)
		}
		return left.ID < right.ID
	})
	sort.SliceStable(summary.Recent, func(i, j int) bool {
		left, right := summary.Recent[i], summary.Recent[j]
		leftTime, rightTime := taskRecentTime(left), taskRecentTime(right)
		if !leftTime.Equal(rightTime) {
			return leftTime.After(rightTime)
		}
		return left.ID > right.ID
	})

	if len(summary.Active) > 6 {
		summary.Active = summary.Active[:6]
	}
	if len(summary.Upcoming) > 4 {
		summary.Upcoming = summary.Upcoming[:4]
	}
	recentLimit = clampMobileRecentLimit(recentLimit)
	if len(summary.Recent) > recentLimit {
		summary.Recent = summary.Recent[:recentLimit]
	}
	return summary
}

func (a *App) handleMobileSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	projectID := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	if projectID == "" {
		http.Error(w, "project context required", http.StatusBadRequest)
		return
	}
	empty := ""
	tasks, err := a.store.List(TaskFilter{ProjectID: projectID, ParentTaskID: &empty, Limit: maxMobileSummaryTasks})
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, buildMobileTaskSummary(tasks, mobileRecentLimit(r.URL.Query().Get("recent_limit"))))
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
		filter := TaskFilter{ProjectID: projectID, AgentID: agentID, States: splitStates(r.URL.Query().Get("states"))}
		if limit, _ := strconv.Atoi(r.URL.Query().Get("limit")); limit > 0 {
			filter.Limit = limit
		}
		includeRuns := r.URL.Query().Get("include_runs") == "1"
		if !includeRuns {
			empty := ""
			filter.ParentTaskID = &empty
		}
		tasks, err := a.store.List(filter)
		if err != nil {
			writeTaskError(w, err)
			return
		}
		counts, _ := a.store.Counts(projectID, agentID, includeRuns)
		writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "counts": counts, "enabled": true, "scheduling_enabled": true})
	case http.MethodPost:
		var body struct {
			AgentID          int64          `json:"agent_id"`
			Title            string         `json:"title"`
			Description      string         `json:"description"`
			AssignedThreadID string         `json:"assigned_thread_id"`
			IdempotencyKey   string         `json:"idempotency_key"`
			OperationKey     string         `json:"operation_key"`
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
		task, created, err := a.store.Create(CreateTaskInput{AgentID: body.AgentID, ProjectID: projectID, Title: body.Title, Description: body.Description, State: stateQueued, AssignedThreadID: assigned, IdempotencyKey: body.IdempotencyKey, OperationKey: body.OperationKey, Schedule: body.Schedule})
		if err != nil {
			writeTaskError(w, err)
			return
		}
		if created && body.Schedule == nil {
			_ = a.notifyAssigned(task, assigned, "task.assigned")
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
		case "executions":
			if r.Method != http.MethodGet {
				http.Error(w, "GET only", http.StatusMethodNotAllowed)
				return
			}
			executions, err := a.store.AgentExecutions(task.ID)
			if err != nil {
				writeTaskError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"agent_executions": executions})
			return
		case "recover":
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", http.StatusMethodNotAllowed)
				return
			}
			var body struct {
				Reason           string `json:"reason"`
				IdempotencyKey   string `json:"idempotency_key"`
				AssignedThreadID string `json:"assigned_thread_id"`
			}
			if err := decodeStrictJSON(w, r, &body); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			assigned := strings.TrimSpace(body.AssignedThreadID)
			if assigned == "" {
				assigned = task.AssignedThreadID
			}
			recovery, created, err := a.store.RecoverOccurrence(task.ID, "api", assigned,
				body.Reason, body.IdempotencyKey, nowUTC())
			if err != nil {
				writeTaskError(w, err)
				return
			}
			if created {
				recovery, _, err = a.store.MarkDispatched(recovery.ID, "tasks:recovery", nowUTC())
				if err == nil {
					err = a.notifyAssigned(recovery, recovery.AssignedThreadID, "task.recovery.ready")
				}
			}
			if err != nil {
				writeTaskError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"task": recovery, "created": created})
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
		executions, _ := a.store.AgentExecutions(task.ID)
		writeJSON(w, http.StatusOK, map[string]any{"task": task, "events": events, "agent_executions": executions})
	case http.MethodPatch, http.MethodPut:
		var body struct {
			Title            *string        `json:"title"`
			Description      *string        `json:"description"`
			State            *string        `json:"state"`
			Progress         *int           `json:"progress"`
			ClearProgress    bool           `json:"clear_progress"`
			CurrentStep      *string        `json:"current_step"`
			AssignedThreadID *string        `json:"assigned_thread_id"`
			Result           *string        `json:"result"`
			ResultReference  *string        `json:"result_reference"`
			Error            *string        `json:"error"`
			Schedule         *ScheduleInput `json:"schedule"`
		}
		if err := decodeStrictJSON(w, r, &body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		updated, changed, err := a.store.Update(task.ID, "api", UpdateTaskInput{
			Title: body.Title, Description: body.Description, State: body.State, Progress: body.Progress,
			ClearProgress: body.ClearProgress, CurrentStep: body.CurrentStep,
			AssignedThreadID: body.AssignedThreadID, Result: body.Result,
			ResultReference: body.ResultReference, Error: body.Error, Schedule: body.Schedule,
		})
		if err != nil {
			writeTaskError(w, err)
			return
		}
		if terminalState(updated.State) {
			_ = a.notifyCreator(updated)
		}
		writeJSON(w, http.StatusOK, map[string]any{"task": updated, "changed": changed})
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
