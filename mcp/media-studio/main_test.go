package main

// media-studio v0.3 — tests cover:
//
//   - normalizeImageResponse for openai-api shape (and unknown-slug)
//   - mediaBytes for both B64 and URL paths
//   - toolMediaGenerate (kind=image): success + unbound-provider + provider-error paths
//   - toolMediaGenerate dispatch: missing kind, unknown kind, stubbed-kind error
//   - toolMediaHistory: empty + after-insert + limit cap + kind filter
//   - dbInsertGeneration writes the row + roundtrips JSON-list fields
//
// Stubs the platform via tk.BasePlatformClient + a recordingPlatform
// so no real OpenAI calls fly out.

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

type recordingPlatform struct {
	tk.BasePlatformClient
	mu                sync.Mutex
	executeCalls      []executeCall
	callAppCalls      []callAppCall
	nextExecuteResult *sdk.ExecuteResult
	nextExecuteErr    error
	nextCallResult    json.RawMessage
	nextCallErr       error
	identity          *sdk.InstallIdentity
	// appSlug is what GetConnection echoes back. Default openai-api so
	// existing tests keep passing; venice tests override to "venice-ai".
	appSlug string
	// perAppCallResults: when set, CallApp returns the response keyed by
	// (appName, tool); falls back to nextCallResult otherwise. Lets edit
	// tests pre-load both files_get_content + files_upload responses.
	perAppCallResults map[string]json.RawMessage
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
		appSlug: "openai-api",
		identity: &sdk.InstallIdentity{
			AppName:   "media-studio",
			InstallID: 99,
			ProjectID: "test-proj",
			Bindings: map[string]any{
				"image_provider": float64(42),
				"storage":        float64(17),
			},
		},
	}
}

func (p *recordingPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	slug := p.appSlug
	if slug == "" {
		slug = "openai-api"
	}
	return &sdk.PlatformConnection{ID: id, AppSlug: slug, ProjectID: "test-proj"}, nil
}
func (p *recordingPlatform) ListConnections(filter sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	return nil, nil
}
func (p *recordingPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return nil, errors.New("not implemented in stub")
}
func (p *recordingPlatform) SendEvent(int64, string) error              { return nil }
func (p *recordingPlatform) SendToChannel(string, string, string) error { return nil }
func (p *recordingPlatform) WhoAmI() (*sdk.InstallIdentity, error)      { return p.identity, nil }
func (p *recordingPlatform) StartOAuth(sdk.OAuthStartRequest) (*sdk.OAuthStartResult, error) {
	return &sdk.OAuthStartResult{}, nil
}
func (p *recordingPlatform) DisconnectConnection(int64) error                        { return nil }
func (p *recordingPlatform) ListOwnedConnections() ([]sdk.PlatformConnection, error) { return nil, nil }
func (p *recordingPlatform) GetGrants(int64) (*sdk.GrantsResponse, error) {
	return &sdk.GrantsResponse{DefaultEffect: "allow"}, nil
}

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
	keyed, ok := p.perAppCallResults[appName+":"+tool]
	p.mu.Unlock()
	if p.nextCallErr != nil {
		return nil, p.nextCallErr
	}
	if ok {
		return keyed, nil
	}
	return p.nextCallResult, nil
}

func (p *recordingPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	raw, err := p.CallApp(appName, tool, input)
	if err != nil {
		return err
	}
	if len(raw) == 0 || out == nil {
		return nil
	}
	// Mirror app-sdk decodeMCPEnvelope: prefer the wrapped
	// {result:{content:[{text:"<inner>"}]}} shape, fall through to
	// direct decode when the bytes are already unwrapped.
	var env struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Result != nil && len(env.Result.Content) > 0 {
		return json.Unmarshal([]byte(env.Result.Content[0].Text), out)
	}
	return json.Unmarshal(raw, out)
}

// --- helpers --------------------------------------------------------

