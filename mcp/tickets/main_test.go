package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/tickets.db")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	body, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPanelBundleUsesProductionJSXRuntime(t *testing.T) {
	body, err := os.ReadFile("ui/TicketsPanel.mjs")
	if err != nil {
		t.Fatal(err)
	}
	module := string(body)
	if strings.Contains(module, "react/jsx-dev-runtime") || strings.Contains(module, "jsxDEV") {
		t.Fatal("panel bundle contains the unsupported React development JSX runtime")
	}
	if !strings.Contains(module, "react/jsx-runtime") {
		t.Fatal("panel bundle does not import the production React JSX runtime")
	}
	if !strings.Contains(module, "content-start") {
		t.Fatal("panel bundle is missing the compact grid alignment that prevents editor rows from stretching")
	}
	for _, marker := range []string{"data-ticket-kanban", "data-kanban-status", "draggable", "Moved on Kanban board"} {
		if !strings.Contains(module, marker) {
			t.Fatalf("panel bundle is missing Kanban marker %q", marker)
		}
	}
}

func TestManifestAndHandlersStayInSync(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "tickets" || m.Version != "0.1.4" {
		t.Fatalf("manifest identity = %s %s", m.Name, m.Version)
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Fatal("database migrations missing")
	}
	declared := map[string]bool{}
	for _, tool := range m.Provides.MCPTools {
		declared[tool.Name] = true
	}
	implemented := map[string]bool{}
	for _, tool := range app.MCPTools() {
		implemented[tool.Name] = true
	}
	if len(declared) != len(implemented) {
		t.Fatalf("declared=%d implemented=%d", len(declared), len(implemented))
	}
	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest tool %s has no handler", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("handler %s is not declared", name)
		}
	}
	optional := map[string]bool{}
	for _, dep := range m.Requires.Apps {
		optional[dep.Name] = dep.Optional
	}
	for _, name := range []string{"crm", "storage", "tasks", "code", "channels"} {
		if !optional[name] {
			t.Errorf("%s must remain optional", name)
		}
	}
	publicPortalDeclared := false
	for _, route := range m.Provides.HTTPRoutes {
		if route.Prefix == "/p/" && route.NoAuth {
			publicPortalDeclared = true
		}
	}
	if !publicPortalDeclared {
		t.Fatal("manifest must declare /p/ as no_auth so token-authenticated client routes pass the platform proxy")
	}
}

type attachmentContentPlatform struct {
	tk.BasePlatformClient
	calls int
}

func (p *attachmentContentPlatform) CallAppResult(appName, tool string, _ map[string]any, out any) error {
	p.calls++
	if appName != "storage" || tool != "files_get_content" {
		return errors.New("unexpected app call")
	}
	raw, _ := json.Marshal(map[string]any{
		"name":           "proof.txt",
		"content_type":   "text/plain",
		"content_base64": "cHJvb2Y=",
	})
	return json.Unmarshal(raw, out)
}

func TestAttachmentReadsDoNotMintStorageURLs(t *testing.T) {
	ticket := &Ticket{PortalToken: "secret-token"}
	attachments := []*Attachment{
		{ID: 7, Visibility: "public", URL: "https://storage.invalid/original"},
		{ID: 8, Visibility: "internal", URL: "https://storage.invalid/internal"},
	}
	decorateAttachmentURLs(nil, ticket, attachments)
	if got, want := attachments[0].URL, "http://localhost:5280/api/apps/tickets/p/ticket/secret-token/attachments/7/content"; got != want {
		t.Fatalf("public attachment URL = %q, want %q", got, want)
	}
	if got := attachments[1].URL; got != "https://storage.invalid/internal" {
		t.Fatalf("internal attachment URL changed to %q", got)
	}
	for _, file := range []string{"http.go", "portal.go", "integrations.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "files_get_url") {
			t.Fatalf("%s performs synchronous Storage URL minting in a ticket read path", file)
		}
	}
}

