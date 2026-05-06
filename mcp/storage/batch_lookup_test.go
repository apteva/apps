package main

// httpListOrSearch's ?ids= filter — used by the media app's
// enrichment helper to resolve URLs + metadata for a batch of
// file ids in one round-trip. Missing ids return as gaps, not
// errors. >500 ids → 400 (caller chunks).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListByIDs_ReturnsRequestedRows(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	ctx := newTestCtx(t)
	a := mustUpload(t, ctx, "a.txt", "/", "A")
	b := mustUpload(t, ctx, "b.txt", "/", "B")
	mustUpload(t, ctx, "c.txt", "/", "C") // not requested

	app := &App{}
	url := "/files?project_id=test-proj&ids=" + intToString(a.ID) + "," + intToString(b.ID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	app.httpListOrSearch(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct{ Files []*File }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Files) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(body.Files), body.Files)
	}
	// URLs are absolute (the integration's whole point).
	for _, f := range body.Files {
		if !strings.HasPrefix(f.URL, "https://agents.example.com/api/apps/storage/files/") {
			t.Errorf("file %d: url %q not absolute", f.ID, f.URL)
		}
	}
}

func TestListByIDs_SilentlyDropsMissing(t *testing.T) {
	ctx := newTestCtx(t)
	a := mustUpload(t, ctx, "a.txt", "/", "A")

	app := &App{}
	// 999999 doesn't exist; the request still succeeds with the one row.
	url := "/files?project_id=test-proj&ids=" + intToString(a.ID) + ",999999"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	app.httpListOrSearch(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct{ Files []*File }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Files) != 1 || body.Files[0].ID != a.ID {
		t.Fatalf("want only the existing row, got %+v", body.Files)
	}
}

func TestListByIDs_EmptyIDsParsedSafely(t *testing.T) {
	// Trailing/inner commas, whitespace, garbage — all silently dropped.
	got := parseIDList(" 1,, ,2 ,abc, ,3,")
	want := []int64{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%d, want %d", i, got[i], want[i])
		}
	}
}

func TestListByIDs_OverLimitRejected(t *testing.T) {
	_ = newTestCtx(t)
	app := &App{}
	// Build 501 ids.
	var sb strings.Builder
	for i := 1; i <= 501; i++ {
		if i > 1 {
			sb.WriteByte(',')
		}
		sb.WriteString(intToString(int64(i)))
	}
	url := "/files?project_id=test-proj&ids=" + sb.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	app.httpListOrSearch(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400 over the cap, got %d", rec.Code)
	}
}
