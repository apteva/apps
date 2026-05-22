package main

// Image-source coverage for the shape-preserving render ops (crop,
// resize). The bug these guard against: an image source with no
// explicit output_name used to default to a ".mp4" output (video
// content-type, no still-image flags), producing a broken file. The
// planner now preserves the source's media type via the sourceExt hint.

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestPlanCrop_ImageSourcePreservesType(t *testing.T) {
	plan, err := buildPlan("crop", []string{"42"},
		raw(t, map[string]any{"x": 0, "y": 0, "width": 100, "height": 100}), "", ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Ext(plan.Filename), ".jpg"; got != want {
		t.Errorf("filename=%q, want %s extension", plan.Filename, want)
	}
	if plan.ContentType != "image/jpeg" {
		t.Errorf("content_type=%q want image/jpeg", plan.ContentType)
	}
	// Single-frame muxing + jpeg quality, and crucially NO audio copy
	// (images carry no audio stream).
	if !argPair(plan.Args, "-frames:v", "1") {
		t.Errorf("image output must pin -frames:v 1: %v", plan.Args)
	}
	if !argPair(plan.Args, "-q:v", "3") {
		t.Errorf("jpeg output should set -q:v 3: %v", plan.Args)
	}
	if contains(plan.Args, "-c:a") {
		t.Errorf("image output must not emit -c:a copy: %v", plan.Args)
	}
}

func TestPlanResize_ImageSourcePreservesType(t *testing.T) {
	plan, err := buildPlan("resize", []string{"42"},
		raw(t, map[string]any{"width": 640, "keep_aspect": true}), "", ".png")
	if err != nil {
		t.Fatal(err)
	}
	if plan.ContentType != "image/png" {
		t.Errorf("content_type=%q want image/png", plan.ContentType)
	}
	if !argPair(plan.Args, "-frames:v", "1") {
		t.Errorf("image output must pin -frames:v 1: %v", plan.Args)
	}
	// -q:v is jpeg-only; png must not carry it.
	if contains(plan.Args, "-q:v") {
		t.Errorf("png output should not set -q:v: %v", plan.Args)
	}
	if contains(plan.Args, "-c:a") {
		t.Errorf("image output must not emit -c:a copy: %v", plan.Args)
	}
	// The geometric filter still has to be there.
	if !contains(plan.Args, "scale=640:-2") {
		t.Errorf("missing scale filter: %v", plan.Args)
	}
}

func TestPlanCrop_VideoSourceUnchanged(t *testing.T) {
	// sourceExt "" reproduces the legacy path: video container default
	// + audio passthrough. This is the back-compat guard.
	plan, err := buildPlan("crop", []string{"42"},
		raw(t, map[string]any{"x": 0, "y": 0, "width": 100, "height": 100}), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.ContentType != "video/mp4" {
		t.Errorf("content_type=%q want video/mp4 (legacy default)", plan.ContentType)
	}
	if !argPair(plan.Args, "-c:a", "copy") {
		t.Errorf("video output must pass audio through with -c:a copy: %v", plan.Args)
	}
	if contains(plan.Args, "-frames:v") {
		t.Errorf("video output must not pin -frames:v: %v", plan.Args)
	}
}

func TestPlanCrop_ExplicitImageNameOverridesSourceExt(t *testing.T) {
	// An explicit image output_name wins even when the source ext is
	// unknown — the muxer/content-type follow the requested extension.
	plan, err := buildPlan("crop", []string{"42"},
		raw(t, map[string]any{"x": 0, "y": 0, "width": 50, "height": 50}), "cropped.webp", "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Filename != "cropped.webp" {
		t.Errorf("explicit name lost: %q", plan.Filename)
	}
	if plan.ContentType != "image/webp" {
		t.Errorf("content_type=%q want image/webp", plan.ContentType)
	}
	if !argPair(plan.Args, "-frames:v", "1") {
		t.Errorf("webp output must pin -frames:v 1: %v", plan.Args)
	}
	if contains(plan.Args, "-c:a") {
		t.Errorf("image output must not emit -c:a copy: %v", plan.Args)
	}
}

func TestContentTypeForName_ImageTypes(t *testing.T) {
	cases := map[string]string{
		"a.gif":  "image/gif",
		"a.webp": "image/webp",
		"a.bmp":  "image/bmp",
		"a.tif":  "image/tiff",
		"a.tiff": "image/tiff",
		"a.heic": "image/heic",
		"a.heif": "image/heic",
		"a.JPG":  "image/jpeg", // case-insensitive
	}
	for name, want := range cases {
		if got := contentTypeForName(name); got != want {
			t.Errorf("contentTypeForName(%q)=%q want %q", name, got, want)
		}
	}
}

func TestIsImageExt(t *testing.T) {
	for _, ext := range []string{".jpg", ".JPEG", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".heic", ".heif"} {
		if !isImageExt(ext) {
			t.Errorf("isImageExt(%q) = false, want true", ext)
		}
	}
	for _, ext := range []string{".mp4", ".mov", ".mp3", ".wav", "", ".txt"} {
		if isImageExt(ext) {
			t.Errorf("isImageExt(%q) = true, want false", ext)
		}
	}
}

func TestLookupSourceExt(t *testing.T) {
	db := newMediaNameTestDB(t)
	defer db.Close()

	if got := lookupSourceExt(db, "p1", []string{"7"}); got != ".jpg" {
		t.Errorf("lookupSourceExt(p1,7)=%q want .jpg", got)
	}
	// Mixed case in the stored name normalises to lowercase.
	if got := lookupSourceExt(db, "p1", []string{"8"}); got != ".png" {
		t.Errorf("lookupSourceExt(p1,8)=%q want .png", got)
	}
	// Missing row → "".
	if got := lookupSourceExt(db, "p1", []string{"999"}); got != "" {
		t.Errorf("missing row should yield \"\", got %q", got)
	}
	// Multiple sources (concat) → "" by contract.
	if got := lookupSourceExt(db, "p1", []string{"7", "8"}); got != "" {
		t.Errorf("multi-source should yield \"\", got %q", got)
	}
	// Empty project → "".
	if got := lookupSourceExt(db, "", []string{"7"}); got != "" {
		t.Errorf("empty project should yield \"\", got %q", got)
	}
}

func TestExtFromContentType(t *testing.T) {
	cases := map[string]string{
		"image/png":                ".png",
		"image/jpeg":               ".jpg",
		"image/gif":                ".gif",
		"image/webp":               ".webp",
		"video/mp4":                ".mp4",
		"video/quicktime":          ".mov",
		"audio/mpeg":               ".mp3",
		"IMAGE/PNG":                ".png", // case-insensitive
		"image/png; charset=utf8":  ".png", // params stripped
		"application/octet-stream": "",
		"":                         "",
	}
	for ct, want := range cases {
		if got := extFromContentType(ct); got != want {
			t.Errorf("extFromContentType(%q)=%q want %q", ct, got, want)
		}
	}
}

// resolveSourceExt must fall back to storage for files media never
// indexed — the screenshots-in-/.screenshots/ case. Without this a
// crop of an un-cataloged image silently produced a .mp4.
func TestResolveSourceExt_StorageFallbackByName(t *testing.T) {
	db := newMediaNameTestDB(t) // has rows 7,8 only — NOT 136/200
	defer db.Close()
	sc := newStorageStubClient(t)

	// 136: name carries .png → use the name's extension.
	if got := resolveSourceExt(context.Background(), sc, db, "p1", []string{"136"}); got != ".png" {
		t.Errorf("resolveSourceExt(136)=%q want .png (from name)", got)
	}
	// 200: UUID name with no extension → fall back to content_type.
	if got := resolveSourceExt(context.Background(), sc, db, "p1", []string{"200"}); got != ".png" {
		t.Errorf("resolveSourceExt(200)=%q want .png (from content_type)", got)
	}
}

func TestResolveSourceExt_PrefersMediaIndex(t *testing.T) {
	db := newMediaNameTestDB(t) // row 7 = vacation.jpg
	defer db.Close()
	// sc=nil proves the media-index hit short-circuits before any
	// storage call.
	if got := resolveSourceExt(context.Background(), nil, db, "p1", []string{"7"}); got != ".jpg" {
		t.Errorf("resolveSourceExt(7)=%q want .jpg (from media index, no storage)", got)
	}
}

// newStorageStubClient returns a storageClient pointed at an httptest
// server that answers GetFile for ids 136 (png name) and 200 (extension-
// less name, png content-type).
func newStorageStubClient(t *testing.T) *storageClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/files/136"):
			_, _ = w.Write([]byte(`{"file":{"id":136,"name":"0251f8c06d398b45.png","content_type":"image/png"}}`))
		case strings.Contains(r.URL.Path, "/files/200"):
			_, _ = w.Write([]byte(`{"file":{"id":200,"name":"abcd1234","content_type":"image/png"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &storageClient{base: srv.URL, token: "t", httpClient: srv.Client()}
}

func newMediaNameTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(),
		`CREATE TABLE media (project_id TEXT, file_id TEXT, name TEXT, rotation INTEGER DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ pid, fid, name string }{
		{"p1", "7", "vacation.jpg"},
		{"p1", "8", "Diagram.PNG"},
	} {
		if _, err := db.ExecContext(context.Background(),
			`INSERT INTO media (project_id, file_id, name) VALUES (?, ?, ?)`,
			r.pid, r.fid, r.name); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
