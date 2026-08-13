package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func readNativeSurface(t *testing.T, path string) *sdk.NativeSurface {
	t.Helper()
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	surface, err := sdk.ParseNativeSurface(document)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return surface
}

func TestNativeTaskSurfacesMatchManifest(t *testing.T) {
	widget := readNativeSurface(t, "ui/surfaces/task-overview.json")
	panel := readNativeSurface(t, "ui/surfaces/tasks.json")
	manifest := (&App{}).Manifest()

	if widget.ID != "task-overview" || widget.Presentation != "widget" || widget.Navigation == nil || widget.Navigation.Surface != "tasks" {
		t.Fatalf("unexpected widget surface: %+v", widget)
	}
	if len(manifest.Provides.UIComponents) == 0 || manifest.Provides.UIComponents[0].Native == nil || manifest.Provides.UIComponents[0].Native.Entry != "/ui/surfaces/task-overview.json" {
		t.Fatalf("task overview native renderer is not advertised: %+v", manifest.Provides.UIComponents)
	}
	if len(manifest.Provides.UISurfaces) != 1 {
		t.Fatalf("mobile surfaces=%+v", manifest.Provides.UISurfaces)
	}
	if err := sdk.ValidateNativeSurfaceForDescriptor(panel, manifest.Provides.UISurfaces[0]); err != nil {
		t.Fatalf("tasks surface descriptor mismatch: %v", err)
	}
	if panel.ID != "tasks" || len(panel.Sections) != 1 || len(panel.Destinations) != 1 || len(panel.Actions) != 5 {
		t.Fatalf("full Tasks surface is incomplete: %+v", panel)
	}

	for _, block := range widget.Blocks {
		if block.Type == "list" && block.Empty == nil {
			t.Fatalf("widget list %q has no empty state", block.ID)
		}
	}
	if panel.Sections[0].Empty == nil {
		t.Fatal("Tasks collection has no empty state")
	}
}

func taskAt(id, state string, updated time.Time) Task {
	return Task{ID: id, ProjectID: "project-a", AgentID: 7, Title: id, State: state, UpdatedAt: updated}
}

func taskIDs(tasks []Task) []string {
	ids := make([]string, len(tasks))
	for i := range tasks {
		ids[i] = tasks[i].ID
	}
	return ids
}

func TestBuildMobileTaskSummaryPartitionsOrdersAndEmpties(t *testing.T) {
	base := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	blocked := taskAt("blocked", stateBlocked, base.Add(time.Minute))
	running := taskAt("running", stateRunning, base.Add(2*time.Minute))
	queued := taskAt("queued", stateQueued, base.Add(3*time.Minute))
	waiting := taskAt("waiting", stateWaiting, base.Add(4*time.Minute))
	upcomingLater := taskAt("schedule-later", stateWaiting, base)
	upcomingLater.ScheduleKind, upcomingLater.ScheduleEnabled = scheduleInterval, true
	later := base.Add(2 * time.Hour)
	upcomingLater.NextRunAt = &later
	upcomingSooner := taskAt("schedule-sooner", stateWaiting, base)
	upcomingSooner.ScheduleKind, upcomingSooner.ScheduleEnabled = scheduleCron, true
	sooner := base.Add(time.Hour)
	upcomingSooner.NextRunAt = &sooner
	paused := taskAt("paused-schedule", stateWaiting, base)
	paused.ScheduleKind = scheduleCron
	completed := taskAt("completed", stateCompleted, base.Add(5*time.Minute))
	completedAt := base.Add(5 * time.Minute)
	completed.CompletedAt = &completedAt
	failed := taskAt("failed", stateFailed, base.Add(7*time.Minute))
	failedAt := base.Add(7 * time.Minute)
	failed.CompletedAt = &failedAt
	cancelled := taskAt("cancelled", stateCancelled, base.Add(6*time.Minute))
	cancelledAt := base.Add(6 * time.Minute)
	cancelled.CompletedAt = &cancelledAt

	summary := buildMobileTaskSummary([]Task{
		waiting, upcomingLater, completed, running, cancelled, paused,
		blocked, upcomingSooner, failed, queued,
	}, 2)
	if summary.Counts != (mobileSummaryCounts{Active: 4, Scheduled: 2, Blocked: 1}) {
		t.Fatalf("counts=%+v", summary.Counts)
	}
	if got, want := taskIDs(summary.Active), []string{"blocked", "running", "queued", "waiting"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active=%v want=%v", got, want)
	}
	if got, want := taskIDs(summary.Upcoming), []string{"schedule-sooner", "schedule-later"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upcoming=%v want=%v", got, want)
	}
	if got, want := taskIDs(summary.Recent), []string{"failed", "cancelled"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("recent=%v want=%v", got, want)
	}
	for _, tasks := range [][]Task{summary.Active, summary.Upcoming, summary.Recent} {
		for _, task := range tasks {
			if task.ID == paused.ID {
				t.Fatal("paused schedule should not be duplicated into an active or upcoming partition")
			}
		}
	}

	empty := buildMobileTaskSummary(nil, defaultMobileRecentLimit)
	if empty.Active == nil || empty.Upcoming == nil || empty.Recent == nil || len(empty.Active)+len(empty.Upcoming)+len(empty.Recent) != 0 {
		t.Fatalf("empty summary must encode arrays, got %+v", empty)
	}
}

