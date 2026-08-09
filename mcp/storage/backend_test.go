package main

// Tier 1 — backend interface, disk parity, key composition. The real
// S3 round-trip lives behind a `-tags live` integration test (next
// commit) since it requires a MinIO/AWS endpoint.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestObjectKey_PrefixesBy2HexChars(t *testing.T) {
	got := objectKey("deadbeefcafe", "abc.mp4")
	if got != "de/abc.mp4" {
		t.Errorf("got %q, want de/abc.mp4", got)
	}
}

func TestObjectKey_ShortShaFallsBackTo00Prefix(t *testing.T) {
	// Defensive: a corrupted/short hash shouldn't crash the path
	// composer. The "00" prefix is a clear signal that something
	// upstream lost the hash.
	got := objectKey("", "abc.mp4")
	if !strings.HasPrefix(got, "00/") {
		t.Errorf("expected 00 prefix for empty sha, got %q", got)
	}
}

// ─── diskBackend ───────────────────────────────────────────────────

func TestDiskBackend_PutReadDelete_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STORAGE_BLOBS_DIR", dir)

	d := newDiskBackend(nil) // ctx unused with env override
	ctx := context.Background()

	key := "ab/test-key.txt"
	if err := d.Put(ctx, key, "text/plain", strings.NewReader("hello world"), 11); err != nil {
		t.Fatal(err)
	}
	// Stat returns the actual size.
	size, err := d.Stat(ctx, key)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if size != 11 {
		t.Errorf("size=%d want 11", size)
	}
	// LocalPath points at the right file.
	path, ok := d.LocalPath(key)
	if !ok {
		t.Fatal("LocalPath should return ok=true for disk")
	}
	expected := filepath.Join(dir, "ab", "test-key.txt")
	if path != expected {
		t.Errorf("path=%q want %q", path, expected)
	}
	// Bytes round-trip.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("contents=%q", got)
	}
	// Delete is idempotent.
	if err := d.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := d.Delete(ctx, key); err != nil {
		t.Errorf("second delete should be a no-op: %v", err)
	}
	// Stat now returns ErrNotFound.
	if _, err := d.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("stat after delete should ErrNotFound, got %v", err)
	}
}

func TestDiskBackend_Put_HonoursSizeLimit(t *testing.T) {
	// A reader that wouldn't EOF on its own — Put must stop at size
	// bytes so a malformed client can't blow up our disk.
	dir := t.TempDir()
	t.Setenv("STORAGE_BLOBS_DIR", dir)
	d := newDiskBackend(nil)
	ctx := context.Background()

	key := "ab/limited"
	endless := &endlessReader{ch: 'A'}
	if err := d.Put(ctx, key, "application/octet-stream", endless, 100); err != nil {
		t.Fatal(err)
	}
	size, _ := d.Stat(ctx, key)
	if size != 100 {
		t.Errorf("size=%d want 100", size)
	}
}

func TestDiskBackend_PresignsUnsupported(t *testing.T) {
	d := newDiskBackend(nil)
	ctx := context.Background()
	if _, err := d.PresignGet(ctx, "k", GetObjectOptions{Filename: "f", ContentType: "ct"}, 0); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("PresignGet: %v", err)
	}
	if _, err := d.PresignPut(ctx, "k", "ct", 0); !errors.Is(err, ErrPresignNotSupported) {
		t.Errorf("PresignPut: %v", err)
	}
}

func TestDiskBackend_AbsPathDoesNotEscapeRoot(t *testing.T) {
	// Defence in depth: even a malicious key with .. components
	// must stay under blobsDir. filepath.Clean("/..") = "/".
	dir := t.TempDir()
	t.Setenv("STORAGE_BLOBS_DIR", dir)
	d := newDiskBackend(nil)
	abs := d.absPath("../../etc/passwd")
	if !strings.HasPrefix(abs, dir+string(filepath.Separator)) && abs != dir {
		t.Errorf("escape: %q is outside %q", abs, dir)
	}
}

// ─── s3Backend dispatch (no real S3 call) ──────────────────────────

func TestSanitiseFilename_StripsTroublesomeBytes(t *testing.T) {
	cases := map[string]string{
		`hello.mp4`:        `hello.mp4`,
		"weird\"quote.mp4": "weird_quote.mp4",
		"slash\\name.mp4":  "slash_name.mp4",
		"newline\nhere":    "newline_here",
	}
	for in, want := range cases {
		if got := sanitiseFilename(in); got != want {
			t.Errorf("sanitise(%q)=%q want %q", in, got, want)
		}
	}
}

