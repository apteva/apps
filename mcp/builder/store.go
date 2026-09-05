package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	goalPlanning        = "planning"
	goalActive          = "active"
	goalWaitingApproval = "waiting_approval"
	goalBlocked         = "blocked"
	goalCompleted       = "completed"
	goalCancelled       = "cancelled"

	validationBuildOnly  = "build_only"
	validationSimulated  = "simulated"
	validationContinuous = "continuous"
	validationCheckKey   = "builder_validation"
)

var validGoalStatuses = stringSet(goalPlanning, goalActive, goalWaitingApproval, goalBlocked, goalCompleted, goalCancelled)
var validStepStatuses = stringSet("pending", "active", "waiting_approval", "blocked", "completed", "skipped", "failed")
var validApprovalStates = stringSet("none", "required", "requested", "approved", "denied")
var validCheckStatuses = stringSet("pending", "passing", "failing", "blocked")
var validResourceKinds = stringSet("agent", "app", "integration", "credential", "connection", "project_setting", "other")
var validResourceStatuses = stringSet("planned", "creating", "configured", "ready", "drifted", "needs_attention", "removed")
var validEventKinds = stringSet("created", "plan", "progress", "decision", "risk", "approval", "operator_input", "status", "check", "resource", "note")
var validValidationModes = stringSet(validationBuildOnly, validationSimulated, validationContinuous)

type ValidationPolicy struct {
	MaxRuns           int  `json:"max_runs"`
	MaxRepairAttempts int  `json:"max_repair_attempts"`
	AutoRepair        bool `json:"auto_repair"`
	InstallSafeApps   bool `json:"install_safe_apps"`
	RunOnChange       bool `json:"run_on_change"`
}

type ValidationPolicyInput struct {
	MaxRuns           *int  `json:"max_runs"`
	MaxRepairAttempts *int  `json:"max_repair_attempts"`
	AutoRepair        *bool `json:"auto_repair"`
	InstallSafeApps   *bool `json:"install_safe_apps"`
	RunOnChange       *bool `json:"run_on_change"`
}

type Goal struct {
	ID                string           `json:"id"`
	ProjectID         string           `json:"project_id"`
	OwnerAgentID      int64            `json:"owner_agent_id"`
	Title             string           `json:"title"`
	Objective         string           `json:"objective"`
	Status            string           `json:"status"`
	CurrentPhase      string           `json:"current_phase"`
	Summary           string           `json:"summary"`
	NextAction        string           `json:"next_action"`
	SuccessCriteria   []string         `json:"success_criteria"`
	Constraints       []string         `json:"constraints"`
	ValidationMode    string           `json:"validation_mode"`
	ValidationPolicy  ValidationPolicy `json:"validation_policy"`
	IdempotencyKey    string           `json:"idempotency_key,omitempty"`
	CreatedByThreadID string           `json:"created_by_thread_id,omitempty"`
	CreatedAt         string           `json:"created_at"`
	UpdatedAt         string           `json:"updated_at"`
	CompletedAt       string           `json:"completed_at,omitempty"`
}

type Step struct {
	ID             string `json:"id"`
	GoalID         string `json:"goal_id"`
	Position       int    `json:"position"`
	Title          string `json:"title"`
	Detail         string `json:"detail"`
	Status         string `json:"status"`
	ApprovalState  string `json:"approval_state"`
	BlockingReason string `json:"blocking_reason,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	CompletedAt    string `json:"completed_at,omitempty"`
}

type GoalCheck struct {
	ID          string         `json:"id"`
	GoalID      string         `json:"goal_id"`
	Key         string         `json:"key"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Status      string         `json:"status"`
	Result      string         `json:"result"`
	Evidence    map[string]any `json:"evidence"`
	CheckedAt   string         `json:"checked_at,omitempty"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

type ManagedResource struct {
	ID            string         `json:"id"`
	GoalID        string         `json:"goal_id"`
	Key           string         `json:"key"`
	Kind          string         `json:"kind"`
	Name          string         `json:"name"`
	ExternalID    string         `json:"external_id,omitempty"`
	Status        string         `json:"status"`
	DesiredState  map[string]any `json:"desired_state"`
	ObservedState map[string]any `json:"observed_state"`
	Note          string         `json:"note,omitempty"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
	LastCheckedAt string         `json:"last_checked_at,omitempty"`
}

type BuilderEvent struct {
	ID            string         `json:"id"`
	GoalID        string         `json:"goal_id"`
	Kind          string         `json:"kind"`
	Title         string         `json:"title"`
	Detail        string         `json:"detail"`
	Data          map[string]any `json:"data"`
	ActorAgentID  int64          `json:"actor_agent_id,omitempty"`
	ActorThreadID string         `json:"actor_thread_id,omitempty"`
	EventKey      string         `json:"event_key,omitempty"`
	CreatedAt     string         `json:"created_at"`
}

type CompletionReadiness struct {
	Ready             bool     `json:"ready"`
	IncompleteSteps   int      `json:"incomplete_steps"`
	UnsatisfiedChecks int      `json:"unsatisfied_checks"`
	Issues            []string `json:"issues"`
}

type GoalBundle struct {
	Goal       *Goal               `json:"goal"`
	Steps      []*Step             `json:"steps"`
	Checks     []*GoalCheck        `json:"checks"`
	Resources  []*ManagedResource  `json:"resources"`
	Events     []*BuilderEvent     `json:"events"`
	Completion CompletionReadiness `json:"completion"`
}