func newMediaStudioCtx(t *testing.T, pf sdk.PlatformClient) *sdk.AppCtx {
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
	body := `{"data":[{"url":"https://upstream/a.png","revised_prompt":"a tabby cat"}]}`
	imgs, revised, _, err := normalizeImageResponse("openai-api", "image.generate", json.RawMessage(body))
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
	body := `{"data":[{"b64_json":"AAECAwQ="}],"created":1714000000,"model":"gpt-image-2"}`
	imgs, _, model, err := normalizeImageResponse("openai-api", "image.generate", json.RawMessage(body))
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
	imgs, _, _, err := normalizeImageResponse("openai-api", "image.generate", json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 3 {
		t.Errorf("expected 3 images, got %d", len(imgs))
	}
}

func TestNormalizeImageResponse_UnknownSlug(t *testing.T) {
	_, _, _, err := normalizeImageResponse("replicate", "image.generate", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown slug")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- toolMediaGenerate dispatch ------------------------------------

func TestToolMediaGenerate_RequiresKind(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	app := &App{}
	_, err := app.toolMediaGenerate(ctx, map[string]any{"prompt": "x"})
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("expected 'kind required', got %v", err)
	}
}

func TestToolMediaGenerate_RequiresPrompt(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	app := &App{}
	_, err := app.toolMediaGenerate(ctx, map[string]any{"kind": "image"})
	if err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("expected 'prompt required', got %v", err)
	}
}

func TestToolMediaGenerate_UnknownKind(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{"kind": "hologram", "prompt": "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError=true for unknown kind, got %+v", res)
	}
}

func TestToolMediaGenerate_StubbedKind_VideoReturnsCleanError(t *testing.T) {
	pf := newRecordingPlatform()
	// Pretend a video provider is bound so dispatch reaches the build-args stub.
	pf.identity.Bindings["video_provider"] = float64(99)
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{"kind": "video", "prompt": "a cat"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError=true for stubbed kind, got %+v", res)
	}
}

func TestToolMediaGenerate_NoProviderBound(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{} // no image_provider
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{"kind": "image", "prompt": "hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError=true when image_provider unbound, got %+v", res)
	}
}

// --- toolMediaGenerate (kind=image) — full pipeline ----------------

