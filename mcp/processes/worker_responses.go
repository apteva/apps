package main

import "fmt"

// WorkerStep contains the assigned instructions and recoverable checkpoint,
// without delivery diagnostics, audit metadata, or unrelated procedure steps.
type WorkerStep struct {
	ID         string   `json:"id"`
	Key        string   `json:"key"`
	Definition Step     `json:"definition"`
	Executor   Executor `json:"executor"`
	State      string   `json:"state"`
	Progress   int      `json:"progress"`
	Revision   int      `json:"revision"`
	Output     string   `json:"output,omitempty"`
	Error      string   `json:"error,omitempty"`
	StartAt    string   `json:"start_at,omitempty"`
	DueAt      string   `json:"due_at,omitempty"`
}

type WorkerAssignment struct {
	ID           string `json:"id"`
	Revision     int    `json:"revision"`
	Name         string `json:"name"`
	Target       string `json:"target"`
	OwnerAgentID int64  `json:"owner_agent_id"`
}

func workerStep(s StepRun) WorkerStep {
	return WorkerStep{ID: s.ID, Key: s.Key, Definition: s.Definition, Executor: s.Executor,
		State: s.State, Progress: s.Progress, Revision: s.Revision, Output: s.Output,
		Error: s.Error, StartAt: s.StartAt, DueAt: s.DueAt}
}

func workerAction(r Run, s StepRun, actor, worker string, all []StepRun) (bool, string) {
	persistent := worker != "" && actor == fmt.Sprintf("agent:%d:%s", s.Executor.AgentID, worker)
	done := terminal(r.State) || terminal(s.State)
	if persistent {
		done = persistentWorkerDone(r, s.Executor.AgentID, all)
	}
	if done {
		return true, "Call the native done tool immediately before writing any text."
	}
	if s.State == "waiting" || s.State == "blocked" {
		return false, "Retain this worker and await resolution of the recorded blocker; do not poll or repeat completed actions."
	}
	if !terminal(s.State) {
		return false, "Continue only this assigned step; use step_get if you need to recover its context or saved checkpoint."
	}
	return false, "Wait for the next Processes event without polling."
}

// Acknowledgements never echo instructions or output receipts. They confirm
// the accepted saved revision even when a lost response is retried, and retain
// blockers verbatim. Read/claim and run_get remain the checkpoint recovery path.
func workerAcknowledgement(process string, r Run, s StepRun, done bool, next string) map[string]any {
	result := map[string]any{"process_id": process, "run_id": r.ID, "step_id": s.ID,
		"revision": s.Revision, "state": s.State, "progress": s.Progress, "run_state": r.State,
		"done": done, "next_action": next}
	if s.Error != "" {
		result["error"] = s.Error
	}
	return result
}
