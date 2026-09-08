package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPasswordQueueWaitCancelAndSaturation(t *testing.T) {
	gate := newPasswordGate(1, 1, time.Second)
	if err := gate.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gate.acquire(waiting) }()
	deadline := time.After(time.Second)
	for len(gate.admitted) < 2 {
		select {
		case <-deadline:
			t.Fatal("attempt never queued")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case err := <-done:
		t.Fatalf("queued request returned early: %v", err)
	default:
	}
	if err := gate.acquire(context.Background()); !errors.Is(err, errPasswordBusy) {
		t.Fatalf("unbounded admission: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(gate.admitted) != 1 || len(gate.active) != 1 {
		t.Fatal("canceled waiter retained capacity")
	}
	resumed := make(chan error, 1)
	go func() { resumed <- gate.acquire(context.Background()) }()
	gate.release()
	if err := <-resumed; err != nil {
		t.Fatal(err)
	}
	gate.release()
	if len(gate.active) != 0 || len(gate.admitted) != 0 {
		t.Fatal("capacity leaked")
	}
}
func TestPasswordQueueBoundedWait(t *testing.T) {
	gate := newPasswordGate(1, 1, 20*time.Millisecond)
	if err := gate.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer gate.release()
	if err := gate.acquire(context.Background()); !errors.Is(err, errPasswordWaitExpired) {
		t.Fatal(err)
	}
	if len(gate.admitted) != 1 {
		t.Fatal("timeout leaked admission")
	}
}
func TestLoginQueueSaturationIsRetryableAndNotCredentialFailure(t *testing.T) {
	_, clientID := newAuthCtx(t)
	app := &App{}
	signup := callJSON(app.handleSignup, "POST", "/signup", map[string]any{"email": "full@example.com", "password": "GoodPassword123", "client_id": clientID})
	if signup.Code != 201 {
		t.Fatalf("signup: %d", signup.Code)
	}
	// Reserve all capacity without running Argon2: deterministic admission test.
	for i := 0; i < cap(passwordWork.admitted); i++ {
		passwordWork.admitted <- struct{}{}
	}
	defer func() {
		for len(passwordWork.admitted) > 0 {
			<-passwordWork.admitted
		}
	}()
	for _, email := range []string{"full@example.com", "missing@example.com"} {
		rec := callJSON(app.handleLogin, "POST", "/login", map[string]any{"email": email, "password": "GoodPassword123", "client_id": clientID})
		if rec.Code != 503 || rec.Header().Get("Retry-After") == "" || strings.Contains(rec.Body.String(), "invalid_grant") {
			t.Fatalf("queue exhaustion: %d %s", rec.Code, rec.Body.String())
		}
	}
}
func TestCanceledLoginDoesNotCreateSession(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}
	signup := callJSON(app.handleSignup, "POST", "/signup", map[string]any{"email": "cancel@example.com", "password": "GoodPassword123", "client_id": clientID})
	if signup.Code != 201 {
		t.Fatal(signup.Code)
	}
	var before, after int
	if err := ctx.AppDB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&before); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"email":"cancel@example.com","password":"GoodPassword123","client_id":"`+clientID+`"}`)).WithContext(canceled)
	recorder := httptest.NewRecorder()
	app.handleLogin(recorder, request)
	if err := ctx.AppDB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after || recorder.Body.Len() != 0 {
		t.Fatal("canceled login created a session or credential error")
	}
}
