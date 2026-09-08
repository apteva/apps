package main

// Regression coverage for the production upload and integrity audit.
import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type auditStorage struct {
	storagePlatformStub
	signed   []int64
	existing bool
	deleted  []int64
}

func (p *auditStorage) CallAppResult(app, tool string, input map[string]any, out any) error {
	if tool == "files_get_url" {
		p.signed = append(p.signed, int64Cast(input["id"]))
	}
	if tool == "files_delete" {
		p.deleted = append(p.deleted, int64Cast(input["id"]))
	}
	if tool == "storage_upload_complete" && p.existing {
		return json.Unmarshal([]byte(`{"file":{"id":91},"was_existing":true}`), out)
	}
	return p.storagePlatformStub.CallAppResult(app, tool, input, out)
}
func TestRegressionDeduplicatedCompletionRetainsExistingFile(t *testing.T) {
	p := &auditStorage{existing: true}
	ctx, gid, aid := auditWorker(t, p)
	_, err := ctx.AppDB().Exec(`INSERT INTO gig_instructions(gig_id,sort_order,instruction_kind,rendered_body_json,result_key) VALUES (?,0,'input_video_recording','{}','clip')`, gid)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO gig_upload_sessions(upload_id,assignment_id,project_id,status,instruction_key,filename,content_type,size_bytes) VALUES ('01AUDITSESSION',?,'project-a','uploading','clip','clip.mp4','video/mp4',5)`, aid)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	(&App{}).handleWorkerUploadComplete(w, httptest.NewRequest("POST", "/upload/complete", strings.NewReader(`{"upload_id":"01AUDITSESSION"}`)), "audit-token")
	if w.Code != 200 {
		t.Fatalf("complete: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	(&App{}).handleWorkerUploadRemove(w, httptest.NewRequest("POST", "/upload/remove", strings.NewReader(`{"instruction_key":"clip","storage_file_id":91}`)), "audit-token")
	if w.Code != 200 || len(p.deleted) != 0 {
		t.Fatalf("remove: %d %s deleted=%v", w.Code, w.Body.String(), p.deleted)
	}
}

type auditMarketplace struct {
	marketplacePlatformStub
	vendorEmail string
}

func (p *auditMarketplace) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app == "crm" && tool == "contacts_get" {
		id := int64Cast(input["id"])
		raw, _ := json.Marshal(map[string]any{"found": true, "contact": map[string]any{"id": id, "primary_email": map[int64]string{88: "old-worker@example.test", 99: "new-worker@example.test"}[id]}})
		return json.Unmarshal(raw, out)
	}
	if tool == "vendors_upsert_by_email" {
		p.vendorEmail = strArg(input, "email")
	}
	return p.marketplacePlatformStub.CallAppResult(app, tool, input, out)
}
func TestRegressionReassignedGigPaysReviewedWorker(t *testing.T) {
	p := &auditMarketplace{}
	ctx := marketplaceCtx(t, p)
	oldWorker := seedWorker(t, ctx, "project-a", 88)
	newWorker := seedWorker(t, ctx, "project-a", 99)
	gid := seedGig(t, ctx, "project-a", "offered", `{"type":"object","properties":{}}`)
	seedAssignment(t, ctx, gid, oldWorker, "offered", "direct", "original-worker")
	_, err := ctx.AppDB().Exec(`INSERT INTO gig_compensation(project_id,gig_id,worker_id,pricing_model,rate_amount_minor,quantity,worker_amount_minor,currency,rate_source) VALUES('project-a',?,?,'fixed',12500,1,12500,'EUR','audit')`, gid, oldWorker)
	if err != nil {
		t.Fatal(err)
	}
	ass, err := assignGig(ctx, "project-a", gid, newWorker, "direct", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Model the new worker's completed review without making external calls.
	_, err = ctx.AppDB().Exec(`UPDATE gigs SET status='reviewed' WHERE id=?`, gid)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctx.AppDB().Exec(`UPDATE gig_assignments SET status='reviewed' WHERE id=?`, ass.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = createGigPayable(ctx, "project-a", gid)
	if err == nil {
		t.Fatal("reviewed assignment bypassed explicit financial approval")
	}
	if p.vendorEmail != "" {
		t.Fatalf("unexpected payee creation before approval: %s", p.vendorEmail)
	}
}
func auditWorker(t *testing.T, p sdk.PlatformClient) (*sdk.AppCtx, int64, int64) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	wid := seedWorker(t, ctx, "project-a", 1)
	gid := seedGig(t, ctx, "project-a", "accepted", `{"type":"object","properties":{}}`)
	aid := seedAssignment(t, ctx, gid, wid, "accepted", "direct", "audit-token")
	return ctx, gid, aid
}
func TestRegressionDraftCannotSignUnrelatedStorageFile(t *testing.T) {
	p := &auditStorage{}
	ctx, _, aid := auditWorker(t, p)
	r := httptest.NewRequest("POST", "/worker/audit-token/draft", strings.NewReader(`{"payload":{"unrecognized":{"storage_file_id":999}},"attachment_file_ids":[]}`))
	w := httptest.NewRecorder()
	(&App{}).handleWorkerDraft(w, r, "audit-token")
	if w.Code != 400 {
		t.Fatalf("draft accepted forged file: %d %s", w.Code, w.Body.String())
	}
	// Old stored drafts cannot bypass the read boundary either.
	_, err := saveWorkerDraft(ctx.AppDB(), aid, map[string]any{"unrecognized": map[string]any{"storage_file_id": 999, "signed_url": "https://untrusted.test/old"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	(&App{}).handleWorkerGigJSON(w, httptest.NewRequest("GET", "/worker/audit-token/api/gig", nil), "audit-token")
	if w.Code != 200 || len(p.signed) != 0 || strings.Contains(w.Body.String(), "signed_url") {
		t.Fatalf("unsafe read: %d %s calls=%v", w.Code, w.Body.String(), p.signed)
	}
}
func TestRegressionPartialMultiFileDraftPersists(t *testing.T) {
	ctx, gid, aid := auditWorker(t, &auditStorage{})
	_, err := ctx.AppDB().Exec(`INSERT INTO gig_instructions(gig_id,sort_order,instruction_kind,rendered_body_json,result_key) VALUES (?,0,'text',?,'step_1')`, gid, `{"response":{"files":{"enabled":true,"required":true,"min_items":2,"max_items":3}}}`)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"instruction_responses": []any{map[string]any{"key": "step_1", "files": []any{map[string]any{"storage_file_id": 91}}}}}
	_, err = ctx.AppDB().Exec(`INSERT INTO gig_upload_sessions(upload_id,assignment_id,project_id,status,storage_file_id,instruction_key) VALUES('01PARTIAL',?,'project-a','completed',91,'step_1')`, aid)
	if err != nil {
		t.Fatal(err)
	}
	err = validateInstructionResponses(ctx.AppDB(), gid, aid, payload, false)
	if err != nil {
		t.Fatalf("partial draft rejected: %v", err)
	}
	if err = validateInstructionResponses(ctx.AppDB(), gid, aid, payload, true); err == nil {
		t.Fatal("incomplete final submission accepted")
	}
}
func TestRegressionCompletedUploadIsIdempotent(t *testing.T) {
	ctx, gid, aid := auditWorker(t, &auditStorage{})
	_, err := ctx.AppDB().Exec(`INSERT INTO gig_instructions(gig_id,sort_order,instruction_kind,rendered_body_json,result_key) VALUES (?,0,'input_video_recording','{}','clip')`, gid)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO gig_upload_sessions(upload_id,assignment_id,project_id,status,instruction_key,filename,content_type,size_bytes) VALUES ('01AUDITSESSION',?,'project-a','uploading','clip','clip.mp4','video/mp4',5)`, aid)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"upload_id": "01AUDITSESSION"})
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/worker/audit-token/upload/complete", bytes.NewReader(raw))
		(&App{}).handleWorkerUploadComplete(w, r, "audit-token")
		if i == 0 && w.Code != 200 {
			t.Fatalf("first completion: %d %s", w.Code, w.Body.String())
		}
		if i == 1 && w.Code != 200 {
			t.Fatalf("repeated completion: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestRegressionConcurrentReviewsClaimSubmissionOnce(t *testing.T) {
	ctx := testCtx(t)
	wid := seedWorker(t, ctx, "project-a", 55)
	gid := seedGig(t, ctx, "project-a", "submitted", `{"type":"object","properties":{}}`)
	aid := seedAssignment(t, ctx, gid, wid, "submitted", "direct", "concurrent-review")
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_submissions(assignment_id,payload_json,channel) VALUES(?,'{}','web')`, aid); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			<-start
			_, err := (&App{}).toolGigsAccept(ctx, map[string]any{"_project_id": "project-a", "id": gid})
			done <- err
		}()
	}
	close(start)
	successes := 0
	for i := 0; i < 8; i++ {
		if <-done == nil {
			successes++
		}
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT accepted_count FROM workers WHERE id=?`, wid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || count != 1 {
		t.Fatalf("successes=%d accepted_count=%d", successes, count)
	}
}

func TestRegressionConcurrentMilestoneDispatchLeavesOneGig(t *testing.T) {
	ctx := marketplaceCtx(t, &marketplacePlatformStub{})
	wid := seedWorker(t, ctx, "project-a", 88)
	tid, _ := seedPublishedTemplate(t, ctx)
	result, err := (&App{}).toolContractsCreate(ctx, map[string]any{"_project_id": "project-a", "title": "Audit", "source_type": "direct", "activate": true, "worker_id": wid, "template_id": tid, "pricing_model": "fixed", "worker_amount_minor": 10000, "currency": "EUR", "milestones": []any{map[string]any{"title": "Only milestone", "worker_amount_minor": 10000, "currency": "EUR"}}})
	if err != nil {
		t.Fatal(err)
	}
	c := result.(map[string]any)["contract"].(*contract)
	added, err := (&App{}).toolContractsAddMilestone(ctx, map[string]any{"_project_id": "project-a", "contract_id": c.ID, "title": "Only milestone", "worker_amount_minor": 10000, "currency": "EUR"})
	if err != nil {
		t.Fatal(err)
	}
	c = added.(map[string]any)["contract"].(*contract)
	if len(c.Milestones) != 1 {
		t.Fatalf("milestones=%+v", c.Milestones)
	}
	start := make(chan struct{})
	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			<-start
			_, e := (&App{}).toolContractsDispatchMilestone(ctx, map[string]any{"_project_id": "project-a", "contract_id": c.ID, "milestone_id": c.Milestones[0].ID, "notify_worker": false})
			done <- e
		}()
	}
	close(start)
	successes := 0
	for i := 0; i < 4; i++ {
		if <-done == nil {
			successes++
		}
	}
	var gigs, assignments int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM gigs`).Scan(&gigs); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM gig_assignments`).Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || gigs != 1 || assignments != 1 {
		t.Fatalf("successes=%d gigs=%d assignments=%d", successes, gigs, assignments)
	}
}

