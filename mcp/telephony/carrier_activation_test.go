package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func activationExec(t *testing.T, a *App, query string, args ...any) {
	t.Helper()
	if _, err := a.db().db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func activationPending(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errAnswerPreparationInProgress) {
		t.Fatalf("want pending activation, got %v", err)
	}
}
func activationRow(t *testing.T, a *App, id string) *callRow {
	t.Helper()
	row, err := a.db().findCall(id)
	if err != nil || row == nil {
		t.Fatalf("load call: %v", err)
	}
	return row
}
func activationCommands(t *testing.T, p *answerPlatform, want ...string) {
	t.Helper()
	if len(p.integrationCalls) != len(want) {
		t.Fatalf("commands: %+v; want %v", p.integrationCalls, want)
	}
	for i, cmd := range p.integrationCalls {
		if cmd.Tool != want[i] {
			t.Fatalf("commands: %+v; want %v", p.integrationCalls, want)
		}
	}
}

func TestCarrierActivationUnansweredThenConfirmedMedia(t *testing.T) {
	a, ctx, p, route, row, key := reliabilityFixture(t)
	t.Cleanup(a.stopRoutingDispatcher)
	activationExec(t, a, `UPDATE calls SET peer_kind='realtime',agent_id=7,recording_mode='always',recording_channels='dual' WHERE id=?`, row.ID)
	row = activationRow(t, a, row.ID)
	_, err := a.prepareAndActivateTelnyxAI(ctx, row, "Help the caller.", "", "")
	activationPending(t, err)
	activationCommands(t, p, "answer_call")
	answer := p.integrationCalls[0].Input
	if answer["stream_url"] != nil || answer["record"] != "record-from-answer" || answer["record_channels"] != "dual" {
		t.Fatalf("answer settings: %v", answer)
	}
	current := activationRow(t, a, row.ID)
	if current.AnsweredAt != "" || current.CarrierAnsweredAt != "" || current.MediaConnectedAt != "" || current.Status != "answering" {
		t.Fatalf("invented answer/media: %+v", current)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = a.driveCarrierActivation(ctx, row.ID) }()
	}
	wg.Wait()
	activationCommands(t, p, "answer_call")
	for range 2 {
		if rec := reliabilityEvent(t, a, route, row, key, "call.answered"); rec.Code != 204 {
			t.Fatalf("answer callback: %d %s", rec.Code, rec.Body.String())
		}
	}
	activationCommands(t, p, "answer_call", "start_streaming")
	current = activationRow(t, a, row.ID)
	if current.CarrierAnsweredAt == "" || current.MediaConnectedAt != "" {
		t.Fatalf("incorrect carrier/media facts: %+v", current)
	}
	_, err = a.prepareAndActivateTelnyxAI(ctx, current, "Help the caller.", "", "")
	activationPending(t, err)
	if rec := reliabilityEvent(t, a, route, row, key, "streaming.started"); rec.Code != 204 {
		t.Fatal(rec.Body.String())
	}
	current = activationRow(t, a, row.ID)
	if current.MediaConnectedAt == "" || current.StateExpiresAt != "" {
		t.Fatalf("media not confirmed or activation timeout retained: %+v", current)
	}
	if _, err = a.prepareAndActivateTelnyxAI(ctx, current, "Help the caller.", "", ""); err != nil {
		t.Fatal(err)
	}
	activationCommands(t, p, "answer_call", "start_streaming")
	if len(p.spawned) != 1 {
		t.Fatalf("spawned %d times", len(p.spawned))
	}
}

func TestCarrierActivationUsesCarrierEvidence(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "premature_local_answer", true: "answered_IVR"}[confirmed], func(t *testing.T) {
			a, ctx, p, _, row, _ := reliabilityFixture(t)
			t.Cleanup(a.stopRoutingDispatcher)
			// Simulates both the old 0.7.2 ready thread and a legitimately answered IVR.
			activationExec(t, a, `UPDATE calls SET peer_kind='realtime',agent_id=7,thread_id='ready-ai',audio_bridge_url='wss://bridge.test',status='answered',answered_at=? WHERE id=?`, ringTime(time.Now()), row.ID)
			if confirmed {
				if _, err := a.db().updateStatusWithFacts(row.ID, "answered", "", lifecycleFacts{Source: "provider"}); err != nil {
					t.Fatal(err)
				}
			}
			row = activationRow(t, a, row.ID)
			_, err := a.prepareAndActivateTelnyxAI(ctx, row, "Help.", "", "")
			activationPending(t, err)
			if confirmed {
				activationCommands(t, p, "start_streaming")
			} else {
				activationCommands(t, p, "answer_call")
			}
			if len(p.spawned) != 0 {
				t.Fatal("prepared AI twice")
			}
		})
	}
}

