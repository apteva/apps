package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

const testProject = "creators-test"

type storageLookupPlatform struct {
	tk.BasePlatformClient
	found       bool
	contentType string
	signedURL   string
}

func (p *storageLookupPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app != "storage" {
		return nil
	}
	switch tool {
	case "files_get":
		file := out.(*storageFileMetadata)
		if p.found {
			contentType := p.contentType
			if contentType == "" {
				contentType = "image/png"
			}
			*file = storageFileMetadata{ID: int64Arg(input, "id"), Name: "trusted.png", ContentType: contentType, SizeBytes: 68}
		}
	case "files_get_url":
		result := out.(*map[string]any)
		*result = map[string]any{"url": p.signedURL}
	}
	return nil
}

func testContext(t *testing.T) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject))
	globalCtx = ctx
	return ctx
}

func mustSpace(t *testing.T, ctx *sdk.AppCtx, pid, name, slug string) *Space {
	t.Helper()
	space, err := createSpace(ctx, pid, map[string]any{"name": name, "slug": slug, "default_currency": "USD"})
	if err != nil {
		t.Fatalf("create space: %v", err)
	}
	return space
}

func mustTier(t *testing.T, ctx *sdk.AppCtx, pid string, spaceID int64, interval string) *Tier {
	t.Helper()
	tier, err := createTier(ctx, pid, spaceID, map[string]any{"name": "Supporter", "price_cents": 1200, "currency": "USD", "interval": interval})
	if err != nil {
		t.Fatalf("create tier: %v", err)
	}
	return tier
}

func mustMember(t *testing.T, ctx *sdk.AppCtx, pid string, spaceID int64, args map[string]any) *Member {
	t.Helper()
	member, _, _, err := upsertMember(ctx, pid, spaceID, args)
	if err != nil {
		t.Fatalf("upsert member: %v", err)
	}
	return member
}

func TestManifestAndSpaceScopedSchemas(t *testing.T) {
	a := &App{}
	m := a.Manifest()
	if m.Name != "creators" || m.Version != "0.3.0" {
		t.Fatalf("manifest = %s %s", m.Name, m.Version)
	}
	if len(a.Workers()) != 1 || len(a.EventHandlers()) != 3 || a.EventHandlers()[0].Event != "invoice.paid" {
		t.Fatal("membership lifecycle worker and billing lifecycle handlers must be registered")
	}
	publicRoutes := map[string]bool{}
	for _, route := range m.Provides.HTTPRoutes {
		if route.NoAuth {
			publicRoutes[route.Prefix] = true
		}
	}
	if !publicRoutes["/public/"] || !publicRoutes["/member/"] {
		t.Fatalf("public and member routes must be declared no-auth: %#v", m.Provides.HTTPRoutes)
	}
	for _, tool := range a.MCPTools() {
		if strings.HasPrefix(tool.Name, "creators_space_") {
			continue
		}
		props, _ := tool.InputSchema["properties"].(map[string]any)
		if props["space_id"] == nil || props["space_slug"] == nil {
			t.Errorf("%s does not expose creator-space selectors", tool.Name)
		}
	}
}

func TestMemberUpsertDoesNotDowngradeExistingStatus(t *testing.T) {
	ctx := testContext(t)
	space := mustSpace(t, ctx, testProject, "Primary", "primary")
	member := mustMember(t, ctx, testProject, space.ID, map[string]any{"email": "paid@example.com", "status": "active"})
	updated, created, _, err := upsertMember(ctx, testProject, space.ID, map[string]any{"email": member.Email, "display_name": "Paid Member"})
	if err != nil || created {
		t.Fatalf("second upsert: created=%v err=%v", created, err)
	}
	if updated.Status != "active" {
		t.Fatalf("status=%q, want active", updated.Status)
	}
}

