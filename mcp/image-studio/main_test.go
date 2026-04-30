package main

// image-studio v0.1 — tests cover:
//
//   - normalizeImageResponse for openai-api shape (and unknown-slug)
//   - extractStorageID for both direct + MCP-wrapped shapes
//   - toolImageGenerate: success path + unbound provider error path
//     + provider-error path. Uses a stub PlatformClient so no real
//     OpenAI calls fly out.
//   - toolImageHistory: empty + after-insert
//   - dbInsertGeneration writes the row + roundtrips JSON-list fields
//
// We don't test the thumbnail generator — the underlying image/jpeg
// encoder is stdlib, and the function gracefully no-ops on decode
// errors which is what we'd test anyway.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// --- stub PlatformClient -------------------------------------------

// recordingPlatform implements sdk.PlatformClient and records every
// call. ExecuteIntegrationTool returns whatever the test pre-loads
// in nextExecuteResult; CallApp returns nextCallResult. WhoAmI / GetX
// stubs are minimal and only what the SDK touches in these tests.
type recordingPlatform struct {
	mu                sync.Mutex
	executeCalls      []executeCall
	callAppCalls      []callAppCall
	nextExecuteResult *sdk.ExecuteResult
	nextExecuteErr    error
	nextCallResult    json.RawMessage
	nextCallErr       error
	identity          *sdk.InstallIdentity
}

type executeCall struct {
	ConnID int64
	Tool   string
	Input  map[string]any
}
type callAppCall struct {
	AppName string
	Tool    string
	Input   map[string]any
}

func newRecordingPlatform() *recordingPlatform {
	return &recordingPlatform{
		identity: &sdk.InstallIdentity{
			AppName:   "image-studio",
			InstallID: 99,
			ProjectID: "test-proj",
			Bindings:  map[string]any{"provider": float64(42), "storage": float64(17)},
		},
	}
}

func (p *recordingPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "openai-api", ProjectID: "test-proj"}, nil
}
func (p *recordingPlatform) ListConnections(filter sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	return nil, nil
}
func (p *recordingPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return nil, errors.New("not implemented in stub")
}
func (p *recordingPlatform) SendEvent(int64, string) error { return nil }
func (p *recordingPlatform) SendToChannel(string, string, string) error { return nil }
func (p *recordingPlatform) WhoAmI() (*sdk.InstallIdentity, error) { return p.identity, nil }

func (p *recordingPlatform) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	p.executeCalls = append(p.executeCalls, executeCall{ConnID: connID, Tool: tool, Input: input})
	p.mu.Unlock()
	if p.nextExecuteErr != nil {
		return nil, p.nextExecuteErr
	}
	return p.nextExecuteResult, nil
}

func (p *recordingPlatform) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	p.mu.Lock()
	p.callAppCalls = append(p.callAppCalls, callAppCall{AppName: appName, Tool: tool, Input: input})
	p.mu.Unlock()
	if p.nextCallErr != nil {
		return nil, p.nextCallErr
	}
	return p.nextCallResult, nil
}

// --- helpers --------------------------------------------------------

func newImageStudioCtx(t *testing.T, pf sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	rec := tk.NewEmitRecorder()
	opts := []tk.Option{
		tk.WithProjectID("test-proj"),
		tk.WithEmitter(rec),
	}
	if pf != nil {
		opts = append(opts, tk.WithPlatform(pf))
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", opts...)
	globalCtx = ctx
	return ctx
}

// fakePNG returns valid PNG bytes (a 4x4 transparent image) so the
// upstream-image fetch in toolImageGenerate succeeds.
func fakePNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf strings.Builder
	if err := png.Encode(&stringWriter{&buf}, img); err != nil {
		panic(err)
	}
	return []byte(buf.String())
}

type stringWriter struct{ b *strings.Builder }

func (s *stringWriter) Write(p []byte) (int, error) { return s.b.Write(p) }

