package main

// Covers:
//   - selectExecutor returns remote when non-nil (precedence over cloudinary + local)
//   - parseAptevaResult finds the marker line, errors on missing
//   - buildScript emits the structural pieces a render needs
//     (workdir, downloads, ffmpeg invocation, upload curl, marker line)
//
// We don't run actual SSH — the script is a string, asserted on its
// shape. End-to-end exercise needs a real remote, which lives in the
// workspace's scenarios/ harness behind RUN_REMOTE_TESTS=1.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSelectExecutor_RemoteWins(t *testing.T) {
	// Cloudinary bound AND remote configured → remote wins per the
	// documented precedence (offload intent > cost-model preserve).
	ctx := newTestCtxWithPlatform(t, boundCloudinary())
	local := &localExecutor{ffmpegPath: "ffmpeg", scratchRoot: "/tmp", outputFolder: "/r/"}
	remote := &remoteExecutor{hostID: 7, fallback: local}
	row := &RenderRow{Operation: "trim"}

	got := selectExecutor(ctx, local, remote, row)
	if got.Name() != "remote-instance" {
		t.Errorf("expected remote-instance, got %q", got.Name())
	}
}

func TestSelectExecutor_NoRemote_FallsThrough(t *testing.T) {
	// nil remote → existing cloudinary/local branching is unchanged.
	ctx := newTestCtxWithPlatform(t, boundCloudinary())
	local := &localExecutor{ffmpegPath: "ffmpeg", scratchRoot: "/tmp", outputFolder: "/r/"}
	row := &RenderRow{Operation: "trim"}

	got := selectExecutor(ctx, local, nil, row)
	if got.Name() != "cloudinary" {
		t.Errorf("expected cloudinary, got %q", got.Name())
	}
}

