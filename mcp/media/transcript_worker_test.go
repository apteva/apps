package main

// Tier 1 worker tests for transcripts. We don't spawn the sidecar
// here — those live in transcript_integration_test.go (Tier 2). Here
// we exercise transcriberSweep + runOneTranscription with:
//
//   - a stub PlatformClient that fakes WhoAmI / GetConnection /
//     ExecuteIntegrationTool, returning canned Deepgram-shape data
//   - a httptest.Server that mocks storage's signed-URL endpoint so
//     the worker's GetSignedURL → media's storageclient → "storage"
//     plumbing is real
//
// The result: every code path through the transcriber except the
// real Deepgram HTTP round-trip is covered by Tier 1. The remaining
// gap (catalog auth shape, real response variability) is what the
// `-tags live` test in transcript_live_test.go covers.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Stub PlatformClient ────────────────────────────────────────────

// stubPlatform is a minimal-effort PlatformClient. Tests script the
// fields they care about (whoami, executeResp, executeErr) and assert
// against ExecuteCalls afterwards. Methods we don't use return zero.
type stubPlatform struct {
	mu sync.Mutex

	whoami      *sdk.InstallIdentity
	whoamiErr   error
	connections map[int64]*sdk.PlatformConnection

	executeResp *sdk.ExecuteResult
	executeErr  error

	ExecuteCalls []executeCall
}

type executeCall struct {
	ConnID int64
	Tool   string
	Input  map[string]any
}

func (s *stubPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return s.whoami, s.whoamiErr
}
func (s *stubPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	if c, ok := s.connections[id]; ok {
		return c, nil
	}
	return nil, errors.New("not found")
}
func (s *stubPlatform) ListConnections(_ sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	return nil, nil
}
func (s *stubPlatform) GetInstance(int64) (*sdk.PlatformInstance, error)  { return nil, nil }
func (s *stubPlatform) SendEvent(int64, string) error                     { return nil }
func (s *stubPlatform) SendToChannel(string, string, string) error        { return nil }
func (s *stubPlatform) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	s.mu.Lock()
	s.ExecuteCalls = append(s.ExecuteCalls, executeCall{ConnID: connID, Tool: tool, Input: input})
	s.mu.Unlock()
	return s.executeResp, s.executeErr
}
func (s *stubPlatform) CallApp(string, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
}
func (s *stubPlatform) StartOAuth(sdk.OAuthStartRequest) (*sdk.OAuthStartResult, error) {
	return nil, nil
}
func (s *stubPlatform) DisconnectConnection(int64) error                   { return nil }
func (s *stubPlatform) ListOwnedConnections() ([]sdk.PlatformConnection, error) { return nil, nil }

// GetGrants — added for app-sdk v0.3 PlatformClient. Tests don't
// exercise authz scoping, so we return the default-allow shape.
func (s *stubPlatform) GetGrants(int64) (*sdk.GrantsResponse, error) {
	return &sdk.GrantsResponse{DefaultEffect: "allow"}, nil
}

// boundDeepgram returns a stub configured as if a deepgram connection
// with id 7 is bound to the transcripts role. The platform's
// AppCtx.IntegrationFor expects integer-typed binding values; JSON
// numbers come back as float64 so we use that.
func boundDeepgram() *stubPlatform {
	return &stubPlatform{
		whoami: &sdk.InstallIdentity{
			Bindings: map[string]any{"transcripts": float64(7)},
		},
		connections: map[int64]*sdk.PlatformConnection{
			7: {ID: 7, AppSlug: "deepgram", Status: "active"},
		},
	}
}

// noBindings returns a stub with WhoAmI succeeding but no bindings —
// integration unbound case.
func noBindings() *stubPlatform {
	return &stubPlatform{
		whoami: &sdk.InstallIdentity{Bindings: map[string]any{}},
	}
}

// ─── Storage mock ───────────────────────────────────────────────────

