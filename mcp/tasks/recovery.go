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
		return nil, false, errors.New("only a failed scheduled occurrence can be recovered")
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
		return nil, false, errors.New("recovery reason required")
	}
	requestKey = strings.TrimSpace(requestKey)
	if requestKey == "" {
		requestKey = reason
	}
	sum := sha256.Sum256([]byte(source.ID + "\x00" + requestKey))
	idempotencyKey := "recovery:" + source.ID + ":" + hex.EncodeToString(sum[:8])

	var attempt int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(recovery_attempt), 0)+1 FROM tasks WHERE recovery_of_task_id=?`, root.ID).Scan(&attempt); err != nil {
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
	task, created, err := s.Create(CreateTaskInput{
		AgentID: root.AgentID, ProjectID: root.ProjectID, Title: root.Title,
		Description: root.Description, State: stateQueued,
		CurrentStep:       "Reconciliation required before external action",
		CreatedByThreadID: actorThread, AssignedThreadID: assignedThread,
		ParentTaskID: root.ParentTaskID, IdempotencyKey: idempotencyKey,
		RecoveryOfTaskID: root.ID, OriginalOccurrenceKey: originalKey,
		RecoveryAttempt: attempt, RecoveryReason: reason, OperationKey: root.OperationKey,
		ScheduledFor: &now, OccurrenceKey: recoveryKey,
	})
	return task, created, err
}
