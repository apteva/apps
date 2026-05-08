package main

import (
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Fake streaming caller ─────────────────────────────────────────

type fakeStreaming struct {
	mu          sync.Mutex
	nextID      int64
	streams     map[int64]*StreamSnapshot
	deleted     map[int64]bool
	stopped     map[int64]bool
	callsCreate int
}

func newFakeStreaming() *fakeStreaming {
	return &fakeStreaming{
		streams: map[int64]*StreamSnapshot{},
		deleted: map[int64]bool{},
		stopped: map[int64]bool{},
	}
}

func (f *fakeStreaming) CreateStream(req CreateStreamReq) (CreateStreamResp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := f.nextID
	snap := &StreamSnapshot{
		ID:            id,
		Name:          req.Name,
		OwnerApp:      req.OwnerApp,
		OwnerTag:      req.OwnerTag,
		IngestPort:    1935 + int(id),
		IngestURL:     "rtmp://localhost:1935/live/fake-key",
		StreamKey:     "fake-key",
		PlaybackURL:   "/api/apps/streaming/streams/" + intToStr(id) + "/index.m3u8?t=fake-token",
		PlaybackToken: "fake-token",
		Status:        "idle",
	}
	f.streams[id] = snap
	f.callsCreate++
	return CreateStreamResp{Stream: *snap}, nil
}

func (f *fakeStreaming) GetStream(id int64) (StreamSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.streams[id]
	if !ok {
		return StreamSnapshot{}, nil
	}
	return *s, nil
}

func (f *fakeStreaming) StopStream(id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped[id] = true
	if s, ok := f.streams[id]; ok {
		s.Status = "ended"
	}
	return nil
}

func (f *fakeStreaming) DeleteStream(id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted[id] = true
	delete(f.streams, id)
	return nil
}

func (f *fakeStreaming) GetMetrics(id int64) (StreamMetrics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.streams[id]
	if !ok {
		return StreamMetrics{}, nil
	}
	return StreamMetrics{
		ID:                 id,
		Status:             s.Status,
		CurrentBitrateKbps: s.CurrentBitrateKbps,
		CurrentViewers:     s.CurrentViewers,
		PeakViewers:        s.PeakViewers,
		TotalViewerSeconds: s.TotalViewerSeconds,
	}, nil
}

func (f *fakeStreaming) ReplayURL(id int64) (ReplayURLs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.streams[id]
	if !ok || s.Status != "ended" {
		return ReplayURLs{Available: false, Reason: "not ended"}, nil
	}
	return ReplayURLs{
		Available: true,
		HLSURL:    "/api/apps/streaming/streams/" + intToStr(id) + "/index.m3u8?t=fake-token",
		MP4URL:    "/api/apps/streaming/streams/" + intToStr(id) + "/record.mp4?t=fake-token",
	}, nil
}

// ─── Fake CRM caller ───────────────────────────────────────────────

type fakeCRM struct {
	mu       sync.Mutex
	bound    bool
	contacts map[string]int64
	logs     []string
}

func newFakeCRM(bound bool) *fakeCRM {
	return &fakeCRM{bound: bound, contacts: map[string]int64{}}
}

func (f *fakeCRM) UpsertContactByChannel(req CRMUpsertReq) (CRMUpsertResp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.bound {
		return CRMUpsertResp{}, nil
	}
	id, ok := f.contacts[req.Value]
	if !ok {
		id = int64(len(f.contacts) + 1)
		f.contacts[req.Value] = id
		var resp CRMUpsertResp
		resp.Contact.ID = id
		resp.WasCreated = true
		return resp, nil
	}
	var resp CRMUpsertResp
	resp.Contact.ID = id
	return resp, nil
}

func (f *fakeCRM) LogActivity(req CRMLogActivityReq) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.bound {
		return nil
	}
	f.logs = append(f.logs, req.Body)
	return nil
}

// ─── Fake messaging caller ─────────────────────────────────────────

type fakeMessaging struct {
	mu    sync.Mutex
	bound bool
	sent  []MsgSendReq
}

func newFakeMessaging(bound bool) *fakeMessaging {
	return &fakeMessaging{bound: bound}
}

func (f *fakeMessaging) Bound() bool { return f.bound }

func (f *fakeMessaging) SendMessage(req MsgSendReq) (MsgSendResp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.bound {
		return MsgSendResp{}, errMessagingNotBound
	}
	f.sent = append(f.sent, req)
	return MsgSendResp{ID: int64(len(f.sent)), ProviderMessageID: "msg-" + intToStr(int64(len(f.sent)))}, nil
}

// ─── Fixture ───────────────────────────────────────────────────────

func newTestApp(t *testing.T, crmBound, msgBound bool) (*App, *sdk.AppCtx, *fakeStreaming, *fakeCRM, *fakeMessaging) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"reminder_lead_hours":  "24,1,0.25",
			"viewer_idle_seconds":  "30",
			"default_sender_email": "host@example.com",
		}),
	)
	streaming := newFakeStreaming()
	crm := newFakeCRM(crmBound)
	messaging := newFakeMessaging(msgBound)
	app := &App{
		streamingCaller: streaming,
		crmCaller:       crm,
		messagingCaller: messaging,
	}
	globalCtx = ctx
	globalApp = app
	return app, ctx, streaming, crm, messaging
}

