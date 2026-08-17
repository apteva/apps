package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// recordingPlatform captures deliveries and serves a small agent
// roster. Thread-targeted sends are recorded separately so tests can
// assert reply routing.
type recordingPlatform struct {
	tk.BasePlatformClient
	agents       map[int64]*sdk.PlatformAgent
	events       []capturedEvent
	threadEvents []capturedThreadEvent
	failThread   bool // force SendThreadEvent to fail → main-thread fallback
	failSend     bool // force SendEvent to fail
}

type capturedEvent struct {
	AgentID int64
	Message string
}

type capturedThreadEvent struct {
	Ref     sdk.ThreadRef
	Message string
}

func (p *recordingPlatform) GetAgent(id int64) (*sdk.PlatformAgent, error) {
	a, ok := p.agents[id]
	if !ok {
		return nil, fmt.Errorf("agent %d not found", id)
	}
	return a, nil
}

func (p *recordingPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return p.GetAgent(id)
}

func (p *recordingPlatform) SendEvent(instanceID int64, message string) error {
	if p.failSend {
		return fmt.Errorf("agent %d offline", instanceID)
	}
	p.events = append(p.events, capturedEvent{AgentID: instanceID, Message: message})
	return nil
}

func (p *recordingPlatform) SendThreadEvent(target sdk.ThreadRef, message any) error {
	if p.failThread {
		return fmt.Errorf("thread %s gone", target.ThreadID)
	}
	text, _ := message.(string)
	p.threadEvents = append(p.threadEvents, capturedThreadEvent{Ref: target, Message: text})
	return nil
}

func (p *recordingPlatform) lastEvent(t *testing.T) capturedEvent {
	t.Helper()
	if len(p.events) == 0 {
		t.Fatal("no events delivered")
	}
	return p.events[len(p.events)-1]
}

const testProject = "proj-1"

func newTestEnv(t *testing.T) (*sdk.AppCtx, *recordingPlatform) {
	t.Helper()
	platform := &recordingPlatform{agents: map[int64]*sdk.PlatformAgent{
		41: {ID: 41, Name: "Research", ProjectID: testProject},
		42: {ID: 42, Name: "CRM", ProjectID: testProject},
		43: {ID: 43, Name: "Bystander", ProjectID: testProject},
		77: {ID: 77, Name: "Elsewhere", ProjectID: "proj-other"},
	}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject), tk.WithPlatform(platform))
	return ctx, platform
}

func callerCtx(agentID int64, threadID string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{
		AgentID:   agentID,
		ThreadID:  threadID,
		ProjectID: testProject,
	})
}

// resultMap returns a closure so call sites can feed a tool's
// (any, error) pair directly: resultMap(t)(app.toolAsk(...)).
func resultMap(t *testing.T) func(any, error) map[string]any {
	return func(v any, err error) map[string]any {
		t.Helper()
		if err != nil {
			t.Fatalf("tool error: %v", err)
		}
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("tool result is %T, want map", v)
		}
		return m
	}
}

func TestAskDeliversAndReplyRoutesToAskingThread(t *testing.T) {
	ctx, platform := newTestEnv(t)
	app := &App{}

	// Agent 41 (thread th-9) asks agent 42.
	res := resultMap(t)(app.toolAsk(callerCtx(41, "th-9"), ctx, map[string]any{
		"to": "42", "message": "Summarize this week's leads.",
	}))
	taskID := res["task_id"].(int64)
	ev := platform.lastEvent(t)
	if ev.AgentID != 42 {
		t.Fatalf("ask delivered to agent %d, want 42", ev.AgentID)
	}
	for _, want := range []string{
		fmt.Sprintf("[a2a task:%d]", taskID),
		`agent "Research"`,
		"Summarize this week's leads.",
		"not from your operator",
		fmt.Sprintf("agent_reply(task_id=\"%d\"", taskID),
	} {
		if !strings.Contains(ev.Message, want) {
			t.Fatalf("ask event missing %q:\n%s", want, ev.Message)
		}
	}
	task, err := getTask(ctx.AppDB(), testProject, taskID)
	if err != nil || task == nil || task.Status != "submitted" {
		t.Fatalf("task after ask = %+v, err %v; want submitted", task, err)
	}

	// Agent 42 replies completed → must land in thread th-9 of agent 41.
	res = resultMap(t)(app.toolReply(callerCtx(42, "worker-3"), ctx, map[string]any{
		"task_id": fmt.Sprint(taskID), "message": "12 leads, 3 hot.", "status": "completed",
	}))
	if res["status"] != "completed" {
		t.Fatalf("reply status = %v", res["status"])
	}
	if len(platform.threadEvents) != 1 {
		t.Fatalf("thread deliveries = %d, want 1", len(platform.threadEvents))
	}
	te := platform.threadEvents[0]
	if te.Ref.AgentID != 41 || te.Ref.ThreadID != "th-9" {
		t.Fatalf("reply routed to %+v, want agent 41 thread th-9", te.Ref)
	}
	if !strings.Contains(te.Message, "status:completed") || !strings.Contains(te.Message, "12 leads, 3 hot.") {
		t.Fatalf("reply event malformed:\n%s", te.Message)
	}
	task, _ = getTask(ctx.AppDB(), testProject, taskID)
	if task.Status != "completed" {
		t.Fatalf("task status = %s, want completed", task.Status)
	}
}