func TestMemberHTTPReadsRedactPortalToken(t *testing.T) {
	ctx := testContext(t)
	space := mustSpace(t, ctx, testProject, "Primary", "primary")
	member := mustMember(t, ctx, testProject, space.ID, map[string]any{"email": "secret@example.com", "status": "comped"})
	req := httptest.NewRequest(http.MethodGet, "/members?project_id="+testProject+"&space_id="+jsonNumber(space.ID), nil)
	rec := httptest.NewRecorder()
	(&App{}).handleMembers(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), member.PortalToken) || strings.Contains(rec.Body.String(), "portal_token") {
		t.Fatalf("member credential leaked in response: %s", rec.Body.String())
	}
	raw, _ := json.Marshal(member)
	if strings.Contains(string(raw), member.PortalToken) {
		t.Fatal("Member JSON serialization must never expose PortalToken")
	}
	post, err := createPost(ctx, testProject, space.ID, map[string]any{
		"title": "Member picture", "status": "published", "visibility": "members",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO attachments (project_id, space_id, post_id, storage_file_id, filename, content_type, visibility) VALUES (?, ?, ?, 9, 'member.png', 'image/png', 'inherit')`, testProject, space.ID, post.ID); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/member/"+member.PortalToken, nil)
	rec = httptest.NewRecorder()
	(&App{}).handleMemberPortal(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"filename":"member.png"`) {
		t.Fatalf("member portal omitted accessible attachment: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), member.PortalToken) || strings.Contains(rec.Body.String(), "portal_token") {
		t.Fatalf("member credential leaked in portal response: %s", rec.Body.String())
	}
}

func TestPortalTokenCannotAuthorizeAnotherSpace(t *testing.T) {
	ctx := testContext(t)
	a := mustSpace(t, ctx, testProject, "A", "a")
	b := mustSpace(t, ctx, testProject, "B", "b")
	member := mustMember(t, ctx, testProject, a.ID, map[string]any{"email": "a@example.com", "status": "active"})
	post, err := createPost(ctx, testProject, b.ID, map[string]any{"title": "B only", "status": "published", "visibility": "members"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO attachments (project_id, space_id, post_id, storage_file_id, visibility) VALUES (?, ?, ?, 9, 'inherit')`, testProject, b.ID, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	attachmentID, _ := res.LastInsertId()
	_, err = getDownloadLink(ctx, testProject, b.ID, map[string]any{"attachment_id": attachmentID, "portal_token": member.PortalToken})
	if err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("cross-space token error=%v", err)
	}
}

func TestExpiredMembershipCannotAccessAndWorkerMarksPastDue(t *testing.T) {
	ctx := testContext(t)
	space := mustSpace(t, ctx, testProject, "Primary", "primary")
	member := mustMember(t, ctx, testProject, space.ID, map[string]any{"email": "expired@example.com", "status": "active"})
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if _, err := ctx.AppDB().Exec(`UPDATE members SET current_period_end=? WHERE id=?`, past, member.ID); err != nil {
		t.Fatal(err)
	}
	member, _ = getMember(ctx.AppDB(), testProject, space.ID, member.ID)
	if memberCanAccessStatus(member) {
		t.Fatal("expired active member was authorized")
	}
	if err := runCreatorLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	member, _ = getMember(ctx.AppDB(), testProject, space.ID, member.ID)
	if member.Status != "past_due" {
		t.Fatalf("status=%q, want past_due", member.Status)
	}
}

func TestInvoicePaidActivatesMembershipExactlyOnce(t *testing.T) {
	ctx := testContext(t)
	space := mustSpace(t, ctx, testProject, "Primary", "primary")
	tier := mustTier(t, ctx, testProject, space.ID, "month")
	member := mustMember(t, ctx, testProject, space.ID, map[string]any{"email": "payer@example.com"})
	payment, _, err := reserveMembershipPayment(ctx.AppDB(), testProject, space.ID, member.ID, tier.ID, "initial", 2, 2400, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if err := attachMembershipInvoice(ctx.AppDB(), payment.ID, 44); err != nil {
		t.Fatal(err)
	}
	event := sdk.Event{Event: "invoice.paid", SourceApp: "billing", ProjectID: testProject, Data: map[string]any{"id": float64(44), "status": "paid"}}
	partial := event
	partial.Data = map[string]any{"id": float64(44), "status": "open"}
	if err := (&App{}).handleInvoicePaid(ctx, partial); err != nil {
		t.Fatal(err)
	}
	member, _ = getMember(ctx.AppDB(), testProject, space.ID, member.ID)
	if member.Status != "lead" {
		t.Fatalf("partial payment activated membership: %q", member.Status)
	}
	if err := (&App{}).handleInvoicePaid(ctx, event); err != nil {
		t.Fatal(err)
	}
	member, _ = getMember(ctx.AppDB(), testProject, space.ID, member.ID)
	if member.Status != "active" || member.TierID == nil || *member.TierID != tier.ID || member.CurrentPeriodEnd == "" {
		t.Fatalf("member not activated correctly: %#v", member)
	}
	metrics, err := membershipMetrics(ctx.AppDB(), testProject, space.ID)
	if err != nil {
		t.Fatal(err)
	}
	mrr := metrics["mrr_by_currency"].(map[string]int64)
	if mrr["USD"] != 1200 {
		t.Fatalf("paid MRR=%d, want 1200", mrr["USD"])
	}
	firstEnd := member.CurrentPeriodEnd
	if err := (&App{}).handleInvoicePaid(ctx, event); err != nil {
		t.Fatal(err)
	}
	member, _ = getMember(ctx.AppDB(), testProject, space.ID, member.ID)
	if member.CurrentPeriodEnd != firstEnd {
		t.Fatalf("duplicate event extended period twice: %s -> %s", firstEnd, member.CurrentPeriodEnd)
	}
}

func TestScheduledPublisherAndPublicProjectRouting(t *testing.T) {
	ctx := testContext(t)
	a := mustSpace(t, ctx, "project-a", "Creator A", "creator")
	b := mustSpace(t, ctx, "project-b", "Creator B", "creator")
	post, err := createPost(ctx, "project-b", b.ID, map[string]any{
		"title": "Scheduled", "status": "scheduled", "visibility": "public",
		"scheduled_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runCreatorLifecycle(ctx); err != nil {
		t.Fatal(err)
	}
	post, _ = getPost(ctx.AppDB(), "project-b", b.ID, post.ID, false)
	if post.Status != "published" {
		t.Fatalf("scheduled post status=%q", post.Status)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO attachments (project_id, space_id, post_id, storage_file_id, filename, content_type, visibility) VALUES (?, ?, ?, 9, 'public.png', 'image/png', 'inherit')`, "project-b", b.ID, post.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO attachments (project_id, space_id, post_id, storage_file_id, filename, content_type, visibility) VALUES (?, ?, ?, 10, 'members-secret.png', 'image/png', 'members')`, "project-b", b.ID, post.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/public/creator?project_id=project-b", nil)
	rec := httptest.NewRecorder()
	(&App{}).handlePublic(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"project_id":"project-b"`) || strings.Contains(rec.Body.String(), `"project_id":"project-a"`) {
		t.Fatalf("public route selected wrong project: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"filename":"public.png"`) {
		t.Fatalf("public feed omitted accessible attachment: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "members-secret.png") {
		t.Fatalf("public feed leaked restricted attachment metadata: %s", rec.Body.String())
	}
	_ = a
}

func TestTierValidationAndPaymentReservationIdempotency(t *testing.T) {
	ctx := testContext(t)
	space := mustSpace(t, ctx, testProject, "Primary", "primary")
	if _, err := createTier(ctx, testProject, space.ID, map[string]any{"name": "Bad", "price_cents": -1, "currency": "USD"}); err == nil {
		t.Fatal("negative tier price accepted")
	}
	if _, err := createTier(ctx, testProject, space.ID, map[string]any{"name": "Bad", "price_cents": 10, "currency": "US"}); err == nil {
		t.Fatal("invalid currency accepted")
	}
	tier := mustTier(t, ctx, testProject, space.ID, "month")
	member := mustMember(t, ctx, testProject, space.ID, map[string]any{"email": "idem@example.com"})
	first, created, err := reserveMembershipPayment(ctx.AppDB(), testProject, space.ID, member.ID, tier.ID, "renewal", 1, 1200, "USD")
	if err != nil || !created {
		t.Fatalf("first reserve created=%v err=%v", created, err)
	}
	second, created, err := reserveMembershipPayment(ctx.AppDB(), testProject, space.ID, member.ID, tier.ID, "renewal", 1, 1200, "USD")
	if err != nil || created || first.ID != second.ID {
		t.Fatalf("duplicate reserve created=%v first=%d second=%d err=%v", created, first.ID, second.ID, err)
	}
}

func TestMemberCanAccessAttachment(t *testing.T) {
	tierIDs, _ := json.Marshal([]int64{10})
	post := &Post{Status: "published", Visibility: "tier", TierIDs: tierIDs}
	att := &Attachment{Visibility: "inherit"}
	tierID := int64(10)
	member := &Member{Status: "active", TierID: &tierID}
	if !memberCanAccessAttachment(member, post, att) {
		t.Fatal("active member in gated tier should access inherited attachment")
	}
	otherTier := int64(11)
	member.TierID = &otherTier
	if memberCanAccessAttachment(member, post, att) {
		t.Fatal("member in another tier should not access tier-gated post")
	}
	post.Visibility = "members"
	att.Visibility = "public"
	member.Status = "comped"
	if !memberCanAccessAttachment(member, post, att) {
		t.Fatal("entitled member should access a public attachment on a gated post")
	}
}

func TestCRMStateAttributesAreNamespacedByCreatorSpace(t *testing.T) {
	tierID := int64(7)
	member := &Member{SpaceID: 42, Status: "active", TierID: &tierID, CurrentPeriodEnd: "2026-08-14T00:00:00Z"}
	attributes := crmStateAttributes(member)
	raw, _ := json.Marshal(attributes)
	got := string(raw)
	for _, key := range []string{
		"creators_space_42_status",
		"creators_space_42_tier_id",
		"creators_space_42_period_end",
	} {
		if !strings.Contains(got, key) {
			t.Fatalf("missing %q in %s", key, got)
		}
	}
	if strings.Contains(got, `"key":"creators_status"`) {
		t.Fatalf("unscoped creator state would collide across spaces: %s", got)
	}
}

func TestAddFromStorageValidatesFileAndUsesAuthoritativeMetadata(t *testing.T) {
	platform := &storageLookupPlatform{found: true}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject), tk.WithPlatform(platform))
	globalCtx = ctx
	space := mustSpace(t, ctx, testProject, "Primary", "primary")
	post, err := createPost(ctx, testProject, space.ID, map[string]any{"title": "Picture"})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := addAttachment(ctx, testProject, space.ID, map[string]any{
		"post_id": post.ID, "storage_file_id": int64(9),
		"filename": "spoofed.exe", "content_type": "application/octet-stream", "size_bytes": int64(999),
	})
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Filename != "trusted.png" || attachment.ContentType != "image/png" || attachment.SizeBytes != 68 {
		t.Fatalf("attachment metadata was not resolved from storage: %#v", attachment)
	}
	platform.found = false
	if _, err := addAttachment(ctx, testProject, space.ID, map[string]any{"post_id": post.ID, "storage_file_id": int64(10)}); err == nil {
		t.Fatal("missing storage file was attached")
	}
}

func jsonNumber(value int64) string {
	return strings.TrimSpace(string(mustJSON(value)))
}

func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
