package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestManifestPublishesGigLifecycleEvents(t *testing.T) {
	want := []string{
		"gig.created",
		"gig.offered",
		"gig.accepted",
		"gig.declined",
		"gig.submitted",
		"gig.reviewed",
		"gig.rejected",
		"gig.cancelled",
		"gig.schedule_updated",
		"gig.overdue",
		"gig.access_expired",
		"gig.compensation_approved",
		"gig.public_domain_attached",
		"gig.public_domain_detached",
	}

	got := map[string]bool{}
	for _, event := range (&App{}).Manifest().Provides.Publishes {
		got[event.Name] = true
	}
	for _, topic := range want {
		if !got[topic] {
			t.Errorf("manifest missing published event %q", topic)
		}
	}
}

func TestSubmissionEventsIdentifySubmissionWorkerAndRevision(t *testing.T) {
	ctx := testCtx(t)
	recorder := tk.NewEmitRecorder()
	ctx.SetEmitter(recorder)
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })

	workerID := seedWorker(t, ctx, "project-a", 701)
	gigID := seedGig(t, ctx, "project-a", "accepted", `{"type":"object","properties":{}}`)
	assignmentID := seedAssignment(t, ctx, gigID, workerID, "accepted", "direct", "event-token")
	app := &App{}

	submit := func() {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/worker/event-token/submit", bytes.NewBufferString(`{"payload":{}}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.handleWorkerSubmit(rec, req, "event-token")
		if rec.Code != http.StatusOK {
			t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
		}
	}

	submit()
	submit()
	events := recorder.EventsByTopic("gig.submitted")
	if len(events) != 2 {
		t.Fatalf("gig.submitted events=%d, want 2", len(events))
	}
	for i, event := range events {
		data := event.Data.(map[string]any)
		if data["gig_id"] != gigID || data["assignment_id"] != assignmentID || data["worker_id"] != workerID {
			t.Fatalf("event %d identity payload=%#v", i, data)
		}
		if data["submission_id"].(int64) <= 0 {
			t.Fatalf("event %d missing submission_id: %#v", i, data)
		}
		if data["submission_number"] != int64(i+1) || data["is_revision"] != (i == 1) {
			t.Fatalf("event %d revision payload=%#v", i, data)
		}
	}

	if _, err := app.toolGigsAccept(ctx, map[string]any{"_project_id": "project-a", "id": gigID}); err != nil {
		t.Fatal(err)
	}
	reviewed := recorder.EventsByTopic("gig.reviewed")
	if len(reviewed) != 1 {
		t.Fatalf("gig.reviewed events=%d, want 1", len(reviewed))
	}
	data := reviewed[0].Data.(map[string]any)
	if data["gig_id"] != gigID || data["worker_id"] != workerID || data["submission_id"] != events[1].Data.(map[string]any)["submission_id"] {
		t.Fatalf("gig.reviewed identity payload=%#v", data)
	}
}