func TestRegressionDraftSaveKeepsUploadFinalizedAfterCapture(t *testing.T) {
	ctx, _, aid := auditWorker(t, &auditStorage{})
	current := map[string]any{"instruction_responses": []any{map[string]any{"key": "step_1", "files": []any{map[string]any{"storage_file_id": 91}}}}}
	if _, err := saveWorkerDraft(ctx.AppDB(), aid, current, []int64{91}); err != nil {
		t.Fatal(err)
	}
	stale := map[string]any{"instruction_responses": []any{map[string]any{"key": "step_1", "note": "Newest note", "files": []any{}}}}
	draft, err := saveWorkerDraft(ctx.AppDB(), aid, stale, nil, "step_1")
	if err != nil {
		t.Fatal(err)
	}
	if ids := draftAttachmentIDs(draft.Payload); len(ids) != 1 || ids[0] != 91 {
		t.Fatalf("upload lost: %+v", draft)
	}
	if !strings.Contains(mustJSON(draft.Payload), "Newest note") {
		t.Fatal("latest note lost")
	}
}

func TestRegressionWorkerCompensationHidesInternalTerms(t *testing.T) {
	raw := mustJSON(publicWorkerCompensation(&gigCompensation{WorkerAmountMinor: 12345, Currency: "EUR", PricingModel: "fixed", Quantity: 1, CustomerAmountMinor: 56789, PayableBillID: 444}))
	for _, key := range []string{"customer_amount_minor", "payable_bill_id", "rate_source", "worker_id"} {
		if strings.Contains(raw, key) {
			t.Fatalf("internal field leaked: %s", raw)
		}
	}
	if !strings.Contains(raw, `"worker_amount_minor":12345`) {
		t.Fatal("worker terms missing")
	}
}

