package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// The real worker SDK and process are exercised below. This small platform
// bridge supplies only the Server-owned alias routing; Server's own component
// suite separately tests registration, ownership, scope, activation and rollback.
type workerPlatform struct {
	tk.BasePlatformClient
	worker                      *tk.Sidecar
	submits                     atomic.Int64
	loseSubmit                  atomic.Bool
	cancelAfterProcessingSubmit atomic.Pointer[context.CancelFunc]
}

func (p *workerPlatform) CallAppResultContext(ctx context.Context, name, tool string, input map[string]any, out any) error {
	if name != "component:executor" {
		return fmt.Errorf("unexpected app dependency %q", name)
	}
	if tool == "submit" {
		p.submits.Add(1)
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": input}})
	req, _ := http.NewRequestWithContext(ctx, "POST", p.worker.URL()+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.worker.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var rpc struct {
		Error  any `json:"error"`
		Result struct {
			IsError    bool            `json:"isError"`
			Structured json.RawMessage `json:"structuredContent"`
			Content    []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		return err
	}
	if rpc.Error != nil || rpc.Result.IsError {
		return fmt.Errorf("worker RPC error: %+v %+v", rpc.Error, rpc.Result.Content)
	}
	if tool == "submit" && p.loseSubmit.CompareAndSwap(true, false) {
		return errors.New("simulated lost response after acceptance")
	}
	if tool == "submit" && strings.Contains(fmt.Sprint(input["command"]), "APTEVA_RESULT:") {
		if cancel := p.cancelAfterProcessingSubmit.Swap(nil); cancel != nil {
			(*cancel)()
		}
	}
	raw := rpc.Result.Structured
	if len(raw) == 0 {
		if len(rpc.Result.Content) == 0 {
			return errors.New("empty worker response")
		}
		raw = []byte(rpc.Result.Content[0].Text)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
func (p *workerPlatform) CallAppContext(ctx context.Context, name, tool string, input map[string]any) (json.RawMessage, error) {
	var out json.RawMessage
	err := p.CallAppResultContext(ctx, name, tool, input, &out)
	return out, err
}
func (p *workerPlatform) CallAppBatchContext(context.Context, string, []sdk.AppCall, sdk.AppBatchOptions) ([]sdk.AppCallResult, error) {
	return nil, tk.ErrNotImplemented
}
func componentFixture(t *testing.T) (*sdk.AppCtx, *workerPlatform) {
	t.Helper()
	if testing.Short() {
		t.Skip("real executor subprocess integration")
	}
	worker := tk.SpawnSidecar(t, "executor", tk.WithProjectID(testProj), tk.WithEnv("APTEVA_DATA_DIR", t.TempDir()))
	req, _ := http.NewRequest("POST", worker.URL()+"/_apteva/runtime/app/activate", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+worker.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("activate=%d", resp.StatusCode)
	}
	p := &workerPlatform{worker: worker}
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p), tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"execution_mode": "component", "render_max_output_size_mb": "32"}))
	return app, p
}
func TestComponentManifestHasNoInfrastructureDependency(t *testing.T) {
	m := (&App{}).Manifest()
	for _, dep := range m.Requires.Apps {
		if dep.Name == "instances" {
			t.Fatal("Instances is still a Media dependency")
		}
	}
	for _, field := range m.ConfigSchema {
		if field.Name == "render_host_id" {
			t.Fatal("host selector still exposed")
		}
	}
	def, ok := m.Components["executor"]
	if def.Manifest.Runtime.Source == nil || def.Manifest.Runtime.Source.Ref == strings.Repeat("0", 40) {
		t.Fatal("worker source must pin the tested immutable commit")
	}
	if !ok || def.Protocol != "apteva.media-executor/v1" || def.Required || !def.Capabilities.PassiveStart || !def.Capabilities.Drain {
		t.Fatal("invalid executor declaration")
	}
	if len(def.Manifest.Requires.Apps) != 0 || len(def.Manifest.Requires.Integrations) != 0 {
		t.Fatal("worker inherited dependencies")
	}
	for _, file := range []string{"component.go", "remote_ffmpeg_install.go", "remote_exec.go"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "instance_run_command") || strings.Contains(string(raw), "/apps/instances/") {
			t.Fatal("direct infrastructure call remains")
		}
	}
}
func TestComponentDurableIdentitySurvivesLostSubmitResponse(t *testing.T) {
	app, p := componentFixture(t)
	ctx := context.WithValue(t.Context(), componentCallOwnerKey{}, "render:reconcile")
	p.loseSubmit.Store(true)
	command := "echo once >> once-count; printf completed"
	if out, _, err := runComponentPlan(ctx, app, command, 10); err != nil || out != "completed" {
		t.Fatalf("accepted job was not reconciled after response loss: %q %v", out, err)
	}
	out, exit, err := runComponentPlan(ctx, app, command, 10)
	if err != nil || exit != 0 || out != "completed" {
		t.Fatalf("%q %d %v", out, exit, err)
	}
	n := p.submits.Load()
	out, _, err = runComponentPlan(ctx, app, "printf should-not-run", 10)
	if err != nil || out != "completed" || p.submits.Load() != n {
		t.Fatalf("persisted outcome replayed processing: %q %v", out, err)
	}
	var jobs int
	if err := app.AppDB().QueryRow(`SELECT count(*) FROM media_executor_calls WHERE owner_key='render:reconcile'`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("durable identity changed")
	}
}
func TestComponentCancellationIsConfirmed(t *testing.T) {
	app, _ := componentFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	_, _, err := runComponentPlan(ctx, app, "sleep 60", 10)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation=%v", err)
	}
	var raw string
	if err := app.AppDB().QueryRow(`SELECT receipt_json FROM media_executor_calls`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var r componentReceipt
	if json.Unmarshal([]byte(raw), &r) != nil || !r.Stopped || r.Status != "cancelled" {
		t.Fatalf("stop unconfirmed: %s", raw)
	}
}
func TestExecutorTransferGrantScope(t *testing.T) {
	var upstream atomic.Int64
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		if r.Header.Get("Authorization") != "Bearer parent-private-token" || r.URL.Query().Get("project_id") != testProj {
			t.Error("Storage authorization or project lost")
		}
		if r.Method == "PUT" {
			raw, err := io.ReadAll(r.Body)
			if err != nil || string(raw) != "data" || r.Header.Get("X-Apteva-Transfer-Encoding") != "" {
				t.Errorf("Storage must receive original bytes without relay encoding: %q, %v", raw, err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"upload_id":"new-upload"}`))
	}))
	defer storage.Close()
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithEnv("APTEVA_INSTALL_ID", "12"), tk.WithEnv("APTEVA_GATEWAY_URL", storage.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "parent-private-token"), tk.WithConfig(map[string]string{"render_max_output_size_mb": "1"}))
	previous := globalCtx
	globalCtx = app
	t.Cleanup(func() { globalCtx = previous })
	_, token, err := newExecutorGrant(t.Context(), app, "https://gateway.example", 10)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, tail, body string, encoding ...string) int {
		r := httptest.NewRequest(method, "/executor-io"+boundStorageProxyPath+tail+"?project_id=foreign", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if len(encoding) > 0 {
			r.Header.Set("X-Apteva-Transfer-Encoding", encoding[0])
		}
		w := httptest.NewRecorder()
		(&App{}).handleExecutorTransfer(w, r)
		return w.Code
	}
	for _, tc := range []struct{ method, path, body string }{{"GET", "/files/1", ""}, {"DELETE", "/files/1", ""}, {"PUT", "/uploads/foreign/parts/1", "bytes"}, {"POST", "/uploads", `{"folder":"/private/","size":1}`}, {"POST", "/uploads", `{"folder":"/renders/","size":999999999}`}} {
		if code := call(tc.method, tc.path, tc.body); code < 400 {
			t.Fatalf("scope bypass: %+v -> %d", tc, code)
		}
	}
	if upstream.Load() != 0 {
		t.Fatal("rejected operation reached Storage")
	}
	if code := call("POST", "/uploads", `{"folder":"/renders/","size":4}`); code != 200 {
		t.Fatalf("valid init=%d", code)
	}
	if code := call("PUT", "/uploads/new-upload/parts/1", "data"); code != 200 {
		t.Fatalf("owned part=%d", code)
	}
	if code := call("PUT", "/uploads/new-upload/parts/1", base64.StdEncoding.EncodeToString([]byte("data")), "base64"); code != 200 {
		t.Fatalf("encoded owned part=%d", code)
	}
	for _, tc := range []struct{ method, path, encoding string }{{"PUT", "/uploads/new-upload/parts/1", "unknown"}, {"POST", "/uploads", "base64"}, {"PUT", "/uploads/foreign/parts/1", "base64"}} {
		if code := call(tc.method, tc.path, "ZGF0YQ==", tc.encoding); code < 400 {
			t.Fatalf("encoding bypass: %+v -> %d", tc, code)
		}
	}
	if _, err := app.AppDB().Exec(`UPDATE media_executor_grants SET expires_at=0`); err != nil {
		t.Fatal(err)
	}
	if code := call("PUT", "/uploads/new-upload/parts/1", "data"); code != 403 {
		t.Fatalf("expired grant=%d", code)
	}
}

func TestComponentRenderAndIndexUseScopedTransfers(t *testing.T) {
	for _, editedHEVC := range []bool{false, true} {
		t.Run(fmt.Sprintf("editedHEVC=%v", editedHEVC), func(t *testing.T) {
			testComponentRenderAndIndexUseScopedTransfers(t, editedHEVC)
		})
	}
}

func testComponentRenderAndIndexUseScopedTransfers(t *testing.T, editedHEVC bool) {
	app, p := componentFixture(t)
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg not installed")
	}
	sourcePath := filepath.Join(t.TempDir(), "source.mp4")
	cmd := exec.Command(binary, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10", "-f", "lavfi", "-i", "sine=frequency=440", "-t", "1", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", sourcePath)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, raw)
	}
	if editedHEVC {
		sourcePath = normalizationShiftedEditFixture(t)
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256(source)
	var mu sync.Mutex
	var upload []byte
	var target StorageFile
	var requests int
	var gateway *httptest.Server
	gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/executor-io/") {
			if r.Method == "PUT" && strings.Contains(r.URL.Path, "/parts/") {
				if r.Header.Get("X-Apteva-Transfer-Encoding") != "base64" || r.Header.Get("Content-Type") != "text/plain" {
					t.Error("binary component chunk reached the public gateway")
					w.WriteHeader(403)
					return
				}
			}
			clone := r.Clone(r.Context())
			u := *r.URL
			clone.URL = &u
			clone.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/apps/media/_install/42")
			(&App{}).handleExecutorTransfer(w, clone)
			return
		}
		if r.URL.Path == "/source.mp4" {
			http.ServeFile(w, r, sourcePath)
			return
		}
		if !strings.HasPrefix(r.URL.Path, boundStorageProxyPath) {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer parent-storage-secret-abc" || r.URL.Query().Get("project_id") != testProj {
			t.Error("worker obtained broad credentials or lost project")
		}
		tail := strings.TrimPrefix(r.URL.Path, boundStorageProxyPath)
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case tail == "/files/1" && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{"file": StorageFile{ID: 1, Name: "source.mp4", Folder: "/", SizeBytes: int64(len(source)), SHA256: hex.EncodeToString(sha[:]), ContentType: "video/mp4"}})
		case tail == "/files/1/url":
			_ = json.NewEncoder(w).Encode(map[string]any{"url": gateway.URL + "/source.mp4", "delivery": "apteva"})
		case tail == "/files/init":
			w.WriteHeader(501)
			_, _ = w.Write([]byte(`{"error":"disk backend"}`))
		case tail == "/uploads" && r.Method == "POST":
			var in struct {
				Filename, Folder, ContentType, SHA string
				Size                               int64
			}
			var fields map[string]any
			_ = json.NewDecoder(r.Body).Decode(&fields)
			in.Filename, _ = fields["filename"].(string)
			in.Folder, _ = fields["folder"].(string)
			in.ContentType, _ = fields["content_type"].(string)
			in.SHA, _ = fields["sha256"].(string)
			size, _ := fields["size"].(float64)
			in.Size = int64(size)
			target = StorageFile{ID: 42, Name: in.Filename, Folder: in.Folder, ContentType: in.ContentType, SHA256: in.SHA, SizeBytes: in.Size}
			upload = nil
			requests++
			_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": "job-upload", "part_size": 5 << 20, "max_parallel": 1, "max_parts": 100})
		case tail == "/uploads/job-upload/parts/1":
			raw, _ := io.ReadAll(r.Body)
			upload = append(upload, raw...)
			_, _ = w.Write([]byte(`{}`))
		case tail == "/uploads/job-upload/complete":
			actual := sha256.Sum256(upload)
			if target.SizeBytes != int64(len(upload)) || target.SHA256 != hex.EncodeToString(actual[:]) {
				t.Error("upload integrity mismatch")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"file": target})
		case tail == "/files/42":
			_ = json.NewEncoder(w).Encode(map[string]any{"file": target})
		case tail == "/files" && r.Method == "POST":
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			file, h, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			raw, _ := io.ReadAll(file)
			file.Close()
			hash := sha256.Sum256(raw)
			_ = json.NewEncoder(w).Encode(map[string]any{"file": StorageFile{ID: 43, Name: h.Filename, Folder: r.FormValue("folder"), SizeBytes: int64(len(raw)), SHA256: hex.EncodeToString(hash[:])}})
		default:
			t.Errorf("unexpected Storage request %s %s", r.Method, tail)
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	t.Setenv("APTEVA_PUBLIC_URL", gateway.URL)
	t.Setenv("APTEVA_INSTALL_ID", "42")
	t.Setenv("APTEVA_APP_TOKEN", "parent-storage-secret-abc")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "parent-storage-secret-abc")
	old := globalCtx
	globalCtx = app
	t.Cleanup(func() { globalCtx = old })
	probe, err := runProbe(t.Context(), "ffprobe", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = upsertMedia(app.AppDB(), testProj, "1", probe, hex.EncodeToString(sha[:]), "/", "source.mp4"); err != nil {
		t.Fatal(err)
	}
	id, err := insertRender(app.AppDB(), testProj, "resize", []string{"1"}, map[string]any{"width": 80, "height": 46}, "small.mp4", "/renders/", "test")
	if err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPending(app.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := newRemoteExecutor(1, sharedRemoteInstaller(), &localExecutor{outputFolder: "/renders/"}, 20<<20)
	if err != nil {
		t.Fatal(err)
	}
	runOneRender(app, row, remote.fallback, remote, 30)
	var status, output, failure string
	if err = app.AppDB().QueryRow(`SELECT status,COALESCE(output_file_id,''),COALESCE(error,'') FROM renders WHERE id=?`, id).Scan(&status, &output, &failure); err != nil {
		t.Fatal(err)
	}
	if status != "ok" || output != "42" {
		t.Fatalf("render %s output=%s error=%s", status, output, failure)
	}
	mu.Lock()
	rendered := append([]byte{}, upload...)
	uploads := requests
	mu.Unlock()
	if len(rendered) == 0 || uploads != 1 {
		t.Fatal("render did not use resumable disk transfer")
	}
	renderedPath := filepath.Join(t.TempDir(), "render.mp4")
	if err = os.WriteFile(renderedPath, rendered, 0600); err != nil {
		t.Fatal(err)
	}
	renderProbe, err := runProbe(t.Context(), "ffprobe", renderedPath)
	if err != nil || renderProbe.Width != 80 || renderProbe.Height != 46 {
		t.Fatalf("output probe=%+v %v", renderProbe, err)
	}
	_, thumb, _, _, err := runRemoteIndexStage(t.Context(), app, testProj, remoteIndexParams{HostID: 1, SignedURL: gateway.URL + "/source.mp4", FileID: "1", Stage: "derive", ReuseProbe: probe, Summary: probe, ProbeBase64: base64Probe(probe), ThumbWidth: 80, ThumbSeek: 0})
	if err != nil || thumb != 43 {
		t.Fatalf("component indexing thumb=%d err=%v", thumb, err)
	}
	// Exercise the Python guarded-audio path through the same real component.
	if _, err := exec.LookPath("python3"); err == nil {
		name := "normalized.wav"
		if editedHEVC {
			name = "normalized.mp4"
		}
		id, err := insertRender(app.AppDB(), testProj, "audio_filter", []string{"1"}, map[string]any{"mode": "normalize", "target_lufs": -20, "target_peak_dbtp": -1.5}, name, "/renders/", "test")
		if err != nil {
			t.Fatal(err)
		}
		row, err := claimNextPending(app.AppDB())
		if err != nil {
			t.Fatal(err)
		}
		runOneRender(app, row, remote.fallback, remote, 30)
		if err = app.AppDB().QueryRow(`SELECT status,COALESCE(error,'') FROM renders WHERE id=?`, id).Scan(&status, &failure); err != nil || status != "ok" {
			t.Fatalf("guarded component audio: %s %s %v", status, failure, err)
		}
		if editedHEVC {
			var params string
			if err := app.AppDB().QueryRow(`SELECT resolved_params FROM renders WHERE id=?`, id).Scan(&params); err != nil || !strings.Contains(params, `"video_presentation_restored":true`) || !strings.Contains(params, `"video_pictures_match":true`) {
				t.Fatalf("component lost HEVC presentation: %s %v", params, err)
			}
		}
	}
	if p.submits.Load() < 2 {
		t.Fatal("processing did not go through the component")
	}
	// Cancel immediately after the real worker accepts a render. Its workdir
	// may not exist yet, or its trap may already have removed it; only the
	// component's verified job receipt is authoritative.
	previousCancellation := runRemoteCancellation
	t.Cleanup(func() { runRemoteCancellation = previousCancellation })
	var legacyStops atomic.Int64
	runRemoteCancellation = func(context.Context, *sdk.AppCtx, int64, string, int) (string, int, error) {
		legacyStops.Add(1)
		return "missing workdir", 1, errors.New("duplicate legacy cleanup")
	}
	cancelID, err := insertRender(app.AppDB(), testProj, "resize", []string{"1"}, map[string]any{"width": 80, "height": 46}, "cancel.mp4", "/renders/", "test")
	if err != nil {
		t.Fatal(err)
	}
	row, err = claimNextPending(app.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	stopRender := context.CancelFunc(func() { triggerCancel(cancelID) })
	p.cancelAfterProcessingSubmit.Store(&stopRender)
	runOneRender(app, row, remote.fallback, remote, 30)
	cancelled, err := getRender(app.AppDB(), testProj, cancelID)
	if err != nil || cancelled.Status != "cancelled" || legacyStops.Load() != 0 || !strings.Contains(string(cancelled.Metrics), `"cancellation_confirmed":true`) {
		t.Fatalf("component cancellation used duplicate cleanup: row=%+v legacy=%d err=%v", cancelled, legacyStops.Load(), err)
	}
	var leaked int
	if err = app.AppDB().QueryRow(`SELECT count(*) FROM media_executor_calls WHERE request_json LIKE '%parent-storage-secret-abc%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("parent token leaked into worker plans")
	}
}
func base64Probe(p *Probe) string { return p.remoteProbeBase64 }

func TestComponentAdmissionClassifiesRenderGuardsAsProcessing(t *testing.T) {
	weight, control := classifyComponentPlan("if [ -f cancel.requested ]; then echo REMOTE_CANCEL_REQUESTED; exit 1; fi\nAPTEVA_RESULT:")
	if control || weight != 2 {
		t.Fatal("render cancellation guard bypassed processing admission")
	}
	if weight, control = classifyComponentPlan(remoteCancellationScript("/var/tmp/work")); !control || weight != 0 {
		t.Fatal("cancellation cannot use the control lane")
	}
}

func TestComponentMigrationDoesNotReplayLegacyOffloadedRender(t *testing.T) {
	app := newTestCtx(t)
	id, err := insertRender(app.AppDB(), testProj, "trim", []string{"1"}, nil, "old.mp4", "/renders/", "test")
	if err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPending(app.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	recordRenderMetric(app, row, "remote_host_id", 7)
	for _, table := range []string{"media_executor_uploads", "media_executor_grants", "media_executor_calls"} {
		if _, err := app.AppDB().Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile("migrations/030_executor_components.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.AppDB().Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	row, err = getRender(app.AppDB(), testProj, id)
	if err != nil || row.Status != "failed" || !strings.Contains(row.Error, "legacy_executor_cleanup_required") {
		t.Fatalf("legacy execution was replayable: %+v %v", row, err)
	}
	if count, err := recoverInterruptedRenders(app.AppDB()); err != nil || count != 0 {
		t.Fatal("legacy external execution was automatically requeued")
	}
}

func TestComponentLegacyCancellationJoinsWorkerStop(t *testing.T) {
	app, _ := componentFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	work := uniqueRemoteWorkDir(0)
	finish := registerRemoteKill(ctx, app, 1, 0, work)
	script := "set -eu\nWORK=" + shellQuote(work) + "\nmkdir -p \"$WORK\"\necho $$ > \"$WORK/pid\"\nsleep 60\n"
	_, _, err := runRemote(ctx, app, 1, "setsid bash -c "+shellQuote(script), 10)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("processing cancellation=%v", err)
	}
	if err := finish(); err != nil {
		t.Fatalf("joined cancellation was not confirmed: %v", err)
	}
}
