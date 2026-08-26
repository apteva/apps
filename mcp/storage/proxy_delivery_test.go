package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestFilesGetURL_ProxyContractAndPurposeBoundSignature(t *testing.T) {
	t.Setenv("STORAGE_PUBLIC_URL", "https://agents.example.com")
	ctx, f, stub := newRemoteFile(t, "video/mp4", "private")
	out, err := (&App{}).toolGetURL(ctx, map[string]any{
		"id": f.ID, "delivery": "proxy", "disposition": "inline", "ttl_seconds": 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)
	if got["delivery"] != "proxy" || got["presigned"] != false || got["proxied"] != true {
		t.Fatalf("response=%v", got)
	}
	urlString := got["url"].(string)
	if !strings.Contains(urlString, "/public/files/") ||
		!strings.Contains(urlString, "/proxy/content/") ||
		!strings.Contains(urlString, "project_id=test-proj") {
		t.Fatalf("proxy URL=%q", urlString)
	}
	if atomic.LoadInt32(&stub.getCalls) != 0 || atomic.LoadInt32(&stub.openCalls) != 0 {
		t.Fatal("minting a proxy URL contacted the object backend")
	}

	// A normal Apteva content signature must not authorize the proxy route.
	exp := time.Now().Add(time.Minute).Unix()
	path := "/public/files/" + intToString(f.ID) + "/proxy/content/video.mp4?project_id=test-proj" +
		"&sig=" + signFile(f.ID, exp) + "&exp=" + intToString(exp)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ordinary signature status=%d body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&stub.openCalls) != 0 {
		t.Fatal("invalid signature reached the object backend")
	}
}

func TestHTTPMintURL_ProxyContract(t *testing.T) {
	_, f, stub := newRemoteFile(t, "video/mp4", "private")
	body := strings.NewReader(`{"ttl_seconds":60,"delivery":"proxy","disposition":"inline"}`)
	req := httptest.NewRequest(http.MethodPost,
		"/files/"+intToString(f.ID)+"/url?project_id=test-proj", body)
	rec := httptest.NewRecorder()
	(&App{}).handleFilesItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["delivery"] != "proxy" || response["proxied"] != true || response["presigned"] != false {
		t.Fatalf("response=%v", response)
	}
	if atomic.LoadInt32(&stub.getCalls) != 0 || atomic.LoadInt32(&stub.openCalls) != 0 {
		t.Fatal("HTTP mint contacted the object backend")
	}
}

func TestProxyRemoteHEADReturnsAccurateMetadataWithoutRedirect(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "video/mp4", "private")
	modified := time.Unix(1_700_000_123, 0).UTC()
	stub.metadata = &ObjectMetadata{
		Size: 3, ContentType: "video/mp4", ETag: "upstream-etag", LastModified: modified,
	}
	path := mintProxyPath(t, ctx, f, DispositionInline, 60)
	req := httptest.NewRequest(http.MethodHead, path, nil)
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "3" {
		t.Fatalf("body=%q headers=%v", rec.Body.String(), rec.Header())
	}
	assertProxyHeaders(t, rec, "video/mp4", `"upstream-etag"`, modified)
	if rec.Header().Get("Location") != "" {
		t.Fatalf("HEAD redirected to %q", rec.Header().Get("Location"))
	}
	if atomic.LoadInt32(&stub.headCalls) != 1 || atomic.LoadInt32(&stub.openCalls) != 0 || atomic.LoadInt32(&stub.getCalls) != 0 {
		t.Fatalf("calls head=%d open=%d presign=%d", stub.headCalls, stub.openCalls, stub.getCalls)
	}
}

func TestProxyRemoteGETStreamsBytesWithoutRedirectOrPresign(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "video/mp4", "private")
	content := bytes.Repeat([]byte("0123456789abcdef"), 128*1024)
	key := objectKey(f.SHA256, f.StorageKey)
	stub.objects[key] = content
	stub.metadata = &ObjectMetadata{
		Size: int64(len(content)), ContentType: "video/mp4", ETag: "stream-etag",
		LastModified: time.Unix(1_700_000_123, 0).UTC(),
	}

	path := mintProxyPath(t, ctx, f, DispositionInline, 60)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("status=%d bytes=%d want=%d", rec.Code, rec.Body.Len(), len(content))
	}
	if rec.Header().Get("Location") != "" {
		t.Fatalf("GET redirected to %q", rec.Header().Get("Location"))
	}
	if rec.Header().Get("Content-Length") != intToString(int64(len(content))) {
		t.Fatalf("content-length=%q", rec.Header().Get("Content-Length"))
	}
	if atomic.LoadInt32(&stub.openCalls) != 1 || atomic.LoadInt32(&stub.getCalls) != 0 {
		t.Fatalf("calls open=%d presign=%d", stub.openCalls, stub.getCalls)
	}
}

