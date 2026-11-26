package main

import "testing"

func TestOverviewListsActiveAssignmentsByHowTheyStart(t *testing.T) {
	a, _, p := directSetup(t)
	scheduled := addAssignment(t, a, p, "Daily digest", 7, "agent", &Schedule{Kind: "cron", Cron: "0 9 * * *", Timezone: "UTC"}, nil)
	evented := addAssignment(t, a, p, "Inbound email", 8, "agent", nil, nil)
	manual := addAssignment(t, a, p, "Ad hoc review", 7, "agent", nil, nil)
	addAssignment(t, a, p, "Old paused", 8, "agent", nil, nil)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := a.db.Exec(q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`UPDATE processes SET status='active' WHERE id=?`, p.ID)
	exec(`UPDATE process_assignments SET status='active',sync_pending=0,sync_error='' WHERE id IN (?,?,?)`, scheduled.ID, evented.ID, manual.ID)
	exec(`UPDATE process_assignments SET status='paused' WHERE id NOT IN (?,?,?)`, scheduled.ID, evented.ID, manual.ID)
	exec(`UPDATE process_assignments SET next_run_at='2026-12-01T09:00:00Z' WHERE id=?`, scheduled.ID)
	exec(`INSERT INTO process_triggers(id,process_id,assignment_id,project_id,status,config_json,subscription_enabled,created_at,updated_at) VALUES('trigger-email',?,?,?,'active','{"name":"Inbound email","source_install_id":48,"topic":"conversation.message.received"}',0,'2026-11-01T09:00:00Z','2026-11-01T09:00:00Z')`, p.ID, evented.ID, p.ProjectID)
	exec(`INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at,backend,state,assignment_id,assignment_json,overrides_json) VALUES('run-live',?,1,'manual','run-live','2026-11-20T09:00:00Z','agent','running',?,'{}','{}')`, p.ID, manual.ID)
	exec(`INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at,backend,state,assignment_id,assignment_json,overrides_json) VALUES('run-stuck',?,1,'manual','run-stuck','2026-11-21T09:00:00Z','agent','blocked',?,'{}','{}')`, p.ID, manual.ID)
	exec(`INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at,backend,state,assignment_id,assignment_json,overrides_json) VALUES('run-blocked',?,1,'manual','run-blocked','2026-11-21T09:00:00Z','agent','blocked',?,'{}','{}')`, p.ID, scheduled.ID)

	out, e := a.overview(p.ProjectID)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Assignments) != 3 {
		t.Fatalf("want the three active assignments, got %+v", out.Assignments)
	}
	got := map[string]overviewAssignment{}
	order := []string{}
	for _, x := range out.Assignments {
		got[x.ID] = x
		order = append(order, x.Start)
	}
	if order[0] != "schedule" || order[1] != "event" || order[2] != "manual" {
		t.Fatalf("order by start kind: %v", order)
	}
	if s := got[scheduled.ID]; s.NextRunAt != "2026-12-01T09:00:00Z" || s.Schedule == nil || s.AgentID != 7 {
		t.Fatalf("scheduled: %+v", s)
	}
	if s := got[scheduled.ID]; s.LiveRunID != "run-blocked" || s.LiveRunState != "blocked" || !s.NeedsAttention || s.Warning != "Run blocked" {
		t.Fatalf("an open blocked run is not running and needs attention: %+v", s)
	}
	ev := got[evented.ID]
	if len(ev.Triggers) != 1 || ev.Triggers[0].Topic != "conversation.message.received" || !ev.NeedsAttention || ev.Warning != "Trigger not listening" {
		t.Fatalf("event-driven assignment with a silent trigger: %+v", ev)
	}
	if m := got[manual.ID]; m.LiveRunID != "run-live" || m.LiveRunState != "running" || m.OngoingCount != 2 || m.NeedsAttention {
		t.Fatalf("manual assignment's live run: %+v", m)
	}
	if out.Counts.Assignments != 3 || out.Counts.EventDriven != 1 || out.Counts.AssignmentIssues != 2 {
		t.Fatalf("counts: %+v", out.Counts)
	}
}