// mockStorageURL stands up a tiny httptest.Server that satisfies just
// the endpoints media's storageclient hits when minting a signed URL.
// The real signed URL is the test's mock server URL — Deepgram
// (stubbed in PlatformClient) doesn't actually fetch it.
func mockStorageURL(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Match POST /api/apps/storage/files/{id}/url
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/apps/storage/files/") &&
			strings.HasSuffix(r.URL.Path, "/url") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"url":"/files/42/content?sig=stub&exp=999","expires_at":999}`)
			return
		}
		// MCP fallback for files_get_url
		if r.Method == http.MethodPost && r.URL.Path == "/api/apps/storage/mcp" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"text":"{\"url\":\"/files/42/content?sig=stub&exp=999\"}"}]}}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestCtxWithPlatform mirrors newTestCtx but injects a stub
// PlatformClient + storage mock so worker code can run.
func newTestCtxWithPlatform(t *testing.T, p sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	storage := mockStorageURL(t)
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID(testProj),
		tk.WithPlatform(p),
		tk.WithEnv("APTEVA_GATEWAY_URL", storage.URL),
		tk.WithEnv("APTEVA_PUBLIC_URL", storage.URL),
		tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "test-token"),
		tk.WithEnv("APTEVA_APP_TOKEN", "test-token"),
	)
	globalCtx = ctx
	return ctx
}

// ─── Sweep gating tests ────────────────────────────────────────────

func TestTranscriberSweep_NoIntegrationBound_QueueAndSkip(t *testing.T) {
	// With no transcripts integration bound, sweep should still queue
	// candidates (insertPendingTranscript), then immediately flip them
	// to skipped with a clear reason. That way the queue doesn't grow
	// unbounded, and reconnecting Deepgram later requires explicit
	// requeue (media_transcribe with force=true).
	ctx := newTestCtxWithPlatform(t, noBindings())
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(5000), "sha", "")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleAVProbe(5000), "sha", "")

	transcriberSweep(ctx)

	for _, fid := range []string{"1", "2"} {
		tr, err := getTranscript(ctx.AppDB(), testProj, fid)
		if err != nil {
			t.Fatalf("missing row for %s: %v", fid, err)
		}
		if tr.Status != "skipped" {
			t.Errorf("file_id %s status=%q want skipped", fid, tr.Status)
		}
		if !strings.Contains(tr.Error, "no transcripts integration") {
			t.Errorf("file_id %s skip reason missing: %q", fid, tr.Error)
		}
	}
}

func TestTranscriberSweep_BoundButOverDurationCap_Skipped(t *testing.T) {
	// Source over the duration cap → mark skipped, not even attempted.
	stub := boundDeepgram()
	ctx := newTestCtxWithPlatform(t, stub)

	// Default cap is 120 minutes = 7,200,000 ms. Pick something well
	// past that.
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(8*60*60*1000), "sha", "")

	transcriberSweep(ctx)

	tr, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if tr.Status != "skipped" {
		t.Errorf("status=%q want skipped (over cap)", tr.Status)
	}
	if !strings.Contains(tr.Error, "exceeds cap") {
		t.Errorf("skip reason missing duration: %q", tr.Error)
	}
	// And ExecuteIntegrationTool was never called — sweep gated before
	// the network round-trip.
	if len(stub.ExecuteCalls) != 0 {
		t.Errorf("should not call Deepgram for over-cap file, got %d calls", len(stub.ExecuteCalls))
	}
}

func TestTranscriberSweep_BoundHappyPath_PersistsTranscript(t *testing.T) {
	// End-to-end Tier 1 happy path: integration bound, duration in
	// range, ExecuteIntegrationTool returns a Deepgram-shape blob,
	// worker parses it and persists.
	stub := boundDeepgram()
	stub.executeResp = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{
		  "metadata": { "detected_language": "en" },
		  "results": {
		    "channels": [{
		      "alternatives": [{
		        "transcript": "Hello there. This is a test.",
		        "paragraphs": { "paragraphs": [{
		          "sentences": [
		            { "text": "Hello there.", "start": 0.0, "end": 1.2 },
		            { "text": "This is a test.", "start": 1.3, "end": 2.8 }
		          ]
		        }] }
		      }]
		    }]
		  }
		}`),
	}
	ctx := newTestCtxWithPlatform(t, stub)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha", "")

	transcriberSweep(ctx)

	tr, err := getTranscript(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != "ok" {
		t.Errorf("status=%q want ok", tr.Status)
	}
	if !strings.Contains(tr.Text, "Hello there") {
		t.Errorf("text missing: %q", tr.Text)
	}
	if tr.Provider != "deepgram" {
		t.Errorf("provider=%q want deepgram", tr.Provider)
	}
	if len(tr.Segments) == 0 {
		t.Error("segments not persisted")
	}
	if tr.Language != "en" {
		t.Errorf("language=%q want en", tr.Language)
	}

	// Wiring check: we called the right tool with a url arg.
	if len(stub.ExecuteCalls) != 1 {
		t.Fatalf("expected 1 ExecuteIntegrationTool call, got %d", len(stub.ExecuteCalls))
	}
	call := stub.ExecuteCalls[0]
	if call.Tool != "listen" {
		t.Errorf("tool=%q want listen (per manifest tools map)", call.Tool)
	}
	if call.ConnID != 7 {
		t.Errorf("conn_id=%d want 7", call.ConnID)
	}
	if url, _ := call.Input["url"].(string); !strings.HasPrefix(url, "http") {
		t.Errorf("expected absolute https url passed to deepgram, got %q", url)
	}
	// model defaulted from config
	if model, _ := call.Input["model"].(string); model != "nova-3" {
		t.Errorf("model=%q want nova-3", model)
	}
	// language=auto → detect_language=true (not language=auto)
	if _, ok := call.Input["language"]; ok {
		t.Errorf("with language=auto, raw 'language' arg shouldn't be set: %v", call.Input)
	}
	if v, _ := call.Input["detect_language"].(bool); !v {
		t.Errorf("detect_language not set: %v", call.Input)
	}
}

