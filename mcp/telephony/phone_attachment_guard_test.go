package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGuardedAttachmentRetriesCannotReplaceNewerSession(t *testing.T) {
	a, row, _, _ := listenerFixture(t)
	alice := phoneTestIdentity("alice")
	p, e := a.phonePrincipal(row.ProjectID, alice)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.setPhoneOwner(&row, p, "sales"); e != nil {
		t.Fatal(e)
	}
	first, e := a.issuePhoneSession(&row, p)
	if e != nil {
		t.Fatal(e)
	}
	guard := &phoneAttachmentGuard{ID: "attempt-a", Expected: first.SessionGeneration}
	fresh, e := a.issueGuardedPhoneSession(context.Background(), &row, p, audioSessionCorrelation{AttemptID: guard.ID}, guard)
	if e != nil {
		t.Fatal(e)
	}
	retry, e := a.issueGuardedPhoneSession(context.Background(), &row, p, audioSessionCorrelation{AttemptID: guard.ID}, guard)
	if e != nil || retry.SessionToken != fresh.SessionToken || retry.SessionGeneration != fresh.SessionGeneration {
		t.Fatal("unknown response retry rotated token", e)
	}
	if retry.LeaseSeconds > fresh.LeaseSeconds {
		t.Fatal("retry extended lease")
	}
	newer, e := a.issuePhoneSession(&row, p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.issueGuardedPhoneSession(context.Background(), &row, p, audioSessionCorrelation{}, guard); !errors.Is(e, errPhoneSessionChanged) {
		t.Fatal("old retry replaced newer session", e)
	}
	guard.ID = "attempt-b"
	if _, e = a.issueGuardedPhoneSession(context.Background(), &row, p, audioSessionCorrelation{}, guard); !errors.Is(e, errPhoneSessionChanged) {
		t.Fatal("stale new attempt replaced current session", e)
	}
	if !a.validPhoneMedia(&row, newer.SessionToken) {
		t.Fatal("newer session lost")
	}
	guard.Expected = newer.SessionGeneration
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = a.issueGuardedPhoneSession(ctx, &row, p, audioSessionCorrelation{}, guard); e == nil {
		t.Fatal("cancelled issuance accepted")
	}
	if _, e = a.db().db.Exec(`DELETE FROM telephony_media_sessions WHERE call_id=?`, row.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = a.issuePhoneSessionConditional(&row, p, guard); !errors.Is(e, errPhoneSessionChanged) {
		t.Fatal("missing row bypassed condition", e)
	}
}
func TestGuardedAttachmentHTTPAndRevocation(t *testing.T) {
	a, row, _, policy := listenerFixture(t)
	alice := phoneTestIdentity("alice")
	p, _ := a.phonePrincipal(row.ProjectID, alice)
	_ = a.setPhoneOwner(&row, p, "sales")
	first, e := a.issuePhoneSession(&row, p)
	if e != nil {
		t.Fatal(e)
	}
	body := map[string]any{"attachment_guard": phoneAttachmentGuard{ID: "attempt-a", Expected: first.SessionGeneration}}
	r := phoneTestRequest(a, &alice, http.MethodPost, "/softphone/attach/"+row.ID, body)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var fresh softphoneSession
	_ = json.Unmarshal(r.Body.Bytes(), &fresh)
	r = phoneTestRequest(a, &alice, http.MethodPost, "/softphone/attach/"+row.ID, body)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var retry softphoneSession
	_ = json.Unmarshal(r.Body.Bytes(), &retry)
	if fresh.SessionToken != retry.SessionToken {
		t.Fatal("HTTP retry rotated")
	}
	_, e = a.db().db.Exec(`UPDATE telephony_media_sessions SET expires_at=? WHERE call_id=?`, time.Now().Unix()-1, row.ID)
	if e != nil {
		t.Fatal(e)
	}
	r = phoneTestRequest(a, &alice, http.MethodPost, "/softphone/attach/"+row.ID, body)
	if r.Code != 200 {
		t.Fatal("freshly authorized expired recovery denied", r.Code)
	}
	var resumed softphoneSession
	_ = json.Unmarshal(r.Body.Bytes(), &resumed)
	if resumed.SessionGeneration == fresh.SessionGeneration || resumed.SessionToken == fresh.SessionToken {
		t.Fatal("expired credential reused")
	}
	for i := range policy.Users {
		if policy.Users[i].Identity == alice {
			policy.Users[i].Enabled = false
		}
	}
	r = phoneTestRequest(a, nil, http.MethodPut, "/access/policy", policy)
	if r.Code != 200 {
		t.Fatal(r.Body)
	}
	r = phoneTestRequest(a, &alice, http.MethodPost, "/softphone/attach/"+row.ID, body)
	if r.Code != 403 {
		t.Fatal("revoked cache reused", r.Code)
	}
}

func TestGuardedAttachmentConcurrentHTTPRefresh(t *testing.T) {
	a, row, _, _ := listenerFixture(t)
	alice := phoneTestIdentity("alice")
	p, _ := a.phonePrincipal(row.ProjectID, alice)
	_ = a.setPhoneOwner(&row, p, "sales")
	first, e := a.issuePhoneSession(&row, p)
	if e != nil {
		t.Fatal(e)
	}
	body := map[string]any{"attachment_guard": phoneAttachmentGuard{ID: "attempt-a", Expected: first.SessionGeneration}}
	results := make(chan *httptest.ResponseRecorder, 12)
	for i := 0; i < 12; i++ {
		go func() { results <- phoneTestRequest(a, &alice, http.MethodPost, "/softphone/attach/"+row.ID, body) }()
	}
	var current softphoneSession
	for i := 0; i < 12; i++ {
		r := <-results
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		var v softphoneSession
		_ = json.Unmarshal(r.Body.Bytes(), &v)
		if i == 0 {
			current = v
		} else if v.SessionToken != current.SessionToken || v.SessionGeneration != current.SessionGeneration {
			t.Fatal("concurrent refresh rotated sessions")
		}
	}
	if !a.validPhoneMedia(&row, current.SessionToken) {
		t.Fatal("concurrent refresh invalidated current media")
	}
}
func TestGuardedFirstLegacyAttachmentIsConditional(t *testing.T) {
	a, row, _, _ := listenerFixture(t)
	g := &phoneAttachmentGuard{ID: "attempt-a", Expected: ""}
	s, e := a.issueGuardedPhoneSession(context.Background(), &row, nil, audioSessionCorrelation{}, g)
	if e != nil {
		t.Fatal(e)
	}
	retry, e := a.issueGuardedPhoneSession(context.Background(), &row, nil, audioSessionCorrelation{}, g)
	if e != nil || retry.SessionToken != s.SessionToken {
		t.Fatal("first guarded retry changed token", e)
	}
	other := &phoneAttachmentGuard{ID: "attempt-b", Expected: ""}
	if _, e = a.issueGuardedPhoneSession(context.Background(), &row, nil, audioSessionCorrelation{}, other); !errors.Is(e, errPhoneSessionChanged) {
		t.Fatal("first attachment replaced existing generation", e)
	}
}
