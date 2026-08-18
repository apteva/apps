package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Tests for the attendee-facing HTTP surface: the ownership checks on
// the live-room writes, the registration form's CSRF + rate-limit
// mapping, the replay page's token comparison and expiry, and the
// rendering contract the pages depend on (pinned player, CSP, escaping).

// ─── HTTP fixtures ────────────────────────────────────────────────

const testProject = "test-proj"

func withProject(path string) string {
	return withQuery(path, "project_id", testProject)
}

func doGET(t *testing.T, h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	rw := httptest.NewRecorder()
	h(rw, httptest.NewRequest(http.MethodGet, path, nil))
	return rw
}

func doPOST(t *testing.T, h http.HandlerFunc, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rw := httptest.NewRecorder()
	h(rw, req)
	return rw
}

// doForm posts an application/x-www-form-urlencoded body, optionally
// carrying the CSRF cookie from a previous response.
func doForm(t *testing.T, h http.HandlerFunc, path string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rw := httptest.NewRecorder()
	h(rw, req)
	return rw
}

// goLive puts a webinar and its stream in the state the live room needs.
func goLive(t *testing.T, ctx *sdk.AppCtx, streaming *fakeStreaming, w *Webinar) {
	t.Helper()
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET status='live', started_at=? WHERE id=?`, nowRFC3339(), w.ID); err != nil {
		t.Fatal(err)
	}
	streaming.streams[w.StreamID].Status = "live"
	streaming.streams[w.StreamID].CurrentViewers = 42
}

var csrfFieldRE = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// ─── A1: cross-webinar poll / offer writes ────────────────────────

func TestHandleLivePollResponse_RejectsForeignPoll(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	victim := mustCreate(t, app, ctx, map[string]any{"title": "Victim"})
	attacker := mustCreate(t, app, ctx, map[string]any{"title": "Attacker"})
	goLive(t, ctx, streaming, attacker)
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": attacker.ID, "email": "a@example.com"})

	pollOut, err := app.toolPushPoll(ctx, map[string]any{
		"id": victim.ID, "question": "Which plan?", "choices": []any{"Pro", "Team"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pollID := pollOut.(map[string]any)["poll_id"].(int64)

	// The attacker's own join token, someone else's poll id.
	rw := doPOST(t, app.handleLiveRoute,
		withQuery(withQuery(withProject("/live/"+reg.JoinToken+"/poll-response"),
			"poll_id", intToStr(pollID)), "choice", "0"), "")
	if rw.Code != http.StatusNotFound {
		t.Errorf("cross-webinar poll stuffing status=%d, want 404 — body %s", rw.Code, rw.Body.String())
	}
	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_poll_responses WHERE poll_id = ?`, pollID); n != 0 {
		t.Errorf("%d foreign poll responses were written", n)
	}
}