func TestTranscriberSweep_DeepgramNon2xx_MarkedFailed(t *testing.T) {
	// Deepgram returns non-2xx → row marked failed with the body
	// truncated into the error.
	stub := boundDeepgram()
	stub.executeResp = &sdk.ExecuteResult{
		Success: false, Status: 401,
		Data: json.RawMessage(`{"error":"unauthorized"}`),
	}
	ctx := newTestCtxWithPlatform(t, stub)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha", "")

	transcriberSweep(ctx)

	tr, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if tr.Status != "failed" {
		t.Errorf("status=%q want failed", tr.Status)
	}
	if !strings.Contains(tr.Error, "non-2xx") || !strings.Contains(tr.Error, "unauthorized") {
		t.Errorf("error=%q should reference deepgram non-2xx + body", tr.Error)
	}
}

func TestTranscriberSweep_DeepgramNetworkError_MarkedFailed(t *testing.T) {
	// ExecuteIntegrationTool itself errors (network blip) → row failed.
	stub := boundDeepgram()
	stub.executeErr = errors.New("connection reset by peer")
	ctx := newTestCtxWithPlatform(t, stub)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha", "")

	transcriberSweep(ctx)

	tr, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if tr.Status != "failed" {
		t.Errorf("status=%q want failed", tr.Status)
	}
	if !strings.Contains(tr.Error, "connection reset") {
		t.Errorf("error=%q should propagate network detail", tr.Error)
	}
}

func TestTranscriberSweep_MalformedDeepgramResponse_MarkedFailed(t *testing.T) {
	// Deepgram returns 200 but the JSON shape doesn't have channels —
	// parse error → row marked failed with a clear reason.
	stub := boundDeepgram()
	stub.executeResp = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"unexpected":"shape"}`),
	}
	ctx := newTestCtxWithPlatform(t, stub)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha", "")

	transcriberSweep(ctx)

	tr, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if tr.Status != "failed" {
		t.Errorf("status=%q want failed (parse error)", tr.Status)
	}
}

func TestTranscriberSweep_VideoOnlyNotEligible(t *testing.T) {
	// Files without audio shouldn't even be queued. Belt + braces:
	// transcribeCandidates filters at SQL, this asserts the wired-up
	// sweep behaves accordingly.
	stub := boundDeepgram()
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200,
		Data: json.RawMessage(`{"results":{"channels":[{"alternatives":[{"transcript":"x"}]}]}}`)}
	ctx := newTestCtxWithPlatform(t, stub)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoOnlyProbe(), "sha", "")

	transcriberSweep(ctx)

	if _, err := getTranscript(ctx.AppDB(), testProj, "1"); !notFound(err) {
		t.Errorf("video-only should never get queued; got transcript row")
	}
	if len(stub.ExecuteCalls) != 0 {
		t.Errorf("video-only triggered ExecuteIntegrationTool: %v", stub.ExecuteCalls)
	}
}

func TestTranscriberSweep_DiarizeFlagPropagates(t *testing.T) {
	// transcribe_diarize=true should land in the deepgram args.
	stub := boundDeepgram()
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200,
		Data: json.RawMessage(`{"results":{"channels":[{"alternatives":[{"transcript":"x"}]}]}}`)}

	storage := mockStorageURL(t)
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID(testProj),
		tk.WithPlatform(stub),
		tk.WithEnv("APTEVA_GATEWAY_URL", storage.URL),
		tk.WithEnv("APTEVA_PUBLIC_URL", storage.URL),
		tk.WithEnv("APTEVA_APP_TOKEN", "tok"),
		tk.WithConfig(map[string]string{
			"transcribe_diarize":  "true",
			"transcribe_language": "en",
			"transcribe_model":    "nova-2",
		}),
	)
	globalCtx = ctx

	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha", "")
	transcriberSweep(ctx)

	if len(stub.ExecuteCalls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(stub.ExecuteCalls))
	}
	args := stub.ExecuteCalls[0].Input
	if v, _ := args["diarize"].(bool); !v {
		t.Errorf("diarize=%v want true", args["diarize"])
	}
	// Explicit language → no detect_language flag, language=en.
	if v, _ := args["language"].(string); v != "en" {
		t.Errorf("language=%v want en", args["language"])
	}
	if _, ok := args["detect_language"]; ok {
		t.Errorf("detect_language should be omitted when language is explicit: %v", args)
	}
	if v, _ := args["model"].(string); v != "nova-2" {
		t.Errorf("model=%q want nova-2", v)
	}
}

// ensure the io import is exercised when we tweak the file later
var _ = io.Discard
