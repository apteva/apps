package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func testCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	return tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(&tk.BasePlatformClient{}))
}

type storagePlatformStub struct{ tk.BasePlatformClient }

func (storagePlatformStub) CallAppResult(app, tool string, _ map[string]any, out any) error {
	var payload any
	switch app + "/" + tool {
	case "storage/storage_upload_init":
		payload = map[string]any{"upload_id": "01TESTUPLOAD", "part_size": 1048576, "was_existing": false}
	case "storage/storage_upload_part":
		payload = map[string]any{"ok": true}
	case "storage/storage_upload_complete":
		payload = map[string]any{"file": map[string]any{"id": 91}}
	case "storage/files_get":
		payload = map[string]any{"found": true, "file": map[string]any{"id": 91, "name": "clip.mp4", "content_type": "video/mp4", "size_bytes": 5}}
	case "storage/files_delete":
		payload = map[string]any{"ok": true}
	case "storage/files_get_url":
		payload = map[string]any{"url": "https://storage.example.test/signed/image"}
	case "storage/storage_abort_upload":
		payload = map[string]any{"found": true}
	default:
		payload = map[string]any{}
	}
	raw, _ := json.Marshal(payload)
	return json.Unmarshal(raw, out)
}

type identityPlatformStub struct {
	tk.BasePlatformClient
	installID int64
	publicURL string
}

func (p identityPlatformStub) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{InstallID: p.installID, PublicURL: p.publicURL}, nil
}