type GoalIdentity struct {
	ProjectID    string
	OwnerAgentID int64
}

type CreateGoalInput struct {
	ProjectID        string
	OwnerAgentID     int64
	ThreadID         string
	Title            string
	Objective        string
	SuccessCriteria  []string
	Constraints      []string
	ValidationMode   string
	ValidationPolicy ValidationPolicyInput
	IdempotencyKey   string
}

type SetValidationInput struct {
	Mode          string
	Policy        ValidationPolicyInput
	ActorAgentID  int64
	ActorThreadID string
	EventKey      string
}

type PlanStepInput struct {
	Title            string `json:"title"`
	Detail           string `json:"detail"`
	RequiresApproval bool   `json:"requires_approval"`
}

type PlanCheckInput struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type UpdateStepInput struct {
	Status         *string
	ApprovalState  *string
	BlockingReason *string
	Note           string
	EventKey       string
	ActorAgentID   int64
	ActorThreadID  string
}

type UpsertResourceInput struct {
	Key              string
	Kind             string
	Name             string
	ExternalID       string
	ExternalIDSet    bool
	Status           string
	DesiredState     map[string]any
	DesiredStateSet  bool
	ObservedState    map[string]any
	ObservedStateSet bool
	Note             string
	NoteSet          bool
	ActorAgentID     int64
	ActorThreadID    string
	EventKey         string
}

type RecordCheckInput struct {
	Key           string
	Name          string
	Description   string
	Status        string
	Result        string
	Evidence      map[string]any
	ActorAgentID  int64
	ActorThreadID string
	EventKey      string
}

type RecordEventInput struct {
	Kind          string
	Title         string
	Detail        string
	Data          map[string]any
	ActorAgentID  int64
	ActorThreadID string
	EventKey      string
}

type UpdateGoalInput struct {
	Status        *string
	CurrentPhase  *string
	Summary       *string
	NextAction    *string
	ActorAgentID  int64
	ActorThreadID string
	EventKey      string
}

type builderStore struct{ db *sql.DB }

func newBuilderStore(db *sql.DB) *builderStore { return &builderStore{db: db} }

