package main

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRelayPartStreamsBeforeBodyFinishes(t *testing.T) {
	ctx, _, be, id := directFixture(t)
	received := make(chan struct{})
	be.relay = func(c context.Context, n int, r io.Reader, size int64) (remotePart, error) {
		var one [1]byte
		_, err := io.ReadFull(r, one[:])
		if err != nil {
			return remotePart{}, err
		}
		close(received)
		rest, err := io.Copy(io.Discard, r)
		return remotePart{n, rest + 1, "etag"}, err
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() { _, err := writeUploadPart(context.Background(), ctx, id, 2, reader, 3, nil); done <- err }()
	if _, err := writer.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("buffered before backend")
	}
	if _, err := writer.Write([]byte("bc")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(partsDir(ctx, id))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("relay staged files")
	}
}
func TestRelaySizeOwnershipAndProviderFailure(t *testing.T) {
	ctx, app, be, id := directFixture(t)
	called := 0
	be.relay = func(_ context.Context, n int, r io.Reader, size int64) (remotePart, error) {
		called++
		read, err := io.Copy(io.Discard, r)
		return remotePart{n, read, "etag"}, err
	}
	for _, size := range []int64{-1, 0, 2, 4} {
		if _, err := writeUploadPart(context.Background(), ctx, id, 2, strings.NewReader("abc"), size, nil); err == nil {
			t.Fatal("accepted size", size)
		}
	}
	if w := putPart(t, app, id, 2, []byte("abc"), "2"); w.Code != 403 {
		t.Fatal("owner", w.Code)
	}
	if called != 0 {
		t.Fatal("read unauthorized or invalid body")
	}
	if w := putPart(t, app, id, 2, []byte("abc"), "1"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if _, err := writeUploadPart(context.Background(), ctx, id, 2, strings.NewReader("a"), 3, nil); err == nil {
		t.Fatal("accepted short body")
	}
	c, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := writeUploadPart(c, ctx, id, 2, strings.NewReader("abc"), 3, nil); err == nil {
		t.Fatal("ignored cancellation")
	}
	be.relay = func(context.Context, int, io.Reader, int64) (remotePart, error) {
		return remotePart{}, errors.New("private provider details")
	}
	if w := putPart(t, app, id, 2, []byte("abc"), "1"); w.Code == 200 || strings.Contains(w.Body.String(), "private provider") {
		t.Fatal(w.Code, w.Body)
	}
}
func TestCorsFailureNegotiatesRelay(t *testing.T) {
	_, app, be, _ := directFixture(t)
	be.corsError = errors.New("AccessDenied")
	r := httptest.NewRequest("POST", "/uploads?project_id=test-proj", strings.NewReader(`{"filename":"video.mp4","size":30000000,"direct":true}`))
	r.Header.Set("X-User-ID", "1")
	w := httptest.NewRecorder()
	app.handleUploadInit(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"s3_relay"`) || !strings.Contains(w.Body.String(), `"relay_supported":true`) {
		t.Fatal(w.Code, w.Body)
	}
}