// ─── webinars_create ───────────────────────────────────────────────

func TestCreate_AllocatesStreamAndReturnsURLs(t *testing.T) {
	app, ctx, streaming, _, _ := newTestApp(t, false, false)
	out, err := app.toolCreate(ctx, map[string]any{
		"title":        "Q2 Roadmap Webinar",
		"scheduled_at": "2026-06-01T15:00:00Z",
		"host_name":    "Alice",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w := out.(map[string]any)["webinar"].(*Webinar)
	if w.Slug == "" || !strings.Contains(w.Slug, "q2-roadmap-webinar") {
		t.Errorf("slug=%q, expected to start with q2-roadmap-webinar", w.Slug)
	}
	if w.StreamID == 0 {
		t.Errorf("stream_id should be set")
	}
	if streaming.callsCreate != 1 {
		t.Errorf("streaming.streams_create called %d times, want 1", streaming.callsCreate)
	}
	if w.RegistrationURL == "" || !strings.Contains(w.RegistrationURL, w.Slug) {
		t.Errorf("registration_url=%q", w.RegistrationURL)
	}
	if w.IngestURL == "" {
		t.Errorf("ingest_url should be materialized")
	}
	if w.Status != "scheduled" {
		t.Errorf("status=%q, want scheduled (since scheduled_at was set)", w.Status)
	}
}

func TestCreate_DraftWhenNoSchedule(t *testing.T) {
	app, ctx, _, _, _ := newTestApp(t, false, false)
	out, _ := app.toolCreate(ctx, map[string]any{"title": "Ad-hoc"})
	w := out.(map[string]any)["webinar"].(*Webinar)
	if w.Status != "draft" {
		t.Errorf("status=%q, want draft", w.Status)
	}
}

func TestCreate_RejectsBadKind(t *testing.T) {
	app, ctx, _, _, _ := newTestApp(t, false, false)
	_, err := app.toolCreate(ctx, map[string]any{"title": "x", "kind": "bogus"})
	if err == nil {
		t.Fatal("expected error for kind=bogus")
	}
}

// ─── webinars_register ─────────────────────────────────────────────

func TestRegister_CreatesContactWhenCRMBound(t *testing.T) {
	app, ctx, _, crm, _ := newTestApp(t, true, false)
	out, _ := app.toolCreate(ctx, map[string]any{
		"title":        "Demo",
		"scheduled_at": "2026-06-01T15:00:00Z",
	})
	w := out.(map[string]any)["webinar"].(*Webinar)

	rOut, err := app.toolRegister(ctx, map[string]any{
		"webinar_id":   w.ID,
		"email":        "alice@example.com",
		"display_name": "Alice",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	r := rOut.(map[string]any)["registrant"].(*Registrant)
	if r.JoinToken == "" {
		t.Errorf("join_token should be set")
	}
	if r.JoinURL == "" || !strings.Contains(r.JoinURL, r.JoinToken) {
		t.Errorf("join_url=%q", r.JoinURL)
	}
	if r.ContactID == nil {
		t.Errorf("contact_id should be set when CRM bound")
	}
	if len(crm.logs) == 0 {
		t.Errorf("expected at least one CRM activity log")
	}
}

func TestRegister_NoCRM_StillSucceeds(t *testing.T) {
	app, ctx, _, _, _ := newTestApp(t, false, false)
	out, _ := app.toolCreate(ctx, map[string]any{"title": "Demo"})
	w := out.(map[string]any)["webinar"].(*Webinar)

	rOut, err := app.toolRegister(ctx, map[string]any{
		"webinar_id": w.ID,
		"email":      "bob@example.com",
	})
	if err != nil {
		t.Fatalf("register without CRM: %v", err)
	}
	r := rOut.(map[string]any)["registrant"].(*Registrant)
	if r.ContactID != nil {
		t.Errorf("contact_id should be nil when CRM not bound, got %v", *r.ContactID)
	}
}

func TestRegister_RejectsWithoutContactInfo(t *testing.T) {
	app, ctx, _, _, _ := newTestApp(t, false, false)
	out, _ := app.toolCreate(ctx, map[string]any{"title": "Demo"})
	w := out.(map[string]any)["webinar"].(*Webinar)

	_, err := app.toolRegister(ctx, map[string]any{"webinar_id": w.ID})
	if err == nil {
		t.Fatal("expected error when both email and phone empty")
	}
}

func TestRegister_SchedulesRemindersWhenSchedulePresent(t *testing.T) {
	app, ctx, _, _, _ := newTestApp(t, false, false)
	out, _ := app.toolCreate(ctx, map[string]any{
		"title":        "Demo",
		"scheduled_at": "2099-06-01T15:00:00Z", // far future so reminders aren't past
	})
	w := out.(map[string]any)["webinar"].(*Webinar)

	_, err := app.toolRegister(ctx, map[string]any{
		"webinar_id": w.ID,
		"email":      "alice@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 3 lead hours × 1 channel (email only) = 3 reminders.
	var n int
	ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM webinar_reminders WHERE webinar_id = ? AND status = 'pending'`,
		w.ID).Scan(&n)
	if n != 3 {
		t.Errorf("expected 3 pending reminders, got %d", n)
	}
}

// ─── webinars_close + replay ────────────────────────────────────────

func TestClose_StopsStreamAndFlipsStatus(t *testing.T) {
	app, ctx, streaming, _, _ := newTestApp(t, false, false)
	out, _ := app.toolCreate(ctx, map[string]any{
		"title":        "Demo",
		"scheduled_at": "2026-06-01T15:00:00Z",
	})
	w := out.(map[string]any)["webinar"].(*Webinar)
	// Simulate "live" so close has something to do.
	ctx.AppDB().Exec(`UPDATE webinars SET status='live', started_at = '2026-06-01T15:00:00Z' WHERE id = ?`, w.ID)

	if _, err := app.toolClose(ctx, map[string]any{"id": w.ID}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !streaming.stopped[w.StreamID] {
		t.Errorf("expected streaming.streams_stop called for stream %d", w.StreamID)
	}
	getOut, _ := app.toolGet(ctx, map[string]any{"id": w.ID})
	if getOut.(map[string]any)["webinar"].(*Webinar).Status != "ended" {
		t.Errorf("webinar status should be ended")
	}
}

func TestPublishReplay_RequiresEnded(t *testing.T) {
	app, ctx, _, _, _ := newTestApp(t, false, false)
	out, _ := app.toolCreate(ctx, map[string]any{"title": "Demo"})
	w := out.(map[string]any)["webinar"].(*Webinar)

	_, err := app.toolPublishReplay(ctx, map[string]any{"id": w.ID})
	if err == nil {
		t.Fatal("expected error: cannot publish replay before ended")
	}
}

func TestPublishReplay_MintsURL(t *testing.T) {
	app, ctx, streaming, _, _ := newTestApp(t, false, false)
	out, _ := app.toolCreate(ctx, map[string]any{"title": "Demo"})
	w := out.(map[string]any)["webinar"].(*Webinar)
	streaming.streams[w.StreamID].Status = "ended" // pretend the stream has ended
	ctx.AppDB().Exec(`UPDATE webinars SET status='ended', ended_at = CURRENT_TIMESTAMP WHERE id = ?`, w.ID)

	resOut, err := app.toolPublishReplay(ctx, map[string]any{"id": w.ID})
	if err != nil {
		t.Fatalf("publish_replay: %v", err)
	}
	res := resOut.(map[string]any)
	url, _ := res["replay_url"].(string)
	if url == "" || !strings.Contains(url, "/replay/") {
		t.Errorf("replay_url=%q", url)
	}
}

// ─── Lifecycle event handlers ──────────────────────────────────────

func TestStreamStarted_FlipsWebinarToLive(t *testing.T) {
	app, ctx, _, _, _ := newTestApp(t, false, false)
	out, _ := app.toolCreate(ctx, map[string]any{
		"title":        "Demo",
		"scheduled_at": "2026-06-01T15:00:00Z",
	})
	w := out.(map[string]any)["webinar"].(*Webinar)

	app.handleStreamStarted(ctx, sdk.Event{
		Topic:     "stream.started",
		ProjectID: "test-proj",
		Data:      map[string]any{"id": float64(w.StreamID)},
	})

	getOut, _ := app.toolGet(ctx, map[string]any{"id": w.ID})
	gw := getOut.(map[string]any)["webinar"].(*Webinar)
	if gw.Status != "live" {
		t.Errorf("status=%q, want live", gw.Status)
	}
}

// ─── Engagement ────────────────────────────────────────────────────

func TestGetEngagement_AssemblesCounts(t *testing.T) {
	app, ctx, _, _, _ := newTestApp(t, false, false)
	out, _ := app.toolCreate(ctx, map[string]any{
		"title":            "Demo",
		"duration_minutes": 60,
	})
	w := out.(map[string]any)["webinar"].(*Webinar)
	app.toolRegister(ctx, map[string]any{"webinar_id": w.ID, "email": "a@x.com"})
	app.toolRegister(ctx, map[string]any{"webinar_id": w.ID, "email": "b@x.com"})
	app.toolRegister(ctx, map[string]any{"webinar_id": w.ID, "email": "c@x.com"})

	eOut, err := app.toolGetEngagement(ctx, map[string]any{"id": w.ID})
	if err != nil {
		t.Fatal(err)
	}
	e := eOut.(map[string]any)
	if e["registrations"].(int) != 3 {
		t.Errorf("registrations=%v, want 3", e["registrations"])
	}
	if e["joined_live"].(int) != 0 {
		t.Errorf("joined_live=%v, want 0", e["joined_live"])
	}
}

// ─── tiny helpers ──────────────────────────────────────────────────

func intToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	if neg {
		out = "-" + out
	}
	return out
}