func TestHandleLivePollResponse_StatusCodeMapping(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Poll"})
	goLive(t, ctx, streaming, w)
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})

	pollOut, err := app.toolPushPoll(ctx, map[string]any{
		"id": w.ID, "question": "Q?", "choices": []any{"A", "B"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pollID := pollOut.(map[string]any)["poll_id"].(int64)
	base := withProject("/live/" + reg.JoinToken + "/poll-response")

	vote := func(poll int64, choice string) int {
		return doPOST(t, app.handleLiveRoute,
			withQuery(withQuery(base, "poll_id", intToStr(poll)), "choice", choice), "").Code
	}

	if code := vote(pollID, "1"); code != http.StatusOK {
		t.Errorf("legitimate vote status=%d, want 200", code)
	}
	if code := vote(pollID, "9"); code != http.StatusBadRequest {
		t.Errorf("out-of-range choice status=%d, want 400 (errInvalidChoice)", code)
	}
	if code := vote(pollID+9999, "0"); code != http.StatusNotFound {
		t.Errorf("unknown poll status=%d, want 404 (errEngagementNotFound)", code)
	}

	if _, err := ctx.AppDB().Exec(
		`UPDATE webinar_polls SET closes_at = ? WHERE id = ?`, futureRFC3339(-time.Minute), pollID); err != nil {
		t.Fatal(err)
	}
	if code := vote(pollID, "0"); code != http.StatusConflict {
		t.Errorf("closed poll status=%d, want 409 (errPollClosed)", code)
	}
}

func TestHandleLiveOfferClick_RejectsForeignOffer(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	victim := mustCreate(t, app, ctx, map[string]any{"title": "Victim"})
	attacker := mustCreate(t, app, ctx, map[string]any{"title": "Attacker"})
	goLive(t, ctx, streaming, attacker)
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": attacker.ID, "email": "a@example.com"})

	offerOut, err := app.toolPostOffer(ctx, map[string]any{
		"id": victim.ID, "headline": "Half price", "cta_label": "Buy", "cta_url": "https://example.com/buy",
	})
	if err != nil {
		t.Fatal(err)
	}
	offerID := offerOut.(map[string]any)["offer_id"].(int64)

	rw := doPOST(t, app.handleLiveRoute,
		withQuery(withProject("/live/"+reg.JoinToken+"/offer-click"), "offer_id", intToStr(offerID)), "")
	if rw.Code != http.StatusNotFound {
		t.Errorf("cross-webinar offer click status=%d, want 404", rw.Code)
	}
	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_offer_clicks WHERE offer_id = ?`, offerID); n != 0 {
		t.Errorf("%d foreign offer clicks were written — CTR is forgeable", n)
	}
}

// ─── A2: chat goes through the atomic-sequence helper ─────────────

func TestHandleLiveChat_UsesAtomicSequenceAndRFC3339(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Chat"})
	goLive(t, ctx, streaming, w)
	reg := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "a@example.com", "display_name": "Alice"})

	path := withProject("/live/" + reg.JoinToken + "/chat")
	seqs := map[int]bool{}
	for i := 0; i < 3; i++ {
		rw := doPOST(t, app.handleLiveRoute, path, `{"body":"hello there"}`)
		if rw.Code != http.StatusOK {
			t.Fatalf("chat post %d status=%d: %s", i, rw.Code, rw.Body.String())
		}
		var out struct {
			ID       int64 `json:"id"`
			Sequence int   `json:"sequence"`
		}
		if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if seqs[out.Sequence] {
			t.Fatalf("sequence %d handed out twice — /events would drop a message", out.Sequence)
		}
		seqs[out.Sequence] = true
	}

	// created_at must be parseable RFC3339, not SQLite's CURRENT_TIMESTAMP
	// layout — the readers use time.Parse(time.RFC3339, …).
	var createdAt string
	if err := ctx.AppDB().QueryRow(
		`SELECT created_at FROM webinar_chat WHERE webinar_id = ? ORDER BY id DESC LIMIT 1`,
		w.ID).Scan(&createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
		t.Errorf("created_at=%q is not RFC3339: %v", createdAt, err)
	}

	// Empty bodies are still refused.
	if code := doPOST(t, app.handleLiveRoute, path, `{"body":"   "}`).Code; code != http.StatusBadRequest {
		t.Errorf("empty chat body status=%d, want 400", code)
	}
}

// ─── A3: heartbeat is accumulated, not written through ────────────

func TestHandleLiveHeartbeat_AccumulatesInMemory(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Beat"})
	goLive(t, ctx, streaming, w)
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})

	path := withProject("/live/" + reg.JoinToken + "/heartbeat")
	for i := 0; i < 20; i++ {
		if code := doPOST(t, app.handleLiveRoute, path, "").Code; code != http.StatusOK {
			t.Fatalf("heartbeat %d status=%d", i, code)
		}
	}
	// Nothing on the request path touches SQLite; the flush worker writes.
	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_attendance WHERE registrant_id = ?`, reg.ID); n != 0 {
		t.Errorf("%d attendance rows written on the request path — the beat should be batched", n)
	}
	if err := app.runAttendanceFlush(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	var seconds int
	if err := ctx.AppDB().QueryRow(
		`SELECT watch_seconds FROM webinar_attendance WHERE registrant_id = ?`, reg.ID).Scan(&seconds); err != nil {
		t.Fatal(err)
	}
	// 20 beats fired back to back credit one interval, not 20 × 10s: the
	// old handler let anyone mint their own watch time.
	if seconds > 20 {
		t.Errorf("watch_seconds=%d after 20 instant beats — credit is not clamped to elapsed time", seconds)
	}
}

// ─── A4: signed playback + lifecycle states ───────────────────────

func TestLiveRoom_RendersSignedPlaybackAndPinnedPlayer(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Launch", "duration_minutes": 90})
	goLive(t, ctx, streaming, w)
	reg := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "a@example.com", "display_name": "Alice"})

	rw := doGET(t, app.handleLiveRoute, withProject("/live/"+reg.JoinToken))
	if rw.Code != http.StatusOK {
		t.Fatalf("live room status=%d: %s", rw.Code, rw.Body.String())
	}
	body := rw.Body.String()

	// The signed URL, not snap.PlaybackURL — with require_signed_urls on,
	// the static-token form is answered with 404 and the player is black.
	if !strings.Contains(body, "sig=deadbeef") {
		t.Error("live room is not embedding the signed playback URL")
	}
	// …and the heartbeat carries the same exp+sig, or streaming rejects it.
	if !strings.Contains(body, `sig: "deadbeef"`) {
		t.Errorf("streaming heartbeat is missing the signature — viewer counts would read zero")
	}
	if !strings.Contains(body, hlsScriptURL) || !strings.Contains(body, hlsScriptIntegrity) {
		t.Error("player script is not pinned with an integrity hash")
	}
	if !strings.Contains(body, `crossorigin="anonymous"`) {
		t.Error("pinned script needs crossorigin=anonymous for SRI to be checkable")
	}
	if strings.Contains(body, "hls.js@1\"") || strings.Contains(body, "hls.js@1/") {
		t.Error("floating hls.js tag is still present")
	}
	// The rendering discipline that keeps stored chat XSS unexploitable.
	if strings.Contains(body, "innerHTML") {
		t.Error("live room uses innerHTML — chat bodies are stored unsanitized")
	}
	if !strings.Contains(body, "42 watching") {
		t.Error("viewer count should render a real initial value, not a placeholder")
	}
}