func TestCarrierActivationAnsweredIVRPreparesOnce(t *testing.T) {
	a, ctx, p, _, row, _ := reliabilityFixture(t)
	t.Cleanup(a.stopRoutingDispatcher)
	activationExec(t, a, `UPDATE calls SET peer_kind='realtime',agent_id=7,status='answered',carrier_answered_at=?,answered_at=? WHERE id=?`, ringTime(time.Now()), ringTime(time.Now()), row.ID)
	row = activationRow(t, a, row.ID)
	_, err := a.prepareAndActivateTelnyxAI(ctx, row, "Help.", "", "")
	activationPending(t, err)
	activationCommands(t, p, "start_streaming")
	if len(p.spawned) != 1 {
		t.Fatal("missing AI startup")
	}
}

func TestCarrierActivationRejectedCommandsAreBounded(t *testing.T) {
	for _, phase := range []string{"answer", "stream"} {
		t.Run(phase, func(t *testing.T) {
			a, ctx, p, _, row, _ := reliabilityFixture(t)
			t.Cleanup(a.stopRoutingDispatcher)
			activationExec(t, a, `UPDATE calls SET peer_kind='realtime',agent_id=7 WHERE id=?`, row.ID)
			if phase == "stream" {
				activationExec(t, a, `UPDATE calls SET carrier_answered_at=? WHERE id=?`, ringTime(time.Now()), row.ID)
				p.failTool = "start_streaming"
			} else {
				p.failTool = "answer_call"
			}
			row = activationRow(t, a, row.ID)
			_, err := a.prepareAndActivateTelnyxAI(ctx, row, "Help.", "", "")
			activationPending(t, err)
			for range 3 {
				activationExec(t, a, `UPDATE carrier_activations SET next_attempt_at=? WHERE call_id=?`, ringTime(time.Now().Add(-time.Second)), row.ID)
				err = a.driveCarrierActivation(ctx, row.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			a.stopRoutingDispatcher()
			if phase == "answer" {
				activationCommands(t, p, "answer_call", "answer_call", "answer_call", "reject_call")
			} else {
				activationCommands(t, p, "start_streaming", "start_streaming", "start_streaming", "hangup_call")
			}
			if p.integrationCalls[0].Input["command_id"] != p.integrationCalls[2].Input["command_id"] {
				t.Fatal("retry lost idempotency key")
			}
			current := activationRow(t, a, row.ID)
			if current.Status != "failed" || current.MediaConnectedAt != "" || current.TalkDurationSeconds != 0 || callClassification(*current) != "ai_activation_failed" || !callbackEligible(*current) {
				t.Fatalf("bad failure classification: %+v", current)
			}
			if len(p.spawned) != 1 || len(p.killed) != 1 {
				t.Fatalf("startup/cleanup: %d/%d", len(p.spawned), len(p.killed))
			}
			for range 5 {
				_ = a.driveCarrierActivation(ctx, row.ID)
			}
			if len(p.integrationCalls) != 4 {
				t.Fatal("terminal activation retried")
			}
		})
	}
}

func TestCarrierActivationConfirmationTimeout(t *testing.T) {
	a, ctx, p, _, row, _ := reliabilityFixture(t)
	t.Cleanup(a.stopRoutingDispatcher)
	activationExec(t, a, `UPDATE calls SET peer_kind='realtime',agent_id=7 WHERE id=?`, row.ID)
	row = activationRow(t, a, row.ID)
	_, err := a.prepareAndActivateTelnyxAI(ctx, row, "Help.", "", "")
	activationPending(t, err)
	activationExec(t, a, `UPDATE carrier_activations SET next_attempt_at=? WHERE call_id=?`, ringTime(time.Now().Add(-time.Second)), row.ID)
	if err = a.driveCarrierActivation(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	a.stopRoutingDispatcher()
	activationCommands(t, p, "answer_call", "reject_call")
	if activationRow(t, a, row.ID).Status != "failed" {
		t.Fatal("unconfirmed answer left ringing")
	}
}

func TestCarrierActivationCancellationIgnoresLateCallbacks(t *testing.T) {
	a, ctx, p, route, row, key := reliabilityFixture(t)
	t.Cleanup(a.stopRoutingDispatcher)
	activationExec(t, a, `UPDATE calls SET peer_kind='realtime',agent_id=7 WHERE id=?`, row.ID)
	row = activationRow(t, a, row.ID)
	_, err := a.prepareAndActivateTelnyxAI(ctx, row, "Help.", "", "")
	activationPending(t, err)
	for _, event := range []string{"call.hangup", "call.answered", "streaming.started", "call.answered"} {
		if rec := reliabilityEvent(t, a, route, row, key, event); rec.Code != 204 {
			t.Fatalf("%s: %d %s", event, rec.Code, rec.Body.String())
		}
	}
	activationCommands(t, p, "answer_call")
	current := activationRow(t, a, row.ID)
	if !isTerminalStatus(current.Status) || current.MediaConnectedAt != "" || current.TalkDurationSeconds != 0 {
		t.Fatalf("revived canceled call: %+v", current)
	}
}

func TestCarrierActivationRecoveryAfterPreparationOutlivesRequest(t *testing.T) {
	a, ctx, p, route, row, unblock := preparationFixture(t, false)
	t.Cleanup(a.stopRoutingDispatcher)
	activationExec(t, a, `UPDATE inbound_routes SET carrier_slug='telnyx' WHERE id=?`, route.ID)
	activationExec(t, a, `UPDATE calls SET carrier_slug='telnyx' WHERE id=?`, row.ID)
	row = activationRow(t, a, row.ID)
	a.preparations.wait = 20 * time.Millisecond
	_, err := a.prepareAndActivateTelnyxAI(ctx, row, "Help.", "", "")
	activationPending(t, err)
	unblock()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if realtimePreparationReady(activationRow(t, a, row.ID)) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !realtimePreparationReady(activationRow(t, a, row.ID)) {
		t.Fatal("startup did not complete")
	}
	if err = a.runCarrierActivations(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	a.stopRoutingDispatcher()
	spawns, _, commands := p.counts()
	if spawns != 1 || commands != 1 {
		t.Fatalf("recovery startup/answer: %d/%d", spawns, commands)
	}
}

func TestCarrierActivationCancellationDuringPreparation(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, false)
	activationExec(t, a, `UPDATE calls SET carrier_slug='telnyx' WHERE id=?`, row.ID)
	row = activationRow(t, a, row.ID)
	a.preparations.wait = 20 * time.Millisecond
	_, err := a.prepareAndActivateTelnyxAI(ctx, row, "Help.", "", "")
	activationPending(t, err)
	if err = a.db().updateStatus(row.ID, "canceled", ""); err != nil {
		t.Fatal(err)
	}
	unblock()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, killed, _ := p.counts()
		if killed > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	spawns, killed, commands := p.counts()
	if spawns != 1 || killed != 1 || commands != 0 {
		t.Fatalf("late startup escaped cancellation: %d/%d/%d", spawns, killed, commands)
	}
}

func TestCarrierActivationTalkTimeRequiresMedia(t *testing.T) {
	for _, media := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_media", true: "connected"}[media], func(t *testing.T) {
			a, _, _, _, row, _ := reliabilityFixture(t)
			start := time.Now().Add(-time.Minute)
			activationExec(t, a, `UPDATE calls SET peer_kind='realtime',status='answering' WHERE id=?`, row.ID)
			if _, err := a.db().updateStatusWithFacts(row.ID, "answered", "", lifecycleFacts{Source: "provider", OccurredAt: ringTime(start)}); err != nil {
				t.Fatal(err)
			}
			if media {
				activationExec(t, a, `UPDATE calls SET media_connected_at=? WHERE id=?`, ringTime(start.Add(20*time.Second)), row.ID)
			}
			if _, err := a.db().updateStatusWithFacts(row.ID, "completed", "", lifecycleFacts{Source: "provider", OccurredAt: ringTime(start.Add(50 * time.Second))}); err != nil {
				t.Fatal(err)
			}
			want := 0
			if media {
				want = 30
			}
			if got := activationRow(t, a, row.ID).TalkDurationSeconds; got != want {
				t.Fatalf("talk seconds %d want %d", got, want)
			}
		})
	}
}

