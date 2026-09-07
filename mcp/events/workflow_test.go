package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func openWorkflowEvent(t *testing.T, slug string) *Event {
	t.Helper()
	e := testEvent(t, map[string]any{"slug": slug, "status": "published", "visibility": "public", "starts_at": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339), "ends_at": time.Now().Add(50 * time.Hour).UTC().Format(time.RFC3339)})
	if _, err := saveSettings(e.ID, map[string]any{"applications_open": true, "performer_capacity": 2}); err != nil {
		t.Fatal(err)
	}
	return e
}
func TestApplicationLifecycleAndDedupe(t *testing.T) {
	setupEvents(t)
	e := openWorkflowEvent(t, "show-one")
	second := openWorkflowEvent(t, "show-two")
	input := map[string]any{"applicant_name": "Synthetic Artist", "instagram": "@test.artist", "phone": "+34 600 000 001"}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- submitPublicApplication(e, input) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	apps, err := listApplications(e.ID, "")
	if err != nil || len(apps) != 1 {
		t.Fatalf("retry duplicated: %#v %v", apps, err)
	}
	if err = submitPublicApplication(second, input); err != nil {
		t.Fatal(err)
	}
	if rows, _ := listApplications(second.ID, ""); len(rows) != 1 {
		t.Fatal("same artist must be able to apply to another event")
	}
	for _, settings := range []map[string]any{{"applications_open": false}, {"applications_open": true, "opens_at": time.Now().Add(time.Hour).Format(time.RFC3339)}, {"opens_at": "", "closes_at": time.Now().Add(-time.Hour).Format(time.RFC3339)}, {"closes_at": "", "cancelled": true}} {
		if _, err := saveSettings(e.ID, settings); err != nil {
			t.Fatal(err)
		}
		if err := submitPublicApplication(e, map[string]any{"applicant_name": "Another", "instagram": "other_artist"}); err == nil {
			t.Fatalf("accepted closed form: %#v", settings)
		}
	}
	if w := publicRequest("GET", "/public/"+e.Slug+"?format=json", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancelled shared link must remain informative: %s", w.Body.String())
	}
	if _, err = saveSettings(second.ID, map[string]any{"email_required": true}); err != nil {
		t.Fatal(err)
	}
	if err = submitPublicApplication(second, map[string]any{"applicant_name": "Another", "instagram": "another"}); err == nil {
		t.Fatal("required email ignored")
	}
	if _, err = updateEvent(second.ID, map[string]any{"status": "closed"}); err != nil {
		t.Fatal(err)
	}
	if w := publicRequest("GET", "/public/"+second.Slug+"?format=json", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"can_apply":false`) {
		t.Fatal("closed shared link missing")
	}
}
func TestPrivatePhotoAndLineupPublication(t *testing.T) {
	setupEvents(t)
	e := openWorkflowEvent(t, "photos")
	photo := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jgXkAAAAASUVORK5CYII="
	in := map[string]any{"applicant_name": "Private Real Name", "stage_name": "Stage Name", "email": "private@example.com", "photo_base64": photo, "photo_consent": false}
	if err := submitPublicApplication(e, in); err == nil {
		t.Fatal("photo without consent accepted")
	}
	in["photo_consent"] = true
	if err := submitPublicApplication(e, in); err != nil {
		t.Fatal(err)
	}
	rows, _ := listApplications(e.ID, "")
	app := rows[0]
	if _, err := reviewApplication(app.ID, map[string]any{"reviewer_notes": "SECRET_REVIEW"}); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduleApplication(map[string]any{"application_id": app.ID}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/public/%s/photos/%d", e.Slug, app.ID)
	if w := publicRequest("GET", path, ""); w.Code != 404 {
		t.Fatal("unpublished photo was exposed")
	}
	page, err := makePublicPage(e)
	if err != nil || len(page.Schedule) != 0 {
		t.Fatal("unpublished lineup exposed")
	}
	if _, err := saveSettings(e.ID, map[string]any{"lineup_published": true}); err != nil {
		t.Fatal(err)
	}
	w := publicRequest("GET", "/public/photos?format=json", "")
	if !strings.Contains(w.Body.String(), "Stage Name") {
		t.Fatal("published performer missing")
	}
	for _, private := range []string{"private@example.com", "Private Real Name", "SECRET_REVIEW", "photo_base64"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatalf("leaked %s", private)
		}
	}
	if w := publicRequest("GET", path, ""); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatal("approved photo unavailable")
	}
	if _, err := reviewApplication(app.ID, map[string]any{"status": "rejected"}); err != nil {
		t.Fatal(err)
	}
	if w := publicRequest("GET", path, ""); w.Code != 404 {
		t.Fatal("rejected artist photo exposed")
	}
}
func TestScheduleAtomicCapacityAndOverlap(t *testing.T) {
	setupEvents(t)
	e := openWorkflowEvent(t, "slots")
	saveSettings(e.ID, map[string]any{"performer_capacity": 1})
	one, _ := submitApplication(map[string]any{"event_id": e.ID, "applicant_name": "One", "email": "one@example.com"})
	two, _ := submitApplication(map[string]any{"event_id": e.ID, "applicant_name": "Two", "email": "two@example.com"})
	start, _ := time.Parse(time.RFC3339, e.StartsAt)
	end := start.Add(5 * time.Minute).Format(time.RFC3339)
	slot, err := scheduleApplication(map[string]any{"application_id": one.ID, "starts_at": e.StartsAt, "ends_at": end})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = scheduleApplication(map[string]any{"application_id": two.ID, "starts_at": end, "ends_at": start.Add(10 * time.Minute).Format(time.RFC3339)}); err == nil {
		t.Fatal("overfilled performer spots")
	}
	unchanged, _ := getApplication(two.ID)
	if unchanged.Status != "submitted" {
		t.Fatal("failed scheduling accepted artist")
	}
	saveSettings(e.ID, map[string]any{"performer_capacity": 2})
	if _, err = scheduleApplication(map[string]any{"application_id": two.ID, "starts_at": e.StartsAt, "ends_at": end}); err == nil {
		t.Fatal("overlap allowed")
	}
	if _, err = scheduleApplication(map[string]any{"application_id": one.ID}); err == nil {
		t.Fatal("duplicate lineup entry")
	}
	other := openWorkflowEvent(t, "other-slots")
	if _, err = createSlot(map[string]any{"event_id": other.ID, "application_id": one.ID, "performer_name": "Wrong show"}); err == nil {
		t.Fatal("cross-event application accepted")
	}
	req := httptest.NewRequest("DELETE", fmt.Sprintf("/slots/%d", slot.ID), nil)
	w := httptest.NewRecorder()
	(&App{}).handleSlotItem(w, req)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if rows, _ := listSlots(e.ID); len(rows) != 0 {
		t.Fatal("slot not removed")
	}
	if _, err := getApplication(one.ID); err != nil {
		t.Fatal("slot removal deleted application")
	}
}
func TestPermanentLinksAndCleanDraftDuplicate(t *testing.T) {
	setupEvents(t)
	e := openWorkflowEvent(t, "permanent")
	if _, err := updateEvent(e.ID, map[string]any{"title": "Rescheduled title", "starts_at": time.Now().Add(49 * time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if _, err := getPublicEvent("permanent"); err != nil {
		t.Fatal("title or date broke shared link")
	}
	if _, err := updateEvent(e.ID, map[string]any{"slug": "changed"}); err == nil {
		t.Fatal("existing link changed")
	}
	submitApplication(map[string]any{"event_id": e.ID, "applicant_name": "Artist", "email": "artist@example.com"})
	saveSettings(e.ID, map[string]any{"lineup_published": true})
	r := httptest.NewRequest("POST", fmt.Sprintf("/shows/%d/duplicate", e.ID), nil)
	w := httptest.NewRecorder()
	(&App{}).handleEventsItem(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var copied Event
	if err := json.Unmarshal(w.Body.Bytes(), &copied); err != nil {
		t.Fatal(err)
	}
	if copied.Status != "draft" || copied.StartsAt != "" || copied.Slug == e.Slug || copied.ApplicationCount != 0 {
		t.Fatalf("unsafe duplicate: %+v", copied)
	}
	settings, _ := settingsFrom(db(), copied.ID)
	if settings.ApplicationsOpen || settings.LineupPublished || settings.ClosesAt != "" {
		t.Fatal("duplicate inherited publication or windows")
	}
	if w := publicRequest("GET", "/public/", ""); strings.Contains(w.Body.String(), copied.Slug) {
		t.Fatal("draft listed publicly")
	}
}
func TestPublicPhotoValidationAndProjectIsolation(t *testing.T) {
	setupEvents(t)
	e := openWorkflowEvent(t, "isolated")
	for _, in := range []map[string]any{{"applicant_name": "Artist"}, {"applicant_name": "Artist", "email": "bad"}, {"applicant_name": "Artist", "instagram": "https://instagram.com/test"}, {"applicant_name": "Artist", "email": "ok@example.com", "photo_base64": "not-base64", "photo_consent": true}} {
		if err := submitPublicApplication(e, in); err == nil {
			t.Fatalf("invalid accepted: %v", in)
		}
	}
	t.Setenv("APTEVA_PROJECT_ID", "other-project")
	if w := publicRequest("GET", "/public/isolated", ""); w.Code != 404 {
		t.Fatal("cross-project public access")
	}
	w := httptest.NewRecorder()
	(&App{}).handleEventsItem(w, httptest.NewRequest("GET", fmt.Sprintf("/shows/%d/settings", e.ID), nil))
	if w.Code != 404 {
		t.Fatal("cross-project settings access")
	}
}
