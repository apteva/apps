package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const twilioAPIBase = "https://api.twilio.com/2010-04-01"

// These tests are intentionally opt-in. They talk to real Twilio APIs and the
// outbound-call test can place a real billable call.
//
// Required for the account check:
//
//	RUN_TELEPHONY_LIVE_TESTS=1
//	TWILIO_ACCOUNT_SID=AC...
//	TWILIO_AUTH_TOKEN=...
//
// Additionally required to place a real call:
//
//	RUN_TELEPHONY_LIVE_CALL=1
//	TWILIO_FROM_NUMBER=+1...
//	TWILIO_TO_NUMBER=+1...
//
// Optional:
//
//	TWILIO_LIVE_TWIML='<Response><Say>custom smoke test</Say></Response>'
func TestTwilioLiveAccount(t *testing.T) {
	accountSID, authToken := requireTwilioLiveCredentials(t)

	req, err := twilioRequest(http.MethodGet, fmt.Sprintf("/Accounts/%s.json", url.PathEscape(accountSID)), nil, accountSID, authToken)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		SID          string `json:"sid"`
		FriendlyName string `json:"friendly_name"`
		Status       string `json:"status"`
		Type         string `json:"type"`
	}
	doTwilio(t, req, http.StatusOK, &out)
	if out.SID != accountSID {
		t.Fatalf("unexpected account sid: got %q want %q", out.SID, accountSID)
	}
	if out.Status == "" {
		t.Fatal("twilio account response did not include status")
	}
	t.Logf("twilio account reachable: sid=%s status=%s type=%s friendly_name=%q", out.SID, out.Status, out.Type, out.FriendlyName)
}

func TestTwilioLiveOutboundCall(t *testing.T) {
	accountSID, authToken := requireTwilioLiveCredentials(t)
	if os.Getenv("RUN_TELEPHONY_LIVE_CALL") != "1" {
		t.Skip("set RUN_TELEPHONY_LIVE_CALL=1 to place a real Twilio outbound call")
	}

	from := strings.TrimSpace(os.Getenv("TWILIO_FROM_NUMBER"))
	to := strings.TrimSpace(os.Getenv("TWILIO_TO_NUMBER"))
	if from == "" || to == "" {
		t.Fatal("TWILIO_FROM_NUMBER and TWILIO_TO_NUMBER are required for live outbound call test")
	}
	if !strings.HasPrefix(from, "+") || !strings.HasPrefix(to, "+") {
		t.Fatal("TWILIO_FROM_NUMBER and TWILIO_TO_NUMBER must be E.164 numbers, e.g. +14155551234")
	}

	twiml := strings.TrimSpace(os.Getenv("TWILIO_LIVE_TWIML"))
	if twiml == "" {
		twiml = `<Response><Say voice="alice">Apteva telephony live smoke test.</Say><Pause length="1"/></Response>`
	}

	form := url.Values{}
	form.Set("To", to)
	form.Set("From", from)
	form.Set("Twiml", twiml)
	form.Set("Timeout", "15")

	req, err := twilioRequest(http.MethodPost, fmt.Sprintf("/Accounts/%s/Calls.json", url.PathEscape(accountSID)), strings.NewReader(form.Encode()), accountSID, authToken)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var created struct {
		SID    string `json:"sid"`
		Status string `json:"status"`
		To     string `json:"to"`
		From   string `json:"from"`
	}
	doTwilio(t, req, http.StatusCreated, &created)
	if created.SID == "" {
		t.Fatal("twilio call response did not include sid")
	}
	t.Logf("twilio call created: sid=%s status=%s from=%s to=%s", created.SID, created.Status, created.From, created.To)

	time.Sleep(2 * time.Second)
	completeTwilioCall(t, accountSID, authToken, created.SID)
}

func requireTwilioLiveCredentials(t *testing.T) (string, string) {
	t.Helper()
	if os.Getenv("RUN_TELEPHONY_LIVE_TESTS") != "1" {
		t.Skip("set RUN_TELEPHONY_LIVE_TESTS=1 to run Twilio live tests")
	}
	accountSID := strings.TrimSpace(os.Getenv("TWILIO_ACCOUNT_SID"))
	authToken := strings.TrimSpace(os.Getenv("TWILIO_AUTH_TOKEN"))
	if accountSID == "" || authToken == "" {
		t.Fatal("TWILIO_ACCOUNT_SID and TWILIO_AUTH_TOKEN are required")
	}
	return accountSID, authToken
}

func completeTwilioCall(t *testing.T, accountSID, authToken, callSID string) {
	t.Helper()
	form := url.Values{}
	form.Set("Status", "completed")

	req, err := twilioRequest(http.MethodPost, fmt.Sprintf("/Accounts/%s/Calls/%s.json", url.PathEscape(accountSID), url.PathEscape(callSID)), strings.NewReader(form.Encode()), accountSID, authToken)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var out struct {
		SID    string `json:"sid"`
		Status string `json:"status"`
	}
	doTwilio(t, req, http.StatusOK, &out)
	t.Logf("twilio call cleanup requested: sid=%s status=%s", out.SID, out.Status)
}

func twilioRequest(method, path string, body io.Reader, accountSID, authToken string) (*http.Request, error) {
	req, err := http.NewRequest(method, twilioAPIBase+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(accountSID, authToken)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func doTwilio(t *testing.T, req *http.Request, wantStatus int, out any) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("twilio %s %s: status=%d want=%d body=%s", req.Method, req.URL.Path, resp.StatusCode, wantStatus, string(data))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decode twilio response: %v body=%s", err, string(data))
		}
	}
}