func (s *builderStore) CreateGoal(input CreateGoalInput) (*Goal, bool, error) {
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.Title = strings.TrimSpace(input.Title)
	input.Objective = strings.TrimSpace(input.Objective)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.ProjectID == "" || input.OwnerAgentID <= 0 || input.Title == "" || input.Objective == "" {
		return nil, false, errors.New("project, owner agent, title, and objective are required")
	}
	validationMode, validationPolicy, err := normalizeValidationPolicy(input.ValidationMode, input.ValidationPolicy)
	if err != nil {
		return nil, false, err
	}
	if input.IdempotencyKey != "" {
		goal, err := s.goalByIdempotency(input.ProjectID, input.OwnerAgentID, input.IdempotencyKey)
		if err == nil {
			return goal, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
	}

	now := nowUTC()
	goal := &Goal{
		ID: newID("goal"), ProjectID: input.ProjectID, OwnerAgentID: input.OwnerAgentID,
		Title: input.Title, Objective: input.Objective, Status: goalPlanning,
		CurrentPhase: "Planning", NextAction: "Inspect current state and set the execution plan",
		SuccessCriteria: cleanStrings(input.SuccessCriteria), Constraints: cleanStrings(input.Constraints),
		ValidationMode: validationMode, ValidationPolicy: validationPolicy,
		IdempotencyKey: input.IdempotencyKey, CreatedByThreadID: strings.TrimSpace(input.ThreadID),
		CreatedAt: now, UpdatedAt: now,
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO builder_goals
		(id,project_id,owner_agent_id,title,objective,status,current_phase,summary,next_action,success_criteria,constraints_json,validation_mode,validation_policy_json,idempotency_key,created_by_thread,created_at,updated_at,completed_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		goal.ID, goal.ProjectID, goal.OwnerAgentID, goal.Title, goal.Objective, goal.Status,
		goal.CurrentPhase, goal.Summary, goal.NextAction, encodeJSON(goal.SuccessCriteria), encodeJSON(goal.Constraints),
		goal.ValidationMode, encodeJSON(goal.ValidationPolicy),
		goal.IdempotencyKey, goal.CreatedByThreadID, goal.CreatedAt, goal.UpdatedAt, goal.CompletedAt)
	if err != nil {
		if input.IdempotencyKey != "" && isUniqueConstraint(err) {
			_ = tx.Rollback()
			existing, lookupErr := s.goalByIdempotency(input.ProjectID, input.OwnerAgentID, input.IdempotencyKey)
			return existing, false, lookupErr
		}
		return nil, false, err
	}
	_, err = recordEventTx(tx, goal.ID, RecordEventInput{
		Kind: "created", Title: "Goal created", Detail: goal.Objective,
		Data:         map[string]any{"validation_mode": goal.ValidationMode, "validation_policy": goal.ValidationPolicy},
		ActorAgentID: input.OwnerAgentID, ActorThreadID: input.ThreadID,
		EventKey: eventKey("goal-created", input.IdempotencyKey),
	})
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return goal, true, nil
}

func (s *builderStore) goalByIdempotency(projectID string, ownerAgentID int64, key string) (*Goal, error) {
	return scanGoal(s.db.QueryRow(goalSelect+` WHERE project_id=? AND owner_agent_id=? AND idempotency_key=?`, projectID, ownerAgentID, key))
}

func (s *builderStore) GetGoal(identity GoalIdentity, goalID string) (*Goal, error) {
	goal, err := scanGoal(s.db.QueryRow(goalSelect+` WHERE id=? AND project_id=? AND owner_agent_id=?`, strings.TrimSpace(goalID), identity.ProjectID, identity.OwnerAgentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("goal not found in the current Helper and project scope")
	}
	return goal, err
}

func (s *builderStore) ListGoals(identity GoalIdentity, statuses []string, limit int) ([]*Goal, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query := goalSelect + ` WHERE project_id=? AND owner_agent_id=?`
	args := []any{identity.ProjectID, identity.OwnerAgentID}
	if len(statuses) > 0 {
		placeholders := make([]string, 0, len(statuses))
		for _, status := range statuses {
			status = strings.TrimSpace(status)
			if !validGoalStatuses[status] {
				return nil, fmt.Errorf("invalid goal status %q", status)
			}
			placeholders = append(placeholders, "?")
			args = append(args, status)
		}
		query += ` AND status IN (` + strings.Join(placeholders, ",") + `)`
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	goals := make([]*Goal, 0)
	for rows.Next() {
		goal, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}
		goals = append(goals, goal)
	}
	return goals, rows.Err()
}

func (s *builderStore) GetBundle(identity GoalIdentity, goalID string) (*GoalBundle, error) {
	goal, err := s.GetGoal(identity, goalID)
	if err != nil {
		return nil, err
	}
	steps, err := s.listSteps(goal.ID)
	if err != nil {
		return nil, err
	}
	checks, err := s.listChecks(goal.ID)
	if err != nil {
		return nil, err
	}
	resources, err := s.listResources(goal.ID)
	if err != nil {
		return nil, err
	}
	events, err := s.listEvents(goal.ID, 100)
	if err != nil {
		return nil, err
	}
	return &GoalBundle{
		Goal: goal, Steps: steps, Checks: checks, Resources: resources, Events: events,
		Completion: completionReadiness(steps, checks),
	}, nil
}

func (s *builderStore) SetValidation(identity GoalIdentity, goalID string, input SetValidationInput) (*GoalBundle, error) {
	mode, policy, err := normalizeValidationPolicy(input.Mode, input.Policy)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	goal, err := goalForUpdateTx(tx, identity, goalID)
	if err != nil {
		return nil, err
	}
	if goal.Status == goalCompleted || goal.Status == goalCancelled {
		return nil, fmt.Errorf("cannot change validation for a %s goal", goal.Status)
	}
	eKey := eventKey("validation", input.EventKey)
	if eKey != "" {
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM builder_events WHERE goal_id=? AND event_key=?`, goalID, eKey).Scan(&exists); err == nil {
			_ = tx.Rollback()
			return s.GetBundle(identity, goalID)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	now := nowUTC()
	if _, err := tx.Exec(`UPDATE builder_goals SET validation_mode=?,validation_policy_json=?,updated_at=? WHERE id=?`,
		mode, encodeJSON(policy), now, goalID); err != nil {
		return nil, err
	}
	if mode == validationBuildOnly {
		if _, err := tx.Exec(`DELETE FROM builder_checks WHERE goal_id=? AND check_key=?`, goalID, validationCheckKey); err != nil {
			return nil, err
		}
	} else {
		var stepCount int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM builder_steps WHERE goal_id=?`, goalID).Scan(&stepCount); err != nil {
			return nil, err
		}
		if stepCount > 0 {
			if _, err := tx.Exec(`INSERT INTO builder_checks(id,goal_id,check_key,name,description,status,result,evidence_json,checked_at,created_at,updated_at)
				VALUES(?,?,?,?,?,'pending','','{}','',?,?) ON CONFLICT(goal_id,check_key) DO NOTHING`,
				newID("check"), goalID, validationCheckKey, "Virtual-world workflow validation",
				"Evals must pass against an isolated Environments runtime with authoritative suite, experiment, environment, and run evidence.", now, now); err != nil {
				return nil, err
			}
		}
	}
	if _, err := recordEventTx(tx, goalID, RecordEventInput{
		Kind: "decision", Title: "Validation mode set to " + mode,
		Detail:       "Optional virtual-world validation policy updated",
		Data:         map[string]any{"validation_mode": mode, "validation_policy": policy},
		ActorAgentID: input.ActorAgentID, ActorThreadID: input.ActorThreadID, EventKey: eKey,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetBundle(identity, goalID)
}

func (s *builderStore) SetPlan(identity GoalIdentity, goalID string, steps []PlanStepInput, checks []PlanCheckInput, actorAgentID int64, actorThreadID, callKey string) (*GoalBundle, error) {
	if len(steps) == 0 {
		return nil, errors.New("at least one plan step is required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	goal, err := goalForUpdateTx(tx, identity, goalID)
	if err != nil {
		return nil, err
	}
	if goal.Status == goalCompleted || goal.Status == goalCancelled {
		return nil, fmt.Errorf("cannot replace the plan for a %s goal", goal.Status)
	}
	eKey := eventKey("plan", callKey)
	if eKey != "" {
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM builder_events WHERE goal_id=? AND event_key=?`, goalID, eKey).Scan(&exists); err == nil {
			_ = tx.Rollback()
			return s.GetBundle(identity, goalID)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	var started int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM builder_steps WHERE goal_id=? AND status!='pending'`, goalID).Scan(&started); err != nil {
		return nil, err
	}
	if started > 0 {
		return nil, errors.New("the plan has started; update existing steps instead of replacing it")
	}
	if _, err := tx.Exec(`DELETE FROM builder_steps WHERE goal_id=?`, goalID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM builder_checks WHERE goal_id=?`, goalID); err != nil {
		return nil, err
	}
	now := nowUTC()
	for i, input := range steps {
		title := strings.TrimSpace(input.Title)
		if title == "" {
			return nil, fmt.Errorf("plan step %d has no title", i+1)
		}
		approval := "none"
		if input.RequiresApproval {
			approval = "required"
		}
		if _, err := tx.Exec(`INSERT INTO builder_steps(id,goal_id,position,title,detail,status,approval_state,blocking_reason,created_at,updated_at,completed_at) VALUES(?,?,?,?,?,'pending',?,'',?,?,'')`,
			newID("step"), goalID, i+1, title, strings.TrimSpace(input.Detail), approval, now, now); err != nil {
			return nil, err
		}
	}
	if goal.ValidationMode != validationBuildOnly {
		hasValidationCheck := false
		for _, input := range checks {
			if stableKey(input.Key, input.Name) == validationCheckKey {
				hasValidationCheck = true
				break
			}
		}
		if !hasValidationCheck {
			checks = append(checks, PlanCheckInput{
				Key:         validationCheckKey,
				Name:        "Virtual-world workflow validation",
				Description: "Evals must pass against an isolated Environments runtime with authoritative suite, experiment, environment, and run evidence.",
			})
		}
	}
	seenChecks := map[string]bool{}
	for i, input := range checks {
		name := strings.TrimSpace(input.Name)
		if name == "" {
			return nil, fmt.Errorf("success check %d has no name", i+1)
		}
		key := stableKey(input.Key, name)
		if seenChecks[key] {
			return nil, fmt.Errorf("duplicate success check key %q", key)
		}
		seenChecks[key] = true
		if _, err := tx.Exec(`INSERT INTO builder_checks(id,goal_id,check_key,name,description,status,result,evidence_json,checked_at,created_at,updated_at) VALUES(?,?,?,?,?,'pending','','{}','',?,?)`,
			newID("check"), goalID, key, name, strings.TrimSpace(input.Description), now, now); err != nil {
			return nil, err
		}
	}
	first := strings.TrimSpace(steps[0].Title)
	if _, err := tx.Exec(`UPDATE builder_goals SET status=?,current_phase='Execution',next_action=?,updated_at=? WHERE id=?`, goalActive, first, now, goalID); err != nil {
		return nil, err
	}
	if _, err := recordEventTx(tx, goalID, RecordEventInput{
		Kind: "plan", Title: "Execution plan set", Detail: fmt.Sprintf("%d steps and %d success checks", len(steps), len(checks)),
		ActorAgentID: actorAgentID, ActorThreadID: actorThreadID, EventKey: eKey,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetBundle(identity, goalID)
}

func (s *builderStore) UpdateStep(identity GoalIdentity, goalID, stepID string, input UpdateStepInput) (*GoalBundle, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := goalForUpdateTx(tx, identity, goalID); err != nil {
		return nil, err
	}
	step, err := stepForUpdateTx(tx, goalID, stepID)
	if err != nil {
		return nil, err
	}
	if input.Status != nil {
		status := strings.TrimSpace(*input.Status)
		if !validStepStatuses[status] {
			return nil, fmt.Errorf("invalid step status %q", status)
		}
		step.Status = status
	}
	if input.ApprovalState != nil {
		approval := strings.TrimSpace(*input.ApprovalState)
		if !validApprovalStates[approval] {
			return nil, fmt.Errorf("invalid approval state %q", approval)
		}
		step.ApprovalState = approval
	}
	if step.Status == "waiting_approval" && (step.ApprovalState == "none" || step.ApprovalState == "required") {
		step.ApprovalState = "requested"
	}
	if step.ApprovalState == "denied" {
		step.Status = "blocked"
	}
	if step.Status == "completed" && step.ApprovalState != "none" && step.ApprovalState != "approved" {
		return nil, errors.New("an approval-gated step cannot complete before approval")
	}
	if input.BlockingReason != nil {
		step.BlockingReason = strings.TrimSpace(*input.BlockingReason)
	}
	if (step.Status == "blocked" || step.Status == "failed") && step.BlockingReason == "" {
		return nil, errors.New("blocking_reason is required for blocked or failed steps")
	}
	now := nowUTC()
	step.CompletedAt = ""
	if step.Status == "completed" || step.Status == "skipped" {
		step.CompletedAt = now
	}
	if _, err := tx.Exec(`UPDATE builder_steps SET status=?,approval_state=?,blocking_reason=?,updated_at=?,completed_at=? WHERE id=? AND goal_id=?`,
		step.Status, step.ApprovalState, step.BlockingReason, now, step.CompletedAt, step.ID, goalID); err != nil {
		return nil, err
	}
	goalStatus := goalActive
	phase := step.Title
	nextAction := step.Title
	switch step.Status {
	case "waiting_approval":
		goalStatus = goalWaitingApproval
		nextAction = "Wait for approval: " + step.Title
	case "blocked", "failed":
		goalStatus = goalBlocked
		nextAction = step.BlockingReason
	case "completed", "skipped":
		var nextTitle string
		_ = tx.QueryRow(`SELECT title FROM builder_steps WHERE goal_id=? AND status='pending' ORDER BY position LIMIT 1`, goalID).Scan(&nextTitle)
		if nextTitle != "" {
			nextAction = nextTitle
		} else {
			nextAction = "Run and record all success checks"
		}
	}
	if _, err := tx.Exec(`UPDATE builder_goals SET status=?,current_phase=?,next_action=?,updated_at=? WHERE id=?`, goalStatus, phase, nextAction, now, goalID); err != nil {
		return nil, err
	}
	detail := strings.TrimSpace(input.Note)
	if detail == "" {
		detail = "Step is now " + step.Status
	}
	if _, err := recordEventTx(tx, goalID, RecordEventInput{
		Kind: "progress", Title: step.Title, Detail: detail,
		Data:         map[string]any{"step_id": step.ID, "status": step.Status, "approval_state": step.ApprovalState},
		ActorAgentID: input.ActorAgentID, ActorThreadID: input.ActorThreadID, EventKey: eventKey("step", input.EventKey),
	}); err != nil && !isUniqueConstraint(err) {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetBundle(identity, goalID)
}

func (s *builderStore) UpsertResource(identity GoalIdentity, goalID string, input UpsertResourceInput) (*ManagedResource, error) {
	if _, err := s.GetGoal(identity, goalID); err != nil {
		return nil, err
	}
	input.Key = stableKey(input.Key, input.Kind+"-"+input.Name)
	input.Kind = strings.TrimSpace(input.Kind)
	input.Name = strings.TrimSpace(input.Name)
	input.Status = strings.TrimSpace(input.Status)
	if !validResourceKinds[input.Kind] {
		return nil, fmt.Errorf("invalid resource kind %q", input.Kind)
	}
	if input.Name == "" {
		return nil, errors.New("resource name required")
	}
	if input.Status == "" {
		input.Status = "planned"
	}
	if !validResourceStatuses[input.Status] {
		return nil, fmt.Errorf("invalid resource status %q", input.Status)
	}
	now := nowUTC()
	checkedAt := ""
	if input.ObservedStateSet {
		checkedAt = now
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO builder_resources(id,goal_id,resource_key,kind,name,external_id,status,desired_state_json,observed_state_json,note,created_at,updated_at,last_checked_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(goal_id,resource_key) DO UPDATE SET
			kind=excluded.kind,
			name=excluded.name,
			external_id=CASE WHEN ? THEN excluded.external_id ELSE builder_resources.external_id END,
			status=excluded.status,
			desired_state_json=CASE WHEN ? THEN excluded.desired_state_json ELSE builder_resources.desired_state_json END,
			observed_state_json=CASE WHEN ? THEN excluded.observed_state_json ELSE builder_resources.observed_state_json END,
			note=CASE WHEN ? THEN excluded.note ELSE builder_resources.note END,
			updated_at=excluded.updated_at,
			last_checked_at=CASE WHEN ? THEN excluded.last_checked_at ELSE builder_resources.last_checked_at END`,
		newID("resource"), goalID, input.Key, input.Kind, input.Name, strings.TrimSpace(input.ExternalID), input.Status,
		encodeJSON(nonNilMap(input.DesiredState)), encodeJSON(nonNilMap(input.ObservedState)), strings.TrimSpace(input.Note), now, now, checkedAt,
		input.ExternalIDSet, input.DesiredStateSet, input.ObservedStateSet, input.NoteSet, input.ObservedStateSet)
	if err != nil {
		return nil, err
	}
	if _, err := recordEventTx(tx, goalID, RecordEventInput{
		Kind: "resource", Title: input.Name, Detail: strings.TrimSpace(input.Note),
		Data:         map[string]any{"resource_key": input.Key, "kind": input.Kind, "status": input.Status, "external_id": input.ExternalID},
		ActorAgentID: input.ActorAgentID, ActorThreadID: input.ActorThreadID, EventKey: eventKey("resource", input.EventKey),
	}); err != nil && !isUniqueConstraint(err) {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.resourceByKey(goalID, input.Key)
}

func (s *builderStore) RecordCheck(identity GoalIdentity, goalID string, input RecordCheckInput) (*GoalCheck, error) {
	if _, err := s.GetGoal(identity, goalID); err != nil {
		return nil, err
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Key = stableKey(input.Key, input.Name)
	input.Status = strings.TrimSpace(input.Status)
	if input.Name == "" {
		return nil, errors.New("check name required")
	}
	if !validCheckStatuses[input.Status] {
		return nil, fmt.Errorf("invalid check status %q", input.Status)
	}
	if input.Key == validationCheckKey && input.Status == "passing" && len(input.Evidence) == 0 {
		return nil, errors.New("passing virtual-world validation requires authoritative Environments and Evals evidence")
	}
	now := nowUTC()
	checkedAt := ""
	if input.Status != "pending" {
		checkedAt = now
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO builder_checks(id,goal_id,check_key,name,description,status,result,evidence_json,checked_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(goal_id,check_key) DO UPDATE SET name=excluded.name,description=CASE WHEN excluded.description!='' THEN excluded.description ELSE builder_checks.description END,status=excluded.status,result=excluded.result,evidence_json=excluded.evidence_json,checked_at=excluded.checked_at,updated_at=excluded.updated_at`,
		newID("check"), goalID, input.Key, input.Name, strings.TrimSpace(input.Description), input.Status,
		strings.TrimSpace(input.Result), encodeJSON(nonNilMap(input.Evidence)), checkedAt, now, now)
	if err != nil {
		return nil, err
	}
	if _, err := recordEventTx(tx, goalID, RecordEventInput{
		Kind: "check", Title: input.Name, Detail: strings.TrimSpace(input.Result),
		Data:         map[string]any{"check_key": input.Key, "status": input.Status, "evidence": nonNilMap(input.Evidence)},
		ActorAgentID: input.ActorAgentID, ActorThreadID: input.ActorThreadID, EventKey: eventKey("check", input.EventKey),
	}); err != nil && !isUniqueConstraint(err) {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.checkByKey(goalID, input.Key)
}

func (s *builderStore) RecordEvent(identity GoalIdentity, goalID string, input RecordEventInput) (*BuilderEvent, error) {
	if _, err := s.GetGoal(identity, goalID); err != nil {
		return nil, err
	}
	input.Kind = strings.TrimSpace(input.Kind)
	input.Title = strings.TrimSpace(input.Title)
	if !validEventKinds[input.Kind] {
		return nil, fmt.Errorf("invalid event kind %q", input.Kind)
	}
	if input.Title == "" {
		return nil, errors.New("event title required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	event, err := recordEventTx(tx, goalID, input)
	if isUniqueConstraint(err) && input.EventKey != "" {
		_ = tx.Rollback()
		return s.eventByKey(goalID, input.EventKey)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return event, nil
}

func (s *builderStore) UpdateGoal(identity GoalIdentity, goalID string, input UpdateGoalInput) (*GoalBundle, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	goal, err := goalForUpdateTx(tx, identity, goalID)
	if err != nil {
		return nil, err
	}
	if input.Status != nil {
		status := strings.TrimSpace(*input.Status)
		if !validGoalStatuses[status] {
			return nil, fmt.Errorf("invalid goal status %q", status)
		}
		if status == goalCompleted {
			var totalSteps, incomplete, totalChecks, unsatisfied int
			if err := tx.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status NOT IN ('completed','skipped') THEN 1 ELSE 0 END),0) FROM builder_steps WHERE goal_id=?`, goalID).Scan(&totalSteps, &incomplete); err != nil {
				return nil, err
			}
			if err := tx.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status!='passing' THEN 1 ELSE 0 END),0) FROM builder_checks WHERE goal_id=?`, goalID).Scan(&totalChecks, &unsatisfied); err != nil {
				return nil, err
			}
			if totalSteps == 0 || totalChecks == 0 {
				return nil, errors.New("goal cannot complete before an execution plan and success checks are recorded")
			}
			if incomplete > 0 || unsatisfied > 0 {
				return nil, fmt.Errorf("goal cannot complete: %d incomplete steps and %d unsatisfied checks", incomplete, unsatisfied)
			}
		}
		goal.Status = status
	}
	if input.CurrentPhase != nil {
		goal.CurrentPhase = strings.TrimSpace(*input.CurrentPhase)
	}
	if input.Summary != nil {
		goal.Summary = strings.TrimSpace(*input.Summary)
	}
	if input.NextAction != nil {
		goal.NextAction = strings.TrimSpace(*input.NextAction)
	}
	now := nowUTC()
	if goal.Status == goalCompleted {
		goal.CompletedAt = now
		goal.NextAction = ""
	} else if goal.Status == goalCancelled {
		goal.CompletedAt = ""
		goal.NextAction = ""
	} else {
		goal.CompletedAt = ""
	}
	if _, err := tx.Exec(`UPDATE builder_goals SET status=?,current_phase=?,summary=?,next_action=?,updated_at=?,completed_at=? WHERE id=?`,
		goal.Status, goal.CurrentPhase, goal.Summary, goal.NextAction, now, goal.CompletedAt, goalID); err != nil {
		return nil, err
	}
	if _, err := recordEventTx(tx, goalID, RecordEventInput{
		Kind: "status", Title: "Goal is " + goal.Status, Detail: goal.Summary,
		Data:         map[string]any{"status": goal.Status, "current_phase": goal.CurrentPhase, "next_action": goal.NextAction},
		ActorAgentID: input.ActorAgentID, ActorThreadID: input.ActorThreadID, EventKey: eventKey("goal-status", input.EventKey),
	}); err != nil && !isUniqueConstraint(err) {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetBundle(identity, goalID)
}

const goalSelect = `SELECT id,project_id,owner_agent_id,title,objective,status,current_phase,summary,next_action,success_criteria,constraints_json,validation_mode,validation_policy_json,idempotency_key,created_by_thread,created_at,updated_at,completed_at FROM builder_goals`

type rowScanner interface{ Scan(...any) error }

func scanGoal(row rowScanner) (*Goal, error) {
	var goal Goal
	var criteriaRaw, constraintsRaw, validationPolicyRaw string
	if err := row.Scan(&goal.ID, &goal.ProjectID, &goal.OwnerAgentID, &goal.Title, &goal.Objective, &goal.Status,
		&goal.CurrentPhase, &goal.Summary, &goal.NextAction, &criteriaRaw, &constraintsRaw, &goal.ValidationMode, &validationPolicyRaw, &goal.IdempotencyKey,
		&goal.CreatedByThreadID, &goal.CreatedAt, &goal.UpdatedAt, &goal.CompletedAt); err != nil {
		return nil, err
	}
	decodeJSON(criteriaRaw, &goal.SuccessCriteria)
	decodeJSON(constraintsRaw, &goal.Constraints)
	var validationInput ValidationPolicyInput
	decodeJSON(validationPolicyRaw, &validationInput)
	_, goal.ValidationPolicy, _ = normalizeValidationPolicy(goal.ValidationMode, validationInput)
	if goal.SuccessCriteria == nil {
		goal.SuccessCriteria = []string{}
	}
	if goal.Constraints == nil {
		goal.Constraints = []string{}
	}
	return &goal, nil
}

func normalizeValidationPolicy(mode string, input ValidationPolicyInput) (string, ValidationPolicy, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = validationBuildOnly
	}
	if !validValidationModes[mode] {
		return "", ValidationPolicy{}, fmt.Errorf("invalid validation mode %q", mode)
	}
	policy := ValidationPolicy{}
	if mode != validationBuildOnly {
		policy = ValidationPolicy{
			MaxRuns: 20, MaxRepairAttempts: 2, AutoRepair: true,
			InstallSafeApps: true, RunOnChange: mode == validationContinuous,
		}
	}
	if input.MaxRuns != nil {
		policy.MaxRuns = *input.MaxRuns
	}
	if input.MaxRepairAttempts != nil {
		policy.MaxRepairAttempts = *input.MaxRepairAttempts
	}
	if input.AutoRepair != nil {
		policy.AutoRepair = *input.AutoRepair
	}
	if input.InstallSafeApps != nil {
		policy.InstallSafeApps = *input.InstallSafeApps
	}
	if input.RunOnChange != nil {
		policy.RunOnChange = *input.RunOnChange
	}
	if mode == validationBuildOnly {
		policy = ValidationPolicy{}
		return mode, policy, nil
	}
	if policy.MaxRuns < 1 || policy.MaxRuns > 100 {
		return "", ValidationPolicy{}, errors.New("validation max_runs must be between 1 and 100")
	}
	if policy.MaxRepairAttempts < 0 || policy.MaxRepairAttempts > 10 {
		return "", ValidationPolicy{}, errors.New("validation max_repair_attempts must be between 0 and 10")
	}
	if mode == validationSimulated {
		policy.RunOnChange = false
	} else if mode == validationContinuous {
		policy.RunOnChange = true
	}
	return mode, policy, nil
}

func goalForUpdateTx(tx *sql.Tx, identity GoalIdentity, goalID string) (*Goal, error) {
	goal, err := scanGoal(tx.QueryRow(goalSelect+` WHERE id=? AND project_id=? AND owner_agent_id=?`, strings.TrimSpace(goalID), identity.ProjectID, identity.OwnerAgentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("goal not found in the current Helper and project scope")
	}
	return goal, err
}

func stepForUpdateTx(tx *sql.Tx, goalID, stepID string) (*Step, error) {
	var step Step
	err := tx.QueryRow(`SELECT id,goal_id,position,title,detail,status,approval_state,blocking_reason,created_at,updated_at,completed_at FROM builder_steps WHERE id=? AND goal_id=?`,
		strings.TrimSpace(stepID), goalID).Scan(&step.ID, &step.GoalID, &step.Position, &step.Title, &step.Detail, &step.Status,
		&step.ApprovalState, &step.BlockingReason, &step.CreatedAt, &step.UpdatedAt, &step.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("step not found in goal")
	}
	return &step, err
}

func (s *builderStore) listSteps(goalID string) ([]*Step, error) {
	rows, err := s.db.Query(`SELECT id,goal_id,position,title,detail,status,approval_state,blocking_reason,created_at,updated_at,completed_at FROM builder_steps WHERE goal_id=? ORDER BY position`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Step, 0)
	for rows.Next() {
		var step Step
		if err := rows.Scan(&step.ID, &step.GoalID, &step.Position, &step.Title, &step.Detail, &step.Status, &step.ApprovalState, &step.BlockingReason, &step.CreatedAt, &step.UpdatedAt, &step.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, &step)
	}
	return out, rows.Err()
}

func (s *builderStore) listChecks(goalID string) ([]*GoalCheck, error) {
	rows, err := s.db.Query(`SELECT id,goal_id,check_key,name,description,status,result,evidence_json,checked_at,created_at,updated_at FROM builder_checks WHERE goal_id=? ORDER BY created_at,id`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*GoalCheck, 0)
	for rows.Next() {
		check, err := scanCheck(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, check)
	}
	return out, rows.Err()
}

func (s *builderStore) listResources(goalID string) ([]*ManagedResource, error) {
	rows, err := s.db.Query(`SELECT id,goal_id,resource_key,kind,name,external_id,status,desired_state_json,observed_state_json,note,created_at,updated_at,last_checked_at FROM builder_resources WHERE goal_id=? ORDER BY kind,name`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*ManagedResource, 0)
	for rows.Next() {
		resource, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, resource)
	}
	return out, rows.Err()
}

func (s *builderStore) listEvents(goalID string, limit int) ([]*BuilderEvent, error) {
	rows, err := s.db.Query(`SELECT id,goal_id,kind,title,detail,data_json,actor_agent_id,actor_thread_id,event_key,created_at FROM builder_events WHERE goal_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, goalID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*BuilderEvent, 0)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *builderStore) resourceByKey(goalID, key string) (*ManagedResource, error) {
	return scanResource(s.db.QueryRow(`SELECT id,goal_id,resource_key,kind,name,external_id,status,desired_state_json,observed_state_json,note,created_at,updated_at,last_checked_at FROM builder_resources WHERE goal_id=? AND resource_key=?`, goalID, key))
}

func (s *builderStore) checkByKey(goalID, key string) (*GoalCheck, error) {
	return scanCheck(s.db.QueryRow(`SELECT id,goal_id,check_key,name,description,status,result,evidence_json,checked_at,created_at,updated_at FROM builder_checks WHERE goal_id=? AND check_key=?`, goalID, key))
}

func (s *builderStore) eventByKey(goalID, key string) (*BuilderEvent, error) {
	return scanEvent(s.db.QueryRow(`SELECT id,goal_id,kind,title,detail,data_json,actor_agent_id,actor_thread_id,event_key,created_at FROM builder_events WHERE goal_id=? AND event_key=?`, goalID, key))
}

func scanCheck(row rowScanner) (*GoalCheck, error) {
	var check GoalCheck
	var evidenceRaw string
	if err := row.Scan(&check.ID, &check.GoalID, &check.Key, &check.Name, &check.Description, &check.Status, &check.Result, &evidenceRaw, &check.CheckedAt, &check.CreatedAt, &check.UpdatedAt); err != nil {
		return nil, err
	}
	decodeJSON(evidenceRaw, &check.Evidence)
	check.Evidence = nonNilMap(check.Evidence)
	return &check, nil
}

func scanResource(row rowScanner) (*ManagedResource, error) {
	var resource ManagedResource
	var desiredRaw, observedRaw string
	if err := row.Scan(&resource.ID, &resource.GoalID, &resource.Key, &resource.Kind, &resource.Name, &resource.ExternalID,
		&resource.Status, &desiredRaw, &observedRaw, &resource.Note, &resource.CreatedAt, &resource.UpdatedAt, &resource.LastCheckedAt); err != nil {
		return nil, err
	}
	decodeJSON(desiredRaw, &resource.DesiredState)
	decodeJSON(observedRaw, &resource.ObservedState)
	resource.DesiredState = nonNilMap(resource.DesiredState)
	resource.ObservedState = nonNilMap(resource.ObservedState)
	return &resource, nil
}

func scanEvent(row rowScanner) (*BuilderEvent, error) {
	var event BuilderEvent
	var dataRaw string
	if err := row.Scan(&event.ID, &event.GoalID, &event.Kind, &event.Title, &event.Detail, &dataRaw,
		&event.ActorAgentID, &event.ActorThreadID, &event.EventKey, &event.CreatedAt); err != nil {
		return nil, err
	}
	decodeJSON(dataRaw, &event.Data)
	event.Data = nonNilMap(event.Data)
	return &event, nil
}

func recordEventTx(tx *sql.Tx, goalID string, input RecordEventInput) (*BuilderEvent, error) {
	if !validEventKinds[input.Kind] {
		return nil, fmt.Errorf("invalid event kind %q", input.Kind)
	}
	event := &BuilderEvent{
		ID: newID("event"), GoalID: goalID, Kind: input.Kind, Title: strings.TrimSpace(input.Title),
		Detail: strings.TrimSpace(input.Detail), Data: nonNilMap(input.Data), ActorAgentID: input.ActorAgentID,
		ActorThreadID: strings.TrimSpace(input.ActorThreadID), EventKey: strings.TrimSpace(input.EventKey), CreatedAt: nowUTC(),
	}
	_, err := tx.Exec(`INSERT INTO builder_events(id,goal_id,kind,title,detail,data_json,actor_agent_id,actor_thread_id,event_key,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		event.ID, event.GoalID, event.Kind, event.Title, event.Detail, encodeJSON(event.Data), event.ActorAgentID, event.ActorThreadID, event.EventKey, event.CreatedAt)
	return event, err
}

func completionReadiness(steps []*Step, checks []*GoalCheck) CompletionReadiness {
	readiness := CompletionReadiness{Ready: true, Issues: []string{}}
	for _, step := range steps {
		if step.Status != "completed" && step.Status != "skipped" {
			readiness.IncompleteSteps++
		}
	}
	for _, check := range checks {
		if check.Status != "passing" {
			readiness.UnsatisfiedChecks++
		}
	}
	if readiness.IncompleteSteps > 0 {
		readiness.Issues = append(readiness.Issues, fmt.Sprintf("%d plan steps are incomplete", readiness.IncompleteSteps))
	}
	if readiness.UnsatisfiedChecks > 0 {
		readiness.Issues = append(readiness.Issues, fmt.Sprintf("%d success checks are not passing", readiness.UnsatisfiedChecks))
	}
	readiness.Ready = len(steps) > 0 && len(checks) > 0 && readiness.IncompleteSteps == 0 && readiness.UnsatisfiedChecks == 0
	if len(steps) == 0 {
		readiness.Issues = append(readiness.Issues, "no execution plan has been recorded")
	}
	if len(checks) == 0 {
		readiness.Issues = append(readiness.Issues, "no success checks have been recorded")
	}
	return readiness
}

func newID(prefix string) string {
	raw := make([]byte, 10)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(raw)
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func encodeJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func decodeJSON(raw string, out any) { _ = json.Unmarshal([]byte(raw), out) }

func nonNilMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	return input
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func stableKey(explicit, fallback string) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return value
	}
	value := strings.ToLower(strings.TrimSpace(fallback))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func eventKey(prefix, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return prefix + ":" + key
}

func stringSet(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func isUniqueConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}
