package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLargePlaybackUsesTemporaryFileAndSharesReaders(t *testing.T) {
	key := t.Name()
	fetches := 0
	var path string
	fetch := func() (string, error) {
		fetches++
		f, err := os.CreateTemp(t.TempDir(), "large-")
		if err != nil {
			return "", err
		}
		path = f.Name()
		err = f.Truncate(playbackCacheBytes + 1)
		if err == nil {
			_, err = f.WriteAt([]byte{123}, playbackCacheBytes)
		}
		f.Close()
		return path, err
	}
	first, err := cachedRecording(t.Context(), key, fetch)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cachedRecording(t.Context(), key, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if fetches != 1 {
		t.Fatal("concurrent reader regenerated large file", fetches)
	}
	var b [1]byte
	if _, err = second.ReadAt(b[:], playbackCacheBytes); err != nil || b[0] != 123 {
		t.Fatal("large seek failed", b, err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("removed file while reader active", err)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("temporary file retained", err)
	}
	if len(playbackCache.slots) != 0 {
		t.Fatal("temporary disk permit leaked")
	}
}
func TestPlaybackDiskAdmissionCancelsWithoutFetching(t *testing.T) {
	var files []*recordingPlaybackFile
	for i := 0; i < 2; i++ {
		f, err := cachedRecording(t.Context(), fmt.Sprintf("%s-%d", t.Name(), i), func() (string, error) {
			f, err := os.CreateTemp(t.TempDir(), "large-")
			if err != nil {
				return "", err
			}
			err = f.Truncate(playbackCacheBytes + 1)
			f.Close()
			return f.Name(), err
		})
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
		defer f.Close()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := cachedRecording(ctx, t.Name()+"-blocked", func() (string, error) { t.Error("third disk job started above budget"); return "", nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	for _, f := range files {
		f.Close()
	}
	if len(playbackCache.slots) != 0 {
		t.Fatal("disk permits leaked")
	}
}
func TestPlaybackExpiryKeepsReadersAndCleansIdleFile(t *testing.T) {
	f, err := cachedRecording(t.Context(), t.Name(), func() (string, error) {
		f, err := os.CreateTemp(t.TempDir(), "cache-")
		if err != nil {
			return "", err
		}
		_, err = f.Write([]byte("abcdef"))
		f.Close()
		return f.Name(), err
	})
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	playbackCache.Lock()
	entry := playbackCache.entries[t.Name()]
	entry.expires = time.Now().Add(-time.Second)
	playbackCache.Unlock()
	expireRecordingCacheEntry(t.Name(), entry)
	if _, err = os.Stat(path); err != nil {
		t.Fatal("active cache file evicted", err)
	}
	if _, err = f.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expired file remains", err)
	}
}
func TestRecordingLimitsAndPausedImports(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	settings := defaultRecordingSettings("project-a")
	if settings.MaxFileBytes != 1<<30 {
		t.Fatal(settings)
	}
	for _, v := range []any{true, "garbage", 0, -1, 1.5, float64(1<<30) + 1} {
		s := settings
		applyRecordingSettingsArgs(&s, map[string]any{"max_file_bytes": v})
		if validateRecordingSettings(s) == nil {
			t.Fatal("accepted invalid limit", v)
		}
	}
	call := testCall(t.Name(), "completed")
	call.ProjectID = "project-a"
	if err := app.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	rec, err := app.db().upsertTwilioRecording(&call, "RE-paused", "completed", 2000, 2)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := app.db().claimRecordingImport(call.ProjectID)
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	failure := fmt.Errorf("upload: %w", &storageProxyError{StatusCode: 413, Body: "file too large"})
	app.db().failRecordingImport(rec.ID, failure, claim.ImportStartedAt)
	if _, err = ctx.AppDB().Exec(`UPDATE recordings SET next_attempt_at='' WHERE id=?`, rec.ID); err != nil {
		t.Fatal(err)
	}
	if next, err := app.db().claimRecordingImport(call.ProjectID); err != nil || next != nil {
		t.Fatal("413 retried", next, err)
	}
	if _, err = app.db().upsertTwilioRecording(&call, "RE-paused", "completed", 2000, 2); err != nil {
		t.Fatal(err)
	}
	paused, _ := app.db().findRecording(rec.ID)
	if paused.StorageStatus != "failed" || paused.ImportBlockedReason != "storage_file_too_large" {
		t.Fatal("callback revived paused import", paused)
	}
	if next, err := app.db().claimRecordingImport(call.ProjectID); err != nil || next != nil {
		t.Fatal("callback unblocked retry", next, err)
	}
	settings.MaxFileBytes = 512 << 20
	if err := app.db().saveRecordingSettings(settings); err != nil {
		t.Fatal(err)
	}
	if _, err = ctx.AppDB().Exec(`UPDATE recordings SET next_attempt_at=? WHERE id=?`, ringTime(time.Now().Add(time.Hour)), rec.ID); err != nil {
		t.Fatal(err)
	}
	changed, err := app.db().claimRecordingImport(call.ProjectID)
	if err != nil || changed == nil || changed.ImportLimitBytes != 512<<20 {
		t.Fatal("changed limit not retried", changed, err)
	}
	app.db().failRecordingImport(rec.ID, failure, changed.ImportStartedAt)
	if err := app.db().retryRecordingImport(rec.ID); err != nil {
		t.Fatal(err)
	}
	if retry, err := app.db().claimRecordingImport(call.ProjectID); err != nil || retry == nil {
		t.Fatal("manual retry blocked", retry, err)
	}
}
func TestRecordingTemporaryStartupCleanupPreservesOriginals(t *testing.T) {
	recordingTemps.Lock()
	previous := recordingTemps.root
	recordingTemps.Unlock()
	t.Cleanup(func() { recordingTemps.Lock(); recordingTemps.root = previous; recordingTemps.Unlock() })
	root := t.TempDir()
	tmp := filepath.Join(root, "recording-playback-tmp")
	os.MkdirAll(tmp, 0700)
	os.WriteFile(filepath.Join(tmp, "leftover.wav"), []byte("temporary"), 0600)
	original := filepath.Join(root, "recordings", "original.wav")
	os.MkdirAll(filepath.Dir(original), 0700)
	os.WriteFile(original, []byte("durable"), 0600)
	if err := initializeRecordingTemporaryFiles(root); err != nil {
		t.Fatal(err)
	}
	if files, err := os.ReadDir(tmp); err != nil || len(files) != 0 {
		t.Fatal("stale temporary files retained", files, err)
	}
	if data, err := os.ReadFile(original); err != nil || string(data) != "durable" {
		t.Fatal("original removed", err)
	}
}
func TestDefaultMixedPlaybackFallsBackButExplicitChannelsDoNot(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	call := testCall(t.Name(), "completed")
	call.ProjectID = "project-a"
	app.db().insertCall(call)
	rec, err := app.db().upsertProviderRecording(&call, localRecordingProvider, "bad-wav", "completed", "wav", 2000, 2, "both")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "original.wav")
	os.WriteFile(path, []byte("unsupported WAV but original remains available"), 0600)
	if _, err = ctx.AppDB().Exec(`INSERT INTO local_recording_files(recording_id,path) VALUES(?,?)`, rec.ID, path); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"", "caller", "agent"} {
		r := httptest.NewRequest("GET", "/recordings/"+rec.ID+"/content?variant="+variant, nil)
		w := httptest.NewRecorder()
		app.serveRecordingContent(w, r, ctx, rec)
		if variant == "" {
			if w.Code != 307 || w.Header().Get("X-Telephony-Playback-Variant") != "original" {
				t.Fatal(w.Code, w.Body.String())
			}
		} else if w.Code != 422 || w.Header().Get("Location") != "" {
			t.Fatal("explicit channel played both sides", w.Code, w.Header())
		}
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("original removed on conversion failure", err)
	}
}
func TestStoredRecordingDownloadBoundsAndCancellationCleanup(t *testing.T) {
	root, err := recordingTemporaryRoot()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(root)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer server.Close()
	client := &recordingStorageClient{base: server.URL, token: "test", http: server.Client()}
	ctx := context.WithValue(t.Context(), playbackDownloadContextKey{}, int64(512))
	if _, _, err := client.download(ctx, "project-a", 1, "wav"); recordingImportBlockReason(err) != "recording_file_too_large" {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(root)
	if len(before) != len(after) {
		t.Fatal("oversized download retained temporary file")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := client.download(canceled, "project-a", 1, "wav"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
