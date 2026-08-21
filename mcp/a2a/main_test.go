package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	failList     bool // simulate a platform without the agent directory
	// attached marks which agents the server reports as bound to the
	// calling install. An empty map simulates a pre-annotation server.
	attached map[int64]bool
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

func (p *recordingPlatform) ListAgents(projectID string) ([]sdk.PlatformAgent, error) {
	if p.failList {
		return nil, fmt.Errorf("directory unavailable")
	}
	var out []sdk.PlatformAgent
	for _, a := range p.agents {
		if projectID == "" || a.ProjectID == projectID {
			entry := *a
			entry.AttachedToCaller = p.attached[a.ID]
			out = append(out, entry)
		}
	}
	return out, nil
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
	return newTestEnvWithConfig(t, nil)
}

func newTestEnvWithConfig(t *testing.T, config map[string]string) (*sdk.AppCtx, *recordingPlatform) {
	t.Helper()
	platform := &recordingPlatform{
		agents: map[int64]*sdk.PlatformAgent{
			41: {ID: 41, Name: "Research", ProjectID: testProject},
			42: {ID: 42, Name: "CRM", ProjectID: testProject},
			43: {ID: 43, Name: "Bystander", ProjectID: testProject},
			77: {ID: 77, Name: "Elsewhere", ProjectID: "proj-other"},
		},
		attached: map[int64]bool{41: true, 42: true, 43: true},
	}
	opts := []tk.Option{tk.WithProjectID(testProject), tk.WithPlatform(platform)}
	if config != nil {
		opts = append(opts, tk.WithConfig(config))
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", opts...)
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
		{"unknown name target", func() (any, error) {
			return app.toolSend(callerCtx(41, ""), ctx, map[string]any{"to": "No Such Agent", "message": "x"})
		}, "agents_discover"},
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

func TestFederationRoutesOwnPeerAuthentication(t *testing.T) {
	routes := (&App{}).HTTPRoutes()
	wantPublic := map[string]bool{
		"/directory/agents": true,
		"/agent-cards/":     true,
		"/agents/":          true,
	}
	for _, route := range routes {
		if wantPublic[route.Pattern] && !route.NoAuth {
			t.Fatalf("federation route %q must bypass the SDK token gate and authenticate its peer itself", route.Pattern)
		}
		if strings.HasPrefix(route.Pattern, "/tasks") && route.NoAuth {
			t.Fatalf("operator route %q must remain platform-authenticated", route.Pattern)
		}
	}
	manifest := (&App{}).Manifest()
	for _, route := range manifest.Provides.HTTPRoutes {
		if wantPublic[route.Prefix] && !route.NoAuth {
			t.Fatalf("manifest federation prefix %q missing no_auth", route.Prefix)
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

func TestDiscoverListsProjectPeers(t *testing.T) {
	ctx, _ := newTestEnv(t)
	app := &App{}
	res := resultMap(t)(app.toolDiscover(callerCtx(41, ""), ctx, map[string]any{}))
	entries := res["agents"].([]discoverEntry)
	ids := map[int64]discoverEntry{}
	for _, e := range entries {
		ids[e.ID] = e
	}
	if _, self := ids[41]; self {
		t.Fatal("discover included the caller itself")
	}
	if _, foreign := ids[77]; foreign {
		t.Fatal("discover leaked another project's agent")
	}
	crm, ok := ids[42]
	if !ok || crm.Address != "agent:42" || crm.Name != "CRM" || crm.Peer != "local" {
		t.Fatalf("bad CRM entry: %+v", crm)
	}

	// Old topology scopes are ignored with a migration note; discovery
	// remains generic and returns the same actionable agents.
	res = resultMap(t)(app.toolDiscover(callerCtx(41, ""), ctx, map[string]any{"scope": "fleet"}))
	if len(res["agents"].([]discoverEntry)) != 2 || !strings.Contains(res["note"].(string), "deprecated") {
		t.Fatalf("legacy scope should be ignored with note, got %+v", res)
	}
}

func TestSendByNameAndAmbiguity(t *testing.T) {
	ctx, platform := newTestEnv(t)
	app := &App{}

	// Exact name, case-insensitive.
	res := resultMap(t)(app.toolAsk(callerCtx(41, ""), ctx, map[string]any{
		"to": "crm", "message": "By name please.",
	}))
	to := res["to"].(map[string]any)
	if to["id"].(int64) != 42 {
		t.Fatalf("name resolution sent to %v", to)
	}

	// Ambiguous name → error listing candidate ids.
	platform.agents[44] = &sdk.PlatformAgent{ID: 44, Name: "CRM", ProjectID: testProject}
	if _, err := app.toolSend(callerCtx(41, ""), ctx, map[string]any{
		"to": "CRM", "message": "x",
	}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous name err = %v", err)
	}

	// Unknown name → guidance toward agents_discover.
	if _, err := app.toolSend(callerCtx(41, ""), ctx, map[string]any{
		"to": "Nobody", "message": "x",
	}); err == nil || !strings.Contains(err.Error(), "agents_discover") {
		t.Fatalf("unknown name err = %v", err)
	}

	// Directory unavailable → ids still work, names fail with clear error.
	platform.failList = true
	if _, err := app.toolSend(callerCtx(41, ""), ctx, map[string]any{
		"to": "crm", "message": "x",
	}); err == nil || !strings.Contains(err.Error(), "name lookup unavailable") {
		t.Fatalf("no-directory name err = %v", err)
	}
	if _, err := app.toolSend(callerCtx(41, ""), ctx, map[string]any{
		"to": "42", "message": "id still fine",
	}); err != nil {
		t.Fatalf("id send with no directory failed: %v", err)
	}
}

func TestDiscoverHidesUnattachedPeers(t *testing.T) {
	ctx, platform := newTestEnv(t)
	app := &App{}
	// Agent 45 is in the project but has no a2a tools attached.
	platform.agents[45] = &sdk.PlatformAgent{ID: 45, Name: "NoTools", ProjectID: testProject}

	res := resultMap(t)(app.toolDiscover(callerCtx(41, ""), ctx, map[string]any{}))
	for _, e := range res["agents"].([]discoverEntry) {
		if e.ID == 45 {
			t.Fatal("discover listed an agent without a2a attached")
		}
	}
	note, _ := res["note"].(string)
	if !strings.Contains(note, "without Agent to Agent attached") {
		t.Fatalf("hidden peers not surfaced in note: %q", note)
	}

	// agent_ask to it is rejected with actionable guidance…
	if _, err := app.toolAsk(callerCtx(41, ""), ctx, map[string]any{
		"to": "45", "message": "please respond",
	}); err == nil || !strings.Contains(err.Error(), "can never reply") {
		t.Fatalf("ask to unattached err = %v", err)
	}
	// …but a one-way agent_send still delivers, with a warning note.
	res = resultMap(t)(app.toolSend(callerCtx(41, ""), ctx, map[string]any{
		"to": "45", "message": "FYI only",
	}))
	if note, _ := res["note"].(string); !strings.Contains(note, "cannot reply") {
		t.Fatalf("one-way send to unattached missing warning: %v", res)
	}
}

func TestDiscoverListsEveryoneOnPreAnnotationPlatform(t *testing.T) {
	ctx, platform := newTestEnv(t)
	app := &App{}
	platform.attached = map[int64]bool{} // server never sets the flag
	res := resultMap(t)(app.toolDiscover(callerCtx(41, ""), ctx, map[string]any{}))
	if len(res["agents"].([]discoverEntry)) != 2 {
		t.Fatalf("pre-annotation discover should list all project peers: %+v", res)
	}
	note, _ := res["note"].(string)
	if !strings.Contains(note, "does not report") {
		t.Fatalf("missing compatibility note: %q", note)
	}
	// Asks stay allowed when reachability is unknown.
	if _, err := app.toolAsk(callerCtx(41, ""), ctx, map[string]any{
		"to": "42", "message": "still works",
	}); err != nil {
		t.Fatalf("ask on pre-annotation platform failed: %v", err)
	}
}

func TestFollowUpRoutesToResponderThread(t *testing.T) {
	ctx, platform := newTestEnv(t)
	app := &App{}
	res := resultMap(t)(app.toolAsk(callerCtx(41, "th-9"), ctx, map[string]any{
		"to": "42", "message": "Big job please.",
	}))
	taskID := fmt.Sprint(res["task_id"].(int64))

	// B's worker thread takes the task and asks a question.
	resultMap(t)(app.toolReply(callerCtx(42, "worker-w"), ctx, map[string]any{
		"task_id": taskID, "message": "Which format?", "status": "input_required",
	}))
	// A answers: the follow-up must land in B's worker-w, not B's main.
	resultMap(t)(app.toolSend(callerCtx(41, "th-9"), ctx, map[string]any{
		"task_id": taskID, "message": "CSV please.",
	}))
	last := platform.threadEvents[len(platform.threadEvents)-1]
	if last.Ref.AgentID != 42 || last.Ref.ThreadID != "worker-w" {
		t.Fatalf("follow-up routed to %+v, want agent 42 thread worker-w", last.Ref)
	}
	if !strings.Contains(last.Message, "CSV please.") {
		t.Fatalf("follow-up content wrong: %s", last.Message)
	}

	// If the worker thread died, delivery falls back to B's main.
	platform.failThread = true
	resultMap(t)(app.toolSend(callerCtx(41, "th-9"), ctx, map[string]any{
		"task_id": taskID, "message": "Still there?",
	}))
	ev := platform.lastEvent(t)
	if ev.AgentID != 42 || !strings.Contains(ev.Message, "Still there?") {
		t.Fatalf("worker-death fallback wrong: %+v", ev)
	}
}

func TestFederatedDiscoveryAddressIsImmediatelyActionable(t *testing.T) {
	const sharedToken = "test-peer-token-with-enough-entropy"
	targetPeerJSON, _ := json.Marshal([]peerConfig{{
		ID: "source", Name: "Source instance", BaseURL: "http://127.0.0.1:1", Token: sharedToken,
		DiscoverAgents: []string{"CRM"}, InvokeAgents: []string{"CRM"},
	}})
	targetCtx, targetPlatform := newTestEnvWithConfig(t, map[string]string{"peers_json": string(targetPeerJSON)})
	targetApp := &App{}
	if err := targetApp.OnMount(targetCtx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/directory/agents", targetApp.handleDirectory)
	mux.HandleFunc("/agent-cards/", targetApp.handleAgentCard)
	mux.HandleFunc("/agents/", targetApp.handleAgentProtocol)
	server := httptest.NewServer(http.StripPrefix("/api/apps/a2a", mux))
	t.Cleanup(server.Close)
	targetCtx.Config()["public_url"] = server.URL
	unauthorized, err := http.Get(server.URL + "/api/apps/a2a/directory/agents")
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous directory status = %d, want 401", unauthorized.StatusCode)
	}

	sourcePeerJSON, _ := json.Marshal([]peerConfig{{
		ID: "target", Name: "Main instance", BaseURL: server.URL + "/api/apps/a2a", Token: sharedToken,
	}})
	sourceCtx, sourcePlatform := newTestEnvWithConfig(t, map[string]string{
		"peers_json": string(sourcePeerJSON), "public_url": "http://127.0.0.1:9",
	})
	sourceApp := &App{}
	// Import source configuration without mounting it: this unit test hosts
	// both nodes in one process, while production has one globalCtx per sidecar.
	if _, err := ensureLocalNode(sourceCtx); err != nil {
		t.Fatal(err)
	}
	if err := syncConfiguredPeers(sourceCtx); err != nil {
		t.Fatal(err)
	}

	// One discovery call returns a remote address that is already valid for
	// agent_ask; agent_get is not required for resolution.
	discovered := resultMap(t)(sourceApp.toolDiscover(callerCtx(41, "source-thread"), sourceCtx, map[string]any{
		"query": "CRM", "peer": "target",
	}))
	entries := discovered["agents"].([]discoverEntry)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Address, "a2a:remote_") {
		t.Fatalf("remote discovery = %+v", discovered)
	}
	address := entries[0].Address

	// Use the discovery address directly: no agent_get call is required.
	asked := resultMap(t)(sourceApp.toolAsk(callerCtx(41, "source-thread"), sourceCtx, map[string]any{
		"to": address, "message": "Check the deployment.",
	}))
	localTaskID := asked["task_id"].(int64)
	if event := targetPlatform.lastEvent(t); event.AgentID != 42 || !strings.Contains(event.Message, "Check the deployment.") {
		t.Fatalf("inbound remote delivery = %+v", event)
	}

	// Optional inspection still returns the complete generated Agent Card and
	// preserves the actionable address.
	details := resultMap(t)(sourceApp.toolGetAgent(callerCtx(41, "source-thread"), sourceCtx, map[string]any{
		"agent": address,
	}))
	if details["address"] != address || details["card"] == nil {
		t.Fatalf("agent_get = %+v", details)
	}
	card, ok := details["card"].(*AgentCard)
	if !ok || len(card.SupportedInterfaces) != 1 || card.SupportedInterfaces[0].ProtocolVersion != "1.0" ||
		len(card.DefaultInputModes) != 1 || card.DefaultInputModes[0] != "text/plain" {
		t.Fatalf("generated A2A v1 card = %+v", details["card"])
	}

	// The target's ordinary agent_reply updates the protocol task. The source
	// worker then retrieves it and routes it back to the originating thread.
	inbound, err := listTasks(targetCtx.AppDB(), testProject, taskFilter{Direction: "inbound", Limit: 10})
	if err != nil || len(inbound) != 1 {
		t.Fatalf("target inbound tasks = %+v, err %v", inbound, err)
	}
	resultMap(t)(targetApp.toolReply(callerCtx(42, "target-worker"), targetCtx, map[string]any{
		"task_id": fmt.Sprint(inbound[0].ID), "message": "Which deployment?", "status": "input_required",
	}))
	if err := sourceApp.syncRemoteTasks(context.Background(), sourceCtx); err != nil {
		t.Fatal(err)
	}
	if len(sourcePlatform.threadEvents) != 1 {
		t.Fatalf("source thread deliveries = %d", len(sourcePlatform.threadEvents))
	}
	question := sourcePlatform.threadEvents[0]
	if question.Ref.AgentID != 41 || question.Ref.ThreadID != "source-thread" || !strings.Contains(question.Message, "Which deployment?") {
		t.Fatalf("input-required delivery = %+v", question)
	}

	// A local follow-up continues the same remote task and reaches the target
	// agent's responder thread.
	resultMap(t)(sourceApp.toolSend(callerCtx(41, "source-thread"), sourceCtx, map[string]any{
		"task_id": fmt.Sprint(localTaskID), "message": "Deployment 728.",
	}))
	if len(targetPlatform.threadEvents) != 1 || targetPlatform.threadEvents[0].Ref.ThreadID != "target-worker" ||
		!strings.Contains(targetPlatform.threadEvents[0].Message, "Deployment 728.") {
		t.Fatalf("remote follow-up delivery = %+v", targetPlatform.threadEvents)
	}

	resultMap(t)(targetApp.toolReply(callerCtx(42, "target-worker"), targetCtx, map[string]any{
		"task_id": fmt.Sprint(inbound[0].ID), "message": "Credential expired.", "status": "completed",
	}))
	if err := sourceApp.syncRemoteTasks(context.Background(), sourceCtx); err != nil {
		t.Fatal(err)
	}
	if len(sourcePlatform.threadEvents) != 2 {
		t.Fatalf("source thread deliveries after completion = %d", len(sourcePlatform.threadEvents))
	}
	delivery := sourcePlatform.threadEvents[1]
	if delivery.Ref.AgentID != 41 || delivery.Ref.ThreadID != "source-thread" || !strings.Contains(delivery.Message, "Credential expired.") {
		t.Fatalf("remote reply delivery = %+v", delivery)
	}
	localTask, _ := getTask(sourceCtx.AppDB(), testProject, localTaskID)
	if localTask == nil || localTask.Status != "completed" {
		t.Fatalf("source outbound task = %+v", localTask)
	}

	// Cancellation also maps to the same remote protocol task.
	cancelAsk := resultMap(t)(sourceApp.toolAsk(callerCtx(41, "source-thread"), sourceCtx, map[string]any{
		"to": address, "message": "Start another check.",
	}))
	cancelTaskID := cancelAsk["task_id"].(int64)
	resultMap(t)(sourceApp.toolReply(callerCtx(41, "source-thread"), sourceCtx, map[string]any{
		"task_id": fmt.Sprint(cancelTaskID), "message": "No longer needed.", "status": "canceled",
	}))
	canceledOutbound, _ := getTask(sourceCtx.AppDB(), testProject, cancelTaskID)
	if canceledOutbound == nil || canceledOutbound.Status != "canceled" {
		t.Fatalf("canceled outbound task = %+v", canceledOutbound)
	}
	canceledInbound, err := getTaskByProtocolID(targetCtx.AppDB(), canceledOutbound.RemoteTaskID)
	if err != nil || canceledInbound == nil || canceledInbound.Status != "canceled" {
		t.Fatalf("canceled authoritative task = %+v, err %v", canceledInbound, err)
	}
}