func TestRegressionBinaryPartsAreScopedAndSized(t *testing.T) {
	ctx, _, aid := auditWorker(t, &auditStorage{})
	if _, err := ctx.AppDB().Exec(`INSERT INTO gig_upload_sessions(upload_id,assignment_id,project_id,status,instruction_key,transport,part_size,size_bytes) VALUES('01SCOPEAUDIT',?,'project-a','uploading','clip','binary',1024,1500)`, aid); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query        string
		size, status int
	}{
		{"upload_id=01OTHERSESSION&part_number=1", 1024, 404},
		{"upload_id=01SCOPEAUDIT&part_number=3", 1024, 400},
		{"upload_id=01SCOPEAUDIT&part_number=1", 512, 400},
	} {
		r := httptest.NewRequest("PUT", "/worker/audit-token/upload/part?"+tc.query, bytes.NewReader(make([]byte, tc.size)))
		w := httptest.NewRecorder()
		(&App{}).handleWorkerRoot(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.query, w.Code, w.Body.String())
		}
	}
}

func TestRegressionEmbeddedWorkerScriptParses(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("Bun is needed to parse the browser script")
	}
	page := workerPageHTML("script-audit-token")
	scripts := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(page, -1)
	if len(scripts) == 0 {
		t.Fatal("worker script missing")
	}
	for _, script := range scripts {
		cmd := exec.Command(bun, "-e", `new Function(await Bun.stdin.text());`)
		cmd.Stdin = strings.NewReader(script[1])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("worker script: %s %v", out, err)
		}
	}
}

func TestRegressionInitialWorkerFetchPreservesInstallSelector(t *testing.T) {
	page := workerPageHTML("test-token")
	if !strings.Contains(page, `fetch(publicWorkerURL("/api/gig"))`) || strings.Contains(page, `fetch(API + "/api/gig")`) {
		t.Fatal("initial gig fetch bypasses the installation-scoped URL helper")
	}
}
