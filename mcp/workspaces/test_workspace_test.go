package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type testEntry struct {
	name, body, link string
	mode             int64
	dir              bool
}

func testArchive(t *testing.T, entries ...testEntry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode}
		switch {
		case e.dir:
			h.Typeflag = tar.TypeDir
		case e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func mustInspect(t *testing.T, archive string) testSourceInfo {
	t.Helper()
	info, err := inspectTestSource(archive)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestSourceDigestIsCanonical(t *testing.T) {
	base := mustInspect(t, testArchive(t,
		testEntry{name: "package.json", body: "{}"},
		testEntry{name: "bun.lock", body: "lock-1"},
		testEntry{name: "src/a.ts", body: "a"},
	))
	if !strings.HasPrefix(base.Digest, "sha256:") || base.Files != 3 || len(base.CacheKey) != 32 {
		t.Fatalf("unexpected info %+v", base)
	}
	// Order, ./ prefixes, folder entries and node_modules do not matter.
	same := mustInspect(t, testArchive(t,
		testEntry{name: "./src/", dir: true},
		testEntry{name: "./src/a.ts", body: "a"},
		testEntry{name: "node_modules/x/index.js", body: "ignored"},
		testEntry{name: "./bun.lock", body: "lock-1"},
		testEntry{name: "package.json", body: "{}"},
	))
	if same.Digest != base.Digest || same.CacheKey != base.CacheKey {
		t.Fatalf("digest must not depend on order or dependency folders: %+v vs %+v", same, base)
	}
	content := mustInspect(t, testArchive(t,
		testEntry{name: "package.json", body: "{}"},
		testEntry{name: "bun.lock", body: "lock-1"},
		testEntry{name: "src/a.ts", body: "b"},
	))
	if content.Digest == base.Digest || content.CacheKey != base.CacheKey {
		t.Fatalf("source change must change digest but not the cache key: %+v", content)
	}
	exec := mustInspect(t, testArchive(t,
		testEntry{name: "package.json", body: "{}"},
		testEntry{name: "bun.lock", body: "lock-1"},
		testEntry{name: "src/a.ts", body: "a", mode: 0o755},
	))
	if exec.Digest == base.Digest {
		t.Fatal("executable bit must change the digest")
	}
	lock := mustInspect(t, testArchive(t,
		testEntry{name: "package.json", body: "{}"},
		testEntry{name: "bun.lock", body: "lock-2"},
		testEntry{name: "src/a.ts", body: "a"},
	))
	if lock.CacheKey == base.CacheKey {
		t.Fatal("lockfile change must change the cache key")
	}
	noLock := mustInspect(t, testArchive(t, testEntry{name: "a.ts", body: "a"}, testEntry{name: "sub/bun.lock", body: "nested"}))
	if noLock.CacheKey != "" {
		t.Fatalf("only root lockfiles key the cache, got %q", noLock.CacheKey)
	}
}

func TestSourceDigestRejectsUnsafeArchives(t *testing.T) {
	for name, archive := range map[string]string{
		"git":      testArchive(t, testEntry{name: ".git/config", body: "x"}),
		"abs link": testArchive(t, testEntry{name: "a", link: "/etc/passwd"}),
		"escape":   testArchive(t, testEntry{name: "a/b", link: "../../x"}),
		"empty":    testArchive(t, testEntry{name: "only/", dir: true}),
		"parent":   testArchive(t, testEntry{name: "../x", body: "x"}),
	} {
		if _, err := inspectTestSource(archive); err == nil {
			t.Errorf("%s: expected refusal", name)
		}
	}
}

func appCaller(name string, install int64, toolCall string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "p", AppName: name, AppInstallID: install, ToolCallID: toolCall})
}