func TestLiveRoom_SecurityHeaders(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Headers"})
	goLive(t, ctx, streaming, w)
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})

	rw := doGET(t, app.handleLiveRoute, withProject("/live/"+reg.JoinToken))
	h := rw.Header()

	// The page URL contains the join token; it must never travel as a
	// Referer on the cross-origin player fetch.
	if got := h.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy=%q, want no-referrer", got)
	}
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'none'", "script-src 'nonce-", "style-src 'nonce-",
		"media-src 'self' blob:", "worker-src blob:", "frame-ancestors 'none'",
		"base-uri 'none'", "form-action 'self'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q; got %q", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP is a rubber stamp: %q", csp)
	}
	// The nonce in the header has to be the one the page actually uses.
	m := regexp.MustCompile(`script-src 'nonce-([^']+)'`).FindStringSubmatch(csp)
	if m == nil {
		t.Fatalf("no nonce in CSP: %q", csp)
	}
	// Every <script> and every <style> has to carry that nonce, or the
	// policy we just shipped silently breaks the page it protects.
	body := rw.Body.String()
	blocks := strings.Count(body, "<script") + strings.Count(body, "<style")
	if nonces := strings.Count(body, `nonce="`+m[1]+`"`); nonces != blocks {
		t.Errorf("%d script/style blocks but %d nonces — the CSP would block one of them", blocks, nonces)
	}
}

func TestLiveRoom_EscapesDisplayName(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": `Q3 <script>alert("t")</script>`})
	goLive(t, ctx, streaming, w)
	reg := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "a@example.com",
		"display_name": `<script>alert(1)</script>`})

	body := doGET(t, app.handleLiveRoute, withProject("/live/"+reg.JoinToken)).Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("display name rendered unescaped into the live room")
	}
	if strings.Contains(body, `<script>alert("t")</script>`) {
		t.Error("webinar title rendered unescaped into the live room")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected the escaped form to be present")
	}
}

