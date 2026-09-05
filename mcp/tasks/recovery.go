package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RecoverOccurrence creates a separate, linked reconciliation attempt. It
// never mutates or reopens the failed occurrence and never performs the domain
// action itself.
func (s *taskStore) RecoverOccurrence(id, actorThread, assignedThread, reason, requestKey string, now time.Time) (*Task, bool, error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()

	source, err := s.Get(id)
	if err != nil {
		return nil, false, err
	}
	if source.State != stateFailed || source.ScheduledFor == nil {
		return nil, false, validationError("only a failed scheduled occurrence can be recovered")
	}
	root := source
	if source.RecoveryOfTaskID != "" {
		root, err = s.Get(source.RecoveryOfTaskID)
		if err != nil {
			return nil, false, fmt.Errorf("load original occurrence: %w", err)
		}
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, false, validationError("recovery reason required")
	}
	requestKey = strings.TrimSpace(requestKey)
	if requestKey == "" {
		requestKey = reason
	}
	sum := sha256.Sum256([]byte(source.ID + "\x00" + requestKey))
	idempotencyKey := "recovery:" + source.ID + ":" + hex.EncodeToString(sum[:8])

	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if existing, err := scanTask(tx.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE agent_id=? AND idempotency_key=?`, root.AgentID, idempotencyKey)); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, errTaskNotFound) {
		return nil, false, err
	}
	var open int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE recovery_of_task_id=? AND state IN ('queued','running','waiting','blocked','completed')`, root.ID).Scan(&open); err != nil {
		return nil, false, err
	}
	if open > 0 {
		return nil, false, validationError("occurrence already has an open or successful recovery")
	}
	var attempt int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(recovery_attempt), 0)+1 FROM tasks WHERE recovery_of_task_id=?`, root.ID).Scan(&attempt); err != nil {
		return nil, false, err
	}
	now = now.UTC()
	originalKey := root.ScheduleOccurrenceKey
	if originalKey == "" && root.ScheduledFor != nil {
		originalKey = root.ScheduledFor.UTC().Format(time.RFC3339Nano)
	}
	recoveryKey := originalKey + ":recovery:" + fmt.Sprint(attempt)
	assignedThread = strings.TrimSpace(assignedThread)
	if assignedThread == "" {
		assignedThread = source.AssignedThreadID
	}
	var events []TaskEvent
	creator := actorThread
	if creator == "api" {
		creator = root.CreatedByThreadID
	}
	task, created, err := s.createTx(tx, CreateTaskInput{
		AgentID: root.AgentID, ProjectID: root.ProjectID, Title: root.Title,
		Description: root.Description, State: stateQueued,
		CurrentStep:       "Reconciliation required before external action",
		CreatedByThreadID: creator, AssignedThreadID: assignedThread,
		ParentTaskID: root.ParentTaskID, IdempotencyKey: idempotencyKey,
		RecoveryOfTaskID: root.ID, OriginalOccurrenceKey: originalKey,
		RecoveryAttempt: attempt, RecoveryReason: reason, OperationKey: root.OperationKey,
		ScheduledFor: &now, OccurrenceKey: recoveryKey,
	}, now, &events)
	if err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	for _, event := range events {
		s.emit(event)
	}
	return task, created, nil
}
