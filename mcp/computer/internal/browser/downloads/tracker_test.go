package downloads

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestTrackerCapturesHashesAndExportsBrowserBytes(t *testing.T) {
	dir := t.TempDir()
	tracker := New(Options{Directory: dir})
	const guid = "provider-guid-1"
	payload := []byte("PK\x03\x04browser-captured-zip-fixture")
	tracker.started(guid, `../nested\\RM6173-bid-pack.zip`, "https://downloads.example/private/file?token=secret")
	started := tracker.DownloadsStartedSince(0)
	if len(started) != 1 || !strings.HasPrefix(started[0].ID, "dl_") {
		t.Fatalf("started metadata: %+v", started)
	}
	if started[0].Filename != "RM6173-bid-pack.zip" || started[0].SourceOrigin != "https://downloads.example" {
		t.Fatalf("unsafe metadata escaped tracker: %+v", started[0])
	}
	if err := os.WriteFile(filepath.Join(dir, guid), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	tracker.progress(guid, int64(len(payload)), int64(len(payload)), "completed", "/outside-the-session/never-read.zip")

	completed, err := tracker.WaitForDownload(context.Background(), started[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(payload)
	if completed.Status != computer.DownloadCompleted || completed.Size != int64(len(payload)) || completed.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("completion metadata: %+v", completed)
	}
	reader, exported, err := tracker.OpenDownload(context.Background(), completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, payload) || exported.ID != completed.ID {
		t.Fatalf("exported bytes=%q meta=%+v err=%v", got, exported, err)
	}
}

func TestTrackerHandlesDuplicateNamesTimeoutCancellationAndClose(t *testing.T) {
	dir := t.TempDir()
	tracker := New(Options{Directory: dir})
	tracker.started("one", "same.zip", "blob:https://example.test/id")
	tracker.started("two", "same.zip", "https://example.test/two")
	items := tracker.DownloadsStartedSince(0)
	if len(items) != 2 || items[0].ID == items[1].ID || items[0].Filename != items[1].Filename {
		t.Fatalf("duplicate filenames need distinct stable ids: %+v", items)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	partial, err := tracker.WaitForDownload(ctx, items[0].ID)
	if !errors.Is(err, context.DeadlineExceeded) || partial.Status != computer.DownloadInProgress {
		t.Fatalf("bounded wait: meta=%+v err=%v", partial, err)
	}
	tracker.progress("one", 3, 9, "canceled", "")
	cancelled, err := tracker.WaitForDownload(context.Background(), items[0].ID)
	if err != nil || cancelled.Status != computer.DownloadCancelled || cancelled.ErrorCode != "download_cancelled" {
		t.Fatalf("cancelled state: meta=%+v err=%v", cancelled, err)
	}
	if _, _, err := tracker.OpenDownload(context.Background(), items[0].ID); err == nil || !strings.Contains(err.Error(), "download_not_ready") {
		t.Fatalf("cancelled download must not export: %v", err)
	}
	if _, err := tracker.WaitForDownload(context.Background(), "dl_unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown download: %v", err)
	}
	waitResult := make(chan error, 1)
	go func() {
		_, err := tracker.WaitForDownload(context.Background(), items[1].ID)
		waitResult <- err
	}()
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-waitResult; !errors.Is(err, ErrClosed) {
		t.Fatalf("session close did not release pending wait: %v", err)
	}
	if _, err := tracker.ListDownloads(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed session: %v", err)
	}
}

func TestTrackerEnforcesFileAndSessionLimits(t *testing.T) {
	dir := t.TempDir()
	cancelled := make(chan string, 4)
	tracker := New(Options{Directory: dir, MaxFileBytes: 4, MaxSessionBytes: 6, MaxFiles: 2, Cancel: func(guid string) { cancelled <- guid }})
	tracker.started("large", "large.bin", "")
	tracker.progress("large", 5, 5, "inProgress", "")
	first := tracker.DownloadsStartedSince(0)[0]
	failed, err := tracker.WaitForDownload(context.Background(), first.ID)
	if err != nil || failed.Status != computer.DownloadFailed || failed.ErrorCode != "download_size_limit_exceeded" {
		t.Fatalf("file limit: meta=%+v err=%v", failed, err)
	}
	select {
	case guid := <-cancelled:
		if guid != "large" {
			t.Fatalf("cancelled wrong browser download: %s", guid)
		}
	case <-time.After(time.Second):
		t.Fatal("over-limit download was not cancelled in the browser")
	}
	tracker.started("second", "second.bin", "")
	tracker.started("third", "third.bin", "")
	items, err := tracker.ListDownloads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var limited bool
	for _, item := range items {
		if item.ErrorCode == "download_session_limit_exceeded" {
			limited = true
		}
	}
	if !limited {
		t.Fatalf("file-count limit not reported: %+v", items)
	}
}

func TestTrackerLimitsConcurrentSessionBytes(t *testing.T) {
	tracker := New(Options{MaxFileBytes: 10, MaxSessionBytes: 6})
	tracker.started("one", "same.zip", "")
	tracker.started("two", "same.zip", "")
	tracker.progress("one", 4, 4, "inProgress", "")
	tracker.progress("two", 4, 4, "inProgress", "")
	items := tracker.DownloadsStartedSince(0)
	if items[1].Status != computer.DownloadFailed || items[1].ErrorCode != "download_session_limit_exceeded" {
		t.Fatalf("concurrent bytes did not count toward session limit: %+v", items)
	}
}

func TestSanitizeFilenamePreservesUTF8AndBlocksTraversal(t *testing.T) {
	name := SanitizeFilename("../../" + strings.Repeat("🐰", 100) + ".zip")
	if !utf8.ValidString(name) || len(name) > 180 || strings.ContainsAny(name, `/\\`) || filepath.Ext(name) != ".zip" {
		t.Fatalf("unsafe filename %q (%d bytes)", name, len(name))
	}
}