func TestLiveRoom_LifecycleStates(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)

	// 1. Scheduled in the future → styled waiting page with the time.
	future := mustCreate(t, app, ctx, map[string]any{
		"title": "Next week", "scheduled_at": futureRFC3339(48 * time.Hour)})
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": future.ID, "email": "a@example.com"})
	rw := doGET(t, app.handleLiveRoute, withProject("/live/"+reg.JoinToken))
	if rw.Code != http.StatusOK {
		t.Errorf("early arrival status=%d, want 200", rw.Code)
	}
	if ct := rw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("early arrival Content-Type=%q — attendees used to get a JSON blob", ct)
	}
	if !strings.Contains(rw.Body.String(), "<time datetime=") {
		t.Error("waiting page should carry the scheduled time for client-side localization")
	}
	if !strings.Contains(rw.Body.String(), `id="countdown"`) {
		t.Error("waiting page should carry a countdown")
	}

	// 2. Live webinar, streaming not ready → styled 503, still HTML.
	soon := mustCreate(t, app, ctx, map[string]any{"title": "Now"})
	reg2 := mustRegister(t, app, ctx, map[string]any{"webinar_id": soon.ID, "email": "b@example.com"})
	if _, err := ctx.AppDB().Exec(`UPDATE webinars SET status='live' WHERE id=?`, soon.ID); err != nil {
		t.Fatal(err)
	}
	streaming.streams[soon.StreamID].Status = "idle"
	rw = doGET(t, app.handleLiveRoute, withProject("/live/"+reg2.JoinToken))
	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("stream-not-ready status=%d, want 503", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "Starting shortly") {
		t.Errorf("stream-not-ready body=%q", rw.Body.String())
	}

	// 3. Ended with a published replay → ended page carrying the link,
	//    with r=<join_token> so replay watch time is attributable.
	streaming.streams[soon.StreamID].Status = "ended"
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET status='ended', ended_at=? WHERE id=?`, nowRFC3339(), soon.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPublishReplay(ctx, map[string]any{"id": soon.ID}); err != nil {
		t.Fatal(err)
	}
	rw = doGET(t, app.handleLiveRoute, withProject("/live/"+reg2.JoinToken))
	if rw.Code != http.StatusOK {
		t.Errorf("ended status=%d, want 200", rw.Code)
	}
	body := rw.Body.String()
	if !strings.Contains(body, "This webinar has ended") {
		t.Errorf("ended body=%q", body)
	}
	if !strings.Contains(body, "r="+reg2.JoinToken) {
		t.Error("ended page should link the replay with the registrant's token")
	}
}

func TestLiveRoute_NotFoundIsADesignedPage(t *testing.T) {
	app, _, _, _, _ := newTestAppCfg(t, false, false, nil)
	rw := doGET(t, app.handleLiveRoute, withProject("/live/nope"))
	if rw.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "find that page") {
		t.Errorf("unknown token should get the designed 404, got %q", rw.Body.String())
	}
	// The XHR sub-routes still answer in JSON.
	rw = doPOST(t, app.handleLiveRoute, withProject("/live/nope/chat"), `{"body":"x"}`)
	if ct := rw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("sub-route 404 Content-Type=%q, want JSON", ct)
	}
}

// ─── A5: registration rate limit maps to 429 ──────────────────────

func TestRegistrationForm_RateLimitIs429(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, map[string]string{
		"registration_rate_limit_per_minute": "1",
	})
	w := mustCreate(t, app, ctx, map[string]any{"title": "Amplifier"})
	path := withProject("/r/" + w.Slug)

	token, cookies := registrationFormToken(t, app, path)
	form := url.Values{
		"csrf_token":   {token},
		"display_name": {"Alice"},
		"email":        {"alice@example.com"},
	}
	if code := doForm(t, app.handleRegistrationPage, path, form, cookies).Code; code != http.StatusFound {
		t.Fatalf("first registration status=%d, want 302", code)
	}

	token2, cookies2 := registrationFormToken(t, app, path)
	form.Set("csrf_token", token2)
	form.Set("email", "bob@example.com")
	rw := doForm(t, app.handleRegistrationPage, path, form, cookies2)
	if rw.Code != http.StatusTooManyRequests {
		t.Errorf("over-budget registration status=%d, want 429", rw.Code)
	}
	if rw.Header().Get("Retry-After") == "" {
		t.Error("a 429 should say when to come back")
	}
	// …and it re-renders the form rather than replacing the funnel with
	// an error blob.
	if !strings.Contains(rw.Body.String(), "<form method=\"POST\"") {
		t.Error("rate-limited submit should re-render the form")
	}
}

// The authenticated admin mirror shares toolRegister, so it shares the
// budget — and should say so with the same status code.
func TestAdminRegister_RateLimitIs429(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, map[string]string{
		"registration_rate_limit_per_minute": "1",
	})
	w := mustCreate(t, app, ctx, map[string]any{"title": "Admin"})
	path := withProject("/admin/webinars/" + intToStr(w.ID) + "/register")

	if code := doPOST(t, app.handleAdminItem, path, `{"email":"a@example.com"}`).Code; code != http.StatusOK {
		t.Fatalf("first admin register status=%d, want 200", code)
	}
	rw := doPOST(t, app.handleAdminItem, path, `{"email":"b@example.com"}`)
	if rw.Code != http.StatusTooManyRequests {
		t.Errorf("over-budget admin register status=%d, want 429", rw.Code)
	}
	if rw.Header().Get("Retry-After") == "" {
		t.Error("a 429 should say when to come back")
	}
}

func TestRegistrationForm_InvalidContactIsInline(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Validate"})
	path := withProject("/r/" + w.Slug)

	token, cookies := registrationFormToken(t, app, path)
	rw := doForm(t, app.handleRegistrationPage, path, url.Values{
		"csrf_token":   {token},
		"display_name": {"Alice"},
		"email":        {"not-an-email"},
	}, cookies)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rw.Code)
	}
	body := rw.Body.String()
	if !strings.Contains(body, `role="alert"`) {
		t.Error("validation failure should re-render the form with an inline message")
	}
	if !strings.Contains(body, `value="Alice"`) {
		t.Error("a rejected submit should keep what the visitor typed")
	}
	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_registrants WHERE webinar_id = ?`, w.ID); n != 0 {
		t.Errorf("%d registrants created from an invalid submit", n)
	}
}

