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

func TestListByIDs_PreservesOrderAndDuplicates(t *testing.T) {
	ctx := newTestCtx(t)
	a := mustUpload(t, ctx, "a.txt", "/", "A")
	b := mustUpload(t, ctx, "b.txt", "/", "B")
	out, err := dbGetByIDs(ctx.AppDB(), "test-proj", []int64{b.ID, a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[0].ID != b.ID || out[1].ID != a.ID || out[2].ID != b.ID {
		t.Fatalf("batch order changed: got ids [%d %d %d]", out[0].ID, out[1].ID, out[2].ID)
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

func TestHTTPList_PaginatesCompleteStableInventory(t *testing.T) {
	ctx := newTestCtx(t)
	var uploaded []int64
	for i := 0; i < 7; i++ {
		f := mustUpload(t, ctx, "page-"+intToString(int64(i))+".txt", "/", string(rune('a'+i)))
		uploaded = append(uploaded, f.ID)
	}

	app := &App{}
	var got []int64
	for offset := 0; ; offset += 3 {
		req := httptest.NewRequest(http.MethodGet,
			"/files?project_id=test-proj&limit=3&offset="+intToString(int64(offset)), nil)
		rec := httptest.NewRecorder()
		app.httpListOrSearch(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("offset %d: status=%d body=%s", offset, rec.Code, rec.Body.String())
		}
		var body struct {
			Files      []*File `json:"files"`
			Offset     int     `json:"offset"`
			NextOffset int     `json:"next_offset"`
			HasMore    bool    `json:"has_more"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("offset %d: decode: %v", offset, err)
		}
		if body.Offset != offset || body.NextOffset != offset+len(body.Files) {
			t.Fatalf("offset metadata = (%d,%d), want (%d,%d)",
				body.Offset, body.NextOffset, offset, offset+len(body.Files))
		}
		for _, f := range body.Files {
			got = append(got, f.ID)
		}
		if len(body.Files) < 3 {
			if body.HasMore {
				t.Fatalf("short final page incorrectly reports has_more")
			}
			break
		}
	}

	if len(got) != len(uploaded) {
		t.Fatalf("paginated inventory returned %d ids, want %d: %v", len(got), len(uploaded), got)
	}
	seen := map[int64]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("duplicate id %d across pages: %v", id, got)
		}
		seen[id] = true
	}
	for _, id := range uploaded {
		if !seen[id] {
			t.Fatalf("uploaded id %d missing from paginated inventory: %v", id, got)
		}
	}
}
