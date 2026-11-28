package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type recordedEvents struct{ topics []string }

func (r *recordedEvents) EmitWithProject(topic, _ string, _ any) { r.topics = append(r.topics, topic) }

func insertActivityAt(t *testing.T, ctx *sdk.AppCtx, workspaceID, eventType, actorID, summary string, data any, at time.Time) {
	t.Helper()
	raw, _ := json.Marshal(data)
	if data == nil {
		raw = []byte("{}")
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO workspace_activity (workspace_id, project_id, event_type, actor_kind, actor_id, actor_label, summary, data_json, created_at)
		VALUES (?, 'p', ?, 'agent', ?, ?, ?, ?, ?)`, workspaceID, eventType, actorID, "Agent "+actorID, summary, string(raw), at.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func widgetFixture(t *testing.T) (*App, *sdk.AppCtx, *Workspace, *Workspace) {
	t.Helper()
	t.Setenv("APTEVA_PROJECT_ID", "p")
	ctx, _ := newTestContext(t, &platformStub{})
	globalCtx = ctx
	a := &App{}
	agent := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "p", AgentID: 7, ThreadID: "thr_1"})
	one, err := a.createWorkspace(agent, ctx, map[string]any{"name": "one", "profile": "go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	code := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "p", AppName: "code", AppInstallID: 91})
	two, err := a.createWorkspace(code, ctx, map[string]any{"name": "two", "profile": "go", "owner_agent_id": float64(8)}, true)
	if err != nil {
		t.Fatal(err)
	}
	// Start from an empty log so each test controls the feed.
	if _, err := ctx.AppDB().Exec(`DELETE FROM workspace_activity`); err != nil {
		t.Fatal(err)
	}
	return a, ctx, one, two
}

func TestActivityFeedGroupsFileBurstsAndCommandPairs(t *testing.T) {
	_, ctx, one, two := widgetFixture(t)
	base := time.Now().UTC().Add(-30 * time.Minute)
	insertActivityAt(t, ctx, one.ID, "workspace.created", "7", "Workspace is ready", nil, base)
	for i := 0; i < 5; i++ { // five edits 20 s apart: one burst
		insertActivityAt(t, ctx, one.ID, "file.edited", "7", "Edited a.go", nil, base.Add(time.Duration(60+i*20)*time.Second))
	}
	insertActivityAt(t, ctx, two.ID, "file.written", "8", "Wrote b.go", nil, base.Add(150*time.Second)) // other workspace: own line
	insertActivityAt(t, ctx, one.ID, "command.started", "7", "Command started: go test ./...", map[string]any{"command_id": "cmd_1"}, base.Add(4*time.Minute))
	insertActivityAt(t, ctx, one.ID, "command.failed", "runtime", "Command failed: go test ./...", map[string]any{"command_id": "cmd_1", "exit_code": 1, "duration_ms": 42000}, base.Add(5*time.Minute))
	insertActivityAt(t, ctx, one.ID, "file.edited", "7", "Edited c.go", nil, base.Add(20*time.Minute)) // after the gap: new line

	lines, err := listProjectActivity(ctx.AppDB(), "p", activityQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range lines {
		got = append(got, line.EventType+"/"+line.WorkspaceName+"/"+strconv.Itoa(line.Count))
	}
	want := []string{"file.edited/one/1", "command.failed/one/1", "file.written/two/1", "file.edited/one/5", "workspace.created/one/1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("feed\n got %v\nwant %v", got, want)
	}
	if lines[1].Severity != "error" || lines[1].Category != "command" || !strings.Contains(string(lines[1].Data), "duration_ms") {
		t.Fatalf("failed command line: %+v", lines[1])
	}
	if lines[3].Summary != "Changed 5 files" || lines[3].FirstAt >= lines[3].CreatedAt {
		t.Fatalf("burst line: %+v", lines[3])
	}

	commands, err := listProjectActivity(ctx.AppDB(), "p", activityQuery{Limit: 10, Kinds: "commands"})
	if err != nil || len(commands) != 1 || commands[0].EventType != "command.failed" {
		t.Fatalf("commands only: %+v %v", commands, err)
	}
	limited, _ := listProjectActivity(ctx.AppDB(), "p", activityQuery{Limit: 2})
	if len(limited) != 2 {
		t.Fatalf("limit counts lines: %d", len(limited))
	}
	if _, err = listProjectActivity(ctx.AppDB(), "p", activityQuery{Kinds: "bogus"}); err == nil {
		t.Fatal("bad kinds accepted")
	}
}

func TestActivityFeedHidesTestWorkspacesUnlessAsked(t *testing.T) {
	_, ctx, one, _ := widgetFixture(t)
	if _, err := ctx.AppDB().Exec(`UPDATE workspaces SET kind='test' WHERE id=?`, one.ID); err != nil {
		t.Fatal(err)
	}
	insertActivityAt(t, ctx, one.ID, "command.succeeded", "runtime", "Command succeeded: bun test", nil, time.Now())
	lines, _ := listProjectActivity(ctx.AppDB(), "p", activityQuery{Limit: 5})
	if len(lines) != 0 {
		t.Fatalf("test workspace activity shown: %+v", lines)
	}
	lines, _ = listProjectActivity(ctx.AppDB(), "p", activityQuery{Limit: 5, IncludeTest: true})
	if len(lines) != 1 {
		t.Fatalf("include_test ignored: %+v", lines)
	}
}

func TestActivityBucketsCountTheLastHour(t *testing.T) {
	_, ctx, one, two := widgetFixture(t)
	now := time.Now().UTC()
	insertActivityAt(t, ctx, one.ID, "file.edited", "7", "x", nil, now.Add(-time.Minute))
	insertActivityAt(t, ctx, one.ID, "file.edited", "7", "x", nil, now.Add(-2*time.Minute))
	insertActivityAt(t, ctx, one.ID, "file.edited", "7", "x", nil, now.Add(-58*time.Minute))
	insertActivityAt(t, ctx, two.ID, "file.edited", "8", "x", nil, now.Add(-2*time.Hour)) // too old
	buckets, err := activityBucketsFor(ctx.AppDB(), "p", now)
	if err != nil {
		t.Fatal(err)
	}
	if b := buckets[one.ID]; len(b) != 12 || b[11] != 2 || b[0] != 1 {
		t.Fatalf("buckets %v", b)
	}
	if buckets[two.ID] != nil {
		t.Fatalf("old activity counted: %v", buckets[two.ID])
	}
}

func TestListFiltersAndPreviewOnRows(t *testing.T) {
	a, ctx, one, two := widgetFixture(t)
	if _, err := ctx.AppDB().Exec(`UPDATE workspaces SET instance_id=3 WHERE id=?`, two.ID); err != nil {
		t.Fatal(err)
	}
	if err := savePreview(ctx.AppDB(), &Preview{WorkspaceID: one.ID, ContainerPort: 3000, HostPort: 41000, Status: "live", StartedAt: nowUTC()}); err != nil {
		t.Fatal(err)
	}
	get := func(query string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		a.handleWorkspaces(rec, httptest.NewRequest(http.MethodGet, "/api/workspaces?"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	names := func(out map[string]any) string {
		var list []string
		for _, row := range out["workspaces"].([]any) {
			list = append(list, row.(map[string]any)["name"].(string))
		}
		return strings.Join(list, ",")
	}
	if got := names(get("owner_agent_id=7")); got != "one" {
		t.Fatalf("owner_agent_id: %s", got)
	}
	if got := names(get("owner_thread_id=thr_1")); got != "one" {
		t.Fatalf("owner_thread_id: %s", got)
	}
	if got := names(get("host=remote")); got != "two" {
		t.Fatalf("host=remote: %s", got)
	}
	if got := names(get("host=local&kind=dev")); got != "one" {
		t.Fatalf("host=local: %s", got)
	}
	out := get("include=activity_buckets")
	for _, row := range out["workspaces"].([]any) {
		w := row.(map[string]any)
		if len(w["activity_buckets"].([]any)) != 12 {
			t.Fatalf("buckets missing: %v", w)
		}
		if w["name"] == "one" {
			preview := w["preview"].(map[string]any)
			if preview["status"] != "live" || preview["url"] != nil || preview["local_url"] != "http://127.0.0.1:41000/" {
				t.Fatalf("preview on row: %v", preview)
			}
		} else if w["preview"] != nil {
			t.Fatalf("preview on wrong row: %v", w)
		}
	}
	if out := get(""); out["workspaces"].([]any)[0].(map[string]any)["activity_buckets"] != nil {
		t.Fatal("buckets returned without include")
	}
	rec := httptest.NewRecorder()
	a.handleWorkspaces(rec, httptest.NewRequest(http.MethodGet, "/api/workspaces?host=moon", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad host accepted: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	a.handleActivity(rec, httptest.NewRequest(http.MethodGet, "/api/activity?limit=3", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"activity"`) {
		t.Fatalf("activity route: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPreviewChangesEmitAnEvent(t *testing.T) {
	a, ctx, c, w, _ := previewFixture(t)
	events := &recordedEvents{}
	ctx.SetEmitter(events)
	if _, err := a.toolPreviewStart(c, ctx, map[string]any{"workspace_id": w.ID, "argv": []string{"bun", "run", "dev"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.refreshPreview(ctx, w); err != nil { // starting -> live
		t.Fatal(err)
	}
	if _, err := a.refreshPreview(ctx, w); err != nil { // unchanged: no event
		t.Fatal(err)
	}
	if _, err := a.toolPreviewStop(c, ctx, map[string]any{"workspace_id": w.ID, "keep_workspace_running": true}); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, topic := range events.topics {
		if topic == "workspace.preview.changed" {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("preview events = %d (%v)", count, events.topics)
	}
	activity, _ := listActivity(ctx.AppDB(), "p", w.ID, 20)
	live := false
	for _, row := range activity {
		live = live || row.EventType == "preview.live"
	}
	if !live {
		t.Fatal("preview.live not recorded")
	}
}

func TestManifestDeclaresWidgets(t *testing.T) {
	manifest := (&App{}).Manifest()
	if len(manifest.Provides.UIComponents) != 2 {
		t.Fatalf("components: %+v", manifest.Provides.UIComponents)
	}
	overview, agent := manifest.Provides.UIComponents[0], manifest.Provides.UIComponents[1]
	if overview.Name != "workspaces-overview" || overview.Entry != "/ui/WorkspacesOverviewWidget.mjs" || overview.Slots[0] != "dashboard.home" || overview.Visibility != "project" || overview.DefaultSize != "half" {
		t.Fatalf("overview: %+v", overview)
	}
	if agent.Name != "agent-workspace" || agent.Visibility != "attached" || len(agent.Slots) != 2 || agent.SettingsSchema["type"] != "object" {
		t.Fatalf("agent: %+v", agent)
	}
	published := map[string]bool{}
	for _, event := range manifest.Provides.Publishes {
		published[event.Name] = true
	}
	for _, topic := range append(overview.RefreshTopics, agent.RefreshTopics...) {
		if !published[topic] {
			t.Fatalf("refresh topic %s is not published", topic)
		}
	}
}
