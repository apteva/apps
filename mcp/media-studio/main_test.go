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
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
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
	perExecuteResults map[string]*sdk.ExecuteResult
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
	keyed := p.perExecuteResults[tool]
	p.mu.Unlock()
	if p.nextExecuteErr != nil {
		return nil, p.nextExecuteErr
	}
	if keyed != nil {
		return keyed, nil
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

func newGlobalMediaStudioCtx(t *testing.T, pf sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	t.Setenv("APTEVA_PROJECT_ID", "")
	rec := tk.NewEmitRecorder()
	opts := []tk.Option{
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

func fakeJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf strings.Builder
	if err := jpeg.Encode(&stringWriter{&buf}, img, &jpeg.Options{Quality: 80}); err != nil {
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

func TestNormalizeImageResponse_OpenAICodex_B64(t *testing.T) {
	body := `{"data":[{"b64_json":"AAECAwQ=","revised_prompt":"a precise red door"}],"model":"gpt-5.5"}`
	imgs, revised, model, err := normalizeImageResponse("openai-codex", "image.generate", json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].B64 != "AAECAwQ=" {
		t.Errorf("imgs = %+v", imgs)
	}
	if revised != "a precise red door" {
		t.Errorf("revised = %q", revised)
	}
	if model != "gpt-5.5" {
		t.Errorf("model = %q, want gpt-5.5", model)
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

func TestToolMediaGenerate_Image_CustomStorageFolder(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG())
	}))
	defer upstream.Close()

	pf := newRecordingPlatform()
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(fmt.Sprintf(`{"data":[{"url":"%s/img.png"}]}`, upstream.URL)),
	}
	pf.nextCallResult = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":4321}"}]}}`,
	)

	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":           "image",
		"prompt":         "folder test",
		"storage_folder": "campaigns/launch",
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}
	if len(pf.callAppCalls) != 1 {
		t.Fatalf("expected 1 storage call, got %d", len(pf.callAppCalls))
	}
	if folder, _ := pf.callAppCalls[0].Input["folder"].(string); folder != "/campaigns/launch/" {
		t.Fatalf("storage folder = %q, want /campaigns/launch/", folder)
	}
	meta := out.(map[string]any)["_meta"].(map[string]any)
	if meta["storage_folder"] != "/campaigns/launch/" {
		t.Fatalf("storage_folder meta = %#v", meta["storage_folder"])
	}
	var extraJSON string
	if err := ctx.AppDB().QueryRow(`SELECT extra_json FROM generations LIMIT 1`).Scan(&extraJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(extraJSON, "/campaigns/launch/") {
		t.Fatalf("extra_json missing storage folder: %s", extraJSON)
	}
}

func TestToolMediaGenerate_Image_DraftDoesNotCallProvider(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}

	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":   "image",
		"prompt": "launch campaign concept",
		"model":  "gpt-image-2",
		"mode":   "draft",
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}
	meta := out.(map[string]any)["_meta"].(map[string]any)
	if meta["status"] != "draft" {
		t.Fatalf("status = %v, want draft", meta["status"])
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("draft should not call provider, got %+v", pf.executeCalls)
	}
	id := meta["generation_id"].(int64)
	var status, requestJSON string
	if err := ctx.AppDB().QueryRow(`SELECT status, request_json FROM generations WHERE id=?`, id).Scan(&status, &requestJSON); err != nil {
		t.Fatal(err)
	}
	if status != "draft" {
		t.Fatalf("db status = %q, want draft", status)
	}
	if !strings.Contains(requestJSON, "launch campaign concept") {
		t.Fatalf("request_json did not preserve prompt: %s", requestJSON)
	}
}

func TestToolMediaGenerate_Image_GenerateDraftUpdatesSameRow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG())
	}))
	defer upstream.Close()

	pf := newRecordingPlatform()
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(fmt.Sprintf(`{"data":[{"url":"%s/img.png"}]}`, upstream.URL)),
	}
	pf.nextCallResult = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":9876}"}]}}`,
	)
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}

	draftOut, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":   "image",
		"prompt": "generate this later",
		"model":  "gpt-image-2",
		"mode":   "draft",
	})
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	draftID := draftOut.(map[string]any)["_meta"].(map[string]any)["generation_id"].(int64)

	out, err := app.toolMediaGenerate(ctx, map[string]any{"generation_id": draftID})
	if err != nil {
		t.Fatalf("generate draft: %v", err)
	}
	meta := out.(map[string]any)["_meta"].(map[string]any)
	if meta["generation_id"] != draftID {
		t.Fatalf("generation_id = %v, want existing draft id %d", meta["generation_id"], draftID)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM generations`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected draft row to be updated in place, got %d rows", count)
	}
	var status, storageIDs string
	if err := ctx.AppDB().QueryRow(`SELECT status, storage_ids FROM generations WHERE id=?`, draftID).Scan(&status, &storageIDs); err != nil {
		t.Fatal(err)
	}
	if status != "ready" || !strings.Contains(storageIDs, "9876") {
		t.Fatalf("row not completed in place: status=%q storage_ids=%s", status, storageIDs)
	}
}

func TestNormalizeStorageFolderRejectsParentTraversal(t *testing.T) {
	if got, err := normalizeStorageFolder("campaigns/launch"); err != nil || got != "/campaigns/launch/" {
		t.Fatalf("normalize = %q, %v", got, err)
	}
	if _, err := normalizeStorageFolder("/campaigns/../private"); err == nil {
		t.Fatal("expected parent traversal to be rejected")
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

func TestMediaDelete_RemovesGenerationStorageAndJob(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	id := app.dbInsertGeneration(generationRecord{
		ProjectID:  "test-proj",
		Kind:       "video",
		Prompt:     "delete me",
		Provider:   "venice-ai",
		Model:      "veo3.1-fast-text-to-video",
		StorageIDs: []int64{123, 456},
		Count:      1,
	})
	if id == 0 {
		t.Fatal("insert generation returned id=0")
	}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO video_jobs
			(project_id, queue_id, provider, model, prompt, status, generation_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"test-proj", "q-delete", "venice-ai", "veo3.1-fast-text-to-video", "delete me", "complete", id,
	); err != nil {
		t.Fatal(err)
	}

	out, err := app.deleteGeneration(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	if !out["deleted"].(bool) {
		t.Fatalf("deleted flag false: %+v", out)
	}
	if len(pf.callAppCalls) != 2 {
		t.Fatalf("expected 2 storage delete calls, got %+v", pf.callAppCalls)
	}
	for i, want := range []int64{123, 456} {
		got := pf.callAppCalls[i]
		if got.AppName != "storage" || got.Tool != "files_delete" {
			t.Fatalf("storage call %d = %+v", i, got)
		}
		if got.Input["id"] != want || got.Input["keep_record"] != false {
			t.Fatalf("storage call %d input = %+v", i, got.Input)
		}
		if got.Input["_project_id"] != "test-proj" {
			t.Fatalf("storage call %d _project_id = %v", i, got.Input["_project_id"])
		}
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM generations WHERE id=?`, id).Scan(&count)
	if count != 0 {
		t.Fatalf("generation row still exists")
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM video_jobs WHERE generation_id=?`, id).Scan(&count)
	if count != 0 {
		t.Fatalf("video job row still exists")
	}
}

func TestMediaDelete_CanKeepStorageFiles(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	id := app.dbInsertGeneration(generationRecord{
		ProjectID:  "test-proj",
		Kind:       "image",
		Prompt:     "draft",
		Provider:   "openai-api",
		StorageIDs: []int64{789},
		Count:      1,
	})
	if _, err := app.deleteGeneration(ctx, map[string]any{"id": id, "delete_storage": false}); err != nil {
		t.Fatal(err)
	}
	if len(pf.callAppCalls) != 0 {
		t.Fatalf("delete_storage=false should not call storage, got %+v", pf.callAppCalls)
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

func TestBuildVeniceImageArgs_DerivesAspectFromGenericSize(t *testing.T) {
	got, err := buildImageArgs(map[string]any{
		"model":  "flux-2-pro",
		"prompt": "portrait pasta still",
		"size":   "720x1280",
	}, "venice-ai", "image.generate")
	if err != nil {
		t.Fatal(err)
	}
	if got["width"] != 720 || got["height"] != 1280 {
		t.Fatalf("pixel width/height not preserved: %+v", got)
	}
	if got["aspect_ratio"] != "9:16" {
		t.Fatalf("aspect_ratio = %v, want 9:16: %+v", got["aspect_ratio"], got)
	}
}

func TestBuildVeniceImageArgs_GenericAspectAndResolution(t *testing.T) {
	got, err := buildImageArgs(map[string]any{
		"model":      "gpt-image-2",
		"prompt":     "portrait pasta still",
		"aspect":     "9:16",
		"resolution": "2K",
		"options": map[string]any{
			"output_format": "jpeg",
			"resolution":    "1024x1536",
		},
	}, "venice-ai", "image.generate")
	if err != nil {
		t.Fatal(err)
	}
	if got["aspect_ratio"] != "9:16" || got["resolution"] != "2K" {
		t.Fatalf("generic aspect/resolution not translated: %+v", got)
	}
	if got["format"] != "jpeg" {
		t.Fatalf("output_format should map to Venice format: %+v", got)
	}
}

func TestBuildVeniceImageArgs_NormalizesAgentShapeMistakes(t *testing.T) {
	got, err := buildImageArgs(map[string]any{
		"model":      "flux-2-pro",
		"prompt":     "portrait pasta still",
		"resolution": "720x1280",
	}, "venice-ai", "image.generate")
	if err != nil {
		t.Fatal(err)
	}
	if got["width"] != 720 || got["height"] != 1280 || got["aspect_ratio"] != "9:16" {
		t.Fatalf("pixel-looking resolution should normalize to pixels+aspect: %+v", got)
	}
	if _, ok := got["resolution"]; ok {
		t.Fatalf("pixel-looking resolution must not be sent as Venice tier: %+v", got)
	}

	got, err = buildImageArgs(map[string]any{
		"model":  "flux-2-pro",
		"prompt": "portrait pasta still",
		"size":   "9:16",
	}, "venice-ai", "image.generate")
	if err != nil {
		t.Fatal(err)
	}
	if got["aspect_ratio"] != "9:16" {
		t.Fatalf("aspect-looking size should normalize to aspect_ratio: %+v", got)
	}
	if _, ok := got["width"]; ok {
		t.Fatalf("aspect-looking size must not create pixel width: %+v", got)
	}

	got, err = buildImageArgs(map[string]any{
		"model":  "gpt-image-2",
		"prompt": "portrait pasta still",
		"options": map[string]any{
			"resolution": "9:16",
		},
	}, "venice-ai", "image.generate")
	if err != nil {
		t.Fatal(err)
	}
	if got["aspect_ratio"] != "9:16" {
		t.Fatalf("aspect-looking options.resolution should normalize to aspect_ratio: %+v", got)
	}
	if _, ok := got["resolution"]; ok {
		t.Fatalf("aspect-looking options.resolution must not be sent as Venice tier: %+v", got)
	}
}

func TestBuildModelEntryFromVeniceSpec_SurfaceImageConstraints(t *testing.T) {
	raw := json.RawMessage(`{
		"id":"flux-2-pro",
		"model_spec":{
			"constraints":{
				"aspectRatios":["1:1","9:16"],
				"defaultAspectRatio":"1:1",
				"steps":{"default":20,"max":50},
				"promptCharacterLimit":3000
			},
			"pricing":{"generation":{"usd":0.04}}
		}
	}`)
	got := buildModelEntryFromVeniceSpec("flux-2-pro", raw, "image")
	if strings.Join(got.SizeModes, ",") != "aspect" {
		t.Fatalf("size modes = %+v, want aspect", got.SizeModes)
	}
	if len(got.AspectRatios) != 2 || got.AspectRatios[1] != "9:16" || got.DefaultAspectRatio != "1:1" {
		t.Fatalf("aspect constraints not surfaced: %+v", got)
	}
	if got.PriceUSD != 0.04 {
		t.Fatalf("price = %v, want 0.04", got.PriceUSD)
	}
}

func TestBuildOpenAICodexImageArgs(t *testing.T) {
	args, err := buildImageArgs(map[string]any{
		"prompt": "draw a door",
		"model":  "gpt-image-2",
		"size":   "1024x1536",
		"options": map[string]any{
			"quality":            "high",
			"output_format":      "webp",
			"background":         "transparent",
			"output_compression": 80,
		},
	}, "openai-codex", "image.generate")
	if err != nil {
		t.Fatal(err)
	}
	if args["model"] != "gpt-5.5" || args["prompt"] != "draw a door" || args["size"] != "1024x1536" {
		t.Fatalf("base args wrong: %+v", args)
	}
	if strings.TrimSpace(fmt.Sprint(args["instructions"])) == "" {
		t.Fatalf("instructions missing: %+v", args)
	}
	if args["quality"] != "high" || args["output_format"] != "webp" || args["background"] != "transparent" {
		t.Fatalf("image_generation options missing: %+v", args)
	}
	if args["output_compression"] != 80 {
		t.Fatalf("output_compression = %v, want 80", args["output_compression"])
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

func TestSnapDurationToSupported_ChoosesNextSupportedDuration(t *testing.T) {
	supported := []string{"3s", "5s", "8s", "10s", "15s"}
	if got := snapDurationToSupported(8.55, supported); got != 10 {
		t.Fatalf("duration = %d, want 10", got)
	}
	if got := snapDurationToSupported(3, supported); got != 3 {
		t.Fatalf("exact duration = %d, want 3", got)
	}
	if got := snapDurationToSupported(16, supported); got != 15 {
		t.Fatalf("overflow duration = %d, want largest supported 15", got)
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
	if got.Input["_project_id"] != "test-proj" {
		t.Errorf("_project_id = %v, want test-proj", got.Input["_project_id"])
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

func TestToolMediaGenerate_VeniceB64_SniffsJPEGDespitePNGFormat(t *testing.T) {
	jpegB64 := base64.StdEncoding.EncodeToString(fakeJPEG())

	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(fmt.Sprintf(
			`{"id":"venice-1","images":[%q],"request":{"success":true,"data":{"format":"png","model":"grok-imagine-image"}}}`,
			jpegB64,
		)),
	}
	pf.nextCallResult = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":4818,\"url\":\"/files/4818\"}"}]}}`,
	)

	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":   "image",
		"prompt": "studio portrait",
		"model":  "grok-imagine-image",
		"options": map[string]any{
			"format": "png",
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
		t.Fatalf("storage tool = %q, want files_upload", got.Tool)
	}
	if ct, _ := got.Input["content_type"].(string); ct != "image/jpeg" {
		t.Fatalf("content_type = %q, want image/jpeg; input=%+v", ct, got.Input)
	}
	if name, _ := got.Input["name"].(string); !strings.HasSuffix(name, ".jpg") {
		t.Fatalf("name = %q, want .jpg suffix", name)
	}
	if cb, _ := got.Input["content_base64"].(string); cb != jpegB64 {
		t.Fatalf("content_base64 changed")
	}

	res := out.(map[string]any)
	content := res["content"].([]map[string]any)
	var foundJPEGResource bool
	for _, block := range content {
		if block["type"] != "resource" {
			continue
		}
		resource := block["resource"].(map[string]any)
		if resource["mimeType"] == "image/jpeg" {
			foundJPEGResource = true
		}
	}
	if !foundJPEGResource {
		t.Fatalf("expected image/jpeg resource block, got %+v", content)
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

func TestHandleGenerate_GlobalInstallThreadsProjectToStorage(t *testing.T) {
	pngB64 := base64.StdEncoding.EncodeToString(fakePNG())
	pf := newRecordingPlatform()
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(fmt.Sprintf(
			`{"data":[{"b64_json":%q,"mime_type":"image/png"}],"model":"gpt-image-2"}`,
			pngB64,
		)),
	}
	pf.nextCallResult = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":4321}"}]}}`,
	)
	newGlobalMediaStudioCtx(t, pf)
	app := &App{}

	body := strings.NewReader(`{"kind":"image","prompt":"x","project_id":"panel-proj"}`)
	req := httptest.NewRequest(http.MethodPost, "/generate", body)
	rec := httptest.NewRecorder()
	app.handleGenerate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(pf.callAppCalls) != 1 {
		t.Fatalf("expected 1 storage call, got %d", len(pf.callAppCalls))
	}
	if got := pf.callAppCalls[0].Input["_project_id"]; got != "panel-proj" {
		t.Fatalf("storage _project_id = %v, want panel-proj; input=%+v", got, pf.callAppCalls[0].Input)
	}
	var storedProject string
	if err := globalCtx.AppDB().QueryRow(`SELECT project_id FROM generations ORDER BY id DESC LIMIT 1`).Scan(&storedProject); err != nil {
		t.Fatal(err)
	}
	if storedProject != "panel-proj" {
		t.Fatalf("generation project_id = %q, want panel-proj", storedProject)
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
	if got := resolveImageCapability(map[string]any{"source_images": []any{"storage:1", "storage:2"}}); got != "image.edit" {
		t.Errorf("source_images set → got %q, want image.edit", got)
	}
	if got := resolveImageCapability(map[string]any{"source_image": "   "}); got != "image.generate" {
		t.Errorf("whitespace-only source_image → got %q, want image.generate (treated as empty)", got)
	}
}

func TestVeniceTypeForCapability(t *testing.T) {
	if got := veniceTypeForCapability(KindImage, ""); got != "image" {
		t.Errorf("image generate type = %q, want image", got)
	}
	if got := veniceTypeForCapability(KindImage, "image.edit"); got != "inpaint" {
		t.Errorf("image.edit type = %q, want inpaint", got)
	}
	if got := veniceTypeForCapability(KindVideo, "image.edit"); got != "video" {
		t.Errorf("video type = %q, want video", got)
	}
}

func TestFilterModelsForImageEditCapability(t *testing.T) {
	models := []modelEntry{
		{ID: "gpt-image-2", SupportsImageEdit: true},
		{ID: "dall-e-3"},
		{ID: "firered-image-edit", SupportsImageEdit: true},
	}
	got := filterModelsForCapability(models, "image.edit")
	if len(got) != 2 {
		t.Fatalf("got %d edit models, want 2: %+v", len(got), got)
	}
	for _, model := range got {
		if !model.SupportsImageEdit {
			t.Fatalf("non-edit model leaked into image.edit list: %+v", got)
		}
	}
	if got := filterModelsForCapability(models, ""); len(got) != len(models) {
		t.Fatalf("plain model list was filtered: %+v", got)
	}
}

func TestModelCatalogForImageDefaultUsesGenerateCapability(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	pf.identity.Bindings["image_provider"] = float64(4242)
	pf.perExecuteResults = map[string]*sdk.ExecuteResult{
		"list_models": {
			Success: true,
			Status:  200,
			Data: json.RawMessage(`{"data":[
				{"id":"grok-imagine-image","model_spec":{"constraints":{"aspectRatios":["1:1","16:9"],"defaultAspectRatio":"1:1"}}}
			]}`),
		},
	}
	ctx := newMediaStudioCtx(t, pf)

	out, err := modelCatalogForKind(ctx, KindImage, "")
	if err != nil {
		t.Fatalf("modelCatalogForKind: %v", err)
	}
	if out["bound"] != true {
		t.Fatalf("bound = %v, want true; out=%+v", out["bound"], out)
	}
	models, ok := out["models"].([]modelEntry)
	if !ok || len(models) != 1 || models[0].ID != "grok-imagine-image" {
		t.Fatalf("models = %#v, want grok-imagine-image", out["models"])
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("ExecuteIntegrationTool calls = %d, want 1", len(pf.executeCalls))
	}
	if pf.executeCalls[0].Tool != "list_models" || pf.executeCalls[0].Input["type"] != "image" {
		t.Fatalf("provider call = %+v, want list_models type=image", pf.executeCalls[0])
	}
}

func TestBuildVeniceImageEditArgs_DropsUnsupportedModelResolution(t *testing.T) {
	for _, model := range []string{
		"qwen-edit",
		"qwen-image-2-edit",
		"qwen-image-2-pro-edit",
		"flux-2-max-edit",
		"gpt-image-1-5-edit",
		"seedream-v5-lite-edit",
	} {
		t.Run(model, func(t *testing.T) {
			args := map[string]any{
				"prompt":       "remove the tree",
				"source_image": "AAAA",
				"model":        model,
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
			if got["model"] != model || got["prompt"] != "remove the tree" || got["image"] != "AAAA" {
				t.Errorf("base fields: %+v", got)
			}
			if got["aspect_ratio"] != "16:9" || got["output_format"] != "png" {
				t.Errorf("options not passed through: %+v", got)
			}
			if _, ok := got["resolution"]; ok {
				t.Errorf("%s must not receive resolution: %+v", model, got)
			}
			if got["safe_mode"] != false {
				t.Errorf("safe_mode not passed through: %+v", got["safe_mode"])
			}
		})
	}
}

func TestBuildVeniceImageEditArgs_KeepsSupportedModelResolution(t *testing.T) {
	for _, model := range []string{
		"grok-imagine-edit",
		"gpt-image-2-edit",
		"nano-banana-2-edit",
		"nano-banana-pro-edit",
	} {
		t.Run(model, func(t *testing.T) {
			args := map[string]any{
				"prompt":       "update the style",
				"source_image": "AAAA",
				"model":        model,
				"options": map[string]any{
					"aspect_ratio":  "9:16",
					"resolution":    "1K",
					"output_format": "png",
				},
			}
			got, err := buildImageArgs(args, "venice-ai", "image.edit")
			if err != nil {
				t.Fatal(err)
			}
			if got["resolution"] != "1K" {
				t.Fatalf("%s should keep tier resolution: %+v", model, got)
			}
		})
	}
}

func TestBuildVeniceImageMultiEditArgs(t *testing.T) {
	args := map[string]any{
		"prompt":        "blend the two images",
		"source_images": []string{"AAAA", "BBBB"},
		"model":         "gpt-image-2-edit",
		"options": map[string]any{
			"aspect_ratio":  "16:9",
			"resolution":    "2K",
			"output_format": "webp",
			"quality":       "medium",
			"safe_mode":     false,
		},
	}
	got, err := buildImageArgs(args, "venice-ai", "image.edit")
	if err != nil {
		t.Fatal(err)
	}
	if got["modelId"] != "gpt-image-2-edit" || got["prompt"] != "blend the two images" {
		t.Errorf("base fields: %+v", got)
	}
	images, ok := got["images"].([]string)
	if !ok || len(images) != 2 || images[0] != "AAAA" || images[1] != "BBBB" {
		t.Fatalf("images = %#v", got["images"])
	}
	if got["aspect_ratio"] != "16:9" || got["resolution"] != "2K" || got["output_format"] != "webp" || got["quality"] != "medium" {
		t.Errorf("options not passed through: %+v", got)
	}
}

func TestBuildVeniceImageMultiEditArgs_DropsUnsupportedAutoQualityAndPixelResolution(t *testing.T) {
	args := map[string]any{
		"prompt":        "blend the two images",
		"source_images": []string{"AAAA", "BBBB"},
		"model":         "firered-image-edit",
		"options": map[string]any{
			"aspect_ratio":  "9:16",
			"resolution":    "1024x1536",
			"output_format": "png",
			"quality":       "auto",
			"safe_mode":     false,
		},
	}
	got, err := buildImageArgs(args, "venice-ai", "image.edit")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["quality"]; ok {
		t.Fatalf("quality=auto must not be sent to Venice multi-edit: %+v", got)
	}
	if _, ok := got["resolution"]; ok {
		t.Fatalf("pixel resolution must not be sent as Venice edit resolution: %+v", got)
	}
	if got["aspect_ratio"] != "9:16" || got["output_format"] != "png" || got["safe_mode"] != false {
		t.Fatalf("valid options not preserved: %+v", got)
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

func TestBuildImageArgs_OpenAIEdit_JSONImages(t *testing.T) {
	got, err := buildImageArgs(map[string]any{
		"prompt":        "make a party scene",
		"model":         "gpt-image-1.5",
		"source_images": []string{"AAAA", "https://example.com/ref.png"},
		"options": map[string]any{
			"quality":       "high",
			"output_format": "webp",
		},
	}, "openai-api", "image.edit")
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gpt-image-1.5" || got["prompt"] != "make a party scene" {
		t.Errorf("base fields: %+v", got)
	}
	images, ok := got["images"].([]map[string]any)
	if !ok || len(images) != 2 {
		t.Fatalf("images = %#v", got["images"])
	}
	if images[0]["image_url"] != "data:image/png;base64,AAAA" || images[1]["image_url"] != "https://example.com/ref.png" {
		t.Errorf("image refs = %#v", images)
	}
	if got["quality"] != "high" || got["output_format"] != "webp" {
		t.Errorf("options not passed through: %+v", got)
	}
}

func TestBuildImageArgs_GeminiEdit_InlineImages(t *testing.T) {
	got, err := buildImageArgs(map[string]any{
		"prompt":        "use these references",
		"model":         "gemini-2.5-flash-image",
		"source_images": []string{"AAAA", "data:image/jpeg;base64,BBBB"},
		"options": map[string]any{
			"aspect_ratio": "16:9",
		},
	}, "gemini", "image.edit")
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gemini-2.5-flash-image" {
		t.Errorf("model = %v", got["model"])
	}
	contents := got["contents"].([]map[string]any)
	parts := contents[0]["parts"].([]map[string]any)
	if len(parts) != 3 {
		t.Fatalf("parts = %#v", parts)
	}
	cfg := got["generationConfig"].(map[string]any)
	rf := cfg["responseFormat"].(map[string]any)
	img := rf["image"].(map[string]any)
	if img["aspectRatio"] != "16:9" {
		t.Errorf("aspect = %v", img["aspectRatio"])
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

func TestToolMediaGenerate_Image_MultiEditPath_VeniceStorageSources(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	pf.perAppCallResults = map[string]json.RawMessage{
		"storage:files_get_content": json.RawMessage(
			`{"result":{"content":[{"type":"text","text":"{\"content_base64\":\"U09VUkNF\"}"}]}}`,
		),
	}
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"_binary":true,"base64":"RURJVA==","mimeType":"image/png","size":4}`),
	}
	pf.identity.Bindings = map[string]any{"image_provider": float64(42)}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":          "image",
		"prompt":        "combine the outfit and face",
		"source_images": []any{"storage:1001", "https://example.com/outfit.png"},
		"model":         "firered-image-edit",
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}
	res := out.(map[string]any)
	if res["isError"] == true {
		t.Fatalf("unexpected error result: %+v", res)
	}
	var call executeCall
	for _, c := range pf.executeCalls {
		if c.Tool == "multi_edit_image" {
			call = c
			break
		}
	}
	if call.Tool == "" {
		t.Fatalf("expected multi_edit_image call, got %+v", pf.executeCalls)
	}
	images, ok := call.Input["images"].([]string)
	if !ok || len(images) != 2 || images[0] != "U09VUkNF" || images[1] != "https://example.com/outfit.png" {
		t.Fatalf("images not passed through: %#v", call.Input["images"])
	}
	if call.Input["image"] != nil {
		t.Fatalf("multi-edit payload should not include single image field: %+v", call.Input)
	}
	var extraJSON string
	if err := ctx.AppDB().QueryRow(`SELECT extra_json FROM generations LIMIT 1`).Scan(&extraJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(extraJSON, "source_image_refs") ||
		!strings.Contains(extraJSON, "storage:1001") ||
		!strings.Contains(extraJSON, "https://example.com/outfit.png") {
		t.Errorf("extra_json missing multi-source lineage: %s", extraJSON)
	}
}