func TestToolMediaGenerate_Image_HappyPath_WithStorage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG())
	}))
	defer upstream.Close()

	pf := newRecordingPlatform()
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(fmt.Sprintf(
			`{"data":[{"url":"%s/img.png","revised_prompt":"a regal cat"}]}`,
			upstream.URL,
		)),
	}
	pf.nextCallResult = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":1234,\"url\":\"/files/1234\",\"sha256\":\"abc\"}"}]}}`,
	)

	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":   "image",
		"prompt": "a cat in a hat",
		"size":   "1024x1024",
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}

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

	if len(pf.callAppCalls) != 1 {
		t.Fatalf("expected 1 CallApp, got %d", len(pf.callAppCalls))
	}
	if pf.callAppCalls[0].AppName != "storage" || pf.callAppCalls[0].Tool != "files_from_url" {
		t.Errorf("storage call = %+v", pf.callAppCalls[0])
	}
	// Folder must be the dotted convention.
	if folder, _ := pf.callAppCalls[0].Input["folder"].(string); folder != "/.generated/images/" {
		t.Errorf("storage folder = %q, want /.generated/images/", folder)
	}

	res := out.(map[string]any)
	content, ok := res["content"].([]map[string]any)
	if !ok {
		t.Fatalf("content not []map[string]any: %T", res["content"])
	}
	if len(content) < 2 {
		t.Errorf("expected at least 2 content blocks, got %d", len(content))
	}
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

	meta := res["_meta"].(map[string]any)
	if meta["kind"] != "image" {
		t.Errorf("_meta.kind = %v, want image", meta["kind"])
	}
	ids := meta["storage_ids"].([]int64)
	if len(ids) != 1 || ids[0] != 1234 {
		t.Errorf("storage_ids = %+v", ids)
	}

	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 history row, got %d", count)
	}
	var kind string
	ctx.AppDB().QueryRow(`SELECT kind FROM generations LIMIT 1`).Scan(&kind)
	if kind != "image" {
		t.Errorf("inserted kind = %q, want image", kind)
	}
}

func TestToolMediaGenerate_Image_NoStorageBound(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fakePNG())
	}))
	defer upstream.Close()

	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{"image_provider": float64(42)} // no storage
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(fmt.Sprintf(
			`{"data":[{"url":"%s/img.png"}]}`, upstream.URL,
		)),
	}

	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{"kind": "image", "prompt": "hi"})
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

func TestToolMediaGenerate_Image_ProviderError(t *testing.T) {
	pf := newRecordingPlatform()
	pf.nextExecuteResult = &sdk.ExecuteResult{Success: false, Status: 429, Data: json.RawMessage(`"rate limited"`)}

	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, _ := app.toolMediaGenerate(ctx, map[string]any{"kind": "image", "prompt": "hi"})
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError=true on provider failure, got %+v", res)
	}
}

// --- toolMediaHistory ----------------------------------------------

func TestToolMediaHistory_EmptyByDefault(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	app := &App{}
	out, err := app.toolMediaHistory(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	gens := out.(map[string]any)["generations"].([]map[string]any)
	if len(gens) != 0 {
		t.Errorf("expected empty history, got %d", len(gens))
	}
}

func TestToolMediaHistory_AfterInsert(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	app := &App{}
	app.dbInsertGeneration(generationRecord{
		ProjectID: "test-proj", Kind: "image", Prompt: "p1", Revised: "rev1",
		Provider: "openai-api", Model: "dall-e-3", Size: "1024x1024",
		StorageIDs: []int64{1, 2}, UpstreamURLs: []string{"u1", "u2"},
		ThumbnailB64: base64.StdEncoding.EncodeToString([]byte("thumb")), Count: 2,
	})
	out, err := app.toolMediaHistory(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	gens := out.(map[string]any)["generations"].([]map[string]any)
	if len(gens) != 1 {
		t.Fatalf("expected 1 row, got %d", len(gens))
	}
	g := gens[0]
	if g["prompt"] != "p1" || g["provider"] != "openai-api" || g["kind"] != "image" {
		t.Errorf("row mismatch: %+v", g)
	}
	if ids := g["storage_ids"].([]int64); len(ids) != 2 || ids[0] != 1 {
		t.Errorf("storage_ids: %+v", ids)
	}
}

func TestToolMediaHistory_LimitCap(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	app := &App{}
	for i := 0; i < 5; i++ {
		app.dbInsertGeneration(generationRecord{
			ProjectID: "test-proj", Kind: "image",
			Prompt: fmt.Sprintf("p%d", i), Provider: "openai-api", Count: 1,
		})
	}
	out, _ := app.toolMediaHistory(ctx, map[string]any{"limit": 3})
	gens := out.(map[string]any)["generations"].([]map[string]any)
	if len(gens) != 3 {
		t.Errorf("expected limit=3, got %d", len(gens))
	}
}

func TestToolMediaHistory_KindFilter(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	app := &App{}
	app.dbInsertGeneration(generationRecord{ProjectID: "test-proj", Kind: "image", Prompt: "i1", Provider: "openai-api", Count: 1})
	app.dbInsertGeneration(generationRecord{ProjectID: "test-proj", Kind: "video", Prompt: "v1", Provider: "replicate", Count: 1})
	app.dbInsertGeneration(generationRecord{ProjectID: "test-proj", Kind: "image", Prompt: "i2", Provider: "openai-api", Count: 1})

	out, _ := app.toolMediaHistory(ctx, map[string]any{"kind": "image"})
	gens := out.(map[string]any)["generations"].([]map[string]any)
	if len(gens) != 2 {
		t.Fatalf("kind=image filter: expected 2 rows, got %d", len(gens))
	}
	for _, g := range gens {
		if g["kind"] != "image" {
			t.Errorf("row leaked through kind filter: %+v", g)
		}
	}

	out, _ = app.toolMediaHistory(ctx, map[string]any{"kind": "video"})
	gens = out.(map[string]any)["generations"].([]map[string]any)
	if len(gens) != 1 {
		t.Errorf("kind=video filter: expected 1 row, got %d", len(gens))
	}

	out, _ = app.toolMediaHistory(ctx, map[string]any{})
	gens = out.(map[string]any)["generations"].([]map[string]any)
	if len(gens) != 3 {
		t.Errorf("no filter: expected 3 rows, got %d", len(gens))
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

// --- mediaBytes ----------------------------------------------------

func TestMediaBytes_PrefersB64(t *testing.T) {
	want := []byte("hello")
	enc := base64.StdEncoding.EncodeToString(want)
	got, err := mediaBytes(generatedMedia{B64: enc, UpstreamURL: "http://should-not-be-fetched.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMediaBytes_NoSource(t *testing.T) {
	if _, err := mediaBytes(generatedMedia{}); err == nil {
		t.Fatal("expected error when neither URL nor B64 set")
	}
}

// --- gpt-image-* b64 storage upload --------------------------------

func TestToolMediaGenerate_GPTImage_B64_StorageUpload(t *testing.T) {
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

	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":   "image",
		"prompt": "moonlit owl",
		"model":  "gpt-image-2",
		"options": map[string]any{
			"output_format": "png",
		},
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
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

	if pf.executeCalls[0].Input["model"] != "gpt-image-2" {
		t.Errorf("model not forwarded: %+v", pf.executeCalls[0].Input)
	}
	if pf.executeCalls[0].Input["output_format"] != "png" {
		t.Errorf("output_format not forwarded: %+v", pf.executeCalls[0].Input)
	}

	res := out.(map[string]any)
	meta := res["_meta"].(map[string]any)
	ids := meta["storage_ids"].([]int64)
	if len(ids) != 1 || ids[0] != 7777 {
		t.Errorf("storage_ids = %+v", ids)
	}
}

// --- storage URL surfacing -----------------------------------------

func TestToolMediaGenerate_WithStorage_OmitsInlineImage_AddsURLs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG())
	}))
	defer upstream.Close()

	pf := newRecordingPlatform()
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(fmt.Sprintf(
			`{"data":[{"url":"%s/img.png"}]}`, upstream.URL,
		)),
	}
	pf.nextCallResult = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":1234}"}]}}`,
	)

	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{"kind": "image", "prompt": "x"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)

	meta := res["_meta"].(map[string]any)
	urls, ok := meta["storage_urls"].([]string)
	if !ok || len(urls) != 1 {
		t.Fatalf("storage_urls missing or wrong length: %+v", meta["storage_urls"])
	}
	if !strings.Contains(urls[0], "/api/apps/storage/files/1234/content") {
		t.Errorf("storage URL format unexpected: %q", urls[0])
	}
	if !strings.Contains(urls[0], "project_id=test-proj") {
		t.Errorf("storage URL missing project_id: %q", urls[0])
	}

	content := res["content"].([]map[string]any)
	for _, c := range content {
		if c["type"] == "image" {
			t.Errorf("expected NO inline image when storage saved, got %+v", c)
		}
	}

	var foundURL bool
	for _, c := range content {
		if c["type"] == "text" {
			if s, _ := c["text"].(string); strings.Contains(s, "/api/apps/storage/files/1234/content") {
				foundURL = true
			}
		}
	}
	if !foundURL {
		t.Errorf("text block doesn't reference the storage URL; got %+v", content)
	}

	var foundResource bool
	for _, c := range content {
		if c["type"] == "resource" {
			r := c["resource"].(map[string]any)
			uri, _ := r["uri"].(string)
			if strings.HasPrefix(uri, "/api/apps/storage/") {
				foundResource = true
			}
		}
	}
	if !foundResource {
		t.Errorf("expected resource block with fetchable URI; got %+v", content)
	}
}