func TestProxyRemoteGETReadsIncrementally(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "video/mp4", "private")
	body := &trackingReadCloser{data: bytes.Repeat([]byte("x"), 1024*1024)}
	stub.openHook = func(_ context.Context, _ string, _ ObjectReadOptions) (*ObjectReadResult, error) {
		return &ObjectReadResult{
			Body: body, StatusCode: http.StatusOK, ContentLength: int64(len(body.data)), ContentType: "video/mp4",
		}, nil
	}
	path := mintProxyPath(t, ctx, f, DispositionInline, 60)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != len(body.data) {
		t.Fatalf("status=%d bytes=%d", rec.Code, rec.Body.Len())
	}
	if body.maxRequest > 128*1024 {
		t.Fatalf("largest upstream read=%d, want <= 128 KiB", body.maxRequest)
	}
}

func TestProxyRemoteSingleRangeAndUnsatisfiableRange(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "video/mp4", "private")
	content := make([]byte, 2*1024*1024)
	for i := range content {
		content[i] = byte(i % 251)
	}
	key := objectKey(f.SHA256, f.StorageKey)
	stub.objects[key] = content
	stub.metadata = &ObjectMetadata{Size: int64(len(content)), ContentType: "video/mp4"}
	path := mintProxyPath(t, ctx, f, DispositionInline, 60)

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Range", "bytes=0-1048575")
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != 1024*1024 {
		t.Fatalf("range status=%d bytes=%d", rec.Code, rec.Body.Len())
	}
	if rec.Header().Get("Content-Range") != "bytes 0-1048575/2097152" ||
		rec.Header().Get("Content-Length") != "1048576" {
		t.Fatalf("range headers=%v", rec.Header())
	}
	if !bytes.Equal(rec.Body.Bytes(), content[:1024*1024]) {
		t.Fatal("range bytes differ")
	}

	req = httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Range", "bytes=3000000-")
	rec = httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable ||
		rec.Header().Get("Content-Range") != "bytes */2097152" {
		t.Fatalf("unsatisfiable status=%d headers=%v", rec.Code, rec.Header())
	}
	if atomic.LoadInt32(&stub.getCalls) != 0 {
		t.Fatalf("proxy used PresignGet %d times", stub.getCalls)
	}
}

func TestProxyRejectsMultipleRangesBeforeBackendRead(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "video/mp4", "private")
	path := mintProxyPath(t, ctx, f, DispositionInline, 60)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Range", "bytes=0-1,4-5")
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&stub.openCalls) != 0 {
		t.Fatal("invalid multiple range reached backend")
	}
}

func TestProxyRejectsInvalidBackendRangeResponse(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "video/mp4", "private")
	stub.openHook = func(_ context.Context, _ string, _ ObjectReadOptions) (*ObjectReadResult, error) {
		return &ObjectReadResult{
			Body:       io.NopCloser(strings.NewReader("unexpected full body")),
			StatusCode: http.StatusOK, ContentLength: 20, ContentType: "video/mp4",
		}, nil
	}
	path := mintProxyPath(t, ctx, f, DispositionInline, 60)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProxyDiskUsesServeFileRangePath(t *testing.T) {
	ctx := newTestCtx(t)
	globalBackend = newDiskBackend(ctx)
	t.Cleanup(func() { globalBackend = nil })
	f := mustUploadTyped(t, ctx, "clip.mp4", "video/mp4", "0123456789")
	path := mintProxyPath(t, ctx, f, DispositionInline, 60)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "2345" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Range") != "bytes 2-5/10" || rec.Header().Get("Location") != "" {
		t.Fatalf("headers=%v", rec.Header())
	}
}

