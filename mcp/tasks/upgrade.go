package main

import "strings"

// Legacy records remain quarantined until the platform resolves their actual
// project. A missing agent never assigns old work to a guessed project/thread.
func (a *App) reconcileLegacyTasks() error {
	rows, err := a.store.db.Query(`SELECT DISTINCT agent_id FROM tasks WHERE project_id='' AND id LIKE 'legacy-%'`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		agent, err := a.ctx.GetAgent(id)
		if err != nil || agent == nil || strings.TrimSpace(agent.ProjectID) == "" {
			a.logger().Warn("legacy tasks awaiting agent resolution", "agent_id", id)
			continue
		}
		if _, err = a.store.db.Exec(`UPDATE tasks SET project_id=?,assigned_thread_id=CASE WHEN assigned_thread_id='' THEN ? ELSE assigned_thread_id END WHERE agent_id=? AND project_id='' AND id LIKE 'legacy-%'`, agent.ProjectID, agent.DefaultThreadID, id); err != nil {
			return err
		}
	}
	return nil
}

// Seed delivery work that was stranded before the transactional outbox existed.
func (a *App) reconcileUndispatchedTasks() error {
	rows, err := a.store.db.Query(`SELECT ` + taskColumns + ` FROM tasks WHERE scheduled_for IS NOT NULL AND accepted_at IS NULL AND state='queued' AND project_id<>''`)
	if err != nil {
		return err
	}
	var tasks []*Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return err
		}
		tasks = append(tasks, task)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, task := range tasks {
		if err = enqueueCreatedTaskTx(tx, task, a.store.now()); err != nil {
			return err
		}
	}
	// A crash in the old claim-before-send path can also leave a reconciliation
	// record without a Core receipt. Recover it with its original stable source.
	rows, err = tx.Query(`SELECT `+taskColumns+` FROM tasks WHERE state NOT IN ('completed','failed','cancelled') AND project_id<>'' AND EXISTS (SELECT 1 FROM task_agent_executions e WHERE e.task_id=tasks.id AND e.purpose=? AND e.execution_id='')`, agentTerminalizationPurpose)
	if err != nil {
		return err
	}
	tasks = nil
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return err
		}
		tasks = append(tasks, task)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if err = enqueueDeliveryTx(tx, task, task.AssignedThreadID, "task.terminalization_required", "terminalization:"+task.ID, a.store.now()); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE task_agent_executions SET deadline_at=NULL WHERE task_id=? AND purpose=? AND execution_id=''`, task.ID, agentTerminalizationPurpose); err != nil {
			return err
		}
	}
	return tx.Commit()
}