func TestMobileSummaryUsesInjectedProjectAndClampsRecentLimit(t *testing.T) {
	app, _, _ := newTestApp(t)
	for index := 0; index < 14; index++ {
		if _, _, err := app.store.Create(CreateTaskInput{
			AgentID: 7, ProjectID: "project-a", Title: "completed-a", State: stateCompleted, AssignedThreadID: "thread-a",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := app.store.Create(CreateTaskInput{
		AgentID: 8, ProjectID: "project-b", Title: "secret-b", State: stateCompleted, AssignedThreadID: "thread-b",
	}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/mobile/summary?recent_limit=99&project_id=project-b", nil)
	request.Header.Set("X-Apteva-Project-ID", "project-a")
	recorder := httptest.NewRecorder()
	app.handleMobileSummary(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var summary mobileTaskSummary
	if err := json.Unmarshal(recorder.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Recent) != maxMobileRecentLimit {
		t.Fatalf("recent len=%d want=%d", len(summary.Recent), maxMobileRecentLimit)
	}
	for _, task := range summary.Recent {
		if task.ProjectID != "project-a" || task.Title == "secret-b" {
			t.Fatalf("cross-project task leaked: %+v", task)
		}
	}

	request = httptest.NewRequest(http.MethodGet, "/mobile/summary?recent_limit=0", nil)
	request.Header.Set("X-Apteva-Project-ID", "project-a")
	recorder = httptest.NewRecorder()
	app.handleMobileSummary(recorder, request)
	if err := json.Unmarshal(recorder.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || len(summary.Recent) != 1 {
		t.Fatalf("minimum clamp status=%d summary=%+v", recorder.Code, summary)
	}

	request = httptest.NewRequest(http.MethodGet, "/mobile/summary?project_id=project-a", nil)
	recorder = httptest.NewRecorder()
	app.handleMobileSummary(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("query project context must not authorize native summary: status=%d", recorder.Code)
	}
}

func TestTaskMutationsEmitDeclaredRefreshTopics(t *testing.T) {
	app, _, _ := newTestApp(t)
	manifest := app.Manifest()
	wantTopics := []string{
		"task.created",
		"task.updated",
		"task.state_changed",
		"task.schedule_updated",
		"task.schedule_paused",
		"task.schedule_resumed",
		"task.schedule_run_requested",
		"task.occurrence_skipped_overlap",
	}
	for _, componentIndex := range []int{0, 1} {
		if got := manifest.Provides.UIComponents[componentIndex].RefreshTopics; !reflect.DeepEqual(got, wantTopics) {
			t.Fatalf("component %q refresh topics=%v want=%v", manifest.Provides.UIComponents[componentIndex].Name, got, wantTopics)
		}
	}
	declared := map[string]bool{}
	for _, topic := range wantTopics {
		declared[topic] = true
	}
	events := []TaskEvent{}
	app.store.onEvent = func(event TaskEvent) { events = append(events, event) }
	assertOne := func(t *testing.T, eventType string, mutate func() error) {
		t.Helper()
		before := len(events)
		if err := mutate(); err != nil {
			t.Fatal(err)
		}
		if len(events) != before+1 {
			t.Fatalf("mutation %q emitted %d events, want 1", eventType, len(events)-before)
		}
		got := events[len(events)-1].EventType
		if got != eventType || !declared["task."+got] {
			t.Fatalf("mutation emitted undeclared event %q", got)
		}
	}

	var task *Task
	assertOne(t, "created", func() error {
		var err error
		task, _, err = app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "mutable", AssignedThreadID: "thread-a"})
		return err
	})
	step := "Checking data"
	assertOne(t, "updated", func() error {
		_, _, err := app.store.Update(task.ID, "thread-a", UpdateTaskInput{CurrentStep: &step})
		return err
	})
	running := stateRunning
	assertOne(t, "state_changed", func() error {
		_, _, err := app.store.Update(task.ID, "thread-a", UpdateTaskInput{State: &running})
		return err
	})
	assertOne(t, "schedule_updated", func() error {
		_, err := app.store.SetSchedule(task.ID, "thread-a", ScheduleInput{Kind: scheduleInterval, Every: "10m"})
		return err
	})
	assertOne(t, "schedule_paused", func() error {
		_, err := app.store.Pause(task.ID, "thread-a")
		return err
	})
	assertOne(t, "schedule_resumed", func() error {
		_, err := app.store.Resume(task.ID, "thread-a")
		return err
	})
	assertOne(t, "schedule_run_requested", func() error {
		_, err := app.store.RunNow(task.ID, "thread-a")
		return err
	})

	if _, _, err := app.store.Create(CreateTaskInput{
		AgentID: 7, ProjectID: "project-a", Title: "active occurrence", AssignedThreadID: "thread-a", ParentTaskID: task.ID,
	}); err != nil {
		t.Fatal(err)
	}
	task, _ = app.store.Get(task.ID)
	assertOne(t, "occurrence_skipped_overlap", func() error {
		return app.scheduler.Tick(task.NextRunAt.Add(time.Second), "project-a")
	})

	var cancellable *Task
	if created, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: "cancel me", AssignedThreadID: "thread-a"}); err != nil {
		t.Fatal(err)
	} else {
		cancellable = created
	}
	cancelled := stateCancelled
	assertOne(t, "state_changed", func() error {
		_, _, err := app.store.Update(cancellable.ID, "operator", UpdateTaskInput{State: &cancelled})
		return err
	})
}