func TestProxyCancellationClosesUpstream(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "video/mp4", "private")
	started := make(chan struct{})
	closed := make(chan struct{})
	stub.openHook = func(ctx context.Context, _ string, _ ObjectReadOptions) (*ObjectReadResult, error) {
		return &ObjectReadResult{
			Body:       &contextBlockingBody{ctx: ctx, started: started, closed: closed},
			StatusCode: http.StatusOK, ContentLength: 1024, ContentType: "video/mp4",
		}, nil
	}
	path := mintProxyPath(t, ctx, f, DispositionInline, 60)
	requestCtx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(requestCtx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&App{}).handlePublicFilesItem(rec, req)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("backend read did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("proxy did not stop after cancellation")
	}
	select {
	case <-closed:
	default:
		t.Fatal("upstream body was not closed")
	}
	if atomic.LoadInt32(&stub.getCalls) != 0 {
		t.Fatal("cancellation path used a presigned URL")
	}
}

func TestProxyTransferContinuesAfterURLExpires(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "video/mp4", "private")
	started := make(chan struct{})
	release := make(chan struct{})
	stub.openHook = func(_ context.Context, _ string, _ ObjectReadOptions) (*ObjectReadResult, error) {
		return &ObjectReadResult{
			Body:       &gatedReadCloser{reader: bytes.NewReader([]byte("complete")), started: started, release: release},
			StatusCode: http.StatusOK, ContentLength: 8, ContentType: "video/mp4",
		}, nil
	}
	path, expiresAt := mintProxyPathWithExpiry(t, ctx, f, DispositionInline, 2)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&App{}).handlePublicFilesItem(rec, req)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("backend read did not start")
	}
	for time.Now().Unix() <= expiresAt {
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("transfer did not complete")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "complete" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestProxyDispositionIsSignatureBound(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "video/mp4", "private")
	inlinePath := mintProxyPath(t, ctx, f, DispositionInline, 60)
	tampered := strings.Replace(inlinePath, "/proxy/content/", "/proxy/download/", 1)
	req := httptest.NewRequest(http.MethodGet, tampered, nil)
	rec := httptest.NewRecorder()
	(&App{}).handlePublicFilesItem(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&stub.openCalls) != 0 {
		t.Fatal("tampered disposition reached backend")
	}
}

func mintProxyPath(t *testing.T, ctx *sdk.AppCtx, f *File, disposition ContentDisposition, ttl int) string {
	t.Helper()
	path, _ := mintProxyPathWithExpiry(t, ctx, f, disposition, ttl)
	return path
}

func mintProxyPathWithExpiry(t *testing.T, ctx *sdk.AppCtx, f *File, disposition ContentDisposition, ttl int) (string, int64) {
	t.Helper()
	out, err := (&App{}).toolGetURL(ctx, map[string]any{
		"id": f.ID, "delivery": "proxy", "disposition": string(disposition), "ttl_seconds": ttl,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	path := result["url"].(string)
	path = strings.TrimPrefix(path, publicBase(ctx))
	path = strings.TrimPrefix(path, "/api/apps/storage")
	return path, result["expires_at"].(int64)
}

func assertProxyHeaders(t *testing.T, rec *httptest.ResponseRecorder, contentType, etag string, modified time.Time) {
	t.Helper()
	if rec.Header().Get("Content-Type") != contentType || rec.Header().Get("ETag") != etag ||
		rec.Header().Get("Last-Modified") != modified.Format(http.TimeFormat) ||
		rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("headers=%v", rec.Header())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline;") {
		t.Fatalf("content-disposition=%q", rec.Header().Get("Content-Disposition"))
	}
}

type contextBlockingBody struct {
	ctx       context.Context
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func (b *contextBlockingBody) Read([]byte) (int, error) {
	b.startOnce.Do(func() { close(b.started) })
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *contextBlockingBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

type gatedReadCloser struct {
	reader    *bytes.Reader
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
}

func (r *gatedReadCloser) Read(p []byte) (int, error) {
	r.startOnce.Do(func() {
		close(r.started)
		<-r.release
	})
	return r.reader.Read(p)
}

func (r *gatedReadCloser) Close() error { return nil }

type trackingReadCloser struct {
	data       []byte
	position   int
	maxRequest int
}

func (r *trackingReadCloser) Read(p []byte) (int, error) {
	if len(p) > r.maxRequest {
		r.maxRequest = len(p)
	}
	if r.position >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.position:])
	r.position += n
	return n, nil
}

func (r *trackingReadCloser) Close() error { return nil }

var _ io.ReadCloser = (*contextBlockingBody)(nil)
var _ io.ReadCloser = (*gatedReadCloser)(nil)
var _ io.ReadCloser = (*trackingReadCloser)(nil)