func TestPublicAttachmentProxy(t *testing.T) {
	pid := "attachment-project"
	platform := &attachmentContentPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(pid), tk.WithPlatform(platform))
	if err := ensureProject(ctx.AppDB(), pid); err != nil {
		t.Fatal(err)
	}
	ticket, err := createTicket(ctx.AppDB(), pid, map[string]any{"title": "Attachment"}, Actor{Kind: "client"})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := addAttachmentRecord(ctx.AppDB(), pid, ticket.ID, nil, "42", "proof.txt", "text/plain", 5, "", "public", Actor{Kind: "client"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := addLink(ctx.AppDB(), pid, ticket.ID, map[string]any{"kind": "task", "external_id": "42"}, Actor{Kind: "agent"}); err != nil {
		t.Fatal(err)
	}
	detailDone := make(chan error, 1)
	go func() {
		detail, detailErr := ticketDetail(ctx.AppDB(), pid, ticket.ID, true)
		if detailErr == nil && (len(detail.Attachments) != 1 || len(detail.Links) != 1) {
			detailErr = fmt.Errorf("detail attachments=%d links=%d", len(detail.Attachments), len(detail.Links))
		}
		detailDone <- detailErr
	}()
	select {
	case err := <-detailDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		// The pre-v0.1.3 implementation queried each attachment/link while
		// its outer rows cursor still held the app's single SQLite connection.
		ctx.AppDB().SetMaxOpenConns(2) // release the blocked goroutine before failing
		<-detailDone
		t.Fatal("ticket detail deadlocked while listing attachments or links")
	}
	recorder := httptest.NewRecorder()
	(&App{}).servePublicAttachment(recorder, ctx, ticket, strconv.FormatInt(attachment.ID, 10))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "proof" {
		t.Fatalf("download status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.Contains(got, "proof.txt") {
		t.Fatalf("content disposition = %q", got)
	}
	if platform.calls != 1 {
		t.Fatalf("storage content calls = %d, want 1", platform.calls)
	}
	internal, err := addAttachmentRecord(ctx.AppDB(), pid, ticket.ID, nil, "43", "secret.txt", "text/plain", 6, "", "internal", Actor{Kind: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	(&App{}).servePublicAttachment(recorder, ctx, ticket, strconv.FormatInt(internal.ID, 10))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("internal download status = %d, want 404", recorder.Code)
	}
	if platform.calls != 1 {
		t.Fatalf("internal attachment reached Storage; calls = %d", platform.calls)
	}
}

func TestTicketLifecycleHistoryAndVisibility(t *testing.T) {
	db := testDB(t)
	pid := "client-project"
	if err := ensureProject(db, pid); err != nil {
		t.Fatal(err)
	}
	client := Actor{Kind: "client", Ref: "alice@example.com", Name: "Alice"}
	ticket, err := createTicket(db, pid, map[string]any{"title": "Backend returns 500", "description": "Saving the profile fails.", "area": "backend", "type": "bug", "requester_email": "alice@example.com"}, client)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.AreaSlug != "backend" || ticket.DueAt != "" || ticket.Status != "new" {
		t.Fatalf("unexpected ticket: %+v", ticket)
	}
	public, err := addComment(db, pid, ticket.ID, "public", "We are looking into it.", Actor{Kind: "agent", Name: "Support agent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := addComment(db, pid, ticket.ID, "internal", "Likely in the session middleware.", Actor{Kind: "agent", Name: "Support agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := editComment(db, pid, ticket.ID, public.ID, "We reproduced it and are looking into it.", Actor{Kind: "agent", Name: "Support agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := setTicketStatus(db, pid, ticket.ID, "in_progress", "Assigned to backend", Actor{Kind: "agent", Name: "Support agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := setTicketStatus(db, pid, ticket.ID, "resolved", "Fix deployed", Actor{Kind: "agent", Name: "Support agent"}); err != nil {
		t.Fatal(err)
	}
	resolved, _ := getTicket(db, pid, ticket.ID)
	if resolved.ResolvedAt == "" {
		t.Fatal("resolved_at was not set")
	}
	publicDetail, err := ticketDetail(db, pid, ticket.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(publicDetail.Comments) != 1 || publicDetail.Comments[0].Visibility != "public" {
		t.Fatalf("public comments leaked internal content: %+v", publicDetail.Comments)
	}
	for _, event := range publicDetail.Events {
		if event.Visibility == "internal" {
			t.Fatalf("public history leaked internal event: %+v", event)
		}
	}
	teamDetail, err := ticketDetail(db, pid, ticket.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(teamDetail.Comments) != 2 {
		t.Fatalf("team comments=%d, want 2", len(teamDetail.Comments))
	}
	foundEdit := false
	for _, event := range teamDetail.Events {
		if event.EventType == "ticket.comment.edited" {
			foundEdit = true
			var data map[string]any
			if json.Unmarshal(event.Data, &data) != nil || data["before"] != "We are looking into it." {
				t.Errorf("edit history=%s", event.Data)
			}
		}
	}
	if !foundEdit {
		t.Fatal("comment edit was not recorded")
	}
}

func TestProjectIsolationAndPortalTokens(t *testing.T) {
	db := testDB(t)
	for _, pid := range []string{"project-a", "project-b"} {
		if err := ensureProject(db, pid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := createTicket(db, "project-a", map[string]any{"title": "Only A"}, Actor{Kind: "human", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	rows, total, err := listTickets(db, "project-b", TicketFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("project B saw project A tickets: %d", total)
	}
	a, err := getPortalByProject(db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := getPortalByProject(db, "project-b")
	if err != nil {
		t.Fatal(err)
	}
	if a.Token == "" || a.Token == b.Token {
		t.Fatalf("portal tokens are not unique: %q %q", a.Token, b.Token)
	}
}

func TestActorUsesTrustedCaller(t *testing.T) {
	ctx := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 42, ThreadID: "thread-a", ProjectID: "p"})
	actor := actorFrom(ctx, map[string]any{}, "agent")
	if actor.Kind != "agent" || actor.Ref != "42" {
		t.Fatalf("actor=%+v", actor)
	}
}
