package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type snapshotTestPlatform struct {
	tk.BasePlatformClient
	archive        []byte
	receipt        sourceReceipt
	reads, exports int
	failAt         int
	bad            bool
	subdir         string
}

func (p *snapshotTestPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	var result any
	if app != "code" || args["_project_id"] != "p1" {
		return errors.New("scope mismatch")
	}
	if tool == "repos_export" {
		p.exports++
		p.subdir, _ = args["subdir"].(string)
		result = p.receipt
	} else {
		offset := args["offset"].(int64)
		p.reads++
		if p.failAt == p.reads {
			return errors.New("interrupted")
		}
		end := min(offset+args["limit"].(int64), int64(len(p.archive)))
		id := p.receipt.SnapshotID
		if p.bad {
			id = "wrong"
		}
		result = map[string]any{"snapshot_id": id, "sha256": p.receipt.SHA256, "size": p.receipt.Size, "offset": offset, "next_offset": end, "eof": end == p.receipt.Size, "data_b64": base64.StdEncoding.EncodeToString(p.archive[offset:end])}
	}
	raw, _ := json.Marshal(result)
	return json.Unmarshal(raw, out)
}
func snapshotFixture(t *testing.T) *snapshotTestPlatform {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "asset.bin", Method: zip.Store}
	h.SetMode(0755)
	f, e := w.CreateHeader(h)
	if e != nil {
		t.Fatal(e)
	}
	data := make([]byte, 3<<20)
	rand.Read(data)
	f.Write(data)
	w.Close()
	sum := sha256.Sum256(buf.Bytes())
	id := hex.EncodeToString(sum[:])
	return &snapshotTestPlatform{archive: buf.Bytes(), receipt: sourceReceipt{SnapshotID: id, SHA256: id, SourceRevision: "sha256:" + id, Size: int64(buf.Len()), Format: "zip-v1", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}}
}
func TestSnapshotConsumerResumeIdentityAndRetention(t *testing.T) {
	p := snapshotFixture(t)
	p.failAt = 2
	cache := t.TempDir()
	dest := t.TempDir()
	d := &Deployment{ProjectID: "p1", SourceRef: "repo", SourceExtraJSON: `{"subdir":"client"}`}
	f := &codeFetcher{platform: p, config: sourceConfig{CacheDir: cache}}
	if e := f.Fetch(d, dest); e == nil {
		t.Fatal("expected interruption")
	}
	p.failAt = 0
	if e := f.Fetch(d, dest); e != nil {
		t.Fatal(e)
	}
	if p.exports != 1 || p.subdir != "client" {
		t.Fatal("source recaptured or subdir lost")
	}
	info, e := os.Stat(filepath.Join(dest, "asset.bin"))
	if e != nil || info.Mode().Perm() != 0755 {
		t.Fatal("lost binary or executable mode")
	}
	reads := p.reads
	p.archive = nil
	p.receipt.ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
	if e = f.Fetch(d, t.TempDir()); e != nil {
		t.Fatal(e)
	}
	if p.reads != reads {
		t.Fatal("retained archive re-downloaded")
	}
	d.SourceRef = "different"
	if e = f.Fetch(d, t.TempDir()); e == nil {
		t.Fatal("accepted different source")
	}
}
func TestSnapshotConsumerRejectsBadChunksExpiryAndBudget(t *testing.T) {
	for _, kind := range []string{"identity", "expired", "budget"} {
		t.Run(kind, func(t *testing.T) {
			p := snapshotFixture(t)
			d := &Deployment{ProjectID: "p1", SourceRef: "repo"}
			switch kind {
			case "identity":
				p.bad = true
			case "expired":
				p.receipt.ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
			case "budget":
				d.SourceExtraJSON = `{"max_bytes":10}`
			}
			f := &codeFetcher{platform: p}
			if e := f.Fetch(d, t.TempDir()); e == nil {
				t.Fatal("accepted invalid snapshot")
			}
		})
	}
}
func TestCommandPipelineExportAndArtifactTests(t *testing.T) {
	src, dist := t.TempDir(), t.TempDir()
	p := commandPipeline{Prepare: []commandStage{{Name: "export", Command: []string{"sh", "-c", "mkdir native; printf source > native/input"}, Outputs: []string{"native/input"}}}, BuildDirectory: "native", Outputs: []string{"app"}, Tests: []commandStage{{Name: "smoke", Command: []string{"sh", "-c", "test \"$(cat app)\" = source"}}}}
	ov := BuildOverrides{BuildCmd: `cp input "$DEPLOY_ARTIFACT_DIR/app"`, TargetConfigJSON: mustJSON(genericTargetConfig{Pipeline: &p})}
	if _, e := buildWithPipeline(&commandBuilder{}, src, dist, ov, io.Discard); e != nil {
		t.Fatal(e)
	}
	manifest, e := readArtifactManifestFile(filepath.Join(dist, artifactManifestFilename))
	if e != nil || manifest.Pipeline == nil || len(manifest.Pipeline.Tests) != 1 {
		t.Fatalf("evidence: %+v %v", manifest, e)
	}
	p.Prepare = nil
	p.BuildDirectory = ""
	p.Tests[0].Command = []string{"sh", "-c", "echo modified > app"}
	ov.BuildCmd = `echo original > "$DEPLOY_ARTIFACT_DIR/app"`
	ov.TargetConfigJSON = mustJSON(genericTargetConfig{Pipeline: &p})
	if _, e = buildWithPipeline(&commandBuilder{}, t.TempDir(), t.TempDir(), ov, io.Discard); e == nil || !strings.Contains(e.Error(), "modified") {
		t.Fatalf("tampering accepted: %v", e)
	}
}
func TestCommandPipelineRejectsEscapesAndCancellation(t *testing.T) {
	src := t.TempDir()
	os.Symlink(t.TempDir(), filepath.Join(src, "outside"))
	if _, e := pipelinePath(src, "outside"); e == nil {
		t.Fatal("escaped link")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := runCommandStage(ctx, commandStage{Name: "cancelled", Command: []string{"sh", "-c", "exit 0"}}, src, src, src, nil, io.Discard); e == nil {
		t.Fatal("ignored cancellation")
	}
}

type genericPublisherPlatform struct {
	tk.BasePlatformClient
	live             bool
	calls            []string
	ids              []int64
	publisherDefault int64
}

func (p *genericPublisherPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"publisher": map[string]any{"ids": []int64{41, 42}, "default_id": int64(41)}}}, nil
}
func (p *genericPublisherPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "steamworks", Status: "active"}, nil
}
func (p *genericPublisherPlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, tool)
	p.ids = append(p.ids, id)
	if tool == "set_build_live" {
		if input["buildid"] != float64(123) {
			return nil, errors.New("wrong build identity")
		}
		return &sdk.ExecuteResult{Success: true, Status: 201, Data: json.RawMessage(`{}`)}, nil
	}
	body := `{"response":{"betas":{"test":{"buildid":0},"public":{"buildid":0}}}}`
	if p.live {
		body = `{"response":{"betas":{"test":{"buildid":123},"public":{"buildid":123}}}}`
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(body)}, nil
}
func publisherFixture(t *testing.T) (*App, *sdk.AppCtx, *Deployment, *Build, *genericPublisherPlatform) {
	t.Helper()
	p := &genericPublisherPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(p))
	setBootRecoveryGlobalCtx(t, ctx)
	a := &App{dataDir: t.TempDir(), registry: NewSupervisorRegistry(), buildSem: make(chan struct{}, 1), retainRollbacks: 3}
	cfg := genericTargetConfig{Connections: map[string]int64{"publisher": 42}, Pipeline: &commandPipeline{Outputs: []string{"app"}, Tests: []commandStage{{Name: "smoke", Command: []string{"sh", "-c", "test -s app"}}}}, Publisher: &integrationPublisher{Role: "publisher", Provider: "steamworks", Identity: map[string]any{"appid": 7}, Upload: []commandStage{{Name: "upload", Command: []string{"sh", "-c", `printf '{"buildid":123}' > upload.json`}}}, ReceiptFile: "upload.json", Publish: integrationAction{Tool: "set_build_live", Input: map[string]any{"appid": "$identity.appid", "buildid": "$upload.buildid", "betakey": "$channel"}, PendingStatuses: []int{201}}, Observe: &integrationObserver{Action: integrationAction{Tool: "list_branches", Input: map[string]any{"appid": "$identity.appid"}}, Published: []observationCheck{{Pointer: "/response/betas/$channel/buildid", Equals: "$upload.buildid"}}}}, ReleasePolicy: &releasePolicy{Version: "1", Channels: map[string]releaseRule{"test": {Automatic: true, RequiredTests: []string{"smoke"}}, "public": {FromChannel: "test", RequiredTests: []string{"smoke"}}}}}
	d, e := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{Name: "generic", TargetKind: "artifact", SourceKind: "local", SourceRef: t.TempDir(), Framework: "command", BuildCmd: `printf artifact > "$DEPLOY_ARTIFACT_DIR/app"`, TargetConfigJSON: mustJSON(cfg)})
	if e != nil {
		t.Fatal(e)
	}
	env, e := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if e != nil {
		t.Fatal(e)
	}
	d = effectiveDeploymentForEnvironment(d, env)
	b, e := a.runLocalBuild(d)
	if e != nil || b.Status != "succeeded" {
		t.Fatalf("build: %+v %v", b, e)
	}
	return a, ctx, d, b, p
}
func TestGenericPublisherUsesSelectedAccountAndExactArtifact(t *testing.T) {
	a, ctx, d, b, p := publisherFixture(t)
	r, e := a.runReleaseWithOptions(d, b, releaseOptions{Channel: "test"})
	if e != nil {
		t.Fatal(e)
	}
	if r.Status == "live" {
		t.Fatal("201 or unmatched observation marked live")
	}
	if _, e = a.runReleaseWithOptions(d, b, releaseOptions{Channel: "public"}); e == nil {
		t.Fatal("promoted without tested channel")
	}
	p.live = true
	if e = a.syncIntegrationRelease(r); e != nil {
		t.Fatal(e)
	}
	r, _ = dbGetRelease(ctx.AppDB(), r.ID)
	var state workflowState
	json.Unmarshal([]byte(r.ReleaseMetaJSON), &state)
	if r.Status != "live" || state.Availability.State != "unconfirmed" {
		t.Fatal("branch publication confused with availability")
	}
	promoted, e := a.runReleaseWithOptions(d, b, releaseOptions{Channel: "public"})
	if e != nil || promoted.BuildID != b.ID {
		t.Fatalf("promotion: %+v %v", promoted, e)
	}
	for _, id := range p.ids {
		if id != 42 {
			t.Fatal("wrong publisher account")
		}
	}
	// Target edits cannot change subsequent observation's account.
	d.TargetConfigJSON = strings.Replace(d.TargetConfigJSON, `"publisher":42`, `"publisher":41`, 1)
	if e = a.syncIntegrationRelease(r); e != nil {
		t.Fatal(e)
	}
	if p.ids[len(p.ids)-1] != 42 {
		t.Fatal("release followed account edit")
	}
	os.WriteFile(filepath.Join(b.ArtifactPath, "app"), []byte("tampered"), 0644)
	if _, e = a.runReleaseWithOptions(d, b, releaseOptions{Channel: "public"}); e == nil {
		t.Fatal("promoted changed artifact")
	}
}
func TestTargetConnectionsNeverFallbackFromExplicitInvalidSelection(t *testing.T) {
	_, _, _, _, _ = publisherFixture(t)
	if _, e := selectedIntegration("publisher", `{"connections":{"publisher":99}}`); e == nil {
		t.Fatal("unbound account accepted")
	}
}