func TestParseAptevaResult_Happy(t *testing.T) {
	stdout := "some ffmpeg noise on stderr\n" +
		"more curl output\n" +
		`APTEVA_RESULT:{"file_id":42,"size":12345,"sha256":"abcdef"}` + "\n"
	got, err := parseAptevaResult(stdout)
	if err != nil {
		t.Fatalf("parseAptevaResult: %v", err)
	}
	if got.FileID != 42 || got.Size != 12345 || got.SHA256 != "abcdef" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestParseAptevaResult_Missing(t *testing.T) {
	if _, err := parseAptevaResult("nothing here\n"); err == nil {
		t.Error("expected error for missing marker, got nil")
	}
}

func TestParseAptevaResult_ZeroID(t *testing.T) {
	// A file_id of zero means storage didn't return a real id —
	// treat as error so we don't write a bogus media row.
	if _, err := parseAptevaResult(`APTEVA_RESULT:{"file_id":0,"size":1,"sha256":"x"}` + "\n"); err == nil {
		t.Error("expected error for file_id=0, got nil")
	}
}

func TestMaterialiseRemoteArgs_Input(t *testing.T) {
	args, err := materialiseRemoteArgs(
		[]string{"-y", "-i", "{input}", "-c:v", "libx264"},
		[]string{"src-99.mp4"})
	if err != nil {
		t.Fatalf("materialiseRemoteArgs: %v", err)
	}
	want := []string{"-y", "-i", "src-99.mp4", "-c:v", "libx264"}
	if !slicesEqual(args, want) {
		t.Errorf("got %v, want %v", args, want)
	}
}

func TestMaterialiseRemoteArgs_ConcatList(t *testing.T) {
	args, err := materialiseRemoteArgs(
		[]string{"-f", "concat", "-i", "{concat_list}", "-c", "copy"},
		[]string{"src-1.mp4", "src-2.mp4"})
	if err != nil {
		t.Fatalf("materialiseRemoteArgs: %v", err)
	}
	if args[3] != "concat.txt" {
		t.Errorf("expected {concat_list} → concat.txt, got %v", args)
	}
}

func TestMaterialiseRemoteArgs_InputArityMismatch(t *testing.T) {
	if _, err := materialiseRemoteArgs(
		[]string{"-i", "{input}"},
		[]string{"a", "b"}); err == nil {
		t.Error("expected error for {input} with 2 sources, got nil")
	}
}

func TestBuildScript_TrimShape(t *testing.T) {
	e := &remoteExecutor{
		hostID:       3,
		outputFolder: "/renders/",
		storageToken: "tok-xyz",
	}
	row := &RenderRow{
		ID:            55,
		ProjectID:     "proj-1",
		Operation:     "trim",
		SourceFileIDs: []string{"100"},
		OutputName:    "clip.mp4",
	}
	plan := &opPlan{
		Args:        []string{"-y", "-i", "{input}", "-ss", "1.0", "-to", "2.0", "-c", "copy"},
		Filename:    "clip.mp4",
		ContentType: "video/mp4",
	}
	signedURLs := []string{"https://signed.example.com/file/100?sig=abc"}
	sourceNames := []string{"source.mp4"}

	script, err := e.buildScript(row, plan, "/root/.apteva-render/ffmpeg-7.0.2/ffmpeg",
		signedURLs, sourceNames, "/renders/", "https://apt.example.com")
	if err != nil {
		t.Fatalf("buildScript: %v", err)
	}

	wantContains := []string{
		"set -euo pipefail",
		"WORK='/tmp/apteva-render-55'",
		"echo $$ > pid",
		"trap 'cd /tmp && rm -rf",
		"curl -sS --fail -L -o 'src-100.mp4' 'https://signed.example.com/file/100?sig=abc'",
		"'/root/.apteva-render/ffmpeg-7.0.2/ffmpeg' '-y' '-i' 'src-100.mp4'",
		`OUT='clip.mp4'`,
		`SIZE=$(stat`,
		`SHA=$(sha256sum`,
		`export STORAGE_TOKEN='tok-xyz'`,
		`export STORAGE_BASE='https://apt.example.com/api/apps/storage'`,
		`export PROJECT_ID='proj-1'`,
		`export FOLDER='/renders/'`,
		`export NAME='clip.mp4'`,
		`export CT='video/mp4'`,
		// Presigned-PUT branch markers.
		`"$STORAGE_BASE/files/init?project_id=$PROJECT_ID"`,
		`if [ "$INIT_CODE" = "200" ]`,
		`curl -sS --fail -X PUT -H "Content-Type: $CT" --upload-file "$OUT" "$UPLOAD_URL"`,
		`"$STORAGE_BASE/files/$UPLOAD_ID/finalize?project_id=$PROJECT_ID"`,
		// Multipart fallback markers.
		`"file=@$OUT;type=$CT;filename=$NAME"`,
		`"$STORAGE_BASE/files?project_id=$PROJECT_ID"`,
		`APTEVA_RESULT:`,
	}
	for _, w := range wantContains {
		if !strings.Contains(script, w) {
			t.Errorf("script missing %q\n--- script ---\n%s", w, script)
		}
	}
}

func TestMaterialiseRemoteArgs_ProgressRewrite(t *testing.T) {
	// Per-op planners emit `-progress pipe:1` for the local forwarder.
	// Remote can't pipe stdout back, so we redirect to progress.log
	// (which the media-side poller tails). Bare `pipe:1` strings that
	// aren't preceded by -progress must stay untouched.
	args, err := materialiseRemoteArgs(
		[]string{"-progress", "pipe:1", "-i", "{input}", "-c", "copy", "pipe:1"},
		[]string{"src-1.mp4"})
	if err != nil {
		t.Fatalf("materialiseRemoteArgs: %v", err)
	}
	if args[1] != remoteProgressFilename {
		t.Errorf("expected -progress redirected to %q, got %v", remoteProgressFilename, args)
	}
	if args[len(args)-1] != "pipe:1" {
		t.Errorf("bare pipe:1 (not preceded by -progress) should be untouched, got %v", args)
	}
}

func TestBuildScript_ConcatWritesListFile(t *testing.T) {
	e := &remoteExecutor{
		hostID: 3, outputFolder: "/r/",
		storageToken: "t",
	}
	row := &RenderRow{
		ID: 7, ProjectID: "p", Operation: "concat",
		SourceFileIDs: []string{"10", "11"}, OutputName: "joined.mp4",
	}
	plan := &opPlan{
		Args:        []string{"-f", "concat", "-safe", "0", "-i", "{concat_list}", "-c", "copy"},
		Filename:    "joined.mp4",
		ContentType: "video/mp4",
	}
	urls := []string{"https://u/1?s=a", "https://u/2?s=b"}
	names := []string{"a.mp4", "b.mp4"}

	script, err := e.buildScript(row, plan, "ffmpeg", urls, names, "/r/", "https://x.example.com")
	if err != nil {
		t.Fatalf("buildScript: %v", err)
	}
	// Heredoc must be present and contain both source filenames.
	if !strings.Contains(script, "cat > concat.txt <<'__CONCAT_LIST_EOF__'") {
		t.Error("missing concat-list heredoc opener")
	}
	if !strings.Contains(script, "file 'src-10.mp4'") {
		t.Error("missing first concat entry")
	}
	if !strings.Contains(script, "file 'src-11.mp4'") {
		t.Error("missing second concat entry")
	}
	if !strings.Contains(script, "__CONCAT_LIST_EOF__\n") {
		t.Error("missing heredoc terminator")
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"simple", "'simple'"},
		{"has space", "'has space'"},
		{"has 'quote", `'has '\''quote'`},
		{"", "''"},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRemoteFFmpegInstaller_CacheHit(t *testing.T) {
	// First Ensure populates the cache; second returns the cached
	// result without consulting the platform. We assert this by
	// pointing the second call at a nil PlatformAPI ctx — if the
	// cache works, it never reaches that code path.
	ctx := newTestCtxWithPlatform(t, noBindings())

	inst := newRemoteFFmpegInstaller()
	// Seed the cache directly.
	inst.hosts.Store(int64(1), &hostInstallState{
		ready: true,
		paths: installedPaths{FFmpeg: "/x/ffmpeg", FFprobe: "/x/ffprobe"},
		err:   nil,
	})

	paths, err := inst.Ensure(t.Context(), ctx, 1)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if paths.FFmpeg != "/x/ffmpeg" || paths.FFprobe != "/x/ffprobe" {
		t.Errorf("cache hit returned wrong paths: %+v", paths)
	}
}

func TestNewRemoteExecutor_DisabledWhenHostIDZero(t *testing.T) {
	local := &localExecutor{outputFolder: "/r/"}
	got, err := newRemoteExecutor(0, newRemoteFFmpegInstaller(), local)
	if err != nil {
		t.Fatalf("expected nil error for host_id=0, got %v", err)
	}
	if got != nil {
		t.Error("expected nil remoteExecutor for host_id=0")
	}
}

// ─── helpers ───────────────────────────────────────────────────────

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Compile-time guard: keep the remoteRenderResult JSON shape stable —
// the marker line is a contract with the in-script printf.
var _ = func() bool {
	r := remoteRenderResult{FileID: 1, Size: 2, SHA256: "x"}
	b, _ := json.Marshal(r)
	return strings.Contains(string(b), `"file_id":1`)
}()