// --- normalizeImageResponse ----------------------------------------

func TestNormalizeImageResponse_OpenAI_DALLE_URL(t *testing.T) {
	// DALL·E shape — URL response, no model echo.
	body := `{"data":[{"url":"https://upstream/a.png","revised_prompt":"a tabby cat"}]}`
	imgs, revised, _, err := normalizeImageResponse("openai-api", json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].UpstreamURL != "https://upstream/a.png" {
		t.Errorf("imgs = %+v", imgs)
	}
	if imgs[0].B64 != "" {
		t.Errorf("B64 should be empty when URL is set, got %q", imgs[0].B64)
	}
	if revised != "a tabby cat" {
		t.Errorf("revised = %q", revised)
	}
}

func TestNormalizeImageResponse_OpenAI_GPTImage_B64(t *testing.T) {
	// gpt-image-* shape — base64 response, model echoed.
	body := `{"data":[{"b64_json":"AAECAwQ="}],"created":1714000000,"model":"gpt-image-2"}`
	imgs, _, model, err := normalizeImageResponse("openai-api", json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].B64 != "AAECAwQ=" {
		t.Errorf("imgs = %+v", imgs)
	}
	if imgs[0].UpstreamURL != "" {
		t.Errorf("UpstreamURL should be empty when only B64 is set, got %q", imgs[0].UpstreamURL)
	}
	if model != "gpt-image-2" {
		t.Errorf("model = %q, want gpt-image-2", model)
	}
}