func TestReleasePolicyApprovalAndInvalidation(t *testing.T) {
	a, ctx, d, b, _ := publisherFixture(t)
	cfg, _ := genericTarget(d.TargetConfigJSON)
	rule := cfg.ReleasePolicy.Channels["test"]
	rule.RequireApproval = true
	cfg.ReleasePolicy.Channels["test"] = rule
	cfg.ReleasePolicy.ApproverAgentIDs = []int64{9}
	d.TargetConfigJSON = mustJSON(cfg)
	if _, e := checkReleasePolicy(d, b, releaseOptions{Channel: "test"}, false); e == nil {
		t.Fatal("approval bypassed")
	}
	if e := dbUpdateEnvironment(ctx.AppDB(), d.EnvironmentID, map[string]any{"target_config_json": d.TargetConfigJSON}); e != nil {
		t.Fatal(e)
	}
	args := map[string]any{"id": d.ID, "build_id": b.ID, "_project_id": "p1", "channel": "test"}
	if _, e := a.toolApproveRelease(context.Background(), ctx, args); e == nil {
		t.Fatal("anonymous approver accepted")
	}
	call := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 9})
	preview, e := a.toolApproveRelease(call, ctx, args)
	if e != nil {
		t.Fatal(e)
	}
	args["approval_digest"] = preview.(map[string]any)["approval_digest"]
	if _, e = a.toolApproveRelease(call, ctx, args); e != nil {
		t.Fatal(e)
	}
	if _, e = checkReleasePolicy(d, b, releaseOptions{Channel: "test"}, false); e != nil {
		t.Fatal(e)
	}
	cfg.ReleasePolicy.Version = "2"
	d.TargetConfigJSON = mustJSON(cfg)
	if _, e = checkReleasePolicy(d, b, releaseOptions{Channel: "test"}, false); e == nil {
		t.Fatal("old approval survived policy change")
	}
}