// ─── B2: constant-time replay token comparison ────────────────────

func TestConstantTimeEqual(t *testing.T) {
	if !constantTimeEqual("abc123", "abc123") {
		t.Error("equal strings should compare equal")
	}
	for _, bad := range []string{"", "abc124", "abc12", "abc1234", "ABC123"} {
		if constantTimeEqual("abc123", bad) {
			t.Errorf("%q should not compare equal to abc123", bad)
		}
	}
}

func TestReplayPage_TokenAndExpiry(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Recorded"})
	streaming.streams[w.StreamID].Status = "ended"
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET status='ended', ended_at=? WHERE id=?`, nowRFC3339(), w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPublishReplay(ctx, map[string]any{
		"id": w.ID, "expires_at": futureRFC3339(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	reload, _ := app.dbGet(ctx, testProject, w.ID)
	base := withProject("/replay/" + w.Slug)

	// Wrong token → the designed 404, and no player.
	rw := doGET(t, app.handleReplayPage, withQuery(base, "t", "wrong-token"))
	if rw.Code != http.StatusNotFound {
		t.Errorf("bad replay token status=%d, want 404", rw.Code)
	}
	if strings.Contains(rw.Body.String(), "sig=deadbeef") {
		t.Fatal("a bad token still produced a playback URL")
	}

	// Right token → a signed, expiring URL.
	rw = doGET(t, app.handleReplayPage, withQuery(base, "t", reload.ReplayToken))
	if rw.Code != http.StatusOK {
		t.Fatalf("replay status=%d: %s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "sig=deadbeef") {
		t.Error("replay page is not serving a signed URL — expiry would be unenforceable")
	}
	if got := rw.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("replay Referrer-Policy=%q", got)
	}
	if !strings.Contains(rw.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Error("replay page has no CSP")
	}

	// Past replay_expires_at → 410, and nothing minted.
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET replay_expires_at = ? WHERE id = ?`, futureRFC3339(-time.Hour), w.ID); err != nil {
		t.Fatal(err)
	}
	rw = doGET(t, app.handleReplayPage, withQuery(base, "t", reload.ReplayToken))
	if rw.Code != http.StatusGone {
		t.Errorf("expired replay status=%d, want 410", rw.Code)
	}
	if strings.Contains(rw.Body.String(), "sig=deadbeef") {
		t.Error("an expired replay still handed out a playback URL")
	}
	if !strings.Contains(rw.Body.String(), "This replay has expired") {
		t.Error("expired replay should be a styled page, not plain text")
	}
}

