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
	"net/http"
	"net/http/httptest"
	"testing"
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
