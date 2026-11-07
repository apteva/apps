package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRecoveryRecordKeepsEvidenceAndDropsEverythingElse(t *testing.T) {
	raw := `{"recovery_id":"recovery-1a2b","trigger":"https://secret.example/token","started_at":"2026-11-04T15:02:30Z","ended_at":"not a time","outcome":"connected","lost_ms":19500,
	"connection_state":"connected","ice_state":"weird","signaling_state":"closed","last_packet_age_ms":-5,"online":true,"visibility":"hidden","microphone":"live",
	"checks":{"telephony":{"ok":false,"ms":3000},"page":{"ok":true,"ms":90000000000}},
	"attempts":[{"attempt_id":"attempt-1","at_ms":0,"duration_ms":4000,"reached":"session","outcome":"failed","code":"socket_open_timeout"},
	{"attempt_id":"bad id","at_ms":5000,"reached":"nonsense","outcome":"x","code":"has spaces and a sk-secret"},{},{},{},{},{}],"attempts_total":7,"extra":"ignored"}`
	var in audioRecoveryRecord
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	got, ok := normalizeAudioRecoveryRecord(in)
	if !ok {
		t.Fatal("rejected a valid recovery")
	}
	if got.Trigger != "other" || got.EndedAt != "" || got.ICEState != "" || *got.LastPacketAgeMS != 0 || got.Checks.Page.MS != audioRecoveryMSLimit || got.Checks.Telephony.OK {
		t.Fatalf("not normalized: %+v", got)
	}
	if len(got.Attempts) != audioRecoveryAttemptsMax || got.AttemptsTotal != 7 {
		t.Fatalf("attempts not bounded: %d total %d", len(got.Attempts), got.AttemptsTotal)
	}
	if a := got.Attempts[0]; a.Code != "socket_open_timeout" || a.Reached != "session" || a.Outcome != "failed" {
		t.Fatalf("attempt lost: %+v", a)
	}
	if a := got.Attempts[1]; a.AttemptID != "" || a.Reached != "" || a.Outcome != "" || a.Code != "" {
		t.Fatalf("attempt not cleaned: %+v", a)
	}
	saved, _ := json.Marshal(got)
	for _, leak := range []string{"secret", "weird", "nonsense", "extra"} {
		if strings.Contains(string(saved), leak) {
			t.Fatalf("%q reached the store: %s", leak, saved)
		}
	}
	if _, ok := normalizeAudioRecoveryRecord(audioRecoveryRecord{RecoveryID: "session-1"}); ok {
		t.Fatal("an ID of another kind was accepted")
	}
	if _, ok := normalizeAudioRecoveryRecord(audioRecoveryRecord{RecoveryID: "recovery-NOT/valid"}); ok {
		t.Fatal("an invalid ID was accepted")
	}
}

func TestRecoveryRecordRouteStoresOnePerRecoveryEvenAfterTheCallEnded(t *testing.T) {
	app, row, _, _ := listenerFixture(t)
	alice := phoneTestIdentity("alice")
	p, err := app.phonePrincipal(row.ProjectID, alice)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.setPhoneOwner(&row, p, "sales"); err != nil {
		t.Fatal(err)
	}
	send := func(id string, who *phoneIdentity, body any) int {
		return phoneTestRequest(app, who, "POST", "/softphone/recovery/"+id, body).Code
	}
	record := map[string]any{"record": map[string]any{"recovery_id": "recovery-abc", "trigger": "signaling_closed", "outcome": "connected", "lost_ms": 900, "attempts": []map[string]any{{"attempt_id": "attempt-1", "reached": "ready", "outcome": "connected"}}, "attempts_total": 1}}
	if code := send(row.ID, &alice, record); code != 202 {
		t.Fatalf("status=%d", code)
	}
	if code := send(row.ID, &alice, record); code != 202 { // A retried POST.
		t.Fatalf("retry status=%d", code)
	}
	bob := phoneTestIdentity("bob")
	if code := send(row.ID, &bob, record); code != 404 {
		t.Fatalf("another user's record was accepted: %d", code)
	}
	if code := send(row.ID, &alice, map[string]any{"record": map[string]any{"recovery_id": "nope"}}); code != 400 {
		t.Fatalf("invalid record status=%d", code)
	}
	if code := send("no-such-call", &alice, record); code != 404 {
		t.Fatalf("unknown call status=%d", code)
	}
	if err := app.db().updateStatus(row.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	ended := map[string]any{"record": map[string]any{"recovery_id": "recovery-def", "trigger": "signaling_closed", "outcome": "cancelled"}}
	if code := send(row.ID, &alice, ended); code != 202 {
		t.Fatalf("record after call end status=%d", code)
	}
	if _, err := app.audioNetworks.flush(t.Context(), app.db(), time.Now()); err != nil {
		t.Fatal(err)
	}
	events, err := app.db().browserNetworkEvents(t.Context(), row.ProjectID, row.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	recoveries := map[string]audioNetworkEvent{}
	for _, e := range events {
		if e.Event == "softphone.browser.recovery" {
			recoveries[e.Recovery.RecoveryID] = e
		}
	}
	if len(recoveries) != 2 {
		t.Fatalf("stored %d recoveries: %+v", len(recoveries), recoveries)
	}
	got := recoveries["recovery-abc"]
	if got.Reason != "connected" || got.Recovery.LostMS != 900 || got.AdviserIdentity != alice || got.IdentitySource != "authorized_http_session" {
		t.Fatalf("stored %+v", got)
	}
}

func TestDisconnectCauseMatchesTheIncidentCalls(t *testing.T) {
	at := time.Date(2026, 11, 4, 15, 3, 0, 0, time.UTC)
	ago := func(seconds float64) string {
		return at.Add(-time.Duration(seconds * float64(time.Second))).Format(time.RFC3339Nano)
	}
	for name, c := range map[string]struct {
		audio, signaling float64
		muted            bool
		want             string
	}{
		// Ages recorded for these browsers at 15:02:40 UTC on 2026-11-04.
		"Bruno":                       {0.02, 18.66, false, "signaling"},
		"Manon":                       {0.01, 17.97, false, "signaling"},
		"Sara":                        {9.95, 0.1, false, "audio"},
		"Tabara":                      {11.41, 17.41, false, "both"},
		"Sandra":                      {12.55, 17.26, false, "both"},
		"Loane":                       {14.77, 18.19, false, "both"},
		"healthy":                     {0.02, 2, false, "neither"},
		"report due is not a failure": {0.02, 7.9, false, "neither"},
		"muted stays silent":          {30, 2, true, "neither"},
	} {
		got := audioDisconnectCauseAt(&audioDisconnectInfo{LastAudioReadAt: ago(c.audio), LastReadAt: ago(c.signaling), MicrophoneMuted: c.muted}, at)
		if got.FirstFailed != c.want {
			t.Errorf("%s: got %+v want %s", name, got, c.want)
		}
	}
	if got := audioDisconnectCauseAt(nil, at); got.FirstFailed != "unknown" {
		t.Fatalf("nil: %+v", got)
	}
	if got := audioDisconnectCauseAt(&audioDisconnectInfo{}, at); got.FirstFailed != "unknown" {
		t.Fatalf("empty: %+v", got)
	}
	if got := audioDisconnectCauseAt(&audioDisconnectInfo{LastReadAt: ago(1)}, at); got.FirstFailed != "audio" || *got.SignalingAgeMS != 1000 || got.AudioAgeMS != nil {
		t.Fatalf("missing audio: %+v", got)
	}
}