// ─── C6: replay attendance ────────────────────────────────────────

func TestReplayPage_TracksAttendanceWhenIdentified(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Recorded"})
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})
	streaming.streams[w.StreamID].Status = "ended"
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET status='ended', ended_at=? WHERE id=?`, nowRFC3339(), w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPublishReplay(ctx, map[string]any{"id": w.ID}); err != nil {
		t.Fatal(err)
	}
	reload, _ := app.dbGet(ctx, testProject, w.ID)
	base := withQuery(withProject("/replay/"+w.Slug), "t", reload.ReplayToken)

	// Anonymous: playable, but no heartbeat wiring.
	anon := doGET(t, app.handleReplayPage, base).Body.String()
	if strings.Contains(anon, "/heartbeat?") {
		t.Error("anonymous replay should not post attendance for anyone")
	}

	// Identified by r=<join_token>: the page beats, and the beat lands as
	// replay watch time.
	body := doGET(t, app.handleReplayPage, withQuery(base, "r", reg.JoinToken)).Body.String()
	if !strings.Contains(body, reg.JoinToken) || !strings.Contains(body, "/heartbeat?") {
		t.Fatal("identified replay page is missing the attendance heartbeat")
	}

	if code := doPOST(t, app.handleLiveRoute,
		withProject("/live/"+reg.JoinToken+"/heartbeat"), "").Code; code != http.StatusOK {
		t.Fatal("replay heartbeat rejected")
	}
	if err := app.runAttendanceFlush(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := ctx.AppDB().QueryRow(
		`SELECT source FROM webinar_attendance WHERE registrant_id = ?`, reg.ID).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "replay" {
		t.Errorf("attendance source=%q, want replay for an ended webinar", source)
	}
	var attended int
	if err := ctx.AppDB().QueryRow(
		`SELECT attended_replay FROM webinar_registrants WHERE id = ?`, reg.ID).Scan(&attended); err != nil {
		t.Fatal(err)
	}
	if attended != 1 {
		t.Error("attended_replay should be promoted by the flush")
	}
}

// ─── B3: CSRF on the registration POST ────────────────────────────

// registrationFormToken performs the GET a browser would and returns the
// CSRF form value plus the cookie the response set.
func registrationFormToken(t *testing.T, app *App, path string) (string, []*http.Cookie) {
	t.Helper()
	rw := doGET(t, app.handleRegistrationPage, path)
	if rw.Code != http.StatusOK {
		t.Fatalf("registration form status=%d: %s", rw.Code, rw.Body.String())
	}
	m := csrfFieldRE.FindStringSubmatch(rw.Body.String())
	if m == nil {
		t.Fatal("registration form carries no CSRF token")
	}
	return m[1], rw.Result().Cookies()
}

func TestRegistrationForm_CSRFRequired(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Forgeable"})
	path := withProject("/r/" + w.Slug)

	token, cookies := registrationFormToken(t, app, path)

	// The cookie half must be HttpOnly + SameSite=Lax: it is the only
	// thing a cross-site page cannot supply.
	var csrfCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == csrfCookieName {
			csrfCookie = c
		}
	}
	if csrfCookie == nil {
		t.Fatal("no CSRF cookie was set")
	}
	if !csrfCookie.HttpOnly {
		t.Error("CSRF cookie should be HttpOnly")
	}
	if csrfCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("CSRF cookie SameSite=%v, want Lax", csrfCookie.SameSite)
	}

	base := url.Values{"display_name": {"Mallory"}, "email": {"mallory@example.com"}}

	// 1. No token at all — the classic forged POST.
	if code := doForm(t, app.handleRegistrationPage, path, base, nil).Code; code != http.StatusForbidden {
		t.Errorf("token-less POST status=%d, want 403", code)
	}
	// 2. A valid form value with no matching cookie.
	withToken := url.Values{}
	for k, v := range base {
		withToken[k] = v
	}
	withToken.Set("csrf_token", token)
	if code := doForm(t, app.handleRegistrationPage, path, withToken, nil).Code; code != http.StatusForbidden {
		t.Errorf("cookie-less POST status=%d, want 403", code)
	}
	// 3. A cookie an attacker injected, with a form value they made up:
	//    the signature is what stops this.
	forged := url.Values{}
	for k, v := range base {
		forged[k] = v
	}
	forged.Set("csrf_token", "attacker-nonce.99999999999.bogus")
	if code := doForm(t, app.handleRegistrationPage, path, forged,
		[]*http.Cookie{{Name: csrfCookieName, Value: "attacker-nonce"}}).Code; code != http.StatusForbidden {
		t.Errorf("forged double-submit status=%d, want 403", code)
	}
	// 4. Mismatched (but individually well-formed) halves.
	other, _ := registrationFormToken(t, app, path)
	mixed := url.Values{}
	for k, v := range base {
		mixed[k] = v
	}
	mixed.Set("csrf_token", other)
	if code := doForm(t, app.handleRegistrationPage, path, mixed, cookies).Code; code != http.StatusForbidden {
		t.Errorf("mismatched cookie/form pair status=%d, want 403", code)
	}

	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_registrants WHERE webinar_id = ?`, w.ID); n != 0 {
		t.Fatalf("%d registrations got through without CSRF", n)
	}

	// The matching pair works, and lands the visitor in the live room.
	withToken.Set("csrf_token", token)
	rw := doForm(t, app.handleRegistrationPage, path, withToken, cookies)
	if rw.Code != http.StatusFound {
		t.Fatalf("legitimate submit status=%d: %s", rw.Code, rw.Body.String())
	}
	if loc := rw.Header().Get("Location"); !strings.Contains(loc, "/live/") {
		t.Errorf("Location=%q, want the live room", loc)
	}
	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_registrants WHERE webinar_id = ?`, w.ID); n != 1 {
		t.Errorf("registrants=%d, want 1", n)
	}
}

func TestCSRFToken_ExpiryAndTampering(t *testing.T) {
	nonce := randomToken()
	expired := nonce + "." + intToStr(time.Now().Add(-time.Minute).Unix())
	req := httptest.NewRequest(http.MethodPost, "/r/x", strings.NewReader(
		url.Values{csrfFormField: {expired + "." + csrfSign(expired)}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: nonce})
	if verifyCSRF(req) {
		t.Error("an expired token should not verify even when correctly signed")
	}

	live := nonce + "." + intToStr(time.Now().Add(time.Hour).Unix())
	req = httptest.NewRequest(http.MethodPost, "/r/x", strings.NewReader(
		url.Values{csrfFormField: {live + "." + csrfSign(live) + "x"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: nonce})
	if verifyCSRF(req) {
		t.Error("a tampered signature should not verify")
	}
}

// ─── C7: dates are rendered for the reader's timezone ─────────────

func TestRegistrationForm_RendersISOTimes(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	when := futureRFC3339(72 * time.Hour)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Scheduled", "scheduled_at": when})

	body := doGET(t, app.handleRegistrationPage, withProject("/r/"+w.Slug)).Body.String()
	if !strings.Contains(body, `<time datetime="`+when+`"`) {
		t.Errorf("registration page should emit an ISO timestamp for client-side localization; got %q", body)
	}
	if !strings.Contains(body, "localizeTimes") {
		t.Error("registration page is missing the timezone localization script")
	}
	// Real labels, not placeholder-only inputs.
	for _, want := range []string{`for="display_name"`, `for="email"`, `for="phone"`} {
		if !strings.Contains(body, want) {
			t.Errorf("registration form is missing a label with %s", want)
		}
	}
}

// ─── Viewer-count cache ───────────────────────────────────────────

func TestEvents_ViewerCountIsCached(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Crowd"})
	goLive(t, ctx, streaming, w)
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})

	// The cache is package-level, so start from a known state.
	viewerCacheMu.Lock()
	delete(viewerCache, w.StreamID)
	viewerCacheMu.Unlock()

	path := withProject("/live/" + reg.JoinToken + "/events")
	for i := 0; i < 25; i++ {
		if code := doGET(t, app.handleLiveRoute, path).Code; code != http.StatusOK {
			t.Fatalf("events poll %d status=%d", i, code)
		}
	}
	var out struct {
		Viewers int    `json:"viewers"`
		Status  string `json:"status"`
	}
	rw := doGET(t, app.handleLiveRoute, path)
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Viewers != 42 {
		t.Errorf("viewers=%d, want 42", out.Viewers)
	}
	if out.Status != "live" {
		t.Errorf("status=%q — the client needs it to leave a room that ended", out.Status)
	}
}

// ─── Text helpers ─────────────────────────────────────────────────

func TestTruncateRunes_DoesNotSplitCharacters(t *testing.T) {
	s := strings.Repeat("é", 600)
	got := truncateRunes(s, 500)
	if len([]rune(got)) != 500 {
		t.Errorf("got %d runes, want 500", len([]rune(got)))
	}
	if !strings.ContainsRune(got, 'é') || strings.ContainsRune(got, '�') {
		t.Error("truncation split a multi-byte character")
	}
	if got := stripControlRunes("hi\x00there\x07"); got != "hithere" {
		t.Errorf("stripControlRunes=%q", got)
	}
}

// The live room must post its streaming heartbeat to the endpoint
// streaming signed for it (streams_signed_url kind=heartbeat), not to a
// URL reassembled from the player's credentials.
//
// Streaming runs media playback and the viewer heartbeat through one
// authorization check, so once this app turns on require_signed_urls an
// unsigned beat is answered with 404 — video keeps playing while
// current_viewers, peak_viewers and total_viewer_seconds all read zero.
// Nothing surfaces the failure, so the regression looks like a broken
// metric rather than a broken release.
func TestLiveRoom_UsesSignedHeartbeatEndpoint(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Launch", "duration_minutes": 90})
	goLive(t, ctx, streaming, w)
	reg := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "a@example.com", "display_name": "Alice"})

	body := doGET(t, app.handleLiveRoute, withProject("/live/"+reg.JoinToken)).Body.String()

	// html/template escapes "/" as "\/" in a JS string context, so
	// normalize before matching the URL.
	if !strings.Contains(strings.ReplaceAll(body, `\/`, "/"),
		"/api/apps/streaming/heartbeat/") {
		t.Fatal("live room is not using streaming's signed heartbeat endpoint")
	}
	// It must be asked for by kind, not derived from the playback URL.
	var sawHeartbeatKind bool
	for _, req := range streaming.signedRequests() {
		if req.Kind == "heartbeat" {
			sawHeartbeatKind = true
			if req.ID != w.StreamID {
				t.Errorf("signed heartbeat requested for stream %d, want %d", req.ID, w.StreamID)
			}
		}
	}
	if !sawHeartbeatKind {
		t.Error("no streams_signed_url{kind:heartbeat} call — the beat would carry lifted credentials")
	}
}

// An older streaming install has no kind=heartbeat, so signing fails.
// Those installs don't enforce require_signed_urls either, so the beat
// must fall back to the legacy token form rather than stop counting.
func TestLiveRoom_HeartbeatFallsBackWhenStreamingCannotSign(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Launch", "duration_minutes": 90})
	goLive(t, ctx, streaming, w)
	reg := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "a@example.com", "display_name": "Alice"})

	streaming.mu.Lock()
	streaming.signedErr = errors.New("unknown tool: streams_signed_url")
	streaming.mu.Unlock()

	rw := doGET(t, app.handleLiveRoute, withProject("/live/"+reg.JoinToken))
	if rw.Code != http.StatusOK {
		t.Fatalf("live room status=%d, want 200 — an unsignable stream must still render", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), `/heartbeat/" + STREAM_ID`) {
		t.Error("no legacy heartbeat fallback — viewer counting would stop on older streaming installs")
	}
}