func TestToolMediaGenerate_Image_MultiEditRejectsTooManySources(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":          "image",
		"prompt":        "combine",
		"source_images": []any{"a", "b", "c", "d"},
		"model":         "firered-image-edit",
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Fatalf("expected isError, got %+v", res)
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("provider should not be called when source limit is exceeded")
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

// --- video (async / queue) path -----------------------------------

func TestToolMediaGenerate_Video_VeniceQueue(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	pf.identity.Bindings["video_provider"] = float64(77)
	pf.perExecuteResults = map[string]*sdk.ExecuteResult{
		"list_models": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"data":[{"id":"kling-2","model_spec":{"constraints":{"model_type":"text-to-video","durations":["3s","5s","8s","10s"]}}}]}`),
		},
		"queue_video": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"model":"kling-2","queue_id":"q-abc-123"}`),
		},
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":     "video",
		"prompt":   "a cat walking through a sunlit garden",
		"model":    "kling-2",
		"duration": "9s",
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}
	res := out.(map[string]any)
	meta := res["_meta"].(map[string]any)
	if meta["status"] != "queued" || meta["queue_id"] != "q-abc-123" {
		t.Errorf("expected queued meta, got %+v", meta)
	}
	if len(pf.executeCalls) != 3 ||
		pf.executeCalls[0].Tool != "list_models" ||
		pf.executeCalls[1].Tool != "queue_video" ||
		pf.executeCalls[2].Tool != "quote_video" {
		t.Fatalf("provider calls = %+v, want list_models, queue_video, quote_video", pf.executeCalls)
	}
	if pf.executeCalls[1].Input["duration"] != "10s" {
		t.Errorf("duration not normalized: %+v", pf.executeCalls[1].Input)
	}

	// video_jobs row landed in 'queued' state.
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM video_jobs WHERE status='queued'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 queued video_jobs row, got %d", count)
	}
}