func TestContentDispositionHeader_EncodesUnicodeAndHostileFilename(t *testing.T) {
	header := contentDispositionHeader(DispositionInline, "résumé\"\r\n.png")
	if strings.ContainsAny(header, "\r\n") {
		t.Fatalf("header contains a newline: %q", header)
	}
	if !strings.HasPrefix(header, `inline; filename="`) {
		t.Fatalf("missing inline ASCII fallback: %q", header)
	}
	if !strings.Contains(header, `filename*=UTF-8''r%C3%A9sum%C3%A9%22%0D%0A.png`) {
		t.Fatalf("missing RFC 5987 filename: %q", header)
	}
}

func TestEffectiveContentDisposition_RestrictsExecutableTypes(t *testing.T) {
	for _, contentType := range []string{"image/png", "video/mp4", "audio/mpeg", "application/pdf", "text/plain; charset=utf-8"} {
		if got := effectiveContentDisposition(DispositionInline, contentType); got != DispositionInline {
			t.Errorf("%s disposition=%q, want inline", contentType, got)
		}
	}
	for _, contentType := range []string{"text/html", "application/javascript", "image/svg+xml", "application/octet-stream", ""} {
		if got := effectiveContentDisposition(DispositionInline, contentType); got != DispositionAttachment {
			t.Errorf("%s disposition=%q, want attachment", contentType, got)
		}
	}
}

func TestPresignedResponseCacheControl_StaysInsideTTL(t *testing.T) {
	if got := presignedResponseCacheControl(15 * time.Minute); got != "private, max-age=870" {
		t.Fatalf("cache control=%q", got)
	}
	if got := presignedResponseCacheControl(20 * time.Second); got != "private, no-store" {
		t.Fatalf("short TTL cache control=%q", got)
	}
}

func TestS3PresignGet_SignsDispositionTypeAndBoundedCache(t *testing.T) {
	client, err := minio.New("s3.example.com", &minio.Options{
		Creds: credentials.NewStaticV4("access", "secret", ""), Secure: true, Region: "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	be := &s3Backend{client: client, bucket: "bucket", region: "us-east-1"}
	raw, err := be.PresignGet(context.Background(), "ab/file", GetObjectOptions{
		Filename: "café.png", ContentType: "image/png", Disposition: DispositionInline,
	}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if got := query.Get("response-content-type"); got != "image/png" {
		t.Fatalf("response content type=%q", got)
	}
	if got := query.Get("response-cache-control"); got != "private, max-age=870" {
		t.Fatalf("response cache control=%q", got)
	}
	disposition := query.Get("response-content-disposition")
	if !strings.HasPrefix(disposition, "inline;") || !strings.Contains(disposition, "filename*=UTF-8''caf%C3%A9.png") {
		t.Fatalf("response disposition=%q", disposition)
	}
}

func TestConfigBool_Defaults(t *testing.T) {
	if !configBool("", true) {
		t.Error("default true must apply on empty input")
	}
	if configBool("false", true) {
		t.Error("explicit false should override default")
	}
	if !configBool("yes", false) {
		t.Error("yes should be true")
	}
	if configBool("garbage", false) {
		t.Error("unknown should fall to default")
	}
}

func TestConfigIntClamped(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"", 4}, {"bad", 4}, {"0", 4}, {"1", 1}, {"6", 6}, {"99", 8}, {"-2", 1},
	} {
		if got := configIntClamped(tc.raw, 4, 1, 8); got != tc.want {
			t.Errorf("configIntClamped(%q)=%d want %d", tc.raw, got, tc.want)
		}
	}
}

func TestConfigUintClamped(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want uint64
	}{
		{"", 16}, {"bad", 16}, {"0", 16}, {"5", 5}, {"64", 64}, {"999", 128},
	} {
		if got := configUintClamped(tc.raw, 16, 5, 128); got != tc.want {
			t.Errorf("configUintClamped(%q)=%d want %d", tc.raw, got, tc.want)
		}
	}
}

// ─── small helpers ─────────────────────────────────────────────────

type endlessReader struct{ ch byte }

func (e *endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = e.ch
	}
	return len(p), nil
}

// io.Discard signature check — keeps the import set honest if we
// later refactor to pipe.
var _ io.Reader = (*endlessReader)(nil)
var _ = bytes.NewReader
