package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func newAgentWorkspace(t *testing.T, platform *platformStub, args map[string]any) (*App, *sdk.AppCtx, *Workspace) {
	t.Helper()
	ctx, _ := newTestContext(t, platform)
	app := &App{}
	if args == nil {
		args = map[string]any{}
	}
	if args["name"] == nil {
		args["name"] = "Agent tools"
	}
	created, err := app.toolCreate(agentContext("p", 4, "thread", "create-"+t.Name()), ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return app, ctx, created.(map[string]any)["workspace"].(*Workspace)
}

func TestCommandOutputCursorAndWait(t *testing.T) {
	first, cursor, gap, err := incrementalCommandOutput("cmd_a", "hello\n", "", false)
	if err != nil || first != "hello\n" || gap {
		t.Fatalf("first output=%q gap=%v err=%v", first, gap, err)
	}
	next, cursor, gap, err := incrementalCommandOutput("cmd_a", "hello\nworld", cursor, false)
	if err != nil || next != "world" || gap {
		t.Fatalf("incremental output=%q gap=%v err=%v", next, gap, err)
	}
	same, _, gap, err := incrementalCommandOutput("cmd_a", "hello\nworld", cursor, false)
	if err != nil || same != "" || gap {
		t.Fatalf("unchanged output=%q gap=%v err=%v", same, gap, err)
	}
	next, _, gap, err = incrementalCommandOutput("cmd_a", "new window", cursor, false)
	if err != nil || next != "new window" || !gap {
		t.Fatalf("lost window output=%q gap=%v err=%v", next, gap, err)
	}
	if _, _, _, err := incrementalCommandOutput("cmd_b", "output", cursor, false); err == nil {
		t.Fatal("cursor must be bound to its command")
	}

	platform := &platformStub{executionLogs: "done"}
	app, ctx, w := newAgentWorkspace(t, platform, nil)
	started, err := app.toolCommandStart(agentContext("p", 4, "thread", "start"), ctx, map[string]any{"workspace_id": w.ID, "argv": []any{"go", "version"}})
	if err != nil {
		t.Fatal(err)
	}
	command := started.(map[string]any)["command"].(*Command)
	zero := 0
	platform.executionStatus, platform.exitCode = "succeeded", &zero
	result, err := app.toolCommandWait(agentContext("p", 4, "thread", "wait"), ctx, map[string]any{"workspace_id": w.ID, "command_id": command.ID, "wait_ms": 1000})
	if err != nil {
		t.Fatal(err)
	}
	response := result.(map[string]any)
	if response["output"] != "done" || response["command"].(*Command).Status != "succeeded" || response["cursor"] == "" {
		t.Fatalf("wait result=%+v", response)
	}
	again, err := app.toolCommandOutput(agentContext("p", 4, "thread", "again"), ctx, map[string]any{"workspace_id": w.ID, "command_id": command.ID, "cursor": response["cursor"]})
	if err != nil || again.(map[string]any)["output"] != "" {
		t.Fatalf("cursor should suppress already-seen output: %+v err=%v", again, err)
	}
	platform.executionLogs = strings.Repeat("x", agentOutputWindowBytes+1024)
	output, err := app.toolCommandOutput(agentContext("p", 4, "thread", "output"), ctx, map[string]any{"workspace_id": w.ID, "command_id": command.ID})
	if err != nil {
		t.Fatal(err)
	}
	window := output.(map[string]any)
	if len(window["output"].(string)) != agentOutputWindowBytes || window["gap"] != true || window["output_truncated"] != true {
		t.Fatalf("bounded output was not signaled: length=%d gap=%v truncated=%v", len(window["output"].(string)), window["gap"], window["output_truncated"])
	}
	if _, err := app.toolCommandWait(agentContext("p", 4, "thread", "badwait"), ctx, map[string]any{"workspace_id": w.ID, "command_id": command.ID, "wait_ms": 60000}); err == nil {
		t.Fatal("wait_ms above 20000 must be rejected")
	}
	if _, err := app.toolCommandWait(agentContext("p", 5, "other", "wait"), ctx, map[string]any{"workspace_id": w.ID, "command_id": command.ID}); err == nil {
		t.Fatal("another agent must not read this command")
	}
}

func TestFileReadReturnsLineWindowAndHash(t *testing.T) {
	platform := &platformStub{}
	app, ctx, w := newAgentWorkspace(t, platform, nil)
	platform.volumeArchive = sourceArchiveForTest(t, map[string]string{"src/main.go": "one\ntwo\nthree\nfour\n"})
	result, err := app.toolFileRead(agentContext("p", 4, "thread", "read"), ctx, map[string]any{
		"workspace_id": w.ID, "path": "/workspace/src/main.go", "offset": 2, "limit": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := result.(map[string]any)
	if r["content"] != "two\nthree\n" || r["start_line"] != 2 || r["end_line"] != 3 || r["total_lines"] != 4 || r["truncated"] != true {
		t.Fatalf("read window=%+v", r)
	}
	if r["sha256"] != sha256Hex([]byte("one\ntwo\nthree\nfour\n")) {
		t.Fatalf("sha256=%v", r["sha256"])
	}
	last := platform.calls[len(platform.calls)-1]
	if last.Tool != "containers_volume_export" || last.Input["path"] != "src/main.go" || last.Input["volume"] != "workspace" {
		t.Fatalf("export call=%+v", last)
	}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "src/../../x", ""} {
		if _, err := app.toolFileRead(agentContext("p", 4, "thread", "bad"), ctx, map[string]any{"workspace_id": w.ID, "path": bad}); err == nil {
			t.Fatalf("path %q must be rejected", bad)
		}
	}
	platform.volumeArchive = sourceArchiveForTest(t, map[string]string{"bin/tool": "ELF\x00\x01"})
	if _, err := app.toolFileRead(agentContext("p", 4, "thread", "bin"), ctx, map[string]any{"workspace_id": w.ID, "path": "bin/tool"}); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary read err=%v", err)
	}
	if _, err := app.toolFileRead(agentContext("p", 5, "other", "read"), ctx, map[string]any{"workspace_id": w.ID, "path": "src/main.go"}); err == nil {
		t.Fatal("another agent must not read this workspace")
	}
}

func TestLineWindowEdges(t *testing.T) {
	if content, start, end, total := lineWindow("", 1, 10); content != "" || start != 0 || end != 0 || total != 0 {
		t.Fatalf("empty file window: %q %d %d %d", content, start, end, total)
	}
	if content, _, end, total := lineWindow("a\nb", 1, 10); content != "a\nb" || end != 2 || total != 2 {
		t.Fatalf("no trailing newline: %q %d %d", content, end, total)
	}
	if content, start, end, total := lineWindow("a\n", 5, 10); content != "" || start != 5 || end != 4 || total != 1 {
		t.Fatalf("offset past end: %q %d %d %d", content, start, end, total)
	}
}

func TestFileWriteCreatesAndGuardsOverwrites(t *testing.T) {
	zero := 0
	platform := &platformStub{exportErr: errors.New("exit status 1"), executionStatus: "succeeded", exitCode: &zero}
	app, ctx, w := newAgentWorkspace(t, platform, nil)
	result, err := app.toolFileWrite(agentContext("p", 4, "thread", "create"), ctx, map[string]any{
		"workspace_id": w.ID, "path": "cmd/app/main.go", "content": "package main\n", "create_only": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := result.(map[string]any); r["created"] != true || r["changed"] != true {
		t.Fatalf("create result=%+v", r)
	}
	last := platform.calls[len(platform.calls)-1]
	if last.Tool != "containers_volume_import" || last.Input["path"] != "cmd/app" {
		t.Fatalf("import must target the parent directory: %+v", last.Input)
	}
	if countCalls(platform.calls, "containers_exec_start") != 0 {
		t.Fatal("writing a file must not run processes in the workspace")
	}
	written, header, err := unpackTextFile(last.Input["archive_base64"].(string), "main.go")
	if err != nil || string(written) != "package main\n" || header.Mode != 0o644 {
		t.Fatalf("written=%q header=%+v err=%v", written, header, err)
	}
	if _, err := app.toolFileWrite(agentContext("p", 4, "thread", "expect-missing"), ctx, map[string]any{
		"workspace_id": w.ID, "path": "missing.txt", "content": "x", "expected_sha256": strings.Repeat("a", 64),
	}); err == nil {
		t.Fatal("expected_sha256 on a missing file must fail")
	}

	platform.exportErr = nil
	platform.volumeArchive = sourceArchiveForTest(t, map[string]string{"README.md": "old\n"})
	imports := countCalls(platform.calls, "containers_volume_import")
	if _, err := app.toolFileWrite(agentContext("p", 4, "thread", "create-only"), ctx, map[string]any{
		"workspace_id": w.ID, "path": "README.md", "content": "new\n", "create_only": true,
	}); err == nil {
		t.Fatal("create_only must refuse an existing file")
	}
	if _, err := app.toolFileWrite(agentContext("p", 4, "thread", "stale"), ctx, map[string]any{
		"workspace_id": w.ID, "path": "README.md", "content": "new\n", "expected_sha256": strings.Repeat("0", 64),
	}); err == nil {
		t.Fatal("stale expected_sha256 must fail")
	}
	if countCalls(platform.calls, "containers_volume_import") != imports {
		t.Fatal("refused writes must not import anything")
	}
	unchanged, err := app.toolFileWrite(agentContext("p", 4, "thread", "same"), ctx, map[string]any{
		"workspace_id": w.ID, "path": "README.md", "content": "old\n",
	})
	if err != nil || unchanged.(map[string]any)["changed"] != false || countCalls(platform.calls, "containers_volume_import") != imports {
		t.Fatalf("identical content should be a no-op: %+v err=%v", unchanged, err)
	}
	replaced, err := app.toolFileWrite(agentContext("p", 4, "thread", "replace"), ctx, map[string]any{
		"workspace_id": w.ID, "path": "README.md", "content": "new\n", "expected_sha256": sha256Hex([]byte("old\n")),
	})
	if err != nil || replaced.(map[string]any)["created"] != false || replaced.(map[string]any)["previous_sha256"] != sha256Hex([]byte("old\n")) {
		t.Fatalf("replace result=%+v err=%v", replaced, err)
	}
	platform.volumeArchive = sourceArchiveForTest(t, map[string]string{"blob.bin": "\x00\x01"})
	if _, err := app.toolFileWrite(agentContext("p", 4, "thread", "binary"), ctx, map[string]any{
		"workspace_id": w.ID, "path": "blob.bin", "content": "text",
	}); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("binary overwrite err=%v", err)
	}
}

func TestApplyEditsOnWorkspaceVolume(t *testing.T) {
	platform := &platformStub{}
	app, ctx, w := newAgentWorkspace(t, platform, nil)
	before := "hello world\n"
	platform.volumeArchive = sourceArchiveForTest(t, map[string]string{"src/main.go": before})
	result, err := app.toolApplyEdits(agentContext("p", 4, "thread", "edit"), ctx, map[string]any{
		"workspace_id": w.ID, "path": "src/main.go", "expected_sha256": sha256Hex([]byte(before)),
		"edits": []any{
			map[string]any{"old_string": "world", "new_string": "agent"},
			map[string]any{"old_string": "hello agent", "new_string": "hi agent"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["changed"] != true {
		t.Fatalf("edit result=%+v", result)
	}
	last := platform.calls[len(platform.calls)-1]
	if last.Tool != "containers_volume_import" || last.Input["path"] != "src" {
		t.Fatalf("expected edited archive import into src, got %+v", last)
	}
	updated, _, err := unpackTextFile(last.Input["archive_base64"].(string), "main.go")
	if err != nil || string(updated) != "hi agent\n" {
		t.Fatalf("edited file=%q err=%v", updated, err)
	}
	imports := countCalls(platform.calls, "containers_volume_import")
	if _, err := app.toolApplyEdits(agentContext("p", 4, "thread", "stale"), ctx, map[string]any{
		"workspace_id": w.ID, "path": "src/main.go", "expected_sha256": strings.Repeat("0", 64),
		"edits": []any{map[string]any{"old_string": "world", "new_string": "agent"}},
	}); err == nil || countCalls(platform.calls, "containers_volume_import") != imports {
		t.Fatal("stale precondition must fail without writing")
	}
	if _, err := app.toolApplyEdits(agentContext("p", 4, "thread", "missing"), ctx, map[string]any{
		"workspace_id": w.ID, "path": "src/main.go",
		"edits": []any{map[string]any{"old_string": "absent", "new_string": "x"}},
	}); err == nil || countCalls(platform.calls, "containers_volume_import") != imports {
		t.Fatal("unmatched old_string must fail without writing")
	}
	if _, err := applyFileEdits("aaa", []fileEdit{{Old: "a", New: "b"}}); err == nil {
		t.Fatal("ambiguous replacement must fail")
	}
	if got, err := applyFileEdits("aaa", []fileEdit{{Old: "a", New: "b", ReplaceAll: true}}); err != nil || got != "bbb" {
		t.Fatalf("replace_all got=%q err=%v", got, err)
	}
}

func TestSourceImportForAgentsRequiresIdleWorkspace(t *testing.T) {
	platform := &platformStub{}
	app, ctx, w := newAgentWorkspace(t, platform, nil)
	if _, err := app.toolSourceImport(agentContext("p", 4, "thread", "import"), ctx, map[string]any{
		"workspace_id": w.ID, "archive_base64": "H4sI", "path": "vendor",
	}); err != nil {
		t.Fatal(err)
	}
	last := platform.calls[len(platform.calls)-1]
	if last.Tool != "containers_volume_import" || last.Input["path"] != "vendor" {
		t.Fatalf("import call=%+v", last)
	}
	if _, err := app.toolSourceImport(agentContext("p", 4, "thread", "traversal"), ctx, map[string]any{
		"workspace_id": w.ID, "archive_base64": "H4sI", "path": "../outside",
	}); err == nil {
		t.Fatal("path traversal should be rejected")
	}
	if _, err := app.toolCommandStart(agentContext("p", 4, "thread", "run"), ctx, map[string]any{"workspace_id": w.ID, "argv": []any{"sleep", "60"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolSourceImport(agentContext("p", 4, "thread", "busy"), ctx, map[string]any{
		"workspace_id": w.ID, "archive_base64": "H4sI",
	}); err == nil {
		t.Fatal("import should reject while a command is active")
	}
}

func TestFileListParsesHelperOutput(t *testing.T) {
	zero := 0
	platform := &platformStub{executionStatus: "succeeded", exitCode: &zero,
		executionLogs: "d\t0\tsrc\r\nf\t12\tsrc/main.go\nl\t0\tlink\nnoise line\n"}
	app, ctx, w := newAgentWorkspace(t, platform, nil)
	result, err := app.toolFileList(agentContext("p", 4, "thread", "list"), ctx, map[string]any{
		"workspace_id": w.ID, "max_depth": 2, "name_pattern": "*.go", "limit": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := result.(map[string]any)
	entries := r["entries"].([]listedEntry)
	if len(entries) != 2 || entries[0] != (listedEntry{Path: "src", Type: "dir"}) || entries[1] != (listedEntry{Path: "src/main.go", Type: "file", Size: 12}) || r["truncated"] != true {
		t.Fatalf("entries=%+v truncated=%v", entries, r["truncated"])
	}
	var exec appCall
	for _, call := range platform.calls {
		if call.Tool == "containers_exec_start" {
			exec = call
		}
	}
	if _, hasSession := exec.Input["session_key"]; hasSession {
		t.Fatal("listing must not run inside the agent's persistent shell session")
	}
	argv := exec.Input["argv"].([]string)
	if argv[4] != "/workspace" || argv[5] != "2" || argv[6] != "*.go" || argv[7] != "2" || strings.Join(argv[8:], ",") != ".git,node_modules,.home" {
		t.Fatalf("argv tail=%q", argv[4:])
	}
	if _, err := app.toolFileList(agentContext("p", 4, "thread", "badpattern"), ctx, map[string]any{"workspace_id": w.ID, "name_pattern": "a/b"}); err == nil {
		t.Fatal("name_pattern with a slash must be rejected")
	}
}

// TestListScriptAgainstRealShell runs the generated find script with /bin/sh
// on a temporary tree, covering pruning, patterns, quoting and the limit.
func TestListScriptAgainstRealShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	root := t.TempDir()
	for _, file := range []string{"a.go", "dir with space/b.go", "dir with space/c.txt", "node_modules/x/y.go", ".git/HEAD", "it's/q.go"} {
		full := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("12345"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(depth int, pattern string, excludes []string, limit int) []listedEntry {
		argv := listArgv(root, depth, pattern, excludes, limit)
		out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("script failed: %v\n%s", err, out)
		}
		entries, _ := parseListOutput(string(out), limit)
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		return entries
	}
	paths := func(entries []listedEntry) string {
		out := make([]string, len(entries))
		for i, e := range entries {
			out[i] = e.Type + ":" + e.Path
		}
		return strings.Join(out, ",")
	}
	if got := paths(run(5, "*.go", defaultListExcludes, 100)); got != "file:a.go,file:dir with space/b.go,file:it's/q.go" {
		t.Fatalf("pattern+prune got %s", got)
	}
	if got := paths(run(1, "", defaultListExcludes, 100)); got != "file:a.go,dir:dir with space,dir:it's" {
		t.Fatalf("depth 1 got %s", got)
	}
	if got := paths(run(1, "", nil, 100)); !strings.Contains(got, "dir:.git") || !strings.Contains(got, "dir:node_modules") {
		t.Fatalf("no excludes got %s", got)
	}
	if entries := run(5, "", nil, 2); len(entries) != 2 {
		t.Fatalf("limit not applied: %+v", entries)
	}
	if sizes := run(1, "a.go", nil, 10); len(sizes) != 1 || sizes[0].Size != 5 {
		t.Fatalf("size got %+v", sizes)
	}
	argv := listArgv(filepath.Join(root, "a.go"), 1, "", nil, 10)
	if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err == nil || !strings.Contains(string(out), "not a directory") {
		t.Fatalf("file root should fail: %v %s", err, out)
	}
}

func TestAgentsCanRunAndStopPreviewsWithoutStoppingWorkspace(t *testing.T) {
	platform := &platformStub{}
	app, ctx, w := newAgentWorkspace(t, platform, map[string]any{"preview_port": 3000})
	run := platform.calls[0]
	if run.Tool != "containers_run" || run.Input["use_local"] != true {
		t.Fatalf("agent preview workspace must publish a local port: %+v", run.Input)
	}
	for _, tool := range app.MCPTools() {
		if strings.HasPrefix(tool.Name, "workspace_preview_") && tool.Exposure != "" {
			t.Fatalf("%s must be exposed to agents, got %q", tool.Name, tool.Exposure)
		}
	}
	if err := savePreview(ctx.AppDB(), &Preview{WorkspaceID: w.ID, ExecutionID: "exe_test", ContainerPort: 3000, HostPort: 41000, Status: "live", StartedAt: nowUTC()}); err != nil {
		t.Fatal(err)
	}
	stops := countCalls(platform.calls, "containers_stop")
	result, err := app.toolPreviewStop(agentContext("p", 4, "thread", "stop"), ctx, map[string]any{"workspace_id": w.ID})
	if err != nil {
		t.Fatal(err)
	}
	if countCalls(platform.calls, "containers_stop") != stops || countCalls(platform.calls, "containers_exec_cancel") != 1 {
		t.Fatalf("agent stop must cancel only the preview process: %+v", platform.calls)
	}
	if p := result.(map[string]any)["preview"].(*Preview); p.Status != "stopped" {
		t.Fatalf("preview=%+v", p)
	}
	current, _ := requireWorkspace(ctx.AppDB(), w.ProjectID, w.ID)
	if current.LifecycleStatus != statusRunning {
		t.Fatalf("workspace should keep running, got %s", current.LifecycleStatus)
	}
	if _, err := app.toolPreviewStop(agentContext("p", 4, "thread", "stop-all"), ctx, map[string]any{"workspace_id": w.ID, "keep_workspace_running": false}); err != nil {
		t.Fatal(err)
	}
	if countCalls(platform.calls, "containers_stop") != stops+1 {
		t.Fatal("keep_workspace_running=false must stop the container")
	}
}
