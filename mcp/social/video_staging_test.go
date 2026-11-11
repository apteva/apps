package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestVideoStagingUsesInstallDiskAndCleansUp(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	staging := filepath.Join(ctx.DataDir(), "video-staging")
	// A broken system temp directory must have no effect on video staging.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "unavailable"))
	configureTestVideo(t, pf, 7, int64(len(testMP4)))
	p, err := (&App{}).prepareVideoSource(ctx, publishJob{mediaProjectID: "test-proj"}, testSourceItem(), "FILE_UPLOAD")
	if err != nil {
		t.Fatal(err)
	}
	path := p.File.Name()
	if filepath.Dir(path) != staging {
		t.Fatalf("staged outside install data: %s", path)
	}
	p.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned staging file retained: %v", err)
	}
}

func TestVideoStagingCapacityFailsBeforeDownloadingOrMintingURL(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()
	setSourceURL(pf, server.URL)
	app := &App{stagingSpace: func(string) (uint64, bool, error) { return 85_581_824, false, nil }}
	item := testSourceItem()
	item.Bytes = 2 << 30
	_, err := app.prepareVideoSource(ctx, publishJob{mediaProjectID: "test-proj"}, item, "FILE_UPLOAD")
	if sourceErrorCode(err) != "local_staging_full" || classifyPublishFailure(err).Retryable || requests != 0 || countStorageCalls(pf, "files_get_url") != 0 {
		t.Fatalf("capacity failure must stop before a download: err=%v requests=%d calls=%+v", err, requests, pf.callAppCalls)
	}
}

func TestVideoStagingRejectsMemoryBackedFilesystem(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{stagingSpace: func(string) (uint64, bool, error) { return 1 << 40, true, nil }}
	_, err := app.videoStagingDirectory(ctx, 24)
	if sourceErrorCode(err) != "local_staging_unavailable" {
		t.Fatalf("RAM staging accepted: %v", err)
	}
}

type fullStagingWriter struct{ remaining int64 }

func (w *fullStagingWriter) Write(p []byte) (int, error) {
	n := int(min(int64(len(p)), w.remaining))
	w.remaining -= int64(n)
	if n != len(p) {
		return n, &os.PathError{Op: "write", Path: "owned-video.spool", Err: syscall.ENOSPC}
	}
	return n, nil
}

func TestVideoStagingReportsExactLilyWriteFailure(t *testing.T) {
	const available = 85_581_824
	n, checksum, err := stageVideoBody(&fullStagingWriter{remaining: available}, io.LimitReader(zeroVideoReader{}, available+1024), available+1024)
	if n != available || checksum != "" || sourceErrorCode(err) != "local_staging_full" || strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("local write failure was masked: n=%d checksum=%q error=%v", n, checksum, err)
	}
}

func TestVideoStagingSourceErrorsStayDistinct(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		body       []byte
		length     int
		checksum   string
	}{
		{"truncated", "source_truncated", testMP4[:12], 24, ""},
		{"changed_size", "source_changed", testMP4, 48, ""},
		{"checksum", "checksum_mismatch", testMP4, 24, strings.Repeat("0", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pf := newRecordingPlatform()
			ctx := newSocialCtx(t, pf)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("Content-Length", strconv.Itoa(tc.length))
				w.Write(tc.body)
			}))
			defer server.Close()
			setSourceURL(pf, server.URL)
			item := testSourceItem()
			item.SHA256 = tc.checksum
			_, err := (&App{}).prepareVideoSource(ctx, publishJob{mediaProjectID: "test-proj"}, item, "FILE_UPLOAD")
			if sourceErrorCode(err) != tc.code || classifyPublishFailure(err).Code != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
			files, err := filepath.Glob(filepath.Join(ctx.DataDir(), "video-staging", "social-video-*.spool"))
			if err != nil || len(files) != 0 {
				t.Fatalf("failed staging left owned files: %v %v", files, err)
			}
		})
	}
}
