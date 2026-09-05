package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type joinedParticipant struct {
	participant *Participant
	token       string
}

func joinForTest(t *testing.T, app *App, ctx *sdk.AppCtx, roomID int64, caps map[string]any) joinedParticipant {
	t.Helper()
	tokenOut, err := app.toolCreateJoinToken(ctx, map[string]any{
		"room_id": roomID, "display_name": "Test User", "capabilities": caps,
	})
	if err != nil {
		t.Fatal(err)
	}
	joinOut, err := app.toolJoinRoom(ctx, map[string]any{"token": tokenOut.(map[string]any)["token"]})
	if err != nil {
		t.Fatal(err)
	}
	out := joinOut.(map[string]any)
	return joinedParticipant{participant: out["participant"].(*Participant), token: out["participant_token"].(string)}
}

func roomRequest(method string, roomID int64, resource, token string, body string) *http.Request {
	path := "/api/rooms/" + formatInt(roomID) + "/" + resource
	if strings.Contains(path, "?") {
		path += "&project_id=test-proj"
	} else {
		path += "?project_id=test-proj"
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func formatInt(v int64) string { return strconv.FormatInt(v, 10) }

func TestPublicRoomAPIRequiresParticipantBearer(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	rec := httptest.NewRecorder()
	app.handleAPIRooms(rec, httptest.NewRequest(http.MethodGet, "/api/rooms/1/participants?project_id=test-proj", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "participant_key") {
		t.Fatal("participant secret leaked")
	}
	_ = room
}

func TestAuthenticatedProjectHeaderCannotBeOverridden(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	req := httptest.NewRequest(http.MethodGet, "/admin/rooms?project_id=other", nil)
	req.Header.Set("X-Apteva-Project-ID", "trusted")
	if _, err := resolveProjectFromRequest(req); err == nil {
		t.Fatal("query-string project overrode authenticated project")
	}
}

func TestAuthenticatedParticipantCannotImpersonate(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	alice := joinForTest(t, app, ctx, room.ID, map[string]any{"chat": true})
	bob := joinForTest(t, app, ctx, room.ID, map[string]any{"chat": true})
	body := `{"participant_id":` + formatInt(bob.participant.ID) + `,"body":"forged"}`
	rec := httptest.NewRecorder()
	app.handleAPIRooms(rec, roomRequest(http.MethodPost, room.ID, "messages", alice.token, body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestParticipantSecretNeverSerialized(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	joined := joinForTest(t, app, ctx, room.ID, map[string]any{"chat": true})
	raw, err := json.Marshal(joined.participant)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("participant_key")) || bytes.Contains(raw, []byte(joined.token)) {
		t.Fatalf("participant credential serialized: %s", raw)
	}
	rec := httptest.NewRecorder()
	app.handleAPIRooms(rec, roomRequest(http.MethodGet, room.ID, "participants", joined.token, ""))
	for _, privateField := range []string{"participant_key", "capabilities", "metadata", "last_seen_at", "project_id"} {
		if strings.Contains(rec.Body.String(), privateField) {
			t.Fatalf("public roster leaked %q: %s", privateField, rec.Body.String())
		}
	}
}

func TestCapabilitiesAndMessageVisibilityAreEnforced(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	alice := joinForTest(t, app, ctx, room.ID, map[string]any{"chat": true})
	bob := joinForTest(t, app, ctx, room.ID, map[string]any{"chat": true})
	muted := joinForTest(t, app, ctx, room.ID, map[string]any{"chat": false})
	if _, err := app.toolSendMessage(ctx, map[string]any{"room_id": room.ID, "participant_id": muted.participant.ID, "body": "forbidden"}); err == nil {
		t.Fatal("participant without chat capability sent a message")
	}
	if _, err := app.toolSendMessage(ctx, map[string]any{"room_id": room.ID, "participant_id": alice.participant.ID, "body": "alice only", "visibility": "private"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		who  joinedParticipant
		want int
	}{{"sender", alice, 1}, {"other participant", bob, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			app.handleAPIRooms(rec, roomRequest(http.MethodGet, room.ID, "messages?since_id=0", tc.who.token, ""))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				Count int `json:"count"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Count != tc.want {
				t.Fatalf("visible messages=%d, want %d", body.Count, tc.want)
			}
		})
	}
}

func TestRoomControlCapabilityGatesEndingRoom(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	guest := joinForTest(t, app, ctx, room.ID, map[string]any{"chat": true})
	host := joinForTest(t, app, ctx, room.ID, map[string]any{"room_control": true})
	rec := httptest.NewRecorder()
	app.handleAPIRooms(rec, roomRequest(http.MethodPost, room.ID, "end", guest.token, `{}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("guest status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	app.handleAPIRooms(rec, roomRequest(http.MethodPost, room.ID, "end", host.token, `{}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("host status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPublicRoomMetadataDoesNotExposeRoster(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	_ = joinForTest(t, app, ctx, room.ID, map[string]any{"chat": true})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/room/"+room.Slug+"?project_id=test-proj", nil)
	app.handleRoomPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, forbidden := range []string{"participants", "participant_key", "tracks", "messages"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("public room leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestPublicJSONBodyIsBounded(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	joined := joinForTest(t, app, ctx, room.ID, map[string]any{"chat": true})
	tooLarge := `{"body":"` + strings.Repeat("x", maxPublicJSONBody+1) + `"}`
	rec := httptest.NewRecorder()
	app.handleAPIRooms(rec, roomRequest(http.MethodPost, room.ID, "messages", joined.token, tooLarge))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestJoinTokenConsumedAtomically(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	tokenOut, err := app.toolCreateJoinToken(ctx, map[string]any{"room_id": room.ID, "max_uses": 1})
	if err != nil {
		t.Fatal(err)
	}
	token := tokenOut.(map[string]any)["token"].(string)
	const attempts = 12
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := app.toolJoinRoom(ctx, map[string]any{"token": token})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful joins=%d, want 1", successes)
	}
}

func TestJoinTokenExpiryMustBeFutureRFC3339(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	for _, expiry := range []string{"tomorrow", time.Now().Add(-time.Minute).Format(time.RFC3339)} {
		if _, err := app.toolCreateJoinToken(ctx, map[string]any{"room_id": room.ID, "expires_at": expiry}); err == nil {
			t.Fatalf("expiry %q should fail", expiry)
		}
	}
}

func TestRevokedJoinTokenCannotJoin(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	out, err := app.toolCreateJoinToken(ctx, map[string]any{"room_id": room.ID})
	if err != nil {
		t.Fatal(err)
	}
	jt := out.(map[string]any)["join_token"].(*JoinToken)
	if ok, err := revokeJoinToken("test-proj", room.ID, jt.ID); err != nil || !ok {
		t.Fatalf("revoke ok=%v err=%v", ok, err)
	}
	if _, err := app.toolJoinRoom(ctx, map[string]any{"token": out.(map[string]any)["token"]}); err == nil {
		t.Fatal("revoked token joined")
	}
}

func TestMessageCursorReturnsRowsAfterHundred(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	joined := joinForTest(t, app, ctx, room.ID, map[string]any{"chat": true})
	for i := 0; i < 105; i++ {
		if _, err := app.toolSendMessage(ctx, map[string]any{"room_id": room.ID, "participant_id": joined.participant.ID, "body": "message " + formatInt(int64(i))}); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	app.handleAPIRooms(rec, roomRequest(http.MethodGet, room.ID, "messages?since_id=100", joined.token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 5 {
		t.Fatalf("messages=%d, want 5", len(body.Messages))
	}
}

func TestSignalRoutesOnlyToAuthenticatedRecipient(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	alice := joinForTest(t, app, ctx, room.ID, map[string]any{"audio": true})
	bob := joinForTest(t, app, ctx, room.ID, map[string]any{"audio": true})
	payload := `{"to_participant_id":` + formatInt(bob.participant.ID) + `,"payload":{"type":"offer","sdp":"v=0\\r\\nm=audio 9 UDP/TLS/RTP/SAVPF 111"}}`
	rec := httptest.NewRecorder()
	app.handleAPIRooms(rec, roomRequest(http.MethodPost, room.ID, "signal/offer", alice.token, payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("send status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	app.handleAPIRooms(rec, roomRequest(http.MethodGet, room.ID, "signal?since_id=0", bob.token, ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"count":1`) {
		t.Fatalf("receive status=%d body=%s", rec.Code, rec.Body.String())
	}
}