func TestReplyFallsBackToMainThread(t *testing.T) {
	ctx, platform := newTestEnv(t)
	app := &App{}
	res := resultMap(t)(app.toolAsk(callerCtx(41, "th-9"), ctx, map[string]any{
		"to": "42", "message": "Ping.",
	}))
	taskID := res["task_id"].(int64)
	platform.failThread = true
	resultMap(t)(app.toolReply(callerCtx(42, ""), ctx, map[string]any{
		"task_id": fmt.Sprint(taskID), "message": "Pong.",
	}))
	ev := platform.lastEvent(t)
	if ev.AgentID != 41 || !strings.Contains(ev.Message, "Pong.") {
		t.Fatalf("fallback delivery wrong: %+v", ev)
	}
}

func TestSendIsOneWayLedgerRecord(t *testing.T) {
	ctx, platform := newTestEnv(t)
	app := &App{}
	res := resultMap(t)(app.toolSend(callerCtx(41, ""), ctx, map[string]any{
		"to": "agent:42", "message": "FYI: quarterly numbers posted.",
	}))
	taskID := res["task_id"].(int64)
	ev := platform.lastEvent(t)
	if !strings.Contains(ev.Message, "no reply required") {
		t.Fatalf("send event missing one-way marker:\n%s", ev.Message)
	}
	task, _ := getTask(ctx.AppDB(), testProject, taskID)
	if task.Kind != "message" || task.Status != "completed" {
		t.Fatalf("one-way task = kind %s status %s", task.Kind, task.Status)
	}
}

func TestFollowUpOnInputRequiredResumesWorking(t *testing.T) {
	ctx, platform := newTestEnv(t)
	app := &App{}
	res := resultMap(t)(app.toolAsk(callerCtx(41, "th-9"), ctx, map[string]any{
		"to": "42", "message": "Book the venue.",
	}))
	taskID := fmt.Sprint(res["task_id"].(int64))

	// Responder needs input.
	resultMap(t)(app.toolReply(callerCtx(42, ""), ctx, map[string]any{
		"task_id": taskID, "message": "Which date?", "status": "input_required",
	}))
	// Requester answers via follow-up → back to working.
	res = resultMap(t)(app.toolSend(callerCtx(41, "th-9"), ctx, map[string]any{
		"task_id": taskID, "message": "March 3rd.",
	}))
	if res["status"] != "working" {
		t.Fatalf("status after follow-up = %v, want working", res["status"])
	}
	ev := platform.lastEvent(t)
	if ev.AgentID != 42 || !strings.Contains(ev.Message, "March 3rd.") {
		t.Fatalf("follow-up delivery wrong: %+v", ev)
	}
}

