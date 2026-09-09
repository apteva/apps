package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type instancesTestPlatform struct {
	backupPlatform
	t        *testing.T
	bound    bool
	commands int
	calls    []string
}

func (p *instancesTestPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	bindings := map[string]any{}
	if p.bound {
		bindings["instances_provider"] = float64(7)
	}
	return &sdk.InstallIdentity{InstallID: 12, Bindings: bindings}, nil
}
func (p *instancesTestPlatform) CallAppResult(app, method string, args map[string]any, out any) error {
	p.calls = append(p.calls, method)
	values := args
	var result any
	switch method {
	case "instance_get":
		result = map[string]any{"instance": map[string]any{"id": values["id"], "name": "Test Mac", "status": "ready", "created_at": "2026-09-09", "capabilities": map[string]any{"run": true, "upload": true, "tunnel": true}}}
	case "instance_list":
		result = map[string]any{"instances": []any{}}
	case "instance_run_command":
		p.commands++
		command := values["cmd"].(string)
		if strings.Contains(command, "ssh ") || strings.Contains(command, "PRIVATE KEY") {
			p.t.Fatal("Backup attempted credential handling")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		raw, e := cmd.CombinedOutput()
		exit := 0
		if e != nil {
			exit = 1
		}
		result = map[string]any{"output": string(raw), "exit_code": exit}
	case "instance_upload_file":
		raw, e := base64.StdEncoding.DecodeString(values["content_b64"].(string))
		if e != nil {
			return e
		}
		if e = os.WriteFile(values["path"].(string), raw, 0600); e != nil {
			return e
		}
		result = map[string]any{"bytes_written": len(raw)}
	case "instance_open_tunnel":
		result = map[string]any{"local_host": "127.0.0.1", "local_port": values["target_port"]}
	case "instance_close_tunnel":
		result = map[string]any{"closed": true}
	case "jobs_schedule":
		result = map[string]any{"job": map[string]any{"id": int64(1)}}
	default:
		return fmt.Errorf("unexpected generic tool %s.%s", app, method)
	}
	b, _ := json.Marshal(result)
	return json.Unmarshal(b, out)
}
func instanceTestContext(t *testing.T, encrypted bool) (*sdk.AppCtx, *instancesTestPlatform) {
	t.Helper()
	helperBuildOnce.Do(func() {
		helperTestDir, helperBuildErr = os.MkdirTemp("", "backup-go-helper-test-*")
		if helperBuildErr != nil {
			return
		}
		cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", filepath.Join(helperTestDir, "helper"), "./cmd/backup-helper")
		cmd.Env = append(os.Environ(), "GOWORK=off")
		var output []byte
		output, helperBuildErr = cmd.CombinedOutput()
		if helperBuildErr != nil {
			helperBuildErr = fmt.Errorf("build helper: %s: %w", output, helperBuildErr)
		}
	})
	if helperBuildErr != nil {
		t.Fatal(helperBuildErr)
	}
	old := helperArtifact
	helperArtifact = func(platform string) ([]byte, string, error) {
		if platform != runtime.GOOS+"-"+runtime.GOARCH {
			return nil, "", fmt.Errorf("unexpected platform %s", platform)
		}
		body, e := os.ReadFile(filepath.Join(helperTestDir, "helper"))
		hash := sha256.Sum256(body)
		return body, hex.EncodeToString(hash[:]), e
	}
	t.Cleanup(func() { helperArtifact = old })
	platform := &instancesTestPlatform{t: t, bound: true}
	manifest := (&App{}).Manifest()
	config := sdk.Config{}
	if encrypted {
		config["encryption_passphrase"] = "test encrypted instance backup"
	}
	ctx := sdk.NewAppCtxForTest(&manifest, openTestDB(t), config, platform, silentLogger{})
	t.Cleanup(func() {
		rows, e := ctx.AppDB().Query(`SELECT run_id,kind FROM instance_operations`)
		if e != nil {
			return
		}
		type entry struct {
			id   int64
			kind string
		}
		var entries []entry
		for rows.Next() {
			var item entry
			rows.Scan(&item.id, &item.kind)
			entries = append(entries, item)
		}
		rows.Close()
		for _, item := range entries {
			cleanupInstanceOperation(ctx, item.id, item.kind)
		}
	})
	return ctx, platform
}
func sourceDirectory(t *testing.T) string {
	t.Helper()
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return dir
}
func TestInstanceOptionalBindingAndValidation(t *testing.T) {
	ctx := newTestCtx(t)
	scope := Scope{Kind: "instance", ID: "1", SourceApp: "instances", Config: InstanceSource{Method: "folders", Paths: []string{"/data"}}}
	if e := prepareScope(ctx, &scope); e == nil {
		t.Fatal("unbound Instances accepted")
	}
	for _, p := range []string{"/", "relative", "/etc/../secret", "/proc", "/a\nb"} {
		copy := scope
		copy.Config.Paths = []string{p}
		if validateScope(copy) == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	for _, method := range []string{"provider_snapshot", "full_machine", ""} {
		copy := scope
		copy.Config.Method = method
		if validateScope(copy) == nil {
			t.Fatalf("accepted unsupported %q", method)
		}
	}
	copy := scope
	copy.Config.Paths = []string{"/data", "/data/sub"}
	if validateScope(copy) == nil {
		t.Fatal("overlap accepted")
	}
	if validateScope(defaultScope()) != nil {
		t.Fatal("optional binding broke platform backups")
	}
}
func TestInstanceFolderEncryptedBackupAndReplacementRestore(t *testing.T) {
	ctx, platform := instanceTestContext(t, true)
	source := sourceDirectory(t)
	content := make([]byte, 17<<20)
	if _, e := rand.Read(content); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(source, "data.bin")
	if e := os.WriteFile(file, content, 0640); e != nil {
		t.Fatal(e)
	}
	stamp := time.Unix(1700000000, 0)
	if e := os.Chtimes(file, stamp, stamp); e != nil {
		t.Fatal(e)
	}
	scope := Scope{Kind: "instance", ID: "41", SourceApp: "instances", Config: InstanceSource{Method: "folders", Paths: []string{source}}}
	dest := auditDestination(t, ctx)
	run, e := runBackup(ctx, dest, nil, scope)
	if e != nil {
		t.Fatal(e)
	}
	if run.Status != "queued" {
		t.Fatalf("interactive backup wasn't durably queued: %s", run.Status)
	}
	if e = processQueuedBackup(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	run, e = dbGetRun(ctx.AppDB(), run.ID)
	if e != nil {
		t.Fatal(e)
	}
	if run.Status != "success" || !run.Encrypted || run.Scope.Config.Identity == "" {
		t.Fatalf("invalid recovery point: %+v", run)
	}
	if !strings.Contains(run.RemoteKey, "instance/admin/"+run.Scope.Config.Identity+"/41/") {
		t.Fatal("instance namespace missing")
	}
	target := filepath.Join(sourceDirectory(t), "restored")
	report, e := restoreFromRunWithOptions(ctx, run.ID, InstanceRestore{InstanceID: 42, Path: target})
	if e != nil {
		t.Fatal(e)
	}
	if report["restored"] != true {
		t.Fatalf("report: %+v", report)
	}
	actual, e := os.ReadFile(filepath.Join(target, "0", "data.bin"))
	if e != nil || string(actual) != string(content) {
		t.Fatal("restored contents mismatch", e)
	}
	info, e := os.Stat(filepath.Join(target, "0", "data.bin"))
	if e != nil {
		t.Fatal(e)
	}
	if info.Mode().Perm() != 0640 || info.ModTime().Unix() != stamp.Unix() {
		t.Fatal("mode or mtime lost")
	}
	repeat, e := restoreFromRunWithOptions(ctx, run.ID, InstanceRestore{InstanceID: 42, Path: target})
	if e != nil || repeat["already_completed"] != true {
		t.Fatal("restore retry was not idempotent", e)
	}
	if _, e = restoreFromRunWithOptions(ctx, run.ID, InstanceRestore{InstanceID: 43, Path: target}); e == nil {
		t.Fatal("overwrote existing restore target")
	}
	for _, tool := range platform.calls {
		if tool == "instance_download_file" {
			t.Fatal("archive crossed MCP file payload")
		}
	}
}
func TestInstancePolicyConfigAndRestart(t *testing.T) {
	ctx, _ := instanceTestContext(t, false)
	source := sourceDirectory(t)
	os.WriteFile(filepath.Join(source, "file"), []byte("original"), 0600)
	scope := Scope{Kind: "instance", ID: "1", SourceApp: "instances", Config: InstanceSource{Method: "folders", Paths: []string{source}}}
	if e := prepareScope(ctx, &scope); e != nil {
		t.Fatal(e)
	}
	dest := auditDestination(t, ctx)
	policy, e := dbCreatePolicy(ctx.AppDB(), &Policy{Name: "Mac folders", Schedule: "0 3 * * *", DestinationID: dest.ID, Scope: scope, RetentionKeep: 1})
	if e != nil {
		t.Fatal(e)
	}
	loaded, e := dbGetPolicy(ctx.AppDB(), policy.ID)
	if e != nil || len(loaded.Scope.Config.Paths) != 1 || loaded.Scope.Config.Identity != scope.Config.Identity {
		t.Fatal("source configuration did not survive storage", e)
	}
	if e = scheduleViaJobs(ctx, policy, ""); e != nil {
		t.Fatal(e)
	}
	run, e := enqueueBackup(ctx, dest, policy)
	if e != nil {
		t.Fatal(e)
	}
	ctx.AppDB().Exec(`UPDATE runs SET status='running' WHERE id=?`, run.ID)
	if e = reconcileInterruptedRuns(ctx); e != nil {
		t.Fatal(e)
	}
	restored, _ := dbGetRun(ctx.AppDB(), run.ID)
	if restored.Status != "queued" {
		t.Fatal("restart lost pending instance operation")
	}
	if e = processQueuedBackup(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM runs`).Scan(&count)
	if count != 1 {
		t.Fatal("restart duplicated run")
	}
}

type failingInstanceWriter struct{}

func (failingInstanceWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("simulated interrupted transfer")
}
func TestInstanceInterruptedTransferReusesRecoveryPoint(t *testing.T) {
	ctx, _ := instanceTestContext(t, false)
	source := sourceDirectory(t)
	file := filepath.Join(source, "file")
	if e := os.WriteFile(file, []byte("before interruption"), 0600); e != nil {
		t.Fatal(e)
	}
	scope := Scope{Kind: "instance", ID: "1", SourceApp: "instances", Config: InstanceSource{Method: "folders", Paths: []string{source}}}
	dest := auditDestination(t, ctx)
	run, e := runBackup(ctx, dest, nil, scope)
	if e != nil {
		t.Fatal(e)
	}
	// Simulate process death after remote capture but during archive transfer.
	if _, e = streamInstanceSnapshot(context.Background(), ctx, failingInstanceWriter{}, run); e == nil {
		t.Fatal("expected interrupted transfer")
	}
	if e = os.Remove(file); e != nil {
		t.Fatal(e)
	}
	ctx.AppDB().Exec(`UPDATE runs SET status='running' WHERE id=?`, run.ID)
	if e = reconcileInterruptedRuns(ctx); e != nil {
		t.Fatal(e)
	}
	if e = processQueuedBackup(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(sourceDirectory(t), "restore")
	if _, e = restoreFromRunWithOptions(ctx, run.ID, InstanceRestore{Path: target}); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(target, "0", "file"))
	if e != nil || string(b) != "before interruption" {
		t.Fatal("retry recaptured completed archive", string(b), e)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM runs`).Scan(&count)
	if count != 1 {
		t.Fatal("retry duplicated recovery point")
	}
}

func TestInstanceRetentionNamespaces(t *testing.T) {
	ctx := newTestCtx(t)
	dest := auditDestination(t, ctx)
	writer, e := openDestination(dest, ctx, "")
	if e != nil {
		t.Fatal(e)
	}
	first := Scope{Kind: "instance", ID: "41", SourceApp: "instances", Config: InstanceSource{Identity: "binding-host-a", Method: "folders", Paths: []string{"/data"}}}
	other := first
	other.Config.Identity = "binding-host-b"
	policy := &Policy{ID: 1, StorageID: "policy-unique", Scope: first, RetentionKeep: 1}
	protected := storagePrefixFor(other, policy.ID, policy.StorageID) + "old.tar.gz"
	for _, key := range []string{protected, storagePrefixFor(first, policy.ID, policy.StorageID) + "one.tar.gz", storagePrefixFor(first, policy.ID, policy.StorageID) + "two.tar.gz"} {
		if e = writer.Put(context.Background(), key, strings.NewReader("archive"), 7); e != nil {
			t.Fatal(e)
		}
	}
	if e = pruneRetention(context.Background(), ctx, writer, dest, policy); e != nil {
		t.Fatal(e)
	}
	body, e := writer.Get(context.Background(), protected)
	if e != nil {
		t.Fatal("retention crossed instance identity", e)
	}
	body.Close()
}

var helperBuildOnce sync.Once
var helperTestDir string
var helperBuildErr error