func TestPublisherUploadCannotChangeTestedBytes(t *testing.T) {
	a, _, d, b, _ := publisherFixture(t)
	cfg, _ := genericTarget(d.TargetConfigJSON)
	cfg.Publisher.Upload[0].Command = []string{"sh", "-c", `echo changed > "$DEPLOY_ARTIFACT_DIR/app"; printf '{"buildid":123}' > upload.json`}
	d.TargetConfigJSON = mustJSON(cfg)
	if _, e := a.runReleaseWithOptions(d, b, releaseOptions{Channel: "test"}); e == nil || !strings.Contains(e.Error(), "modified tested") {
		t.Fatalf("modified upload accepted: %v", e)
	}
}
func TestPublisherCrashDoesNotRepeatUnknownMutation(t *testing.T) {
	a, ctx, d, b, p := publisherFixture(t)
	r, e := a.runReleaseWithOptions(d, b, releaseOptions{Channel: "test"})
	if e != nil {
		t.Fatal(e)
	}
	var config, raw string
	if e = ctx.AppDB().QueryRow(`SELECT config_json,state_json FROM release_workflows WHERE release_id=?`, r.ID).Scan(&config, &raw); e != nil {
		t.Fatal(e)
	}
	var state workflowState
	json.Unmarshal([]byte(raw), &state)
	state.Phase = "publishing"
	ctx.AppDB().Exec(`UPDATE release_workflows SET state_json=? WHERE release_id=?`, mustJSON(state), r.ID)
	calls := len(p.calls)
	if _, e = a.runReleaseWithOptions(d, b, releaseOptions{Channel: "test"}); e == nil {
		t.Fatal("repeated ambiguous publish")
	}
	if len(p.calls) != calls {
		t.Fatal("made external call before resolving previous mutation")
	}
}
func TestPolicyBlocksUnverifiedCloudPipeline(t *testing.T) {
	a, ctx, d, b, _ := publisherFixture(t)
	other, e := dbCreateBuildForEnv(ctx.AppDB(), d.ID, d.EnvironmentID, "command", "")
	if e != nil {
		t.Fatal(e)
	}
	other.ArtifactPath = b.ArtifactPath
	other.TargetConfigJSON = d.TargetConfigJSON
	other.ArtifactManifestJSON = "{}"
	if e = sealBuildArtifact(other); e == nil {
		t.Fatal("unverified pipeline accepted")
	}
	_ = a
}