func TestToolMediaGenerate_Video_VeniceReferenceToVideo_MultipleSourceImages(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	pf.identity.Bindings["video_provider"] = float64(77)
	pf.perAppCallResults = map[string]json.RawMessage{
		"storage:files_get_content": json.RawMessage(
			`{"result":{"content":[{"type":"text","text":"{\"content_base64\":\"U09VUkNF\"}"}]}}`,
		),
	}
	pf.perExecuteResults = map[string]*sdk.ExecuteResult{
		"list_models": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"data":[{"id":"seedance-2-0-mini-enhanced-reference-to-video","model_spec":{"constraints":{"model_type":"image-to-video","durations":["5s","10s"]}}}]}`),
		},
		"queue_video": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"model":"seedance-2-0-mini-enhanced-reference-to-video","queue_id":"q-ref-123"}`),
		},
		"quote_video": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"data":{"quote":{"usd":0.12}}}`),
		},
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":          "video",
		"prompt":        "same presenter reviewing dotted black tights",
		"model":         "seedance-2-0-mini-enhanced-reference-to-video",
		"duration":      "5s",
		"source_images": []any{"storage:1001", "https://example.com/tights.png"},
		"options": map[string]any{
			"consents": map[string]any{
				"seedance": map[string]any{
					"confirmed_terms_and_privacy":      true,
					"confirmed_legal_right":            true,
					"confirmed_screening_acknowledged": true,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}
	res := out.(map[string]any)
	if res["isError"] == true {
		t.Fatalf("unexpected error result: %+v", res)
	}
	var call executeCall
	for _, c := range pf.executeCalls {
		if c.Tool == "queue_video" {
			call = c
			break
		}
	}
	if call.Tool == "" {
		t.Fatalf("expected queue_video call, got %+v", pf.executeCalls)
	}
	refs, ok := call.Input["reference_image_urls"].([]string)
	if !ok || len(refs) != 2 {
		t.Fatalf("reference_image_urls = %#v", call.Input["reference_image_urls"])
	}
	if !strings.HasPrefix(refs[0], "data:image/png;base64,U09VUkNF") {
		t.Fatalf("first reference was not resolved/wrapped as data URL: %q", refs[0])
	}
	if refs[1] != "https://example.com/tights.png" {
		t.Fatalf("second reference = %q", refs[1])
	}
	if _, exists := call.Input["image_url"]; exists {
		t.Fatalf("reference-to-video payload should not include image_url: %+v", call.Input)
	}
	if consents, ok := call.Input["consents"].(map[string]any); !ok || consents["seedance"] == nil {
		t.Fatalf("consents.seedance was not forwarded: %+v", call.Input)
	}
	meta := res["_meta"].(map[string]any)
	sourceRefs, ok := meta["source_image_refs"].([]string)
	if !ok || len(sourceRefs) != 2 || sourceRefs[0] != "storage:1001" || sourceRefs[1] != "https://example.com/tights.png" {
		t.Fatalf("source_image_refs meta = %#v", meta["source_image_refs"])
	}
}

func TestToolMediaGenerate_Video_VeniceImageToVideo_RejectsMultipleSourceImages(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	pf.identity.Bindings["video_provider"] = float64(77)
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":          "video",
		"prompt":        "animate this image",
		"model":         "seedance-2-0-mini-image-to-video",
		"duration":      "5s",
		"source_images": []any{"storage:1001", "https://example.com/ref2.png"},
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Fatalf("expected isError, got %+v", res)
	}
	txt := fmt.Sprint(res["content"])
	if !strings.Contains(txt, "at most 1 source image") {
		t.Fatalf("error did not explain source limit: %+v", res)
	}
	for _, c := range pf.executeCalls {
		if c.Tool == "queue_video" {
			t.Fatalf("provider should not be called when video source limit is exceeded: %+v", pf.executeCalls)
		}
	}
}

func TestToolMediaEstimate_Video_VeniceQuote(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	pf.identity.Bindings["video_provider"] = float64(77)
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"data":{"quote":{"usd":0.42}}}`),
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaEstimate(ctx, map[string]any{
		"kind":     "video",
		"model":    "kling-2",
		"duration": 10,
		"aspect":   "16:9",
		"options":  map[string]any{"audio": false},
	})
	if err != nil {
		t.Fatalf("toolMediaEstimate: %v", err)
	}
	meta := out.(map[string]any)["_meta"].(generationEstimate)
	if !meta.Available || meta.CostUSD != 0.42 || meta.Source != "provider_quote" {
		t.Fatalf("estimate meta = %+v", meta)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "quote_video" {
		t.Fatalf("expected quote_video call, got %+v", pf.executeCalls)
	}
	if got := pf.executeCalls[0].Input["duration"]; got != "10s" {
		t.Fatalf("quote duration = %v, want 10s", got)
	}
	if got := pf.executeCalls[0].Input["audio"]; got != false {
		t.Fatalf("quote audio = %v, want false", got)
	}
}

