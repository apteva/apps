package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type multipartFake struct {
	corsError error
	relay     func(context.Context, int, io.Reader, int64) (remotePart, error)
	Backend
	parts           []remotePart
	size            int64
	complete, abort int
	object          bool
	signSize        int64
}

func (b *multipartFake) Put(context.Context, string, string, io.Reader, int64) error {
	panic("direct completion must not upload object bytes")
}
func (b *multipartFake) PrepareBrowserUpload(context.Context, string) error { return b.corsError }
func (b *multipartFake) PutMultipartPart(c context.Context, _ string, _ string, n int, r io.Reader, size int64) (remotePart, error) {
	if b.relay != nil {
		return b.relay(c, n, r, size)
	}
	read, err := io.Copy(io.Discard, r)
	return remotePart{n, read, "relay-etag"}, err
}
func (b *multipartFake) Kind() string { return "s3" }
func (b *multipartFake) BeginMultipart(context.Context, string, string) (string, error) {
	return "provider-id", nil
}
func (b *multipartFake) SignMultipartPart(_ context.Context, _ string, _ string, n int, size int64) (string, error) {
	b.signSize = size
	return fmt.Sprintf("https://bucket.example/part/%d", n), nil
}
func (b *multipartFake) MultipartParts(context.Context, string, string) ([]remotePart, error) {
	return b.parts, nil
}
func (b *multipartFake) FinishMultipart(context.Context, string, string, []remotePart) error {
	b.complete++
	b.object = true
	return nil
}
func (b *multipartFake) AbortMultipart(context.Context, string, string) error { b.abort++; return nil }
func (b *multipartFake) Stat(context.Context, string) (int64, error) {
	if !b.object {
		return 0, errors.New("not found")
	}
	return b.size, nil
}
func (b *multipartFake) OpenObject(context.Context, string, ObjectReadOptions) (*ObjectReadResult, error) {
	panic("direct completion must not read object bytes")
}

func directFixture(t *testing.T) (*sdk.AppCtx, *App, *multipartFake, string) {
	t.Helper()
	ctx := newTestCtx(t, tk.WithEnv("APTEVA_PUBLIC_URL", "https://dashboard.example"), tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()), tk.WithConfig(map[string]string{"s3_part_size_mb": "5", "max_upload_size_mb": "5120"}))
	be := &multipartFake{Backend: backend(), size: 5*1024*1024 + 3}
	globalBackend = be
	t.Cleanup(func() { globalBackend = nil })
	out := startUpload(t, &App{}, map[string]any{"filename": "video.mp4", "size": be.size, "direct": true, "content_type": "video/mp4"})
	if out["mode"] != "s3_multipart" {
		t.Fatalf("wrong mode: %v", out)
	}
	return ctx, &App{}, be, out["upload_id"].(string)
}
func directRequest(app *App, method, id, suffix, user string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/uploads/"+id+suffix+"?project_id=test-proj", strings.NewReader("{}"))
	r.Header.Set("X-User-ID", user)
	w := httptest.NewRecorder()
	app.handleUploadsItem(w, r)
	return w
}
func TestDirectMultipartSignsBoundedPartsAndRejectsOtherOwners(t *testing.T) {
	_, app, be, id := directFixture(t)
	if w := directRequest(app, "GET", id, "/parts/2", "1"); w.Code != 200 || be.signSize != 3 {
		t.Fatalf("final part signing: %d %s size=%d", w.Code, w.Body, be.signSize)
	}
	if w := directRequest(app, "GET", id, "/parts/3", "1"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := directRequest(app, "GET", id, "/parts/1", "2"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := putPart(t, app, id, 1, []byte("no proxy"), "1"); w.Code != 400 {
		t.Fatal("accepted proxied bytes", w.Code)
	}
}
func TestDirectMultipartCompletesWithoutMovingBytesAndRetriesIdempotently(t *testing.T) {
	ctx, app, be, id := directFixture(t)
	be.parts = []remotePart{{1, 5 * 1024 * 1024, "part1"}, {2, 3, "part2"}}
	if w := completeUpload(t, app, id); w.Code != 200 {
		t.Fatalf("complete %d %s", w.Code, w.Body)
	}
	if w := completeUpload(t, app, id); w.Code != 200 {
		t.Fatalf("retry %d %s", w.Code, w.Body)
	}
	if be.complete != 1 {
		t.Fatal("completed twice")
	}
	f, _, err := completedUpload(ctx, id, "test-proj")
	if err != nil || f == nil || f.SHA256 != "" || f.SizeBytes != be.size {
		t.Fatalf("file: %+v %v", f, err)
	}
	if _, err = os.Stat(uploadSessionDir(ctx, id)); !os.IsNotExist(err) {
		t.Fatal("session not cleaned")
	}
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT count(*) FROM upload_reservations`).Scan(&n)
	if n != 0 {
		t.Fatal("quota not released")
	}
}
func TestDirectMultipartRejectsIncompleteOrIncorrectProviderParts(t *testing.T) {
	for _, parts := range [][]remotePart{{{1, 5 * 1024 * 1024, "one"}}, {{1, 5 * 1024 * 1024, "one"}, {3, 3, "three"}}, {{1, 5 * 1024 * 1024, "one"}, {2, 2, "two"}}} {
		t.Run(fmt.Sprint(parts), func(t *testing.T) {
			_, app, be, id := directFixture(t)
			be.parts = parts
			if w := completeUpload(t, app, id); w.Code == 200 {
				t.Fatal("accepted invalid provider parts")
			}
			if be.complete != 0 {
				t.Fatal("published invalid object")
			}
		})
	}
}
func TestDirectMultipartRecoversProviderCompletionBeforeDBCommit(t *testing.T) {
	_, app, be, id := directFixture(t)
	be.object = true
	if w := completeUpload(t, app, id); w.Code != 200 {
		t.Fatalf("recovery %d %s", w.Code, w.Body)
	}
	if be.complete != 0 {
		t.Fatal("provider completed twice")
	}
}
func TestDirectMultipartCancelAbortsProviderAndReleasesQuota(t *testing.T) {
	ctx, app, be, id := directFixture(t)
	if w := directRequest(app, "DELETE", id, "", "2"); w.Code != 403 {
		t.Fatal("other owner cancelled")
	}
	if w := directRequest(app, "DELETE", id, "", "1"); w.Code != 200 {
		t.Fatalf("abort %d %s", w.Code, w.Body)
	}
	if be.abort != 1 {
		t.Fatal("provider not aborted")
	}
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT count(*) FROM upload_reservations`).Scan(&n)
	if n != 0 {
		t.Fatal("quota not released")
	}
}
func TestUnknownHashesNeverDeduplicate(t *testing.T) {
	ctx := newTestCtx(t)
	for _, sk := range []string{"first", "second"} {
		f, existed, err := publishFile(context.Background(), ctx, "test-proj", uploadInput{Name: "same.mp4", Folder: "/", Visibility: "private"}, "", sk, 12, "")
		if err != nil || existed || f == nil {
			t.Fatalf("unknown hash deduplication: %v %v", existed, err)
		}
	}
}
