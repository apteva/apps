package main

import (
	"sort"
	"strings"
)

// overviewAssignmentLimit bounds the assignment list in one overview payload.
const overviewAssignmentLimit = 40

// overviewAssignment is one active assignment as the dashboard widget lists it:
// how it starts, who runs it, and what happened last.
type overviewAssignment struct {
	ID             string                     `json:"id"`
	ProjectID      string                     `json:"project_id,omitempty"`
	ProjectName    string                     `json:"project_name,omitempty"`
	ProcessID      string                     `json:"process_id"`
	ProcessName    string                     `json:"process_name"`
	Name           string                     `json:"name"`
	AgentID        int64                      `json:"agent_id"`
	AgentName      string                     `json:"agent_name,omitempty"`
	Start          string                     `json:"start"` // schedule, event or manual
	Schedule       *Schedule                  `json:"schedule,omitempty"`
	NextRunAt      string                     `json:"next_run_at,omitempty"`
	Triggers       []assignmentTriggerSummary `json:"triggers"`
	OngoingCount   int                        `json:"ongoing_count"`
	LiveRunID      string                     `json:"live_run_id,omitempty"`
	LiveRunState   string                     `json:"live_run_state,omitempty"`
	LastRunID      string                     `json:"last_run_id,omitempty"`
	LastRunState   string                     `json:"last_run_state,omitempty"`
	LastRunAt      string                     `json:"last_run_at,omitempty"`
	Warning        string                     `json:"warning,omitempty"`
	NeedsAttention bool                       `json:"needs_attention"`
}

// assignmentStart says how an assignment begins: its schedule wins, then any
// non-archived event trigger, else it only runs when started by hand.
func assignmentStart(schedule *Schedule, triggers []assignmentTriggerSummary) string {
	if schedule != nil {
		return "schedule"
	}
	for _, t := range triggers {
		if t.Status != "archived" {
			return "event"
		}
	}
	return "manual"
}

// triggerProblem describes why a live trigger would not start runs, or "".
func triggerProblem(t assignmentTriggerSummary) string {
	switch {
	case t.Status == "archived":
		return ""
	case t.SyncError != "":
		return "Trigger sync error: " + t.SyncError
	case t.Status != "active":
		return "Trigger " + t.Status
	case !t.SubscriptionEnabled:
		return "Trigger not listening"
	}
	return ""
}

// overviewAssignments lists the project's active assignments on active
// procedures. live is the overview's active run list, used to point a running
// assignment at its run.
func (a *App) overviewAssignments(project string, live []overviewItem) ([]overviewAssignment, error) {
	res, e := a.projectAssignments(project)
	if e != nil {
		return nil, e
	}
	all, _ := res["assignments"].([]projectAssignment)
	out := []overviewAssignment{}
	for _, x := range all {
		if x.Status != "active" || x.ProcessStatus != "active" {
			continue
		}
		y := overviewAssignment{
			ID: x.ID, ProcessID: x.ProcessID, ProcessName: x.ProcessName, Name: x.Name,
			AgentID: x.OwnerAgentID, Schedule: x.Schedule, NextRunAt: x.NextRunAt,
			Triggers: x.Triggers, OngoingCount: x.OngoingCount,
			LastRunID: x.LastRunID, LastRunState: x.LastRunState, LastRunAt: x.LastRunAt,
			Start: assignmentStart(x.Schedule, x.Triggers),
		}
		if y.Triggers == nil {
			y.Triggers = []assignmentTriggerSummary{}
		}
		for _, run := range live {
			if run.AssignmentID == x.ID && liveRank(run.State) < liveRank(y.LiveRunState) {
				y.LiveRunID, y.LiveRunState = run.ID, run.State
			}
		}
		if x.SyncPending || x.SyncError != "" {
			y.Warning = "Assignment synchronization pending"
			if x.SyncError != "" {
				y.Warning = x.SyncError
			}
		}
		for _, t := range y.Triggers {
			if p := triggerProblem(t); p != "" && y.Warning == "" {
				y.Warning = p
			}
		}
		if y.Warning == "" && y.LiveRunState == "blocked" {
			y.Warning = "Run blocked"
		}
		y.NeedsAttention = y.Warning != ""
		out = append(out, y)
	}
	sortOverviewAssignments(out)
	return out, nil
}

// liveRank orders open run states so an assignment points at its most active
// run: a running one beats one that is waiting or blocked.
func liveRank(state string) int {
	switch state {
	case "running":
		return 0
	case "ready", "queued", "pending":
		return 1
	case "waiting", "review":
		return 2
	case "blocked":
		return 3
	case "":
		return 99
	}
	return 4
}

// sortOverviewAssignments orders scheduled assignments by next run, then
// event-driven ones, then manual ones, each by name.
func sortOverviewAssignments(xs []overviewAssignment) {
	rank := map[string]int{"schedule": 0, "event": 1, "manual": 2}
	sort.SliceStable(xs, func(i, j int) bool {
		a, b := xs[i], xs[j]
		if rank[a.Start] != rank[b.Start] {
			return rank[a.Start] < rank[b.Start]
		}
		if a.Start == "schedule" && a.NextRunAt != b.NextRunAt {
			if a.NextRunAt == "" || b.NextRunAt == "" {
				return b.NextRunAt == ""
			}
			return newer(b.NextRunAt, a.NextRunAt)
		}
		if !strings.EqualFold(a.Name, b.Name) {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return a.ID < b.ID
	})
}

// countAssignments fills the assignment counters from the full list.
func countAssignments(c *overviewCounts, xs []overviewAssignment) {
	for _, x := range xs {
		c.Assignments++
		if x.Start == "event" {
			c.EventDriven++
		}
		if x.NeedsAttention {
			c.AssignmentIssues++
		}
	}
}

func capAssignments(xs []overviewAssignment) []overviewAssignment {
	if len(xs) > overviewAssignmentLimit {
		return xs[:overviewAssignmentLimit]
	}
	return xs
}