func TestStopCancelsPublisherCommandBeforePromotion(t *testing.T) {
	a, ctx, d, b, p := publisherFixture(t)
	cfg, _ := genericTarget(d.TargetConfigJSON)
	marker := filepath.Join(t.TempDir(), "started")
	cfg.Publisher.Upload[0] = commandStage{Name: "upload", Command: []string{"sh", "-c", `touch "$MARKER"; sleep 30; printf '{"buildid":123}' > upload.json`}, Env: map[string]string{"MARKER": marker}}
	d.TargetConfigJSON = mustJSON(cfg)
	done := make(chan error, 1)
	go func() { _, e := a.runReleaseWithOptions(d, b, releaseOptions{Channel: "test"}); done <- e }()
	deadline := time.Now().Add(5 * time.Second)
	for !exists(marker) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !exists(marker) {
		t.Fatal("upload did not start")
	}
	if _, e := a.stopDeployment(d); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("stopped upload succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("upload did not cancel")
	}
	for _, call := range p.calls {
		if call == "set_build_live" {
			t.Fatal("stopped upload promoted")
		}
	}
	releases, e := dbListReleases(ctx.AppDB(), d.ID, 10)
	if e != nil || len(releases) != 1 || releases[0].Status != "stopped" {
		t.Fatalf("release resurrected: %+v %v", releases, e)
	}
}