func TestGuards(t *testing.T) {
	ctx, platform := newTestEnv(t)
	app := &App{}

	cases := []struct {
		name string
		call func() (any, error)
		want string
	}{
		{"no caller", func() (any, error) {
			return app.toolSend(context.Background(), ctx, map[string]any{"to": "42", "message": "x"})
		}, "identity unavailable"},
		{"self send", func() (any, error) {
			return app.toolSend(callerCtx(41, ""), ctx, map[string]any{"to": "41", "message": "x"})
		}, "yourself"},
		{"unknown agent", func() (any, error) {
			return app.toolSend(callerCtx(41, ""), ctx, map[string]any{"to": "999", "message": "x"})
		}, "not found"},
		{"non-numeric target", func() (any, error) {
			return app.toolSend(callerCtx(41, ""), ctx, map[string]any{"to": "CRM", "message": "x"})
		}, "agents_list"},
		{"cross project", func() (any, error) {
			return app.toolSend(callerCtx(41, ""), ctx, map[string]any{"to": "77", "message": "x"})
		}, "not in your project"},
		{"empty message", func() (any, error) {
			return app.toolSend(callerCtx(41, ""), ctx, map[string]any{"to": "42", "message": "  "})
		}, "message required"},
	}
	for _, tc := range cases {
		_, err := tc.call()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
	if len(platform.events) != 0 {
		t.Fatalf("guard failures still delivered %d events", len(platform.events))
	}
}

func TestReplyAuthorization(t *testing.T) {
	ctx, _ := newTestEnv(t)
	app := &App{}
	res := resultMap(t)(app.toolAsk(callerCtx(41, ""), ctx, map[string]any{
		"to": "42", "message": "Do the thing.",
	}))
	taskID := fmt.Sprint(res["task_id"].(int64))

	// A third agent in the same project cannot touch the task.
	if _, err := app.toolReply(callerCtx(43, ""), ctx, map[string]any{
		"task_id": taskID, "message": "hijack",
	}); err == nil || !strings.Contains(err.Error(), "not a participant") {
		t.Fatalf("bystander reply err = %v", err)
	}

	// The requester cannot complete their own ask…
	if _, err := app.toolReply(callerCtx(41, ""), ctx, map[string]any{
		"task_id": taskID, "message": "done myself", "status": "completed",
	}); err == nil || !strings.Contains(err.Error(), "requester") {
		t.Fatalf("requester-complete err = %v", err)
	}
	// …but may cancel it.
	res = resultMap(t)(app.toolReply(callerCtx(41, ""), ctx, map[string]any{
		"task_id": taskID, "message": "never mind", "status": "canceled",
	}))
	if res["status"] != "canceled" {
		t.Fatalf("cancel status = %v", res["status"])
	}
	// Replying to a closed task fails with follow-up guidance.
	if _, err := app.toolReply(callerCtx(42, ""), ctx, map[string]any{
		"task_id": taskID, "message": "too late",
	}); err == nil || !strings.Contains(err.Error(), "already canceled") {
		t.Fatalf("closed-task reply err = %v", err)
	}
}

func TestRateLimitStopsLoops(t *testing.T) {
	ctx, platform := newTestEnv(t)
	app := &App{}
	var hitLimit bool
	for i := 0; i < defaultRateLimitPerMinute+1; i++ {
		_, err := app.toolSend(callerCtx(41, ""), ctx, map[string]any{
			"to": "42", "message": fmt.Sprintf("msg %d", i),
		})
		if err != nil {
			if !strings.Contains(err.Error(), "rate limit") {
				t.Fatalf("unexpected error: %v", err)
			}
			hitLimit = true
			break
		}
	}
	if !hitLimit {
		t.Fatal("rate limit never engaged")
	}
	if len(platform.events) != defaultRateLimitPerMinute {
		t.Fatalf("delivered %d events, want %d", len(platform.events), defaultRateLimitPerMinute)
	}
}

func TestAgentTasksView(t *testing.T) {
	ctx, _ := newTestEnv(t)
	app := &App{}
	res := resultMap(t)(app.toolAsk(callerCtx(41, ""), ctx, map[string]any{
		"to": "42", "message": "Task A.",
	}))
	taskID := fmt.Sprint(res["task_id"].(int64))
	resultMap(t)(app.toolSend(callerCtx(42, ""), ctx, map[string]any{
		"to": "41", "message": "Unrelated note.",
	}))

	res = resultMap(t)(app.toolTasks(callerCtx(41, ""), ctx, map[string]any{"role": "sent", "status": "open"}))
	tasks := res["tasks"].([]*Task)
	if len(tasks) != 1 || fmt.Sprint(tasks[0].ID) != taskID {
		t.Fatalf("sent-open view = %+v", tasks)
	}
	res = resultMap(t)(app.toolTasks(callerCtx(41, ""), ctx, map[string]any{}))
	if len(res["tasks"].([]*Task)) != 2 {
		t.Fatalf("participant view should see both tasks")
	}
}

// TestNoReservedRoutePrefixes guards the sidecar against claiming the
// SDK's reserved route prefixes, which panics at boot.
func TestNoReservedRoutePrefixes(t *testing.T) {
	reserved := []string{"/health", "/manifest", "/mcp", "/events", "/ui/"}
	for _, route := range (&App{}).HTTPRoutes() {
		for _, prefix := range reserved {
			if route.Pattern == prefix || strings.HasPrefix(route.Pattern, prefix) {
				t.Fatalf("route %q collides with reserved prefix %q", route.Pattern, prefix)
			}
		}
	}
}

// TestManifestMatchesYAML guards the release failure mode where the
// version (or permissions) get bumped in apteva.yaml but not in the
// embedded manifestYAML, or vice versa.
func TestManifestMatchesYAML(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fileM, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("apteva.yaml invalid: %v", err)
	}
	embeddedM := (&App{}).Manifest()
	if fileM.Name != embeddedM.Name || fileM.Version != embeddedM.Version {
		t.Fatalf("manifest drift: apteva.yaml %s@%s vs embedded %s@%s",
			fileM.Name, fileM.Version, embeddedM.Name, embeddedM.Version)
	}
	filePerms := fmt.Sprint(fileM.Requires.Permissions)
	embeddedPerms := fmt.Sprint(embeddedM.Requires.Permissions)
	if filePerms != embeddedPerms {
		t.Fatalf("permission drift: apteva.yaml %s vs embedded %s", filePerms, embeddedPerms)
	}
}