func TestVideoPollWorker_StillProcessing_BumpsAttempts(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	pf.identity.Bindings["video_provider"] = float64(77)
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"status":"PROCESSING","average_execution_time":120000,"execution_duration":40000}`),
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	// Seed a queued job.
	_, err := ctx.AppDB().Exec(
		`INSERT INTO video_jobs (project_id, queue_id, provider, model, prompt, status)
		 VALUES ('test-proj', 'q-1', 'venice-ai', 'kling-2', 'p', 'queued')`,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := app.videoPollWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}

	var status string
	var attempts int
	ctx.AppDB().QueryRow(`SELECT status, attempts FROM video_jobs WHERE queue_id='q-1'`).Scan(&status, &attempts)
	if status != "polling" {
		t.Errorf("status = %q, want polling", status)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

func TestVideoPollWorker_Completes_SavesToStorageAndInsertsGenerations(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	pf.identity.Bindings["video_provider"] = float64(77)
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"_binary":true,"base64":"VklERU8=","mimeType":"video/mp4","size":5}`),
	}
	pf.nextCallResult = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":9999}"}]}}`,
	)
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	_, err := ctx.AppDB().Exec(
		`INSERT INTO video_jobs (project_id, queue_id, provider, model, prompt, status)
		 VALUES ('test-proj', 'q-2', 'venice-ai', 'kling-2', 'sunset clip', 'queued')`,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := app.videoPollWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}

	var status string
	var storageID, generationID int64
	ctx.AppDB().QueryRow(
		`SELECT status, result_storage_id, generation_id FROM video_jobs WHERE queue_id='q-2'`,
	).Scan(&status, &storageID, &generationID)
	if status != "complete" {
		t.Errorf("status = %q, want complete", status)
	}
	if storageID != 9999 {
		t.Errorf("result_storage_id = %d, want 9999", storageID)
	}
	if generationID == 0 {
		t.Error("generation_id should be populated after complete")
	}

	// Storage save call must have used the videos folder.
	if len(pf.callAppCalls) == 0 || pf.callAppCalls[0].Tool != "files_upload" {
		t.Fatalf("expected files_upload, got %+v", pf.callAppCalls)
	}
	if folder, _ := pf.callAppCalls[0].Input["folder"].(string); folder != "/.generated/videos/" {
		t.Errorf("storage folder = %q, want /.generated/videos/", folder)
	}
	if got := pf.callAppCalls[0].Input["_project_id"]; got != "test-proj" {
		t.Errorf("storage _project_id = %v, want test-proj", got)
	}

	// Generations row exists with kind=video.
	var kind string
	ctx.AppDB().QueryRow(`SELECT kind FROM generations WHERE id=?`, generationID).Scan(&kind)
	if kind != "video" {
		t.Errorf("generations.kind = %q, want video", kind)
	}
}

func TestVideoPollWorker_UsesCustomStorageFolderFromRequestJSON(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "venice-ai"
	pf.identity.Bindings["video_provider"] = float64(77)
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"_binary":true,"base64":"VklERU8=","mimeType":"video/mp4","size":5}`),
	}
	pf.nextCallResult = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":7777}"}]}}`,
	)
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	_, err := ctx.AppDB().Exec(
		`INSERT INTO video_jobs (project_id, queue_id, provider, model, prompt, request_json, status)
		 VALUES ('test-proj', 'q-folder', 'venice-ai', 'kling-2', 'custom folder clip', '{"storage_folder":"campaigns/video"}', 'queued')`,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := app.videoPollWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(pf.callAppCalls) == 0 || pf.callAppCalls[0].Tool != "files_upload" {
		t.Fatalf("expected files_upload, got %+v", pf.callAppCalls)
	}
	if folder, _ := pf.callAppCalls[0].Input["folder"].(string); folder != "/campaigns/video/" {
		t.Fatalf("storage folder = %q, want /campaigns/video/", folder)
	}
	var extraJSON string
	if err := ctx.AppDB().QueryRow(`SELECT extra_json FROM generations LIMIT 1`).Scan(&extraJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(extraJSON, "/campaigns/video/") {
		t.Fatalf("extra_json missing custom folder: %s", extraJSON)
	}
}

func TestVideoPollWorker_NoJobsNoOp(t *testing.T) {
	ctx := newMediaStudioCtx(t, newRecordingPlatform())
	app := &App{}
	if err := app.videoPollWorker(context.Background(), ctx); err != nil {
		t.Fatalf("unexpected error on empty queue: %v", err)
	}
}

// --- avatar (talking-head) path ----------------------------------

func TestBuildTavusAvatarArgs(t *testing.T) {
	args := map[string]any{
		"avatar": "r-1", "prompt": "hi there",
		"options": map[string]any{"background_url": "https://bg", "fast": true},
	}
	got, err := buildAvatarArgs(args, "tavus", "avatar.generate")
	if err != nil {
		t.Fatal(err)
	}
	if got["replica_id"] != "r-1" || got["script"] != "hi there" {
		t.Errorf("base fields wrong: %+v", got)
	}
	if got["background_url"] != "https://bg" || got["fast"] != true {
		t.Errorf("options not passed through: %+v", got)
	}
}

func TestBuildTavusAvatarArgs_RequiresReplica(t *testing.T) {
	_, err := buildAvatarArgs(map[string]any{"prompt": "hi"}, "tavus", "avatar.generate")
	if err == nil || !strings.Contains(err.Error(), "avatar") {
		t.Errorf("want avatar-required error, got %v", err)
	}
}

func TestBuildHeyGenAvatarArgs_V3AvatarPayload(t *testing.T) {
	args := map[string]any{
		"avatar": "av-1", "prompt": "hello world", "voice": "vo-9",
		"options": map[string]any{
			"resolution": "720p", "aspect": "9:16", "title": "Promo",
			"engine": "avatar_v", "motion_prompt": "wave a lot",
		},
	}
	got, err := buildAvatarArgs(args, "heygen", "avatar.generate")
	if err != nil {
		t.Fatal(err)
	}
	if got["type"] != "avatar" || got["avatar_id"] != "av-1" {
		t.Errorf("avatar fields wrong: %+v", got)
	}
	if got["script"] != "hello world" || got["voice_id"] != "vo-9" {
		t.Errorf("script/voice wrong: %+v", got)
	}
	if got["resolution"] != "720p" || got["aspect_ratio"] != "9:16" {
		t.Errorf("format fields wrong: %+v", got)
	}
	engine := got["engine"].(map[string]any)
	if engine["type"] != "avatar_v" {
		t.Errorf("engine wrong: %+v", got["engine"])
	}
	if got["title"] != "Promo" {
		t.Errorf("title not set: %+v", got["title"])
	}
	if _, ok := got["motion_prompt"]; ok {
		t.Errorf("motion_prompt must be stripped for avatar_v: %+v", got)
	}
}

func TestBuildHeyGenAvatarArgs_ImageToVideoPayload(t *testing.T) {
	got, err := buildAvatarArgs(map[string]any{
		"source_image": "https://example.com/person.jpg",
		"prompt":       "hello from this photo",
		"voice":        "vo-9",
	}, "heygen", "avatar.generate")
	if err != nil {
		t.Fatal(err)
	}
	if got["type"] != "image" {
		t.Errorf("type = %v, want image", got["type"])
	}
	image := got["image"].(map[string]any)
	if image["type"] != "url" || image["url"] != "https://example.com/person.jpg" {
		t.Errorf("image ref wrong: %+v", image)
	}
}

func TestBuildHeyGenAvatarArgs_ImageToVideoRequiresVoice(t *testing.T) {
	_, err := buildAvatarArgs(map[string]any{"source_image": "abc", "prompt": "hi"}, "heygen", "avatar.generate")
	if err == nil || !strings.Contains(err.Error(), "voice") {
		t.Errorf("want voice-required error, got %v", err)
	}
}

func TestAvatarToolForSlug(t *testing.T) {
	if avatarToolForSlug("tavus", "avatar.generate") != "create_video" {
		t.Error("tavus should map to create_video")
	}
	if avatarToolForSlug("heygen", "avatar.generate") != "create_video" {
		t.Error("heygen should map to create_video")
	}
}

func TestBuildHeyGenAvatarCreateArgs_Photo(t *testing.T) {
	got, err := buildHeyGenAvatarCreateArgs(map[string]any{
		"name":         "Marcus",
		"source_type":  "photo",
		"source_image": "U09VUkNF",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["type"] != "photo" || got["name"] != "Marcus" {
		t.Fatalf("basic fields wrong: %+v", got)
	}
	file, ok := got["file"].(map[string]any)
	if !ok || file["type"] != "base64" || file["data"] != "U09VUkNF" {
		t.Fatalf("file field wrong: %+v", got["file"])
	}
}

func TestNormalizeHeyGenAvatarCreateResponse(t *testing.T) {
	got, err := normalizeAvatarCreateResponse("heygen", json.RawMessage(`{
		"data": {
			"avatar_item": {"id":"look-1","group_id":"group-1","status":"completed"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderAvatarID != "look-1" || got.ProviderGroupID != "group-1" || got.Status != "completed" {
		t.Fatalf("unexpected normalized result: %+v", got)
	}
}

func TestToolMediaAvatarCreate_HeyGenPhotoQueuesJob(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "heygen"
	pf.identity.Bindings = map[string]any{"avatar_provider": float64(56)}
	pf.perAppCallResults = map[string]json.RawMessage{
		"storage:files_get_content": json.RawMessage(
			`{"result":{"content":[{"type":"text","text":"{\"content_base64\":\"U09VUkNF\"}"}]}}`,
		),
	}
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"data":{"avatar_item":{"id":"look-1","group_id":"group-1","status":"processing"}}}`),
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaAvatarCreate(ctx, map[string]any{
		"name":         "Marcus",
		"source_type":  "photo",
		"source_image": "storage:10",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	meta := res["_meta"].(map[string]any)
	if meta["status"] != "training" || meta["provider_avatar_id"] != "look-1" {
		t.Fatalf("unexpected meta: %+v", meta)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "create_avatar" {
		t.Fatalf("expected create_avatar call, got %+v", pf.executeCalls)
	}
	file := pf.executeCalls[0].Input["file"].(map[string]any)
	if file["data"] != "U09VUkNF" {
		t.Fatalf("storage source not resolved into provider file: %+v", pf.executeCalls[0].Input)
	}
	var status, avatarID string
	if err := ctx.AppDB().QueryRow(`SELECT status, provider_avatar_id FROM avatar_create_jobs WHERE name='Marcus'`).Scan(&status, &avatarID); err != nil {
		t.Fatal(err)
	}
	if status != "training" || avatarID != "look-1" {
		t.Fatalf("job row wrong: status=%s avatar_id=%s", status, avatarID)
	}
	var kind, providerIdentityID string
	if err := ctx.AppDB().QueryRow(`SELECT kind, provider_identity_id FROM media_identities WHERE name='Marcus'`).Scan(&kind, &providerIdentityID); err != nil {
		t.Fatal(err)
	}
	if kind != "avatar" || providerIdentityID != "look-1" {
		t.Fatalf("identity row wrong: kind=%s provider_identity_id=%s", kind, providerIdentityID)
	}
}

func TestToolMediaVoiceCreate_ElevenLabsDesignCreatesIdentity(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "elevenlabs"
	pf.identity.Bindings = map[string]any{"audio_provider": float64(61)}
	pf.perExecuteResults = map[string]*sdk.ExecuteResult{
		"design_voice": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"previews":[{"generated_voice_id":"gen-1","audio_base_64":"U0FNUExF","media_type":"audio/mpeg","duration_secs":4.2,"language":"en"}],"text":"This is a sufficiently long preview text for the designed voice."}`),
		},
		"create_voice_from_preview": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"voice_id":"voice-1","name":"Calm Guide","category":"generated","preview_url":"https://example.test/preview.mp3","labels":{"gender":"female"}}`),
		},
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaVoiceCreate(ctx, map[string]any{
		"name":        "Calm Guide",
		"source_type": "prompt",
		"prompt":      "A calm, warm, slow female hypnosis narrator voice with intimate studio quality.",
		"options": map[string]any{
			"auto_generate_text": true,
			"model_id":           "eleven_ttv_v3",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	meta := out.(map[string]any)["_meta"].(map[string]any)
	if meta["provider_identity_id"] != "voice-1" || meta["status"] != "ready" {
		t.Fatalf("unexpected meta: %+v", meta)
	}
	if len(pf.executeCalls) != 2 || pf.executeCalls[0].Tool != "design_voice" || pf.executeCalls[1].Tool != "create_voice_from_preview" {
		t.Fatalf("unexpected provider calls: %+v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["model_id"] != "eleven_ttv_v3" {
		t.Fatalf("design call did not forward model_id: %+v", pf.executeCalls[0].Input)
	}
	if pf.executeCalls[1].Input["generated_voice_id"] != "gen-1" {
		t.Fatalf("create call did not save selected preview: %+v", pf.executeCalls[1].Input)
	}
	var kind, provider, providerIdentityID, providerJobID string
	if err := ctx.AppDB().QueryRow(`SELECT kind, provider, provider_identity_id, provider_job_id FROM media_identities WHERE name='Calm Guide'`).Scan(&kind, &provider, &providerIdentityID, &providerJobID); err != nil {
		t.Fatal(err)
	}
	if kind != "voice" || provider != "elevenlabs" || providerIdentityID != "voice-1" || providerJobID != "gen-1" {
		t.Fatalf("identity row wrong: kind=%s provider=%s identity=%s job=%s", kind, provider, providerIdentityID, providerJobID)
	}
}

func TestNormalizeAvatarResponse_HeyGen(t *testing.T) {
	body := `{"error":null,"data":{"video_id":"hg-77"}}`
	media, _, _, err := normalizeAvatarResponse("heygen", "avatar.generate", json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 1 || media[0].UpstreamURL != "hg-77" {
		t.Errorf("expected data.video_id as handle, got %+v", media)
	}
}

func TestHeyGenDimension(t *testing.T) {
	if w, h := heygenDimension("1080p", "16:9"); w != 1920 || h != 1080 {
		t.Errorf("1080p 16:9 = %dx%d, want 1920x1080", w, h)
	}
	if w, h := heygenDimension("720p", "16:9"); w != 1280 || h != 720 {
		t.Errorf("720p 16:9 = %dx%d, want 1280x720", w, h)
	}
	if w, h := heygenDimension("1080p", "9:16"); w != 1080 || h != 1920 {
		t.Errorf("1080p 9:16 = %dx%d, want 1080x1920", w, h)
	}
}

func TestPollWorker_AvatarHeyGen_Completes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("HEYGEN-MP4"))
	}))
	defer upstream.Close()

	pf := newRecordingPlatform()
	pf.appSlug = "heygen"
	pf.identity.Bindings["avatar_provider"] = float64(56)
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(fmt.Sprintf(`{"data":{"status":"completed","video_url":"%s/v.mp4"}}`, upstream.URL)),
	}
	pf.nextCallResult = json.RawMessage(`{"result":{"content":[{"type":"text","text":"{\"id\":4321}"}]}}`)

	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	ctx.AppDB().Exec(
		`INSERT INTO video_jobs (project_id, kind, role, queue_id, provider, model, prompt, status)
		 VALUES ('test-proj', 'avatar', 'avatar_provider', 'hg-1', 'heygen', 'heygen-avatar', 'hi', 'queued')`,
	)
	if err := app.videoPollWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	var genID int64
	ctx.AppDB().QueryRow(`SELECT status, generation_id FROM video_jobs WHERE queue_id='hg-1'`).Scan(&status, &genID)
	if status != "complete" {
		t.Errorf("status = %q, want complete", status)
	}
	if pf.executeCalls[0].Tool != "get_video" {
		t.Errorf("retrieve tool = %q, want get_video", pf.executeCalls[0].Tool)
	}
	var kind string
	ctx.AppDB().QueryRow(`SELECT kind FROM generations WHERE id=?`, genID).Scan(&kind)
	if kind != "avatar" {
		t.Errorf("generations.kind = %q, want avatar", kind)
	}
}

func TestNormalizeAvatarResponse_Tavus(t *testing.T) {
	body := `{"video_id":"v-abc","video_name":"n","status":"queued"}`
	media, _, _, err := normalizeAvatarResponse("tavus", "avatar.generate", json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 1 || media[0].UpstreamURL != "v-abc" {
		t.Errorf("expected video_id as handle, got %+v", media)
	}
}

func TestToolMediaGenerate_Avatar_TavusQueue(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "tavus"
	pf.identity.Bindings["avatar_provider"] = float64(55)
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"video_id":"v-abc","status":"queued","hosted_url":"https://tavus.video/v-abc"}`),
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":   "avatar",
		"prompt": "Hello there",
		"avatar": "r-123",
	})
	if err != nil {
		t.Fatalf("toolMediaGenerate: %v", err)
	}
	res := out.(map[string]any)
	meta := res["_meta"].(map[string]any)
	if meta["status"] != "queued" || meta["queue_id"] != "v-abc" {
		t.Errorf("expected queued meta, got %+v", meta)
	}
	if pf.executeCalls[0].Tool != "create_video" {
		t.Errorf("tool = %q, want create_video", pf.executeCalls[0].Tool)
	}
	if pf.executeCalls[0].Input["replica_id"] != "r-123" {
		t.Errorf("replica_id not forwarded: %+v", pf.executeCalls[0].Input)
	}
	if pf.executeCalls[0].Input["script"] != "Hello there" {
		t.Errorf("script not forwarded: %+v", pf.executeCalls[0].Input)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM video_jobs WHERE kind='avatar' AND status='queued'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 queued avatar job, got %d", count)
	}
}