func newTestWorkspace(t *testing.T, platform *platformStub) (*App, *sdk.AppCtx, *Workspace) {
	t.Helper()
	ctx, _ := newTestContext(t, platform)
	app := &App{}
	out, err := app.toolTestCreate(appCaller("ci", 50, "create-"+t.Name()), ctx, map[string]any{
		"name": "Test run", "source_archive_base64": testArchive(t,
			testEntry{name: "package.json", body: "{}"}, testEntry{name: "bun.lock", body: "lock"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, ctx, out.(map[string]any)["workspace"].(*Workspace)
}

func callsTo(calls []appCall, tool string) []appCall {
	var out []appCall
	for _, c := range calls {
		if c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

func TestTestWorkspaceCreatesHardenedContainerWithOwnedFolders(t *testing.T) {
	platform := &platformStub{}
	_, _, w := newTestWorkspace(t, platform)
	if w.Kind != kindTest || w.LifecycleStatus != statusRunning || w.NetworkMode != networkNone {
		t.Fatalf("unexpected workspace %+v", w)
	}
	if w.DeleteAt != w.ExpiresAt {
		t.Fatal("test workspaces must have no retention after expiry")
	}
	if ttl := parseTime(w.ExpiresAt).Sub(parseTime(w.CreatedAt)); ttl != 30*time.Minute {
		t.Fatalf("default TTL = %s, want 30m", ttl)
	}
	if !strings.HasPrefix(w.SourceDigest, "sha256:") || w.CacheVolumeID != "vol_cache" || w.CacheKey == "" {
		t.Fatalf("source digest / cache not recorded: %+v", w)
	}
	run := callsTo(platform.calls, "containers_run")[0].Input
	if run["hardened"] != true || run["user"] != testBuilderUser || run["network_mode"] != networkNone ||
		run["working_directory"] != "/workspace" || run["restart_policy"] != "no" {
		t.Fatalf("container not locked down: %+v", run)
	}
	if !strings.Contains(run["image"].(string), "@sha256:") {
		t.Fatalf("image must be digest pinned: %v", run["image"])
	}
	volumes := run["volumes"].([]map[string]any)
	if len(volumes) != 2 || volumes[1]["volume_id"] != "vol_cache" || volumes[1]["mount_path"] != "/cache" {
		t.Fatalf("cache volume not mounted: %+v", volumes)
	}
	env := run["env"].(map[string]string)
	if env["TESTS_OUT"] != testResultsDir || env["HOME"] != "/tmp/home" {
		t.Fatalf("env %+v", env)
	}
	created := callsTo(platform.calls, "containers_volume_create")[0].Input
	if labels := created["labels"].(map[string]string); labels["kind"] != "test-cache" || labels["cache_key"] != w.CacheKey {
		t.Fatalf("cache labels %+v", created)
	}
	imports := callsTo(platform.calls, "containers_volume_import")
	want := [][3]string{{"workspace", "src", testBuilderUser}, {"workspace", "out", testRunnerUser}, {"cache", "bun", testBuilderUser}}
	if len(imports) != len(want) {
		t.Fatalf("imports %+v", imports)
	}
	for i, call := range imports {
		got := [3]string{call.Input["volume"].(string), call.Input["path"].(string), call.Input["owner"].(string)}
		if got != want[i] {
			t.Fatalf("import %d = %v, want %v", i, got, want[i])
		}
	}
}

func TestTestWorkspaceCreateGuards(t *testing.T) {
	platform := &platformStub{}
	ctx, _ := newTestContext(t, platform)
	app := &App{}
	archive := testArchive(t, testEntry{name: "a.ts", body: "a"})
	if _, err := app.toolTestCreate(agentContext("p", 4, "t", "x"), ctx, map[string]any{"name": "x", "source_archive_base64": archive}); err == nil {
		t.Fatal("agents must not create test workspaces")
	}
	if _, err := app.toolTestCreate(appCaller("ci", 50, "ttl"), ctx, map[string]any{"name": "x", "source_archive_base64": archive, "ttl_minutes": 121}); err == nil {
		t.Fatal("ttl above 120 must be refused")
	}
	if _, err := app.toolTestCreate(appCaller("ci", 50, "noarchive"), ctx, map[string]any{"name": "x"}); err == nil {
		t.Fatal("source archive is required")
	}
	ctx.Config()["test_workspace_apps"] = "deploy, tests"
	if _, err := app.toolTestCreate(appCaller("ci", 50, "allow"), ctx, map[string]any{"name": "x", "source_archive_base64": archive}); err == nil || !strings.Contains(err.Error(), "test_workspace_apps") {
		t.Fatalf("app outside the allowlist must be refused, got %v", err)
	}
	out, err := app.toolTestCreate(appCaller("tests", 51, "allowed"), ctx, map[string]any{"name": "y", "source_archive_base64": archive, "use_cache": true})
	if err != nil {
		t.Fatal(err)
	}
	if w := out.(map[string]any)["workspace"].(*Workspace); w.CacheVolumeID != "" {
		t.Fatal("an archive without a lockfile gets no shared cache")
	}
}

func TestTestWorkspaceCommandsUseUsersAndNetwork(t *testing.T) {
	platform := &platformStub{}
	app, ctx, w := newTestWorkspace(t, platform)
	caller := appCaller("ci", 50, "")
	if _, err := app.toolCommandStart(caller, ctx, map[string]any{"workspace_id": w.ID, "shell_command": "bun test"}); err == nil {
		t.Fatal("test workspaces must refuse shell commands")
	}
	if _, err := app.toolCommandStart(caller, ctx, map[string]any{"workspace_id": w.ID, "argv": []any{"bun", "test"}, "user": "root"}); err == nil {
		t.Fatal("unknown user must be refused")
	}
	out, err := app.toolCommandStart(caller, ctx, map[string]any{"workspace_id": w.ID, "argv": []any{"bun", "install"}, "user": "builder", "network": "registry_only"})
	if err != nil {
		t.Fatal(err)
	}
	c := out.(map[string]any)["command"].(*Command)
	if c.User != "builder" || c.Network != networkRegistryOnly || c.Image != w.Image || c.WorkingDirectory != testSourceDir {
		t.Fatalf("command %+v", c)
	}
	nets := callsTo(platform.calls, "containers_network_set")
	if len(nets) != 1 || nets[0].Input["mode"] != networkRegistryOnly {
		t.Fatalf("network switch %+v", nets)
	}
	if allow := nets[0].Input["egress_allow"].([]string); len(allow) != 1 || allow[0] != "registry.npmjs.org" {
		t.Fatalf("egress allow %v", allow)
	}
	execs := callsTo(platform.calls, "containers_exec_start")
	last := execs[len(execs)-1].Input
	if last["user"] != testBuilderUser || last["umask"] != "022" || last["session_key"] != nil {
		t.Fatalf("exec input %+v", last)
	}
	// Second command: runner without network switches back to none.
	platform.executionStatus = "succeeded"
	zero := 0
	platform.exitCode = &zero
	if _, err := app.toolCommandGet(caller, ctx, map[string]any{"workspace_id": w.ID, "command_id": c.ID}); err != nil {
		t.Fatal(err)
	}
	platform.executionStatus = "running"
	out, err = app.toolCommandStart(caller, ctx, map[string]any{"workspace_id": w.ID, "argv": []any{"bun", "test"}})
	if err != nil {
		t.Fatal(err)
	}
	c = out.(map[string]any)["command"].(*Command)
	nets = callsTo(platform.calls, "containers_network_set")
	execs = callsTo(platform.calls, "containers_exec_start")
	if c.User != "runner" || c.Network != networkNone || len(nets) != 2 || nets[1].Input["mode"] != networkNone ||
		execs[len(execs)-1].Input["user"] != testRunnerUser {
		t.Fatalf("runner command %+v nets %+v", c, nets)
	}
}

func TestDevWorkspaceCommandsUseUmaskAndRefuseTestOptions(t *testing.T) {
	platform := &platformStub{}
	app, ctx, w := newAgentWorkspace(t, platform, nil)
	caller := agentContext("p", 4, "thread", "")
	if _, err := app.toolCommandStart(caller, ctx, map[string]any{"workspace_id": w.ID, "shell_command": "ls", "user": "runner"}); err == nil {
		t.Fatal("dev workspaces must refuse user")
	}
	if _, err := app.toolCommandStart(caller, ctx, map[string]any{"workspace_id": w.ID, "shell_command": "ls"}); err != nil {
		t.Fatal(err)
	}
	execs := callsTo(platform.calls, "containers_exec_start")
	last := execs[len(execs)-1].Input
	if last["umask"] != "022" || last["session_key"] != "workspace" || last["user"] != nil {
		t.Fatalf("dev exec input %+v", last)
	}
	if w.Kind != kindDev {
		t.Fatalf("dev workspace kind = %q", w.Kind)
	}
}

func TestTestWorkspaceBlocksAgentsAndDevTools(t *testing.T) {
	platform := &platformStub{}
	app, ctx, w := newTestWorkspace(t, platform)
	if _, err := app.toolGet(agentContext("p", 4, "t", ""), ctx, map[string]any{"workspace_id": w.ID}); err == nil {
		t.Fatal("agents must not see test workspaces")
	}
	if _, err := app.toolGet(appCaller("ci", 99, ""), ctx, map[string]any{"workspace_id": w.ID}); err == nil {
		t.Fatal("other apps must not see test workspaces")
	}
	caller := appCaller("ci", 50, "")
	for name, call := range map[string]func() (any, error){
		"file read":     func() (any, error) { return app.toolFileRead(caller, ctx, map[string]any{"workspace_id": w.ID, "path": "src/a"}) },
		"source import": func() (any, error) { return app.toolSourceImport(caller, ctx, map[string]any{"workspace_id": w.ID, "archive_base64": "x"}) },
		"preview":       func() (any, error) { return app.toolPreviewStart(caller, ctx, map[string]any{"workspace_id": w.ID, "argv": []any{"x"}}) },
		"export":        func() (any, error) { return app.toolSourceExport(caller, ctx, map[string]any{"workspace_id": w.ID}) },
		"sync": func() (any, error) {
			return app.toolSourceSync(caller, ctx, map[string]any{"workspace_id": w.ID, "source_archive_base64": "x", "source_digest": "d"})
		},
	} {
		if _, err := call(); err == nil || !strings.Contains(err.Error(), "not available on test workspaces") {
			t.Errorf("%s: expected refusal, got %v", name, err)
		}
	}
	if _, err := app.toolExtend(caller, ctx, map[string]any{"workspace_id": w.ID, "ttl_minutes": 121}); err == nil {
		t.Fatal("extend must respect the test TTL cap")
	}
	digest, err := app.toolSourceDigest(caller, ctx, map[string]any{"workspace_id": w.ID})
	if err != nil {
		t.Fatal(err)
	}
	if d := digest.(map[string]any); d["source_digest"] != w.SourceDigest || d["method"] != sourceDigestMethod {
		t.Fatalf("digest %+v", d)
	}
}

func TestResultsExportDestroysByDefault(t *testing.T) {
	platform := &platformStub{volumeArchive: emptyArchiveBase64()}
	app, ctx, w := newTestWorkspace(t, platform)
	caller := appCaller("ci", 50, "")
	if _, err := app.toolCommandStart(caller, ctx, map[string]any{"workspace_id": w.ID, "argv": []any{"bun", "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolResultsExport(caller, ctx, map[string]any{"workspace_id": w.ID}); err == nil {
		t.Fatal("export must wait for the active command")
	}
	platform.executionStatus = "succeeded"
	if _, err := app.toolGet(caller, ctx, map[string]any{"workspace_id": w.ID}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolResultsExport(caller, ctx, map[string]any{"workspace_id": w.ID, "destroy": false})
	if err != nil {
		t.Fatal(err)
	}
	if r := out.(map[string]any); r["destroyed"] != false || r["archive_base64"] == "" || r["source_digest"] != w.SourceDigest {
		t.Fatalf("export %+v", r)
	}
	exports := callsTo(platform.calls, "containers_volume_export")
	if exports[len(exports)-1].Input["path"] != testResultsPath || exports[len(exports)-1].Input["volume"] != "workspace" {
		t.Fatalf("export input %+v", exports[len(exports)-1].Input)
	}
	out, err = app.toolResultsExport(caller, ctx, map[string]any{"workspace_id": w.ID})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["destroyed"] != true || len(callsTo(platform.calls, "containers_destroy")) != 1 {
		t.Fatalf("export must destroy by default: %+v", out)
	}
	destroy := callsTo(platform.calls, "containers_destroy")[0].Input
	if destroy["delete_volumes"] != true {
		t.Fatal("workspace volumes must be deleted")
	}
}

func TestExpiryDestroysTestWorkspacesAndCollectsCaches(t *testing.T) {
	old := time.Now().UTC().Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	recent := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	platform := &platformStub{volumes: []map[string]any{
		{"id": "vol_old", "name": "old", "created_at": old, "last_used_at": old},
		{"id": "vol_recent", "name": "recent", "created_at": old, "last_used_at": recent},
		{"id": "vol_mounted", "name": "mounted", "created_at": old, "last_used_at": old, "mounted_by": []string{"wrk_x"}},
	}}
	app, ctx, w := newTestWorkspace(t, platform)
	if _, err := ctx.AppDB().Exec(`UPDATE workspaces SET expires_at=?, delete_at=? WHERE id=?`, old, old, w.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.expire(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := requireWorkspace(ctx.AppDB(), "p", w.ID)
	if got.LifecycleStatus != statusDestroyed {
		t.Fatalf("expired test workspace status = %s", got.LifecycleStatus)
	}
	deletes := callsTo(platform.calls, "containers_volume_delete")
	if len(deletes) != 1 || deletes[0].Input["volume_id"] != "vol_old" {
		t.Fatalf("cache GC deleted %+v", deletes)
	}
	if err := app.expire(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(callsTo(platform.calls, "containers_volume_list")) != 1 {
		t.Fatal("cache GC must run at most once an hour")
	}
}
