package main

// httpUpload — JSON-body fallback regression.
//
// httpUpload accepts two body shapes:
//   - multipart/form-data (the dashboard's drag-drop path)
//   - JSON with content_base64 (sibling-app uploads like media's
//     thumbnails/waveforms, where building a multipart envelope for an
//     in-memory byte slice is pointless)
//
// The bug: the JSON-body fallback used to forward the body straight
// to toolUpload(ctx, body), but toolUpload calls
// resolveProjectFromArgs(args) which looks at args["_project_id"]. The
// query string's project_id had already been resolved into the local
// `pid` variable, but never threaded into the JSON body. Result for
// global-scope storage installs: a confusing
// "project_id missing — pass _project_id when scope=global" error
// even though the caller did pass project_id (just in the URL, where
// resolveProjectFromRequest reads it).
//
// Symptom in prod: every media derivation upload (.media/thumbnail.jpg,
// .media/waveform.png) failed with HTTP 400 once storage was switched
// to a global install. The dashboard kept working because it uses the
// multipart path.

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestHTTPUpload_JSONBodyHonoursQueryStringProjectID(t *testing.T) {
	ctx := newTestCtx(t)
	// Project-scoped ctx (test-proj). Force the upload through the
	// "global scope" code path by clearing APTEVA_PROJECT_ID env so
	// resolveProjectFromArgs has nothing to fall back on — only the
	// _project_id in args (which httpUpload must thread from the
	// query string) can satisfy it.
	t.Setenv("APTEVA_PROJECT_ID", "")
	_ = ctx

	app := &App{}
	body := map[string]any{
		"name":           "thumb.jpg",
		"folder":         "/.media/thumb/",
		"content_type":   "image/jpeg",
		"content_base64": b64("fake jpeg bytes"),
		"visibility":     "private",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(
		http.MethodPost,
		"/files?project_id=p-from-query",
		bytes.NewReader(bodyBytes),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.httpUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["id"]; !ok {
		t.Errorf("expected response.id, got %v", resp)
	}
	if resp["name"] != "thumb.jpg" {
		t.Errorf("name = %v, want thumb.jpg", resp["name"])
	}
}

func TestHTTPUpload_JSONBodyRejectedWithoutQueryProject(t *testing.T) {
	newTestCtx(t)
	t.Setenv("APTEVA_PROJECT_ID", "")
	app := &App{}
	body := map[string]any{"name": "x.txt", "content_base64": b64("x")}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/files", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.httpUpload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without project_id in query, got %d", rec.Code)
	}
}

func TestHTTPUpload_MultipartPersistsSourceAndCommaTags(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("folder", "/.media/transcript-audio/")
	_ = mw.WriteField("visibility", "private")
	_ = mw.WriteField("source", "media-transcript-audio")
	_ = mw.WriteField("tags", "internal,transcript-audio")
	part, err := mw.CreateFormFile("file", "3453.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("fake mp3 bytes")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/files?project_id=test-proj", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	app.httpUpload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	id := int64(resp["id"].(float64))
	got, err := dbGetByID(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "media-transcript-audio" {
		t.Fatalf("source = %q, want media-transcript-audio", got.Source)
	}
	wantTags := map[string]bool{"internal": true, "transcript-audio": true}
	for _, tag := range got.Tags {
		delete(wantTags, tag)
	}
	if len(wantTags) != 0 {
		t.Fatalf("tags = %#v, missing %#v", got.Tags, wantTags)
	}
}

func TestHTTPUpload_RejectsMultipartOverMaxWithoutTruncating(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"max_upload_size_mb": "1"}))
	app := &App{}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("folder", "/renders/")
	_ = mw.WriteField("source", "media-render")
	_ = mw.WriteField("tags", "render")
	part, err := mw.CreateFormFile("file", "too-big.mov")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("x"), 1024*1024+1)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/files?project_id=test-proj", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	app.httpUpload(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM files`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("oversized upload inserted %d file rows", count)
	}
}