func TestPollWorker_AvatarTavus_Completes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("FAKE-MP4-BYTES"))
	}))
	defer upstream.Close()

	pf := newRecordingPlatform()
	pf.appSlug = "tavus"
	pf.identity.Bindings["avatar_provider"] = float64(55)
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(fmt.Sprintf(`{"status":"ready","download_url":"%s/v.mp4"}`, upstream.URL)),
	}
	pf.nextCallResult = json.RawMessage(`{"result":{"content":[{"type":"text","text":"{\"id\":8888}"}]}}`)

	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO video_jobs (project_id, kind, role, queue_id, provider, model, prompt, status)
		 VALUES ('test-proj', 'avatar', 'avatar_provider', 'v-xyz', 'tavus', 'tavus-replica', 'hi', 'queued')`,
	); err != nil {
		t.Fatal(err)
	}

	if err := app.videoPollWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}

	var status string
	var genID int64
	ctx.AppDB().QueryRow(`SELECT status, generation_id FROM video_jobs WHERE queue_id='v-xyz'`).Scan(&status, &genID)
	if status != "complete" {
		t.Errorf("status = %q, want complete", status)
	}
	if genID == 0 {
		t.Error("generation_id should be set after complete")
	}
	if pf.executeCalls[0].Tool != "get_video" {
		t.Errorf("retrieve tool = %q, want get_video", pf.executeCalls[0].Tool)
	}
	var kind string
	ctx.AppDB().QueryRow(`SELECT kind FROM generations WHERE id=?`, genID).Scan(&kind)
	if kind != "avatar" {
		t.Errorf("generations.kind = %q, want avatar", kind)
	}
	if len(pf.callAppCalls) == 0 || pf.callAppCalls[0].Tool != "files_upload" {
		t.Fatalf("expected files_upload, got %+v", pf.callAppCalls)
	}
	if folder, _ := pf.callAppCalls[0].Input["folder"].(string); folder != "/.generated/avatars/" {
		t.Errorf("storage folder = %q, want /.generated/avatars/", folder)
	}
}

func TestPollWorker_AvatarTavus_StillGenerating(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "tavus"
	pf.identity.Bindings["avatar_provider"] = float64(55)
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"status":"generating"}`),
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	ctx.AppDB().Exec(
		`INSERT INTO video_jobs (project_id, kind, role, queue_id, provider, model, prompt, status)
		 VALUES ('test-proj', 'avatar', 'avatar_provider', 'v-q', 'tavus', 'tavus-replica', 'hi', 'queued')`,
	)
	if err := app.videoPollWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	ctx.AppDB().QueryRow(`SELECT status, attempts FROM video_jobs WHERE queue_id='v-q'`).Scan(&status, &attempts)
	if status != "polling" {
		t.Errorf("status = %q, want polling", status)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}
