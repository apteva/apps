package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestManifestAndMigrationsLoad(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-1"))
	if ctx.AppDB() == nil {
		t.Fatal("expected app db")
	}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO messages
			(project_id, direction, from_addr, to_addr, message_id, body_text, status)
		 VALUES ('project-1', 'in', '+111', '+222', 'wamid-test', 'hello', 'received')`,
	); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizePhoneAcceptsWhatsAppForms(t *testing.T) {
	cases := map[string]string{
		"whatsapp:+14155550123":      "+14155550123",
		"tel:+14155550123":           "+14155550123",
		"+1 (415) 555-0123":          "+14155550123",
		"14155550123@s.whatsapp.net": "+14155550123",
		"not-a-phone@s.whatsapp.net": "",
		"whatsapp:not-a-phone":       "",
		"short":                      "",
	}
	for in, want := range cases {
		if got := normalizePhone(in); got != want {
			t.Fatalf("normalizePhone(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestSendWhatsAppRequiresConnection(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-1"))
	app := &App{ctx: ctx, status: "disconnected"}
	_, err := app.toolSendWhatsApp(ctx, map[string]any{
		"To":   "whatsapp:+14155550123",
		"Body": "hello",
	})
	if err == nil || err.Error() != "WhatsApp Web is not connected" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMessagesHTTPListsRecentProjectMessages(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-1"))
	globalCtx = ctx
	app := &App{ctx: ctx}
	_, err := ctx.AppDB().Exec(
		`INSERT INTO messages
			(project_id, direction, from_addr, to_addr, message_id, body_text, status, occurred_at)
		 VALUES
			('project-1', 'in', '+111', '+222', 'm1', 'first', 'received', '2026-06-08T10:00:00Z'),
			('other', 'in', '+333', '+444', 'm2', 'hidden', 'received', '2026-06-08T10:01:00Z'),
			('project-1', 'out', '+222', '+111', 'm3', 'second', 'sent', '2026-06-08T10:02:00Z')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/messages?limit=10", nil)
	rec := httptest.NewRecorder()
	app.handleMessages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !containsAll(body, "second", "first") || containsAll(body, "hidden") {
		t.Fatalf("unexpected body: %s", body)
	}
}

func containsAll(s string, vals ...string) bool {
	for _, v := range vals {
		if !strings.Contains(s, v) {
			return false
		}
	}
	return true
}
