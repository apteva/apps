package main

import (
	"errors"
	"time"
)

const (
	stateQueued    = "queued"
	stateRunning   = "running"
	stateWaiting   = "waiting"
	stateBlocked   = "blocked"
	stateCompleted = "completed"
	stateFailed    = "failed"
	stateCancelled = "cancelled"
)

var (
	errTaskNotFound    = errors.New("task not found")
	errInvalidState    = errors.New("invalid task state")
	errTerminalTask    = errors.New("task is already terminal")
	errInvalidProgress = errors.New("progress must be between 0 and 100")
)

type ThreadRef struct {
	AgentID  int64  `json:"agent_id"`
	ThreadID string `json:"thread_id"`
}

type Task struct {
	ID                     string     `json:"id"`
	AgentID                int64      `json:"agent_id"`
	ProjectID              string     `json:"project_id"`
	Title                  string     `json:"title"`
	Description            string     `json:"description,omitempty"`
	State                  string     `json:"state"`
	Progress               *int       `json:"progress,omitempty"`
	CurrentStep            string     `json:"current_step,omitempty"`
	CreatedByThreadID      string     `json:"created_by_thread_id,omitempty"`
	AssignedThreadID       string     `json:"assigned_thread_id"`
	ExecutionThreadID      string     `json:"execution_thread_id,omitempty"`
	ParentTaskID           string     `json:"parent_task_id,omitempty"`
	IdempotencyKey         string     `json:"idempotency_key,omitempty"`
	RecoveryOfTaskID       string     `json:"recovery_of_task_id,omitempty"`
	OriginalOccurrenceKey  string     `json:"original_occurrence_key,omitempty"`
	RecoveryAttempt        int        `json:"recovery_attempt,omitempty"`
	RecoveryReason         string     `json:"recovery_reason,omitempty"`
	OperationKey           string     `json:"operation_key,omitempty"`
	ScheduleKind           string     `json:"schedule_kind,omitempty"`
	ScheduleExpression     string     `json:"schedule_expression,omitempty"`
	ScheduleTimezone       string     `json:"schedule_timezone,omitempty"`
	ScheduleEnabled        bool       `json:"schedule_enabled"`
	ScheduleOverlapPolicy  string     `json:"schedule_overlap_policy,omitempty"`
	ScheduleCatchupPolicy  string     `json:"schedule_catchup_policy,omitempty"`
	NextRunAt              *time.Time `json:"next_run_at,omitempty"`
	LastRunAt              *time.Time `json:"last_run_at,omitempty"`
	LastDispatchedAt       *time.Time `json:"last_dispatched_at,omitempty"`
	LastOccurrenceID       string     `json:"last_occurrence_id,omitempty"`
	LastOccurrenceStatus   string     `json:"last_occurrence_status,omitempty"`
	LastError              string     `json:"last_error,omitempty"`
	LastResultReference    string     `json:"last_result_reference,omitempty"`
	ScheduledFor           *time.Time `json:"scheduled_for,omitempty"`
	ScheduleOccurrenceKey  string     `json:"schedule_occurrence_key,omitempty"`
	DispatchedAt           *time.Time `json:"dispatched_at,omitempty"`
	DispatchAttempts       int        `json:"dispatch_attempts,omitempty"`
	LastDispatchAttemptAt  *time.Time `json:"last_dispatch_attempt_at,omitempty"`
	AcceptedAt             *time.Time `json:"accepted_at,omitempty"`
	TelemetryReference     string     `json:"telemetry_reference,omitempty"`
	AgentEventSourceID     string     `json:"agent_event_source_id,omitempty"`
	AgentExecutionID       string     `json:"agent_execution_id,omitempty"`
	AgentExecutionState    string     `json:"agent_execution_state,omitempty"`
	AgentExecutionUpdated  *time.Time `json:"agent_execution_updated_at,omitempty"`
	AgentExecutionReason   string     `json:"agent_execution_reason,omitempty"`
	AgentSettleDeadline    *time.Time `json:"agent_settle_deadline_at,omitempty"`
	AgentLifecycleSequence uint64     `json:"agent_lifecycle_sequence,omitempty"`
	Result                 string     `json:"result,omitempty"`
	ResultReference        string     `json:"result_reference,omitempty"`
	Error                  string     `json:"error,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	StartedAt              *time.Time `json:"started_at,omitempty"`
	CompletedAt            *time.Time `json:"completed_at,omitempty"`
}

type TaskEvent struct {
	ID        string         `json:"id"`
	TaskID    string         `json:"task_id"`
	AgentID   int64          `json:"agent_id"`
	EventType string         `json:"event_type"`
	ThreadID  string         `json:"thread_id,omitempty"`
	FromState string         `json:"from_state,omitempty"`
	ToState   string         `json:"to_state,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type CreateTaskInput struct {
	AgentID               int64
	ProjectID             string
	Title                 string
	Description           string
	State                 string
	Progress              *int
	CurrentStep           string
	CreatedByThreadID     string
	AssignedThreadID      string
	ParentTaskID          string
	IdempotencyKey        string
	RecoveryOfTaskID      string
	OriginalOccurrenceKey string
	RecoveryAttempt       int
	RecoveryReason        string
	OperationKey          string
	Schedule              *ScheduleInput
	ScheduledFor          *time.Time
	OccurrenceKey         string
}

type TaskAgentExecution struct {
	SourceEventID string     `json:"source_event_id"`
	TaskID        string     `json:"task_id"`
	Purpose       string     `json:"purpose"`
	ExecutionID   string     `json:"execution_id,omitempty"`
	ThreadID      string     `json:"thread_id,omitempty"`
	State         string     `json:"state,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	Sequence      uint64     `json:"sequence,omitempty"`
	DispatchedAt  time.Time  `json:"dispatched_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DeadlineAt    *time.Time `json:"deadline_at,omitempty"`
}

type UpdateTaskInput struct {
	Title             *string
	Description       *string
	State             *string
	Progress          *int
	ClearProgress     bool
	CurrentStep       *string
	AssignedThreadID  *string
	ExecutionThreadID *string
	Result            *string
	ResultReference   *string
	Error             *string
	Schedule          *ScheduleInput
}

type TaskFilter struct {
	ProjectID    string
	AgentID      int64
	States       []string
	ParentTaskID *string
	Limit        int
}

type TaskCounts struct {
	Active    int `json:"active"`
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Waiting   int `json:"waiting"`
	Blocked   int `json:"blocked"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
	Scheduled int `json:"scheduled"`
	Paused    int `json:"paused"`
}

func validState(state string) bool {
	switch state {
	case stateQueued, stateRunning, stateWaiting, stateBlocked, stateCompleted, stateFailed, stateCancelled:
		return true
	default:
		return false
	}
}

func terminalState(state string) bool {
	return state == stateCompleted || state == stateFailed || state == stateCancelled
}

func validTransition(from, to string) bool {
	if from == to {
		return true
	}
	if terminalState(from) {
		return false
	}
	if to == stateQueued {
		return from == stateWaiting || from == stateBlocked
	}
	return validState(to)
}