func TestToolMediaGenerate_NoStorage_KeepsInlineImage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fakePNG())
	}))
	defer upstream.Close()

	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{"image_provider": float64(42)}
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(fmt.Sprintf(`{"data":[{"url":"%s/img.png"}]}`, upstream.URL)),
	}

	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, _ := app.toolMediaGenerate(ctx, map[string]any{"kind": "image", "prompt": "x"})
	res := out.(map[string]any)
	content := res["content"].([]map[string]any)
	var hasImage bool
	for _, c := range content {
		if c["type"] == "image" {
			hasImage = true
		}
	}
	if !hasImage {
		t.Error("expected inline image block when storage is unbound")
	}
}

func TestToolMediaHistory_IncludesStorageURLs(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	app := &App{}
	app.dbInsertGeneration(generationRecord{
		ProjectID: "test-proj", Kind: "image", Prompt: "p1",
		Provider: "openai-api", Model: "gpt-image-2", Size: "1024x1024",
		StorageIDs: []int64{42, 99}, Count: 2,
	})
	out, err := app.toolMediaHistory(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	gens := out.(map[string]any)["generations"].([]map[string]any)
	if len(gens) != 1 {
		t.Fatalf("expected 1 row, got %d", len(gens))
	}
	urls, ok := gens[0]["storage_urls"].([]string)
	if !ok || len(urls) != 2 {
		t.Fatalf("storage_urls = %+v", gens[0]["storage_urls"])
	}
	if !strings.Contains(urls[0], "/files/42/content") || !strings.Contains(urls[1], "/files/99/content") {
		t.Errorf("URLs malformed: %+v", urls)
	}
}

// --- storageContentURL ---------------------------------------------

func TestStorageContentURL(t *testing.T) {
	got := storageContentURL(123, "proj-abc")
	want := "/api/apps/storage/files/123/content?project_id=proj-abc"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPickExt(t *testing.T) {
	cases := map[string]string{
		"":     "png",
		"png":  "png",
		"jpeg": "jpg",
		"jpg":  "jpg",
		"webp": "webp",
		"gif":  "png",
	}
	for in, want := range cases {
		if got := pickExt(in); got != want {
			t.Errorf("pickExt(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- image edit path (reference-image / source_image) --------------

func TestResolveImageCapability(t *testing.T) {
	if got := resolveImageCapability(map[string]any{}); got != "image.generate" {
		t.Errorf("no source_image → got %q, want image.generate", got)
	}
	if got := resolveImageCapability(map[string]any{"source_image": "storage:1"}); got != "image.edit" {
		t.Errorf("source_image set → got %q, want image.edit", got)
	}
	if got := resolveImageCapability(map[string]any{"source_image": "   "}); got != "image.generate" {
		t.Errorf("whitespace-only source_image → got %q, want image.generate (treated as empty)", got)
	}
}

func TestBuildVeniceImageEditArgs(t *testing.T) {
	args := map[string]any{
		"prompt":       "remove the tree",
		"source_image": "AAAA",
		"model":        "qwen-edit",
		"options": map[string]any{
			"aspect_ratio":  "16:9",
			"resolution":    "2K",
			"output_format": "png",
			"safe_mode":     false,
		},
	}
	got, err := buildImageArgs(args, "venice-ai", "image.edit")
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "qwen-edit" || got["prompt"] != "remove the tree" || got["image"] != "AAAA" {
		t.Errorf("base fields: %+v", got)
	}
	if got["aspect_ratio"] != "16:9" || got["resolution"] != "2K" || got["output_format"] != "png" {
		t.Errorf("options not passed through: %+v", got)
	}
	if got["safe_mode"] != false {
		t.Errorf("safe_mode not passed through: %+v", got["safe_mode"])
	}
}

func TestBuildVeniceImageEditArgs_DefaultModel(t *testing.T) {
	got, err := buildImageArgs(map[string]any{
		"prompt":       "x",
		"source_image": "AAAA",
	}, "venice-ai", "image.edit")
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "firered-image-edit" {
		t.Errorf("default model = %v, want firered-image-edit", got["model"])
	}
}

func TestBuildImageArgs_OpenAIEdit_NotWired(t *testing.T) {
	_, err := buildImageArgs(map[string]any{"prompt": "x", "source_image": "AAAA"}, "openai-api", "image.edit")
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Errorf("expected 'not wired' error for openai edit, got %v", err)
	}
}

func TestNormalizeImageEditResponse_VeniceBinary(t *testing.T) {
	body := `{"_binary":true,"base64":"SGVsbG8=","mimeType":"image/png","size":5}`
	imgs, _, _, err := normalizeImageResponse("venice-ai", "image.edit", json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 {
		t.Fatalf("expected 1 image, got %d", len(imgs))
	}
	if imgs[0].B64 != "SGVsbG8=" || imgs[0].MimeType != "image/png" || imgs[0].Ext != "png" {
		t.Errorf("decoded mismatch: %+v", imgs[0])
	}
}

func TestNormalizeImageEditResponse_MissingBinary(t *testing.T) {
	body := `{"some":"json"}`
	_, _, _, err := normalizeImageResponse("venice-ai", "image.edit", json.RawMessage(body))
	if err == nil || !strings.Contains(err.Error(), "missing binary") {
		t.Errorf("expected 'missing binary' error, got %v", err)
	}
}

// resolveSourceImage unit coverage

func TestResolveSourceImage_URLPassthrough(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	got, err := resolveSourceImage(ctx, "https://example.com/x.png")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/x.png" {
		t.Errorf("URL should pass through unchanged, got %q", got)
	}
}

func TestResolveSourceImage_Base64Passthrough(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	got, err := resolveSourceImage(ctx, "AAECAwQ=")
	if err != nil {
		t.Fatal(err)
	}
	if got != "AAECAwQ=" {
		t.Errorf("base64 should pass through unchanged, got %q", got)
	}
}

func TestResolveSourceImage_StorageHandle(t *testing.T) {
	pf := newRecordingPlatform()
	// Storage's files_get_content returns content_base64 in the MCP envelope.
	pf.perAppCallResults = map[string]json.RawMessage{
		"storage:files_get_content": json.RawMessage(
			`{"result":{"content":[{"type":"text","text":"{\"content_base64\":\"RkFLRUJZVEVT\"}"}]}}`,
		),
	}
	ctx := newMediaStudioCtx(t, pf)
	got, err := resolveSourceImage(ctx, "storage:1234")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "RkFLRUJZVEVT" {
		t.Errorf("got %q, want RkFLRUJZVEVT", got)
	}
	if len(pf.callAppCalls) != 1 || pf.callAppCalls[0].Tool != "files_get_content" {
		t.Errorf("expected files_get_content call, got %+v", pf.callAppCalls)
	}
	if id, _ := pf.callAppCalls[0].Input["id"].(int64); id != 1234 {
		t.Errorf("storage id passed through wrong: %+v", pf.callAppCalls[0].Input)
	}
}

func TestResolveSourceImage_StorageMalformedHandle(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	_, err := resolveSourceImage(ctx, "storage:abc")
	if err == nil || !strings.Contains(err.Error(), "malformed storage handle") {
		t.Errorf("expected malformed-handle error, got %v", err)
	}
}

func TestResolveSourceImage_Empty(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	_, err := resolveSourceImage(ctx, "  ")
	if err == nil {
		t.Error("expected error on empty source")
	}
}

// Full toolMediaGenerate edit-path coverage

func TestToolMediaGenerate_Image_EditPath_VeniceStorageSource(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	// Storage returns the source bytes; Venice returns a binary envelope;
	// storage save returns id 5555.
	pf.perAppCallResults = map[string]json.RawMessage{
		"storage:files_get_content": json.RawMessage(
			`{"result":{"content":[{"type":"text","text":"{\"content_base64\":\"U09VUkNF\"}"}]}}`,
		),
		"storage:files_upload": json.RawMessage(
			`{"result":{"content":[{"type":"text","text":"{\"id\":5555}"}]}}`,
		),
	}
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"_binary":true,"base64":"RURJVA==","mimeType":"image/png","size":4}`),
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":         "image",
		"prompt":       "remove the tree",
		"source_image": "storage:1234",
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}

	// Provider call must have hit Venice's edit tool with the resolved bytes.
	if len(pf.executeCalls) != 1 {
		t.Fatalf("expected 1 ExecuteIntegrationTool call, got %d", len(pf.executeCalls))
	}
	if pf.executeCalls[0].Tool != "edit_image" {
		t.Errorf("tool = %q, want edit_image", pf.executeCalls[0].Tool)
	}
	if pf.executeCalls[0].Input["image"] != "U09VUkNF" {
		t.Errorf("source bytes not passed through: %+v", pf.executeCalls[0].Input)
	}

	// CallApp sequence: files_get_content (resolve) then files_upload (save).
	if len(pf.callAppCalls) < 2 {
		t.Fatalf("expected at least 2 CallApp invocations (resolve+save), got %d", len(pf.callAppCalls))
	}
	if pf.callAppCalls[0].Tool != "files_get_content" {
		t.Errorf("first CallApp = %q, want files_get_content", pf.callAppCalls[0].Tool)
	}
	if pf.callAppCalls[1].Tool != "files_upload" {
		t.Errorf("second CallApp = %q, want files_upload", pf.callAppCalls[1].Tool)
	}

	// _meta carries kind + storage id.
	res := out.(map[string]any)
	meta := res["_meta"].(map[string]any)
	if meta["kind"] != "image" {
		t.Errorf("_meta.kind = %v", meta["kind"])
	}
	ids := meta["storage_ids"].([]int64)
	if len(ids) != 1 || ids[0] != 5555 {
		t.Errorf("storage_ids = %+v", ids)
	}

	// History row carries the source_image_ref lineage in extra_json.
	var extraJSON string
	if err := ctx.AppDB().QueryRow(`SELECT extra_json FROM generations LIMIT 1`).Scan(&extraJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(extraJSON, "source_image_ref") || !strings.Contains(extraJSON, "storage:1234") {
		t.Errorf("extra_json missing source_image_ref lineage: %s", extraJSON)
	}
	if !strings.Contains(extraJSON, `"capability":"image.edit"`) {
		t.Errorf("extra_json missing capability marker: %s", extraJSON)
	}
}

func TestToolMediaGenerate_Image_EditPath_URLSource_NoResolveCall(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"_binary":true,"base64":"RURJVA==","mimeType":"image/png"}`),
	}
	// No storage binding — confirms URL source skips files_get_content.
	pf.identity.Bindings = map[string]any{"image_provider": float64(42)}

	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	_, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":         "image",
		"prompt":       "make sunset",
		"source_image": "https://upstream/ref.png",
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}

	if len(pf.callAppCalls) != 0 {
		t.Errorf("URL source must NOT call CallApp (no storage resolve, no storage save when unbound), got %+v", pf.callAppCalls)
	}
	if got, _ := pf.executeCalls[0].Input["image"].(string); got != "https://upstream/ref.png" {
		t.Errorf("URL not passed through to provider: %q", got)
	}
}

func TestToolMediaGenerate_Image_EditPath_ProviderDoesNotSupportEdit(t *testing.T) {
	pf := newRecordingPlatform()
	// Default appSlug=openai-api. The manifest binds image.edit→edit_image,
	// but bound.ToolFor("image.edit") returns the binding name regardless;
	// the openai-api buildArgs path then refuses. Either way the result
	// is a clean mcpError, not a panic.
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":         "image",
		"prompt":       "x",
		"source_image": "https://upstream/ref.png",
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError=true when openai-api routed to edit, got %+v", res)
	}
}
