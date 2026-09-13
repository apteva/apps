package main

import (
	"context"
	"io"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestMultipartDirectAndRelayEvents(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithProjectID(""), tk.WithEmitter(rec), tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()), tk.WithConfig(map[string]string{"s3_part_size_mb": "5"}))
	be := &multipartFake{Backend: backend(), size: 5*1024*1024 + 3, parts: []remotePart{{1, 5 * 1024 * 1024, "a"}, {2, 3, "b"}}}
	globalBackend = be
	t.Cleanup(func() { globalBackend = nil })
	app := &App{}
	init := func() string {
		return startUpload(t, app, map[string]any{"filename": "video.mp4", "folder": "/videos/", "size": be.size, "content_type": "video/mp4", "direct": true})["upload_id"].(string)
	}
	id := init()
	// First part reached S3 directly; the final part falls back through Apteva.
	be.parts = be.parts[:1]
	be.relay = func(_ context.Context, n int, r io.Reader, size int64) (remotePart, error) {
		written, err := io.Copy(io.Discard, r)
		part := remotePart{n, written, "relay-etag"}
		be.parts = append(be.parts, part)
		return part, err
	}
	if w := putPart(t, app, id, 2, []byte("abc"), "1"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if len(rec.Events()) != 0 {
		t.Fatal("event emitted before completion")
	}
	if w := completeUpload(t, app, id); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	events := rec.EventsByTopic("file.added")
	if len(events) != 1 || events[0].ProjectID != "test-proj" {
		t.Fatalf("wrong file event routing: %+v", events)
	}
	data := events[0].Data.(map[string]any)
	f, _, err := completedUpload(ctx, id, "test-proj")
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"id": f.ID, "name": "video.mp4", "folder": "/videos/", "size_bytes": be.size, "content_type": "video/mp4", "visibility": "private", "sha256": "", "was_existing": false} {
		if data[k] != want {
			t.Fatalf("%s=%v want %v", k, data[k], want)
		}
	}
	if w := completeUpload(t, app, id); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if len(rec.EventsByTopic("file.added")) != 1 {
		t.Fatal("completion retry duplicated event")
	}
	id = init()
	if w := directRequest(app, "DELETE", id, "", "1"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	aborted := rec.EventsByTopic("upload.aborted")
	if len(aborted) != 1 || aborted[0].ProjectID != "test-proj" {
		t.Fatalf("wrong abort event: %+v", aborted)
	}
	data = aborted[0].Data.(map[string]any)
	if data["upload_id"] != id || data["reason"] != "client" {
		t.Fatal(data)
	}
}