func seedWorker(t *testing.T, ctx *sdk.AppCtx, project string, contactID int64) int64 {
	t.Helper()
	res, err := ctx.AppDB().Exec(`INSERT INTO workers (project_id,contact_id,status) VALUES (?,?,'active')`, project, contactID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedGig(t *testing.T, ctx *sdk.AppCtx, project, status, schema string) int64 {
	t.Helper()
	res, err := ctx.AppDB().Exec(`INSERT INTO gigs
		(project_id,created_by,title,derived_result_schema_json,status,deadline_at)
		VALUES (?,'test','Test gig',?,?,datetime('now','+1 day'))`, project, schema, status)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedAssignment(t *testing.T, ctx *sdk.AppCtx, gigID, workerID int64, status, mode, token string) int64 {
	t.Helper()
	res, err := ctx.AppDB().Exec(`INSERT INTO gig_assignments
		(gig_id,worker_id,status,magic_token,mode,token_expires_at)
		VALUES (?,?,?,?,?,datetime('now','+1 day'))`, gigID, workerID, status, token, mode)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestManifestOnlyExposesWorkerRouteWithoutAuth(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Version != "0.4.0" {
		t.Fatalf("version=%q", manifest.Version)
	}
	var public []sdk.RouteSpec
	for _, route := range manifest.Provides.HTTPRoutes {
		if route.NoAuth {
			public = append(public, route)
		}
	}
	if len(public) != 1 || public[0].Prefix != "/worker/" || public[0].Method != "" {
		t.Fatalf("public routes=%+v", public)
	}
}

func TestWorkerOfferSummaryMatchesAcceptanceBoundary(t *testing.T) {
	html := workerPageHTML("worker-token")
	if !strings.Contains(html, `Review the offer details, then accept to see the full instructions.`) {
		t.Fatal("worker page does not explain that instructions appear after acceptance")
	}
}

func TestWorkerPageUsesCustomUploadDropzoneAndCompactPreview(t *testing.T) {
	html := workerPageHTML("worker-token")
	for _, want := range []string{
		`class='upload-dropzone' data-dropzone`,
		`Drop " + escapeHTML(noun) + " here`,
		`dropzone.addEventListener("drop"`,
		`top.className = "preview-top"`,
		`button.textContent = "Preview"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("worker page missing upload UI marker %q", want)
		}
	}
	if strings.Contains(html, `<input data-files type='file'`) {
		t.Fatal("worker page exposes the browser-native file input instead of the custom dropzone")
	}
}

func TestWorkerPageRendersStructuredContentBlocks(t *testing.T) {
	html := workerPageHTML("worker-token")
	for _, want := range []string{
		`case "content":`,
		`renderContentBlocks(body.blocks || [])`,
		`markdownBlock(block.markdown_html, block.markdown || "")`,
		`case "image":`,
		`img.loading = "lazy"`,
		`case "callout":`,
		`className = "content-callout " + tone`,
		`case "divider":`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("worker page missing content renderer marker %q", want)
		}
	}
}

func TestWorkerMarkdownRendersFormattingWithoutRawHTML(t *testing.T) {
	got := renderWorkerMarkdown("## Welcome **Holly**\n\n<script>alert('no')</script>")
	if !strings.Contains(got, "<h2>Welcome <strong>Holly</strong></h2>") {
		t.Fatalf("markdown heading was not rendered: %q", got)
	}
	if strings.Contains(got, "<script") || strings.Contains(got, "alert('no')") {
		t.Fatalf("raw HTML was rendered: %q", got)
	}
}

func TestContentMarkdownGetsRenderedWithoutMutatingStoredBody(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(storagePlatformStub{}))
	body := map[string]any{"blocks": []any{
		map[string]any{"type": "markdown", "markdown": "## Welcome"},
	}}
	enriched := enrichContentBlockURLs(ctx, "project-a", body, 3600)
	block := enriched["blocks"].([]any)[0].(map[string]any)
	if block["markdown_html"] != "<h2>Welcome</h2>\n" {
		t.Fatalf("rendered markdown = %q", block["markdown_html"])
	}
	if _, exists := body["blocks"].([]any)[0].(map[string]any)["markdown_html"]; exists {
		t.Fatal("markdown rendering mutated stored instruction body")
	}
}

func TestWorkerOfferWithholdsInstructionsUntilAccepted(t *testing.T) {
	ctx := testCtx(t)
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })

	workerID := seedWorker(t, ctx, "project-a", 101)
	gigID := seedGig(t, ctx, "project-a", "offered", `{"type":"object","required":["recording"],"properties":{"recording":{"type":"string"}}}`)
	assignmentID := seedAssignment(t, ctx, gigID, workerID, "offered", "direct", "private-offer-token")
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_instructions
		(gig_id,sort_order,instruction_kind,rendered_body_json,result_key)
		VALUES (?,0,'text','{"markdown":"Private recording brief"}','recording')`, gigID); err != nil {
		t.Fatal(err)
	}

	fetch := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		(&App{}).handleWorkerGigJSON(rec, httptest.NewRequest(http.MethodGet, "/worker/private-offer-token/api/gig", nil), "private-offer-token")
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var envelope map[string]map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope["gig"]
	}

	offer := fetch()
	if composition, _ := offer["composition"].([]any); len(composition) != 0 {
		t.Fatalf("offered composition=%v", composition)
	}
	if _, exposed := offer["required_result_keys"]; exposed {
		t.Fatalf("offered response exposed required_result_keys: %v", offer["required_result_keys"])
	}

	if _, err := ctx.AppDB().Exec(`UPDATE gig_assignments SET status='accepted' WHERE id=?`, assignmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE gigs SET status='accepted' WHERE id=?`, gigID); err != nil {
		t.Fatal(err)
	}
	accepted := fetch()
	if composition, _ := accepted["composition"].([]any); len(composition) != 1 {
		t.Fatalf("accepted composition=%v", composition)
	}
	if keys, _ := accepted["required_result_keys"].([]any); len(keys) != 1 || keys[0] != "recording" {
		t.Fatalf("accepted required_result_keys=%v", accepted["required_result_keys"])
	}
}

func TestWorkerURLUsesExactInstallAndWorkerPagePreservesIt(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(identityPlatformStub{
		installID: 42,
		publicURL: "https://agents.example.test/",
	}))
	got, err := buildWorkerURL(ctx, "worker-token", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://agents.example.test/api/apps/gigs/_install/42/worker/worker-token"
	if got != want {
		t.Fatalf("worker URL=%q want=%q", got, want)
	}

	other := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-b"), tk.WithPlatform(identityPlatformStub{
		installID: 84,
		publicURL: "https://agents.example.test",
	}))
	otherURL, err := buildWorkerURL(other, "worker-token", "")
	if err != nil {
		t.Fatal(err)
	}
	if otherURL == got || !strings.Contains(otherURL, "/_install/84/") {
		t.Fatalf("install-isolated URL=%q first=%q", otherURL, got)
	}

	html := workerPageHTML("worker-token")
	if !strings.Contains(html, `const API = window.location.pathname.replace(/\/+$/, "");`) {
		t.Fatal("worker page does not preserve its exact-install path")
	}
	if strings.Contains(html, `const API = "/api/apps/gigs/worker/"`) {
		t.Fatal("worker page still hard-codes the ambiguous app-name route")
	}
}

func TestWorkerURLFailsClosedWithoutInstallIdentity(t *testing.T) {
	ctx := testCtx(t)
	if got, err := buildWorkerURL(ctx, "worker-token", ""); err == nil || got != "" {
		t.Fatalf("worker URL=%q err=%v", got, err)
	}
}

func TestGigActionRoutesRequirePost(t *testing.T) {
	ctx := testCtx(t)
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })

	req := httptest.NewRequest(http.MethodGet, "/gigs/42/cancel?project_id=project-a", nil)
	rec := httptest.NewRecorder()
	(&App{}).handleHTTPGigItem(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("Allow=%q", got)
	}
}

func TestAssignGigDoesNotCrossProjectBoundary(t *testing.T) {
	ctx := testCtx(t)
	workerID := seedWorker(t, ctx, "project-a", 10)
	gigID := seedGig(t, ctx, "project-b", "open", `{"type":"object","properties":{}}`)
	if _, err := assignGig(ctx, "project-a", gigID, workerID, "direct", false, 0); err == nil || !strings.Contains(err.Error(), "gig not found") {
		t.Fatalf("expected project isolation error, got %v", err)
	}
}

func TestValidateSubmissionChecksGeneratedSchema(t *testing.T) {
	ctx := testCtx(t)
	schema := `{"type":"object","properties":{"rating":{"type":"integer","minimum":1,"maximum":5},"choice":{"type":"string","enum":["a","b"]}},"required":["rating","choice"]}`
	gigID := seedGig(t, ctx, "project-a", "accepted", schema)
	if err := validateSubmission(ctx.AppDB(), gigID, 0, map[string]any{"rating": 6.0, "choice": "a"}); err == nil {
		t.Fatal("expected maximum validation error")
	}
	if err := validateSubmission(ctx.AppDB(), gigID, 0, map[string]any{"rating": 4.0, "choice": "x"}); err == nil {
		t.Fatal("expected enum validation error")
	}
	if err := validateSubmission(ctx.AppDB(), gigID, 0, map[string]any{"rating": 4.0, "choice": "b"}); err != nil {
		t.Fatalf("valid submission rejected: %v", err)
	}
}

func TestExplicitResponseSpecSeparatesRequiredFilesFromNotes(t *testing.T) {
	ctx := testCtx(t)
	workerID := seedWorker(t, ctx, "project-a", 101)
	gigID := seedGig(t, ctx, "project-a", "accepted", `{"type":"object","properties":{}}`)
	assignmentID := seedAssignment(t, ctx, gigID, workerID, "accepted", "direct", "response-token")
	body := `{"markdown":"Upload the final recording","response":{"note":{"enabled":true,"required":false},"files":{"enabled":true,"required":true,"accept":["video/*"],"min_items":1,"max_items":3}}}`
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_instructions
		(gig_id,sort_order,instruction_kind,rendered_body_json,result_key)
		VALUES (?,0,'text',?,'recording')`, gigID, body); err != nil {
		t.Fatal(err)
	}
	noteOnly := map[string]any{"instruction_responses": []any{map[string]any{
		"key": "recording", "note": "The files are ready", "files": []any{},
	}}}
	if err := validateSubmission(ctx.AppDB(), gigID, assignmentID, noteOnly); err == nil || !strings.Contains(err.Error(), "requires at least 1 file") {
		t.Fatalf("note-only response err=%v", err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_upload_sessions
		(upload_id,assignment_id,project_id,status,storage_file_id,instruction_key,filename,content_type,size_bytes,completed_at)
		VALUES ('response-upload',?,'project-a','completed',501,'recording','take.mp4','video/mp4',100,CURRENT_TIMESTAMP)`, assignmentID); err != nil {
		t.Fatal(err)
	}
	withVideo := map[string]any{"instruction_responses": []any{map[string]any{
		"key": "recording", "note": "", "files": []any{map[string]any{
			"storage_file_id": 501, "filename": "take.mp4", "mime": "video/mp4",
		}},
	}}}
	if err := validateSubmission(ctx.AppDB(), gigID, assignmentID, withVideo); err != nil {
		t.Fatalf("valid video response rejected: %v", err)
	}
}

func TestExplicitResponseSpecRejectsMalformedRules(t *testing.T) {
	tests := []map[string]any{
		{"markdown": "Record", "response": map[string]any{"files": "video/*"}},
		{"markdown": "Record", "response": map[string]any{"files": map[string]any{"enabled": true, "accept": "video/*"}}},
		{"markdown": "Record", "response": map[string]any{"files": map[string]any{"enabled": true, "max_items": 1.5}}},
		{"markdown": "Record", "response": map[string]any{"note": map[string]any{"enabled": false, "required": true}}},
	}
	for i, body := range tests {
		if err := validateBody(kindText, body); err == nil {
			t.Fatalf("case %d malformed response accepted: %v", i, body)
		}
	}
}

func TestContentInstructionValidationDerivationAndRendering(t *testing.T) {
	body := map[string]any{"blocks": []any{
		map[string]any{"type": "markdown", "markdown": "## Welcome {{model}}"},
		map[string]any{"type": "image", "storage_file_id": float64(501), "caption": "Correct pose for {{shot}}", "alt": "Reference for {{model}}"},
		map[string]any{"type": "callout", "tone": "tip", "text": "Keep {{shot}} visible."},
		map[string]any{"type": "divider"},
	}}
	if err := validateBody(kindContent, body); err != nil {
		t.Fatalf("valid content body rejected: %v", err)
	}
	declared := deriveDeclaredVariables(kindContent, body)
	if !slices.Equal(declared, []string{"model", "shot"}) {
		t.Fatalf("declared variables = %v", declared)
	}
	rendered := renderBody(kindContent, body, map[string]any{"model": "Holly", "shot": "full body"})
	blocks := rendered["blocks"].([]any)
	if got := blocks[0].(map[string]any)["markdown"]; got != "## Welcome Holly" {
		t.Fatalf("rendered markdown = %v", got)
	}
	if got := blocks[1].(map[string]any)["caption"]; got != "Correct pose for full body" {
		t.Fatalf("rendered caption = %v", got)
	}
	if original := body["blocks"].([]any)[0].(map[string]any)["markdown"]; original != "## Welcome {{model}}" {
		t.Fatalf("render mutated source body: %v", original)
	}
	derived := deriveFromComposition([]compositionItem{{
		SortOrder: 2, InstructionID: 7, Kind: kindContent, Body: body, DeclaredVariables: declared,
	}})
	if len(derived.MediaManifest) != 1 {
		t.Fatalf("media manifest = %#v", derived.MediaManifest)
	}
	if derived.MediaManifest[0]["storage_file_id"] != float64(501) || derived.MediaManifest[0]["block_index"] != 1 {
		t.Fatalf("content media entry = %#v", derived.MediaManifest[0])
	}
	if len(derived.ResultSchema["properties"].(map[string]any)) != 0 {
		t.Fatalf("read-only content contributed result schema: %#v", derived.ResultSchema)
	}
}

func TestContentInstructionRejectsMalformedBlocks(t *testing.T) {
	tests := []map[string]any{
		{},
		{"blocks": []any{}},
		{"blocks": []any{map[string]any{"type": "markdown", "markdown": ""}}},
		{"blocks": []any{map[string]any{"type": "image", "storage_file_id": 0}}},
		{"blocks": []any{map[string]any{"type": "callout", "text": "Read", "tone": "urgent"}}},
		{"blocks": []any{map[string]any{"type": "html", "html": "<script>"}}},
	}
	for i, body := range tests {
		if err := validateBody(kindContent, body); err == nil {
			t.Fatalf("case %d malformed content accepted: %#v", i, body)
		}
	}
}

func TestContentInstructionImageGetsSignedWithoutMutatingStoredBody(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(storagePlatformStub{}))
	body := map[string]any{"blocks": []any{
		map[string]any{"type": "markdown", "markdown": "Look here"},
		map[string]any{"type": "image", "storage_file_id": float64(91), "caption": "Reference"},
	}}
	enriched := enrichContentBlockURLs(ctx, "project-a", body, 3600)
	blocks := enriched["blocks"].([]any)
	if got := blocks[1].(map[string]any)["signed_url"]; got != "https://storage.example.test/signed/image" {
		t.Fatalf("signed url = %v", got)
	}
	if _, exists := body["blocks"].([]any)[1].(map[string]any)["signed_url"]; exists {
		t.Fatal("signing mutated stored instruction body")
	}
}

func TestWorkerGigJSONSignsImagesInsideContentInstruction(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(storagePlatformStub{}))
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	workerID := seedWorker(t, ctx, "project-a", 103)
	gigID := seedGig(t, ctx, "project-a", "accepted", `{"type":"object","properties":{}}`)
	seedAssignment(t, ctx, gigID, workerID, "accepted", "direct", "content-image-token")
	body := `{"blocks":[{"type":"markdown","markdown":"Starting pose"},{"type":"image","storage_file_id":91,"caption":"Reference"}]}`
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_instructions
		(gig_id,sort_order,instruction_kind,rendered_body_json)
		VALUES (?,0,'content',?)`, gigID, body); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	(&App{}).handleWorkerGigJSON(rec, httptest.NewRequest(http.MethodGet, "/worker/content-image-token/api/gig", nil), "content-image-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Gig struct {
			Composition []struct {
				RenderedBody map[string]any `json:"rendered_body"`
			} `json:"composition"`
		} `json:"gig"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	blocks := envelope.Gig.Composition[0].RenderedBody["blocks"].([]any)
	image := blocks[1].(map[string]any)
	if image["signed_url"] != "https://storage.example.test/signed/image" {
		t.Fatalf("worker content image = %#v", image)
	}
}

func TestResponseSpecRejectsWrongFileTypeAtUploadInit(t *testing.T) {
	ctx := testCtx(t)
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	workerID := seedWorker(t, ctx, "project-a", 102)
	gigID := seedGig(t, ctx, "project-a", "accepted", `{"type":"object","properties":{}}`)
	seedAssignment(t, ctx, gigID, workerID, "accepted", "direct", "mime-token")
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_instructions
		(gig_id,sort_order,instruction_kind,rendered_body_json,result_key)
		VALUES (?,0,'text',?,'recording')`, gigID,
		`{"markdown":"Upload","response":{"files":{"enabled":true,"required":true,"accept":["video/*"]}}}`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/worker/mime-token/upload/init", bytes.NewBufferString(
		`{"instruction_key":"recording","name":"notes.pdf","content_type":"application/pdf","size_bytes":20}`))
	rec := httptest.NewRecorder()
	(&App{}).handleWorkerUploadInit(rec, req, "mime-token")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not accepted") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorkerDraftPersistsIncompleteResponses(t *testing.T) {
	ctx := testCtx(t)
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	workerID := seedWorker(t, ctx, "project-a", 103)
	gigID := seedGig(t, ctx, "project-a", "accepted", `{"type":"object","properties":{}}`)
	assignmentID := seedAssignment(t, ctx, gigID, workerID, "accepted", "direct", "draft-token")
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_instructions
		(gig_id,sort_order,instruction_kind,rendered_body_json,result_key)
		VALUES (?,0,'text',?,'recording')`, gigID,
		`{"markdown":"Upload","response":{"note":{"enabled":true},"files":{"enabled":true,"required":true,"accept":["video/*"]}}}`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/worker/draft-token/draft", bytes.NewBufferString(
		`{"payload":{"instruction_responses":[{"key":"recording","note":"Recording tomorrow","files":[]}]},"attachment_file_ids":[]}`))
	rec := httptest.NewRecorder()
	(&App{}).handleWorkerDraft(rec, req, "draft-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	draft, err := loadWorkerDraft(ctx.AppDB(), assignmentID)
	if err != nil || draft == nil || !strings.Contains(mustJSON(draft.Payload), "Recording tomorrow") {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
}

func TestRequiredConfirmationMustBeTrue(t *testing.T) {
	ctx := testCtx(t)
	gigID := seedGig(t, ctx, "project-a", "accepted", `{"type":"object","properties":{"confirmed":{"type":"boolean","const":true}},"required":["confirmed"]}`)
	if err := validateSubmission(ctx.AppDB(), gigID, 0, map[string]any{"confirmed": false}); err == nil {
		t.Fatal("false confirmation unexpectedly accepted")
	}
	if err := validateSubmission(ctx.AppDB(), gigID, 0, map[string]any{"confirmed": true}); err != nil {
		t.Fatalf("true confirmation rejected: %v", err)
	}
}

func TestFirstComeSubmissionWithdrawsOtherWorkers(t *testing.T) {
	ctx := testCtx(t)
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })

	w1 := seedWorker(t, ctx, "project-a", 11)
	w2 := seedWorker(t, ctx, "project-a", 12)
	gigID := seedGig(t, ctx, "project-a", "accepted", `{"type":"object","properties":{}}`)
	seedAssignment(t, ctx, gigID, w1, "accepted", "first-come", "token-one")
	secondID := seedAssignment(t, ctx, gigID, w2, "offered", "first-come", "token-two")

	req := httptest.NewRequest(http.MethodPost, "/worker/token-one/submit", bytes.NewBufferString(`{"payload":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	(&App{}).handleWorkerSubmit(rec, req, "token-one")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var status string
	var revoked bool
	if err := ctx.AppDB().QueryRow(`SELECT status,token_revoked_at IS NOT NULL FROM gig_assignments WHERE id=?`, secondID).Scan(&status, &revoked); err != nil {
		t.Fatal(err)
	}
	if status != "withdrawn" || !revoked {
		t.Fatalf("second assignment status=%s revoked=%v", status, revoked)
	}
}

func TestReviewedSubmissionCannotBeAcceptedOrSubmittedTwice(t *testing.T) {
	ctx := testCtx(t)
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })

	workerID := seedWorker(t, ctx, "project-a", 13)
	gigID := seedGig(t, ctx, "project-a", "submitted", `{"type":"object","properties":{}}`)
	assignmentID := seedAssignment(t, ctx, gigID, workerID, "submitted", "direct", "review-token")
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_submissions (assignment_id,payload_json,channel) VALUES (?,'{}','web')`, assignmentID); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	if _, err := app.toolGigsAccept(ctx, map[string]any{"_project_id": "project-a", "id": gigID}); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if _, err := app.toolGigsAccept(ctx, map[string]any{"_project_id": "project-a", "id": gigID}); err == nil {
		t.Fatal("second accept unexpectedly succeeded")
	}
	var acceptedCount int64
	if err := ctx.AppDB().QueryRow(`SELECT accepted_count FROM workers WHERE id=?`, workerID).Scan(&acceptedCount); err != nil {
		t.Fatal(err)
	}
	if acceptedCount != 1 {
		t.Fatalf("accepted_count=%d", acceptedCount)
	}
	req := httptest.NewRequest(http.MethodPost, "/worker/review-token/submit", bytes.NewBufferString(`{"payload":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.handleWorkerSubmit(rec, req, "review-token")
	if rec.Code != http.StatusGone {
		t.Fatalf("resubmit status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLifecycleMarksGigOverdueWithoutRevokingAssignment(t *testing.T) {
	ctx := testCtx(t)
	workerID := seedWorker(t, ctx, "project-a", 14)
	gigID := seedGig(t, ctx, "project-a", "offered", `{"type":"object","properties":{}}`)
	assignmentID := seedAssignment(t, ctx, gigID, workerID, "offered", "direct", "expiry-token")
	if _, err := ctx.AppDB().Exec(`UPDATE gigs SET due_at=datetime('now','-1 minute'),deadline_at=datetime('now','-1 minute') WHERE id=?`, gigID); err != nil {
		t.Fatal(err)
	}
	if err := markOverdueGigs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var gigStatus, assignmentStatus string
	var revoked, overdue bool
	if err := ctx.AppDB().QueryRow(`SELECT g.status,a.status,a.token_revoked_at IS NOT NULL,g.overdue_at IS NOT NULL
		FROM gigs g JOIN gig_assignments a ON a.gig_id=g.id WHERE g.id=? AND a.id=?`, gigID, assignmentID).
		Scan(&gigStatus, &assignmentStatus, &revoked, &overdue); err != nil {
		t.Fatal(err)
	}
	if gigStatus != "offered" || assignmentStatus != "offered" || revoked || !overdue {
		t.Fatalf("gig=%s assignment=%s revoked=%v overdue=%v", gigStatus, assignmentStatus, revoked, overdue)
	}
}

func TestWorkerCanReadAndUploadAfterSoftDueDate(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(storagePlatformStub{}))
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	workerID := seedWorker(t, ctx, "project-a", 140)
	gigID := seedGig(t, ctx, "project-a", "accepted", `{"type":"object","properties":{}}`)
	seedAssignment(t, ctx, gigID, workerID, "accepted", "direct", "late-worker-token")
	if _, err := ctx.AppDB().Exec(`UPDATE gigs SET due_at=datetime('now','-2 days'),deadline_at=datetime('now','-2 days') WHERE id=?`, gigID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_instructions
		(gig_id,sort_order,instruction_kind,rendered_body_json,result_key)
		VALUES (?,0,'text',?,'recording')`, gigID,
		`{"markdown":"Upload the recording","response":{"files":{"enabled":true,"required":true,"accept":["video/*"]}}}`); err != nil {
		t.Fatal(err)
	}
	if err := markOverdueGigs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	read := httptest.NewRecorder()
	app.handleWorkerGigJSON(read, httptest.NewRequest(http.MethodGet, "/worker/late-worker-token/api/gig", nil), "late-worker-token")
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), "Upload the recording") {
		t.Fatalf("read status=%d body=%s", read.Code, read.Body.String())
	}
	upload := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/worker/late-worker-token/upload/init", bytes.NewBufferString(`{"instruction_key":"recording","name":"late.mp4","content_type":"video/mp4","size_bytes":5}`))
	req.Header.Set("Content-Type", "application/json")
	app.handleWorkerUploadInit(upload, req, "late-worker-token")
	if upload.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", upload.Code, upload.Body.String())
	}
}

func TestLifecycleHardAccessExpiryRevokesWorkerLink(t *testing.T) {
	ctx := testCtx(t)
	workerID := seedWorker(t, ctx, "project-a", 141)
	gigID := seedGig(t, ctx, "project-a", "accepted", `{"type":"object","properties":{}}`)
	assignmentID := seedAssignment(t, ctx, gigID, workerID, "accepted", "direct", "hard-expiry-token")
	if _, err := ctx.AppDB().Exec(`UPDATE gig_assignments SET token_expires_at=datetime('now','-1 minute'),access_expiry_source='custom' WHERE id=?`, assignmentID); err != nil {
		t.Fatal(err)
	}
	if err := expireAssignmentAccess(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var gigStatus, assignmentStatus string
	var revoked bool
	if err := ctx.AppDB().QueryRow(`SELECT g.status,a.status,a.token_revoked_at IS NOT NULL
		FROM gigs g JOIN gig_assignments a ON a.gig_id=g.id WHERE g.id=? AND a.id=?`, gigID, assignmentID).
		Scan(&gigStatus, &assignmentStatus, &revoked); err != nil {
		t.Fatal(err)
	}
	if gigStatus != "open" || assignmentStatus != "withdrawn" || !revoked {
		t.Fatalf("gig=%s assignment=%s revoked=%v", gigStatus, assignmentStatus, revoked)
	}
}

func TestExtendDeadlineMovesInheritedAccessButPreservesCustomAccess(t *testing.T) {
	ctx := testCtx(t)
	app := &App{}
	workerID := seedWorker(t, ctx, "project-a", 142)
	oldDue := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	newDue := oldDue.Add(48 * time.Hour)

	inheritedGig := seedGig(t, ctx, "project-a", "accepted", `{}`)
	inheritedAssignment := seedAssignment(t, ctx, inheritedGig, workerID, "accepted", "direct", "inherited-access")
	if _, err := ctx.AppDB().Exec(`UPDATE gigs SET due_at=?,deadline_at=?,access_expires_at=?,access_expiry_source='due' WHERE id=?`, oldDue, oldDue, oldDue.Add(14*24*time.Hour), inheritedGig); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE gig_assignments SET token_expires_at=?,access_expiry_source='due' WHERE id=?`, oldDue.Add(14*24*time.Hour), inheritedAssignment); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolGigsExtendDeadline(ctx, map[string]any{"_project_id": "project-a", "id": inheritedGig, "deadline_at": newDue.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	var inheritedExpiry string
	if err := ctx.AppDB().QueryRow(`SELECT token_expires_at FROM gig_assignments WHERE id=?`, inheritedAssignment).Scan(&inheritedExpiry); err != nil {
		t.Fatal(err)
	}
	parsedInherited, err := time.Parse("2006-01-02 15:04:05-07:00", inheritedExpiry)
	if err != nil {
		parsedInherited, err = time.Parse(time.RFC3339, inheritedExpiry)
	}
	if err != nil || !parsedInherited.Equal(newDue.Add(14*24*time.Hour)) {
		t.Fatalf("inherited expiry=%q parsed=%v err=%v", inheritedExpiry, parsedInherited, err)
	}

	customGig := seedGig(t, ctx, "project-a", "accepted", `{}`)
	customAssignment := seedAssignment(t, ctx, customGig, workerID, "accepted", "direct", "custom-access")
	customExpiry := oldDue.Add(30 * 24 * time.Hour)
	if _, err := ctx.AppDB().Exec(`UPDATE gigs SET due_at=?,deadline_at=?,access_expires_at=?,access_expiry_source='custom' WHERE id=?`, oldDue, oldDue, customExpiry, customGig); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE gig_assignments SET token_expires_at=?,access_expiry_source='custom' WHERE id=?`, customExpiry, customAssignment); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolGigsExtendDeadline(ctx, map[string]any{"_project_id": "project-a", "id": customGig, "deadline_at": newDue.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	var gotCustom string
	if err := ctx.AppDB().QueryRow(`SELECT token_expires_at FROM gig_assignments WHERE id=?`, customAssignment).Scan(&gotCustom); err != nil {
		t.Fatal(err)
	}
	parsedCustom, err := time.Parse("2006-01-02 15:04:05-07:00", gotCustom)
	if err != nil {
		parsedCustom, err = time.Parse(time.RFC3339, gotCustom)
	}
	if err != nil || !parsedCustom.Equal(customExpiry) {
		t.Fatalf("custom expiry changed: %q parsed=%v err=%v", gotCustom, parsedCustom, err)
	}
}

func TestScheduleMigrationRestoresLegacyDeadlineRevokedLink(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i := 1; i <= 5; i++ {
		path := filepath.Join("migrations", fmt.Sprintf("%03d", i)+map[int]string{1: "_init.sql", 2: "_hardening.sql", 3: "_public_domains.sql", 4: "_marketplace.sql", 5: "_worker_responses.sql"}[i])
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := db.Exec(string(raw)); execErr != nil {
			t.Fatalf("apply %s: %v", path, execErr)
		}
	}
	if _, err := db.Exec(`INSERT INTO workers(id,project_id,contact_id,status) VALUES (1,'project-a',1,'active');
		INSERT INTO gigs(id,project_id,created_by,title,derived_result_schema_json,status,deadline_at,completed_at)
		VALUES (10,'project-a','test','Legacy gig','{}','expired','2026-08-20T10:00:00Z','2026-08-20T10:00:00Z');
		INSERT INTO gig_assignments(id,gig_id,worker_id,status,magic_token,mode,token_expires_at,token_revoked_at,responded_at)
		VALUES (20,10,1,'withdrawn','legacy-token','direct','2026-08-20T10:00:00Z','2026-08-20T10:00:00Z','2026-08-19T10:00:00Z');
		INSERT INTO gig_events(project_id,gig_id,kind,actor,body) VALUES ('project-a',10,'expired','system','deadline elapsed');`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("migrations", "006_schedule_access.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	var gigStatus, assignmentStatus, dueAt string
	var revoked, expires bool
	if err := db.QueryRow(`SELECT g.status,a.status,COALESCE(g.due_at,''),a.token_revoked_at IS NOT NULL,a.token_expires_at IS NOT NULL
		FROM gigs g JOIN gig_assignments a ON a.gig_id=g.id WHERE g.id=10`).Scan(&gigStatus, &assignmentStatus, &dueAt, &revoked, &expires); err != nil {
		t.Fatal(err)
	}
	if gigStatus != "accepted" || assignmentStatus != "accepted" || dueAt == "" || revoked || expires {
		t.Fatalf("gig=%s assignment=%s due=%q revoked=%v expires=%v", gigStatus, assignmentStatus, dueAt, revoked, expires)
	}
}

func TestTemplateCanDefaultSoftDueAndAccessGrace(t *testing.T) {
	ctx := testCtx(t)
	app := &App{}
	created, err := app.toolTemplatesCreate(ctx, map[string]any{
		"_project_id":               "project-a",
		"name":                      "Scheduled recording",
		"title_template":            "Recording for {{model}}",
		"default_due_hours":         48,
		"default_access_grace_days": 14,
	})
	if err != nil {
		t.Fatal(err)
	}
	tpl := created.(map[string]any)["template"].(*template)
	if tpl.CurrentVersion == nil || tpl.CurrentVersion.DefaultDueHours != 48 || tpl.CurrentVersion.DefaultAccessGraceDays != 14 {
		t.Fatalf("template defaults=%+v", tpl.CurrentVersion)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE template_versions SET status='active' WHERE id=?`, tpl.CurrentVersion.ID); err != nil {
		t.Fatal(err)
	}
	workerID := seedWorker(t, ctx, "project-a", 143)
	due := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	out, err := app.toolGigsCreateFromTemplate(ctx, map[string]any{
		"_project_id": "project-a",
		"template_id": tpl.ID,
		"vars":        map[string]any{"model": "Ana"},
		"worker_id":   workerID,
		"due_at":      due.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	assignment := out.(map[string]any)["assignment"].(*gigAssignmentView)
	if assignment.AccessExpirySource != "due" {
		t.Fatalf("access source=%q", assignment.AccessExpirySource)
	}
	gotExpiry, err := parseStoredTime(assignment.AccessExpiresAt)
	if err != nil || !gotExpiry.Equal(due.Add(14*24*time.Hour)) {
		t.Fatalf("access expiry=%q parsed=%v err=%v", assignment.AccessExpiresAt, gotExpiry, err)
	}
}

func TestWorkerEndpointsRejectExpiredAndRevokedTokens(t *testing.T) {
	ctx := testCtx(t)
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })

	workerID := seedWorker(t, ctx, "project-a", 16)
	gigID := seedGig(t, ctx, "project-a", "accepted", `{"type":"object","properties":{}}`)
	expiredID := seedAssignment(t, ctx, gigID, workerID, "accepted", "direct", "expired-token")
	revokedID := seedAssignment(t, ctx, gigID, workerID, "accepted", "direct", "revoked-token")
	if _, err := ctx.AppDB().Exec(`UPDATE gig_assignments SET token_expires_at=datetime('now','-1 minute') WHERE id=?`, expiredID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE gig_assignments SET token_revoked_at=CURRENT_TIMESTAMP WHERE id=?`, revokedID); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	for _, tc := range []struct {
		token       string
		writeStatus int
	}{
		{token: "expired-token", writeStatus: http.StatusGone},
		{token: "revoked-token", writeStatus: http.StatusGone},
	} {
		token := tc.token
		t.Run(token+"/read", func(t *testing.T) {
			rec := httptest.NewRecorder()
			app.handleWorkerGigJSON(rec, httptest.NewRequest(http.MethodGet, "/worker/"+token+"/api/gig", nil), token)
			if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "access_ended") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
		t.Run(token+"/submit", func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/worker/"+token+"/submit", bytes.NewBufferString(`{"payload":{}}`))
			req.Header.Set("Content-Type", "application/json")
			app.handleWorkerSubmit(rec, req, token)
			if rec.Code != tc.writeStatus {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
		t.Run(token+"/upload", func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/worker/"+token+"/upload/init", bytes.NewBufferString(`{"name":"proof.txt","content_type":"text/plain","size_bytes":5}`))
			req.Header.Set("Content-Type", "application/json")
			app.handleWorkerUploadInit(rec, req, token)
			if rec.Code != tc.writeStatus {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestWorkerMultipartUploadIsBoundToAssignment(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(storagePlatformStub{}))
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	workerID := seedWorker(t, ctx, "project-a", 15)
	gigID := seedGig(t, ctx, "project-a", "accepted", `{"type":"object","properties":{}}`)
	assignmentID := seedAssignment(t, ctx, gigID, workerID, "accepted", "direct", "upload-token")
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_instructions
		(gig_id,sort_order,instruction_kind,rendered_body_json,result_key)
		VALUES (?,0,'text',?, 'recording')`, gigID,
		`{"markdown":"Upload","response":{"files":{"enabled":true,"required":true,"accept":["video/*"],"min_items":1}}}`); err != nil {
		t.Fatal(err)
	}

	initReq := httptest.NewRequest(http.MethodPost, "/worker/upload-token/upload/init", bytes.NewBufferString(`{"instruction_key":"recording","name":"clip.mp4","content_type":"video/mp4","size_bytes":5}`))
	initRec := httptest.NewRecorder()
	(&App{}).handleWorkerUploadInit(initRec, initReq, "upload-token")
	if initRec.Code != http.StatusOK {
		t.Fatalf("init status=%d body=%s", initRec.Code, initRec.Body.String())
	}
	partReq := httptest.NewRequest(http.MethodPost, "/worker/upload-token/upload/part", bytes.NewBufferString(`{"upload_id":"01TESTUPLOAD","part_number":1,"content_base64":"aGVsbG8="}`))
	partRec := httptest.NewRecorder()
	(&App{}).handleWorkerUploadPart(partRec, partReq, "upload-token")
	if partRec.Code != http.StatusOK {
		t.Fatalf("part status=%d body=%s", partRec.Code, partRec.Body.String())
	}
	completeReq := httptest.NewRequest(http.MethodPost, "/worker/upload-token/upload/complete", bytes.NewBufferString(`{"upload_id":"01TESTUPLOAD"}`))
	completeRec := httptest.NewRecorder()
	(&App{}).handleWorkerUploadComplete(completeRec, completeReq, "upload-token")
	if completeRec.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", completeRec.Code, completeRec.Body.String())
	}
	if err := validateSubmissionAttachments(ctx.AppDB(), assignmentID, []int64{91}); err != nil {
		t.Fatalf("completed upload was not bound: %v", err)
	}
	if err := validateSubmissionAttachments(ctx.AppDB(), assignmentID, []int64{92}); err == nil {
		t.Fatal("unrelated storage id accepted")
	}
}

func TestGigStatusFilterAliasesCompletedAndRejectsUnknown(t *testing.T) {
	ctx := testCtx(t)
	reviewedID := seedGig(t, ctx, "project-a", "reviewed", "{}")
	seedGig(t, ctx, "project-a", "open", "{}")

	// "completed" is the word an agent reaches for when asking what work was
	// finished. It must resolve to "reviewed" instead of matching nothing.
	for _, filter := range []string{"completed", "complete", "done", "reviewed", " Completed "} {
		got, err := listGigSummaries(ctx.AppDB(), "project-a", filter, 0, 0, 50)
		if err != nil {
			t.Fatalf("status=%q: %v", filter, err)
		}
		if len(got) != 1 || got[0].ID != reviewedID || got[0].Status != "reviewed" {
			t.Fatalf("status=%q returned %d gigs, want only the reviewed gig", filter, len(got))
		}
	}

	// An unknown status must fail loudly. Returning an empty list is
	// indistinguishable from a truthful "no such work exists".
	for _, filter := range []string{"banana", "open,banana"} {
		got, err := listGigSummaries(ctx.AppDB(), "project-a", filter, 0, 0, 50)
		if err == nil {
			t.Fatalf("status=%q returned %d gigs, want an error", filter, len(got))
		}
		for _, want := range []string{"banana", "reviewed", "expired"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("status=%q error %q does not mention %q", filter, err, want)
			}
		}
	}

	// The documented default is the live queue, so terminal states stay out.
	got, err := listGigSummaries(ctx.AppDB(), "project-a", "", 0, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != "open" {
		t.Fatalf("default filter returned %d gigs, want only the open gig", len(got))
	}
}

func TestParseGigStatusFilterNormalizesAndRoundTrips(t *testing.T) {
	got, err := parseGigStatusFilter("open, OPEN ,completed,,submitted")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"open", "reviewed", "submitted"}; !slices.Equal(got, want) {
		t.Fatalf("parsed %v want %v", got, want)
	}

	// Every status the app writes must be queryable, so a future status added to
	// the schema without updating gigStatuses fails here rather than in prod.
	for _, status := range gigStatuses {
		got, err := parseGigStatusFilter(status)
		if err != nil || !slices.Equal(got, []string{status}) {
			t.Fatalf("status %q did not round trip: %v (err %v)", status, got, err)
		}
	}
}

// Rescheduling used to move deadline_at only, leaving the title and vars
// stating the original date — and the title is served to the worker.
func TestGigsUpdateFixesRescheduledTitleAndVars(t *testing.T) {
	ctx := testCtx(t)
	app := &App{}
	id := seedGig(t, ctx, "project-a", "offered", "{}")
	if _, err := ctx.AppDB().Exec(`UPDATE gigs SET title=?, vars_json=? WHERE id=?`,
		"HGV — Veronika — Aug 18 recording",
		`{"recording_date":"2026-08-18","recording_window":"18:00-22:00 Moscow time","model":"Veronika"}`,
		id); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolGigsUpdate(ctx, map[string]any{
		"_project_id": "project-a",
		"id":          id,
		"title":       "Veronika — Aug 20 recording",
		"vars": map[string]any{
			"recording_date":   "2026-08-20",
			"recording_window": nil,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	g := out.(map[string]any)["gig"].(*gig)
	if g.Title != "Veronika — Aug 20 recording" {
		t.Fatalf("title = %q", g.Title)
	}
	if g.Vars["recording_date"] != "2026-08-20" {
		t.Fatalf("patched key = %v", g.Vars["recording_date"])
	}
	if _, ok := g.Vars["recording_window"]; ok {
		t.Fatalf("explicit null did not drop the key: %v", g.Vars)
	}
	if g.Vars["model"] != "Veronika" {
		t.Fatalf("untouched key was lost: %v", g.Vars)
	}

	// The worker payload must serve the corrected title, since that is the
	// surface the reschedule bug leaked the stale one through.
	if reloaded, err := loadGig(ctx, "project-a", id); err != nil {
		t.Fatal(err)
	} else if reloaded.Title != "Veronika — Aug 20 recording" {
		t.Fatalf("persisted title = %q", reloaded.Title)
	}
}

func TestGigsUpdateRefusesTerminalGigsAndBadInput(t *testing.T) {
	ctx := testCtx(t)
	app := &App{}

	for _, status := range gigTerminalStatuses {
		id := seedGig(t, ctx, "project-a", status, "{}")
		_, err := app.toolGigsUpdate(ctx, map[string]any{
			"_project_id": "project-a", "id": id, "title": "rewritten",
		})
		if err == nil || !strings.Contains(err.Error(), "cannot update gig in status "+status) {
			t.Fatalf("status %s: expected refusal, got %v", status, err)
		}
	}

	live := seedGig(t, ctx, "project-a", "open", "{}")
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"no fields", map[string]any{"id": live}, "nothing to update"},
		{"empty title", map[string]any{"id": live, "title": "   "}, "non-empty string"},
		{"vars not object", map[string]any{"id": live, "vars": "nope"}, "vars must be an object"},
		{"missing id", map[string]any{"title": "x"}, "id required"},
		{"unknown gig", map[string]any{"id": int64(99999), "title": "x"}, "gig not found"},
	} {
		tc.args["_project_id"] = "project-a"
		if _, err := app.toolGigsUpdate(ctx, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected %q, got %v", tc.name, tc.want, err)
		}
	}
}

// A gig fans out across five child tables. Deleting one must not leave a row
// behind pointing at a parent that no longer exists.
func TestGigsDeleteRemovesGigAndLeavesNoOrphans(t *testing.T) {
	ctx := testCtx(t)
	app := &App{}
	db := ctx.AppDB()

	workerID := seedWorker(t, ctx, "project-a", 30)
	id := seedGig(t, ctx, "project-a", "cancelled", "{}")
	asgID := seedAssignment(t, ctx, id, workerID, "withdrawn", "direct", "delete-me-token")

	// A second gig that must survive untouched — a delete scoped by IN (SELECT
	// ... WHERE gig_id=?) is exactly the shape that can over-reach.
	keepID := seedGig(t, ctx, "project-a", "open", "{}")
	keepAsg := seedAssignment(t, ctx, keepID, workerID, "offered", "direct", "keep-me-token")

	for _, seed := range []struct {
		q    string
		args []any
	}{
		{`INSERT INTO gig_instructions (gig_id,sort_order,instruction_kind,rendered_body_json) VALUES (?,0,'text','{}')`, []any{id}},
		{`INSERT INTO gig_instructions (gig_id,sort_order,instruction_kind,rendered_body_json) VALUES (?,0,'text','{}')`, []any{keepID}},
		{`INSERT INTO gig_submissions (assignment_id,payload_json) VALUES (?,'{}')`, []any{asgID}},
		{`INSERT INTO gig_submissions (assignment_id,payload_json) VALUES (?,'{}')`, []any{keepAsg}},
		{`INSERT INTO gig_events (project_id,gig_id,kind,actor) VALUES ('project-a',?,'created','agent')`, []any{id}},
		{`INSERT INTO gig_events (project_id,gig_id,kind,actor) VALUES ('project-a',?,'created','agent')`, []any{keepID}},
		{`INSERT INTO gig_upload_sessions (upload_id,assignment_id,project_id) VALUES ('up-gone',?,'project-a')`, []any{asgID}},
		{`INSERT INTO gig_upload_sessions (upload_id,assignment_id,project_id) VALUES ('up-kept',?,'project-a')`, []any{keepAsg}},
	} {
		if _, err := db.Exec(seed.q, seed.args...); err != nil {
			t.Fatal(err)
		}
	}

	out, err := app.toolGigsDelete(ctx, map[string]any{"_project_id": "project-a", "id": id})
	if err != nil {
		t.Fatal(err)
	}
	counts := out.(map[string]any)["deleted"].(map[string]int64)
	for label, want := range map[string]int64{
		"gig": 1, "assignments": 1, "submissions": 1, "events": 1,
		"instructions": 1, "upload_sessions": 1,
	} {
		if counts[label] != want {
			t.Fatalf("deleted[%s] = %d want %d (all: %v)", label, counts[label], want, counts)
		}
	}

	// Nothing anywhere may still reference the deleted gig.
	for _, probe := range []struct {
		label, query string
	}{
		{"gigs", `SELECT COUNT(*) FROM gigs WHERE id=?`},
		{"gig_instructions", `SELECT COUNT(*) FROM gig_instructions WHERE gig_id=?`},
		{"gig_assignments", `SELECT COUNT(*) FROM gig_assignments WHERE gig_id=?`},
		{"gig_events", `SELECT COUNT(*) FROM gig_events WHERE gig_id=?`},
		{"gig_submissions", `SELECT COUNT(*) FROM gig_submissions WHERE assignment_id NOT IN (SELECT id FROM gig_assignments)`},
		{"gig_upload_sessions", `SELECT COUNT(*) FROM gig_upload_sessions WHERE assignment_id NOT IN (SELECT id FROM gig_assignments)`},
	} {
		var n int
		var err error
		if strings.Contains(probe.query, "NOT IN") {
			err = db.QueryRow(probe.query).Scan(&n)
		} else {
			err = db.QueryRow(probe.query, id).Scan(&n)
		}
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s still has %d orphaned row(s) after delete", probe.label, n)
		}
	}

	// The bystander gig keeps every one of its own children.
	for _, probe := range []struct {
		label, query string
	}{
		{"gigs", `SELECT COUNT(*) FROM gigs WHERE id=?`},
		{"gig_instructions", `SELECT COUNT(*) FROM gig_instructions WHERE gig_id=?`},
		{"gig_assignments", `SELECT COUNT(*) FROM gig_assignments WHERE gig_id=?`},
		{"gig_events", `SELECT COUNT(*) FROM gig_events WHERE gig_id=?`},
	} {
		var n int
		if err := db.QueryRow(probe.query, keepID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("bystander lost its %s rows (got %d)", probe.label, n)
		}
	}
	var kept int
	if err := db.QueryRow(`SELECT COUNT(*) FROM gig_submissions WHERE assignment_id=?`, keepAsg).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("bystander lost its submission (got %d)", kept)
	}
}

func TestGigsDeleteGuardsLiveWorkUnlessForced(t *testing.T) {
	ctx := testCtx(t)
	app := &App{}

	for _, status := range []string{"open", "offered", "accepted", "submitted"} {
		id := seedGig(t, ctx, "project-a", status, "{}")
		_, err := app.toolGigsDelete(ctx, map[string]any{"_project_id": "project-a", "id": id})
		if err == nil || !strings.Contains(err.Error(), "not finished") {
			t.Fatalf("status %s: expected a guard, got %v", status, err)
		}
		// force=true gets through.
		if _, err := app.toolGigsDelete(ctx, map[string]any{
			"_project_id": "project-a", "id": id, "force": true,
		}); err != nil {
			t.Fatalf("status %s with force: %v", status, err)
		}
		var n int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM gigs WHERE id=?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("status %s: force did not delete", status)
		}
	}

	if _, err := app.toolGigsDelete(ctx, map[string]any{"_project_id": "project-a", "id": int64(98765)}); err == nil ||
		!strings.Contains(err.Error(), "gig not found") {
		t.Fatalf("unknown gig: %v", err)
	}
	if _, err := app.toolGigsDelete(ctx, map[string]any{"_project_id": "project-a"}); err == nil ||
		!strings.Contains(err.Error(), "id required") {
		t.Fatalf("missing id: %v", err)
	}
}

// Cross-project deletion must not be possible.
func TestGigsDeleteIsProjectScoped(t *testing.T) {
	ctx := testCtx(t)
	app := &App{}
	other := seedGig(t, ctx, "project-b", "cancelled", "{}")
	if _, err := app.toolGigsDelete(ctx, map[string]any{"_project_id": "project-a", "id": other}); err == nil {
		t.Fatal("deleted a gig belonging to another project")
	}
	var n int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM gigs WHERE id=?`, other).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("other project's gig was destroyed")
	}
}