func TestCarrierActivationUpgradeRequiresPositiveEvidence(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	files, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name() < "035" {
			applyMigrationFile(t, db, filepath.Join("migrations", file.Name()))
		}
	}
	for _, id := range []string{"local", "provider", "media"} {
		_, err = db.Exec(`INSERT INTO calls(id,thread_id,to_number,from_number,directive,voice,audio_bridge_url,status,placed_at,project_id,answered_at) VALUES(?,'ready-'||?,'+33111111111','+33611111111','','','bridge','answered','2026-09-29T05:00:00Z','project-a','2026-09-29T05:00:01Z')`, id, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO call_events(event_id,call_id,project_id,topic,revision,occurred_at,payload_json,created_at,published_at) VALUES('provider-event','provider','project-a','call.answered',1,'2026-09-29T05:00:02Z','{"source":"provider"}','then','sent')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE calls SET media_connected_at='2026-09-29T05:00:03Z' WHERE id='media'`)
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationFile(t, db, "migrations/035_carrier_activation.sql")
	for id, want := range map[string]string{"local": "", "provider": "2026-09-29T05:00:02Z", "media": "2026-09-29T05:00:03Z"} {
		var got, local string
		if err = db.QueryRow(`SELECT carrier_answered_at,answered_at FROM calls WHERE id=?`, id).Scan(&got, &local); err != nil {
			t.Fatal(err)
		}
		if got != want || local != "2026-09-29T05:00:01Z" {
			t.Fatalf("%s evidence=%s original=%s", id, got, local)
		}
	}
}

func TestCarrierActivationRestartKeepsAcceptedCommandAndDeadline(t *testing.T) {
	a, ctx, p, _, row, _ := reliabilityFixture(t)
	activationExec(t, a, `UPDATE calls SET peer_kind='realtime',agent_id=7 WHERE id=?`, row.ID)
	row = activationRow(t, a, row.ID)
	_, err := a.prepareAndActivateTelnyxAI(ctx, row, "Help.", "", "")
	activationPending(t, err)
	before, err := a.carrierActivationPublic(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &App{installID: 42}
	t.Cleanup(restarted.stopRoutingDispatcher)
	row = activationRow(t, restarted, row.ID)
	_, err = restarted.prepareAndActivateTelnyxAI(ctx, row, "Help.", "", "")
	activationPending(t, err)
	after, err := restarted.carrierActivationPublic(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before["deadline_at"] != after["deadline_at"] || after["attempts"] != 1 {
		t.Fatalf("restart reset budget: %v -> %v", before, after)
	}
	activationCommands(t, p, "answer_call")
	activationExec(t, a, `UPDATE carrier_activations SET deadline_at=? WHERE call_id=?`, ringTime(time.Now().Add(-time.Second)), row.ID)
	if err = restarted.driveCarrierActivation(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	restarted.stopRoutingDispatcher()
	activationCommands(t, p, "answer_call", "reject_call")
}

func TestCarrierActivationLifecycleTimeoutHandlesLostAnswerCallback(t *testing.T) {
	a, ctx, p, _, row, _ := reliabilityFixture(t)
	t.Cleanup(a.stopRoutingDispatcher)
	activationExec(t, a, `UPDATE calls SET peer_kind='realtime',agent_id=7 WHERE id=?`, row.ID)
	row = activationRow(t, a, row.ID)
	_, err := a.prepareAndActivateTelnyxAI(ctx, row, "Help.", "", "")
	activationPending(t, err)
	// Carrier accepted answer, but its confirmation was lost. It will reject a
	// rejection of the now-established leg; hangup must still end that leg.
	p.failTool = "reject_call"
	activationExec(t, a, `UPDATE carrier_activations SET deadline_at=? WHERE call_id=?`, ringTime(time.Now().Add(-time.Second)), row.ID)
	if err = a.expireCall(ctx, activationRow(t, a, row.ID)); err != nil {
		t.Fatal(err)
	}
	a.stopRoutingDispatcher()
	activationCommands(t, p, "answer_call", "reject_call", "hangup_call")
	current := activationRow(t, a, row.ID)
	if callClassification(*current) != "ai_activation_failed" || current.Status != "failed" {
		t.Fatalf("lifecycle overrode activation failure: %+v", current)
	}
	var seconds int
	if err = a.db().db.QueryRow(`SELECT json_extract(payload_json,'$.talk_duration_seconds') FROM call_events WHERE call_id=? AND topic='call.failed' ORDER BY revision DESC LIMIT 1`, row.ID).Scan(&seconds); err != nil {
		t.Fatal(err)
	}
	if seconds != 0 {
		t.Fatalf("unconnected AI emitted talk time: %d", seconds)
	}
}
