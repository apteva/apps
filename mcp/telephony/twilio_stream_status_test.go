package main

import (
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func streamStatusTestApp(t *testing.T) *App {
	t.Helper()
	previous := globalCtx
	globalCtx = tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(&answerPlatform{}))
	t.Cleanup(func() { globalCtx = previous })
	return &App{installID: 42}
}

func sendTestStreamStatus(a *App, call callRow, event string) *httptest.ResponseRecorder {
	form := url.Values{"CallSid": {call.CarrierSID}, "StreamSid": {"MZ-test"}, "StreamEvent": {event}}
	endpoint := strings.TrimPrefix(a.twilioStreamStatusURL(call.ID, call.CallbackSecret, call.ProjectID), a.publicAppURL())
	req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", twilioTestSignature(a.publicRequestURL(req), form, "test-auth-token"))
	rec := httptest.NewRecorder()
	a.handleTwilioStreamStatus(rec, req)
	return rec
}

func TestTwilioStreamNotificationsPreserveSocketOwnership(t *testing.T) {
	for _, order := range []string{"callback-first", "socket-first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			a := streamStatusTestApp(t)
			call := testCall("ownership", "answered")
			if err := a.db().insertCall(call); err != nil {
				t.Fatal(err)
			}
			callback := func() int { return sendTestStreamStatus(a, call, "stream-started").Code }
			var claimed bool
			var err error
			var code int
			switch order {
			case "callback-first":
				code = callback()
				claimed, err = a.db().claimMedia(call.ID)
			case "socket-first":
				claimed, err = a.db().claimMedia(call.ID)
				code = callback()
			case "concurrent":
				done := make(chan int, 1)
				go func() { done <- callback() }()
				claimed, err = a.db().claimMedia(call.ID)
				code = <-done
			}
			if code != 204 || err != nil || !claimed {
				t.Fatalf("status=%d claimed=%v err=%v", code, claimed, err)
			}
			before, err := a.db().findCall(call.ID)
			if err != nil {
				t.Fatal(err)
			}
			if before.MediaStatus != "connecting" || before.MediaConnectedAt != "" || before.StateExpiresAt != call.StateExpiresAt {
				t.Fatal("notification changed bridge status or setup deadline")
			}
			for _, event := range []string{"stream-started", "stream-stopped", "stream-stopped", "stream-error", "stream-started"} {
				if rec := sendTestStreamStatus(a, call, event); rec.Code != 204 {
					t.Fatalf("callback: %d %s", rec.Code, rec.Body.String())
				}
				after, err := a.db().findCall(call.ID)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("%s changed call state: err=%v", event, err)
				}
				duplicate, err := a.db().claimMedia(call.ID)
				if err != nil || duplicate {
					t.Fatalf("%s released socket ownership: claimed=%v err=%v", event, duplicate, err)
				}
			}
			if err := a.db().updateMediaStatus(call.ID, "disconnected", "", 1000, "closed"); err != nil {
				t.Fatal(err)
			}
			if duplicate, err := a.db().claimMedia(call.ID); err != nil || duplicate {
				t.Fatalf("status update released socket: %v %v", duplicate, err)
			}
			if err := a.db().releaseMedia(call.ID); err != nil {
				t.Fatal(err)
			}
			if claimed, err := a.db().claimMedia(call.ID); err != nil || !claimed {
				t.Fatalf("reconnect: %v %v", claimed, err)
			}
		})
	}
}

func TestTwilioStreamNotificationsAfterTerminalCall(t *testing.T) {
	for _, status := range []string{"completed", "failed", "no-answer", "busy", "canceled"} {
		t.Run(status, func(t *testing.T) {
			a := streamStatusTestApp(t)
			call := testCall("terminal", status)
			if err := a.db().insertCall(call); err != nil {
				t.Fatal(err)
			}
			before, err := a.db().findCall(call.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range []string{"stream-started", "stream-stopped", "stream-error"} {
				if rec := sendTestStreamStatus(a, call, event); rec.Code != 204 {
					t.Fatalf("callback status: %d", rec.Code)
				}
				after, err := a.db().findCall(call.ID)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("%s changed terminal state: %v", event, err)
				}
			}
		})
	}
}

func TestTwilioStreamNotificationOrdering(t *testing.T) {
	a := streamStatusTestApp(t)
	call := testCall("ordering", "answered")
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"stream-stopped", "stream-started", "stream-stopped", "stream-error"} {
		if rec := sendTestStreamStatus(a, call, event); rec.Code != 204 {
			t.Fatalf("callback status: %d", rec.Code)
		}
	}
	var status string
	if err := a.db().db.QueryRow(`SELECT status FROM telephony_twilio_streams WHERE call_id=?`, call.ID).Scan(&status); err != nil || status != "stream-stopped" {
		t.Fatalf("terminal provider state: %s %v", status, err)
	}
	// Another stream has its own status, but cannot affect the first stream.
	if recorded, err := a.db().recordTwilioStreamNotification(call.ID, "MZ-second", "stream-started", ""); err != nil || !recorded {
		t.Fatalf("new stream: %v %v", recorded, err)
	}
	if _, err := a.db().updateStatusWithFacts(call.ID, "completed", "", lifecycleFacts{Source: "provider"}); err != nil {
		t.Fatal(err)
	}
	if recorded, err := a.db().recordTwilioStreamNotification(call.ID, "MZ-second", "stream-error", "late"); err != nil || recorded {
		t.Fatalf("late callback persisted: %v %v", recorded, err)
	}
}

func TestTwilioConcurrentSocketClaimsWithNotifications(t *testing.T) {
	a := streamStatusTestApp(t)
	call := testCall("concurrent-sockets", "answered")
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type result struct {
		claimed bool
		err     error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; claimed, err := a.db().claimMedia(call.ID); results <- result{claimed, err} }()
	}
	callbacks := make(chan int, 1)
	go func() {
		<-start
		for _, event := range []string{"stream-started", "stream-started", "stream-error", "stream-stopped"} {
			if code := sendTestStreamStatus(a, call, event).Code; code != 204 {
				callbacks <- code
				return
			}
		}
		callbacks <- 204
	}()
	close(start)
	winners := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Error(r.err)
		}
		if r.claimed {
			winners++
		}
	}
	if code := <-callbacks; code != 204 {
		t.Errorf("callback: %d", code)
	}
	if winners != 1 {
		t.Fatalf("socket winners=%d, want one", winners)
	}
}