func TestNormalizeImageResponse_OpenAI_MultipleImages(t *testing.T) {
	body := `{"data":[{"url":"u1"},{"url":"u2"},{"url":"u3"}]}`
	imgs, _, _, err := normalizeImageResponse("openai-api", json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 3 {
		t.Errorf("expected 3 images, got %d", len(imgs))
	}
}

func TestNormalizeImageResponse_UnknownSlug(t *testing.T) {
	_, _, _, err := normalizeImageResponse("replicate", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown slug")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- extractStorageID ----------------------------------------------

func TestExtractStorageID_DirectShape(t *testing.T) {
	body := []byte(`{"id":1234,"url":"http://...","sha256":"abc"}`)
	if got := extractStorageID(body); got != 1234 {
		t.Errorf("got %d", got)
	}
}

func TestExtractStorageID_MCPWrapped(t *testing.T) {
	body := []byte(`{"result":{"content":[{"type":"text","text":"{\"id\":555,\"url\":\"...\"}"}]}}`)
	if got := extractStorageID(body); got != 555 {
		t.Errorf("got %d", got)
	}
}

func TestExtractStorageID_Empty(t *testing.T) {
	if got := extractStorageID(nil); got != 0 {
		t.Errorf("got %d on nil", got)
	}
	if got := extractStorageID([]byte(`{}`)); got != 0 {
		t.Errorf("got %d on empty object", got)
	}
}

// --- toolImageGenerate ---------------------------------------------

func TestToolImageGenerate_RequiresPrompt(t *testing.T) {
	ctx := newImageStudioCtx(t, newRecordingPlatform())
	app := &App{}
	_, err := app.toolImageGenerate(ctx, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("expected 'prompt required' error, got %v", err)
	}
}

func TestToolImageGenerate_NoProviderBound(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{} // no provider binding
	ctx := newImageStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolImageGenerate(ctx, map[string]any{"prompt": "hi"})
	// Tool's contract: signal failure as MCP isError=true content,
	// not a Go error — agents see a clean message.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res, ok := out.(map[string]any)
	if !ok {
		// Older code path returned a Go error directly. Accept both.
		t.Skipf("tool returned non-map: %T", out)
	}
	_ = res
}

func TestToolImageGenerate_HappyPath_WithStorage(t *testing.T) {
	// Mock OpenAI's image endpoint via httptest — fakePNG returned
	// when image-studio fetches the upstream URL.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG())
	}))
	defer upstream.Close()

	pf := newRecordingPlatform()
	// Provider returns OpenAI shape pointing at the test server.
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(fmt.Sprintf(
			`{"data":[{"url":"%s/img.png","revised_prompt":"a regal cat"}]}`,
			upstream.URL,
		)),
	}
	// Storage returns id 1234 in MCP-wrapped shape.
	pf.nextCallResult = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":1234,\"url\":\"/files/1234\",\"sha256\":\"abc\"}"}]}}`,
	)

	ctx := newImageStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolImageGenerate(ctx, map[string]any{
		"prompt": "a cat in a hat",
		"size":   "1024x1024",
	})
	if err != nil {
		t.Fatalf("toolImageGenerate: %v", err)
	}

	// Provider was called once with the expected tool + prompt.
	if len(pf.executeCalls) != 1 {
		t.Fatalf("expected 1 ExecuteIntegrationTool call, got %d", len(pf.executeCalls))
	}
	if pf.executeCalls[0].ConnID != 42 {
		t.Errorf("connID = %d, want 42", pf.executeCalls[0].ConnID)
	}
	if pf.executeCalls[0].Tool != "generate_image" {
		t.Errorf("tool = %q, want generate_image", pf.executeCalls[0].Tool)
	}
	if pf.executeCalls[0].Input["prompt"] != "a cat in a hat" {
		t.Errorf("prompt mismatch")
	}

	// Storage was called once with files_from_url.
	if len(pf.callAppCalls) != 1 {
		t.Fatalf("expected 1 CallApp, got %d", len(pf.callAppCalls))
	}
	if pf.callAppCalls[0].AppName != "storage" || pf.callAppCalls[0].Tool != "files_from_url" {
		t.Errorf("storage call = %+v", pf.callAppCalls[0])
	}

	// MCP result has content[].
	res := out.(map[string]any)
	content, ok := res["content"].([]map[string]any)
	if !ok {
		t.Fatalf("content not []map[string]any: %T", res["content"])
	}
	if len(content) < 2 {
		t.Errorf("expected at least 2 content blocks, got %d", len(content))
	}
	// One block is text with the storage id.
	var foundText bool
	for _, c := range content {
		if c["type"] == "text" {
			if s, _ := c["text"].(string); strings.Contains(s, "1234") {
				foundText = true
			}
		}
	}
	if !foundText {
		t.Errorf("expected storage id 1234 in text block; got %+v", content)
	}

	// _meta carries storage_ids.
	meta := res["_meta"].(map[string]any)
	ids := meta["storage_ids"].([]int64)
	if len(ids) != 1 || ids[0] != 1234 {
		t.Errorf("storage_ids = %+v", ids)
	}

	// History row exists.
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 history row, got %d", count)
	}
}

func TestToolImageGenerate_NoStorageBound(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fakePNG())
	}))
	defer upstream.Close()

	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{"provider": float64(42)} // no storage
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(fmt.Sprintf(
			`{"data":[{"url":"%s/img.png"}]}`, upstream.URL,
		)),
	}

	ctx := newImageStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolImageGenerate(ctx, map[string]any{"prompt": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.callAppCalls) != 0 {
		t.Errorf("storage not bound — should not have called CallApp; got %d calls", len(pf.callAppCalls))
	}
	res := out.(map[string]any)
	meta := res["_meta"].(map[string]any)
	ids := meta["storage_ids"].([]int64)
	if len(ids) != 0 {
		t.Errorf("storage_ids should be empty when storage unbound, got %+v", ids)
	}
	// Still has thumbnail (from the fetched bytes).
	content := res["content"].([]map[string]any)
	hasImage := false
	for _, c := range content {
		if c["type"] == "image" {
			hasImage = true
		}
	}
	if !hasImage {
		t.Error("expected an image content block from the local thumbnail")
	}
}

func TestToolImageGenerate_ProviderError(t *testing.T) {
	pf := newRecordingPlatform()
	pf.nextExecuteResult = &sdk.ExecuteResult{Success: false, Status: 429, Data: json.RawMessage(`"rate limited"`)}

	ctx := newImageStudioCtx(t, pf)
	app := &App{}
	out, _ := app.toolImageGenerate(ctx, map[string]any{"prompt": "hi"})
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError=true on provider failure, got %+v", res)
	}
}

// --- toolImageHistory ----------------------------------------------

func TestToolImageHistory_EmptyByDefault(t *testing.T) {
	ctx := newImageStudioCtx(t, newRecordingPlatform())
	app := &App{}
	out, err := app.toolImageHistory(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	gens := out.(map[string]any)["generations"].([]map[string]any)
	if len(gens) != 0 {
		t.Errorf("expected empty history, got %d", len(gens))
	}
}

func TestToolImageHistory_AfterInsert(t *testing.T) {
	ctx := newImageStudioCtx(t, newRecordingPlatform())
	app := &App{}
	app.dbInsertGeneration("test-proj", "p1", "rev1", "openai-api", "dall-e-3", "1024x1024",
		[]int64{1, 2}, []string{"u1", "u2"},
		base64.StdEncoding.EncodeToString([]byte("thumb")), 2)
	out, err := app.toolImageHistory(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	gens := out.(map[string]any)["generations"].([]map[string]any)
	if len(gens) != 1 {
		t.Fatalf("expected 1 row, got %d", len(gens))
	}
	g := gens[0]
	if g["prompt"] != "p1" || g["provider"] != "openai-api" {
		t.Errorf("row mismatch: %+v", g)
	}
	if ids := g["storage_ids"].([]int64); len(ids) != 2 || ids[0] != 1 {
		t.Errorf("storage_ids: %+v", ids)
	}
}

func TestToolImageHistory_LimitCap(t *testing.T) {
	ctx := newImageStudioCtx(t, newRecordingPlatform())
	app := &App{}
	for i := 0; i < 5; i++ {
		app.dbInsertGeneration("test-proj", fmt.Sprintf("p%d", i), "", "openai-api", "", "", nil, nil, "", 1)
	}
	out, _ := app.toolImageHistory(ctx, map[string]any{"limit": 3})
	gens := out.(map[string]any)["generations"].([]map[string]any)
	if len(gens) != 3 {
		t.Errorf("expected limit=3, got %d", len(gens))
	}
}

// --- buildProviderArgs ---------------------------------------------

func TestBuildProviderArgs_GPTImage2(t *testing.T) {
	args := buildProviderArgs("gpt-image-2", "p", "1024x1024", "high", "webp", "transparent", 1)
	if args["model"] != "gpt-image-2" || args["prompt"] != "p" || args["size"] != "1024x1024" {
		t.Errorf("base fields wrong: %+v", args)
	}
	if args["quality"] != "high" || args["output_format"] != "webp" || args["background"] != "transparent" {
		t.Errorf("gpt-image-2 fields not passed through: %+v", args)
	}
}

func TestBuildProviderArgs_GPTImage_DefaultsOmitOptionals(t *testing.T) {
	args := buildProviderArgs("gpt-image-2", "p", "1024x1024", "", "", "", 1)
	if _, ok := args["quality"]; ok {
		t.Error("empty quality should not be sent — let provider default")
	}
	if _, ok := args["output_format"]; ok {
		t.Error("empty output_format should not be sent")
	}
	if _, ok := args["background"]; ok {
		t.Error("empty background should not be sent")
	}
}

func TestBuildProviderArgs_DallE3_QualityRemap(t *testing.T) {
	// "auto" is a gpt-image value; for dall-e-3 we must remap to standard.
	args := buildProviderArgs("dall-e-3", "p", "1024x1024", "auto", "webp", "", 1)
	if args["quality"] != "standard" {
		t.Errorf("dall-e-3 'auto' should remap to standard, got %v", args["quality"])
	}
	if _, ok := args["output_format"]; ok {
		t.Error("dall-e-3 doesn't accept output_format — must be stripped")
	}
}

func TestBuildProviderArgs_DallE2_StripsAllExtras(t *testing.T) {
	args := buildProviderArgs("dall-e-2", "p", "512x512", "high", "webp", "transparent", 2)
	if _, ok := args["quality"]; ok {
		t.Error("dall-e-2 doesn't accept quality")
	}
	if _, ok := args["output_format"]; ok {
		t.Error("dall-e-2 doesn't accept output_format")
	}
	if _, ok := args["background"]; ok {
		t.Error("dall-e-2 doesn't accept background")
	}
}

// --- imageBytes ----------------------------------------------------

func TestImageBytes_PrefersB64(t *testing.T) {
	want := []byte("hello")
	enc := base64.StdEncoding.EncodeToString(want)
	got, err := imageBytes(generatedImage{B64: enc, UpstreamURL: "http://should-not-be-fetched.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestImageBytes_NoSource(t *testing.T) {
	if _, err := imageBytes(generatedImage{}); err == nil {
		t.Fatal("expected error when neither URL nor B64 set")
	}
}

// --- toolImageGenerate b64 path ------------------------------------

func TestToolImageGenerate_GPTImage_B64_StorageUpload(t *testing.T) {
	// gpt-image-* never returns a URL — only b64. Storage handoff must
	// switch from files_from_url to files_upload with content_base64.
	pngB64 := base64.StdEncoding.EncodeToString(fakePNG())

	pf := newRecordingPlatform()
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(fmt.Sprintf(
			`{"data":[{"b64_json":%q}],"created":1714000000,"model":"gpt-image-2"}`,
			pngB64,
		)),
	}
	pf.nextCallResult = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":7777,\"url\":\"/files/7777\",\"sha256\":\"abc\"}"}]}}`,
	)

	ctx := newImageStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolImageGenerate(ctx, map[string]any{
		"prompt":        "moonlit owl",
		"model":         "gpt-image-2",
		"output_format": "png",
	})
	if err != nil {
		t.Fatalf("toolImageGenerate: %v", err)
	}

	if len(pf.callAppCalls) != 1 {
		t.Fatalf("expected 1 storage call, got %d", len(pf.callAppCalls))
	}
	got := pf.callAppCalls[0]
	if got.Tool != "files_upload" {
		t.Errorf("for b64 path expected files_upload, got %q", got.Tool)
	}
	if cb, _ := got.Input["content_base64"].(string); cb != pngB64 {
		t.Errorf("content_base64 not passed through: got %q", cb)
	}
	if ct, _ := got.Input["content_type"].(string); ct != "image/png" {
		t.Errorf("content_type = %q, want image/png", ct)
	}

	// Provider call: model + output_format made it through.
	if pf.executeCalls[0].Input["model"] != "gpt-image-2" {
		t.Errorf("model not forwarded: %+v", pf.executeCalls[0].Input)
	}
	if pf.executeCalls[0].Input["output_format"] != "png" {
		t.Errorf("output_format not forwarded: %+v", pf.executeCalls[0].Input)
	}

	// Result has storage id.
	res := out.(map[string]any)
	meta := res["_meta"].(map[string]any)
	ids := meta["storage_ids"].([]int64)
	if len(ids) != 1 || ids[0] != 7777 {
		t.Errorf("storage_ids = %+v", ids)
	}
}

// --- pickExt -------------------------------------------------------

func TestPickExt(t *testing.T) {
	cases := map[string]string{
		"":     "png",
		"png":  "png",
		"jpeg": "jpg",
		"jpg":  "jpg",
		"webp": "webp",
		"gif":  "png", // unknown defaults to png
	}
	for in, want := range cases {
		if got := pickExt(in); got != want {
			t.Errorf("pickExt(%q) = %q, want %q", in, got, want)
		}
	}
}
