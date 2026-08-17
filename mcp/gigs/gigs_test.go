package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

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
	if manifest.Version != "0.1.23" {
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
	if err := validateSubmission(ctx.AppDB(), gigID, map[string]any{"rating": 6.0, "choice": "a"}); err == nil {
		t.Fatal("expected maximum validation error")
	}
	if err := validateSubmission(ctx.AppDB(), gigID, map[string]any{"rating": 4.0, "choice": "x"}); err == nil {
		t.Fatal("expected enum validation error")
	}
	if err := validateSubmission(ctx.AppDB(), gigID, map[string]any{"rating": 4.0, "choice": "b"}); err != nil {
		t.Fatalf("valid submission rejected: %v", err)
	}
}

func TestFirstComeSubmissionWithdrawsOtherWorkers(t *testing.T) {
	ctx := testCtx(t)
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })

	w1 := seedWorker(t, ctx, "project-a", 11)
	w2 := seedWorker(t, ctx, "project-a", 12)
	gigID := seedGig(t, ctx, "project-a", "offered", `{"type":"object","properties":{}}`)
	seedAssignment(t, ctx, gigID, w1, "offered", "first-come", "token-one")
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

func TestLifecycleExpiresGigAndRevokesAssignments(t *testing.T) {
	ctx := testCtx(t)
	workerID := seedWorker(t, ctx, "project-a", 14)
	gigID := seedGig(t, ctx, "project-a", "offered", `{"type":"object","properties":{}}`)
	assignmentID := seedAssignment(t, ctx, gigID, workerID, "offered", "direct", "expiry-token")
	if _, err := ctx.AppDB().Exec(`UPDATE gigs SET deadline_at=datetime('now','-1 minute') WHERE id=?`, gigID); err != nil {
		t.Fatal(err)
	}
	if err := expireDueGigs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var gigStatus, assignmentStatus string
	var revoked bool
	if err := ctx.AppDB().QueryRow(`SELECT g.status,a.status,a.token_revoked_at IS NOT NULL
		FROM gigs g JOIN gig_assignments a ON a.gig_id=g.id WHERE g.id=? AND a.id=?`, gigID, assignmentID).
		Scan(&gigStatus, &assignmentStatus, &revoked); err != nil {
		t.Fatal(err)
	}
	if gigStatus != "expired" || assignmentStatus != "withdrawn" || !revoked {
		t.Fatalf("gig=%s assignment=%s revoked=%v", gigStatus, assignmentStatus, revoked)
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
		{token: "expired-token", writeStatus: http.StatusNotFound},
		{token: "revoked-token", writeStatus: http.StatusGone},
	} {
		token := tc.token
		t.Run(token+"/read", func(t *testing.T) {
			rec := httptest.NewRecorder()
			app.handleWorkerGigJSON(rec, httptest.NewRequest(http.MethodGet, "/worker/"+token+"/api/gig", nil), token)
			if rec.Code != http.StatusNotFound {
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

	initReq := httptest.NewRequest(http.MethodPost, "/worker/upload-token/upload/init", bytes.NewBufferString(`{"name":"clip.mp4","content_type":"video/mp4","size_bytes":5}`))
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
