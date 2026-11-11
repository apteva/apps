package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type virtualVideo struct{ size int64 }

func (v virtualVideo) ReadAt(p []byte, off int64) (int, error) {
	if off >= v.size {
		return 0, io.EOF
	}
	n := min(len(p), int(v.size-off))
	clear(p[:n])
	if off < int64(len(testMP4)) {
		copy(p[:n], testMP4[off:])
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

type rangeVideoResponse func(http.ResponseWriter, *http.Request, int64, int64) bool

type rangeVideoFixture struct {
	platform    *recordingPlatform
	ctx         *sdk.AppCtx
	source      storageVideoSource
	item        mediaItem
	server      *httptest.Server
	gets        atomic.Int64
	transferred atomic.Int64
	peakHeap    atomic.Uint64
	response    atomic.Pointer[rangeVideoResponse]
}

func (f *rangeVideoFixture) setResponse(response rangeVideoResponse) {
	f.response.Store(&response)
}

func newRangeVideoFixture(t *testing.T, size int64) *rangeVideoFixture {
	t.Helper()
	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{"storage": float64(16)}
	ctx := newSocialCtx(t, pf)
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(virtualVideo{size}, 0, size)); err != nil {
		t.Fatal(err)
	}
	source := storageVideoSource{Schema: "storage-source/v1", StorageInstallID: 16, ProjectID: "test-proj", FileID: 7,
		ContentRevision: "approved-v1", SizeBytes: size, ContentType: "video/mp4",
		Checksum: storageVideoChecksum{Algorithm: "sha256", Value: hex.EncodeToString(hash.Sum(nil)), Status: "verified"}}
	f := &rangeVideoFixture{platform: pf, ctx: ctx, source: source}
	f.item = mediaItem{ID: 7, Bytes: size, Mime: "video/mp4", SHA256: source.Checksum.Value, MediaProjectID: "test-proj", StorageInstallID: 16, Source: &source}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gets.Add(1)
		var start, end int64
		if n, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || n != 2 || start < 0 || end < start || end >= size || end-start+1 > 4<<20 {
			t.Errorf("unbounded or invalid Storage request: %q", r.Header.Get("Range"))
			w.WriteHeader(416)
			return
		}
		if r.Header.Get("X-Storage-Expected-Content-Revision") != "approved-v1" {
			t.Error("source revision was not sent")
		}
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		for previous := f.peakHeap.Load(); memory.HeapAlloc > previous; previous = f.peakHeap.Load() {
			if f.peakHeap.CompareAndSwap(previous, memory.HeapAlloc) {
				break
			}
		}
		if response := f.response.Load(); response != nil && (*response)(w, r, start, end) {
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.Header().Set("X-Storage-Content-Revision", source.ContentRevision)
		w.Header().Set("X-Storage-File-ID", "7")
		// This validator deliberately differs from the verified SHA-256.
		w.Header().Set("ETag", `"backend-validator"`)
		w.WriteHeader(http.StatusPartialContent)
		n, _ := io.Copy(w, io.NewSectionReader(virtualVideo{size}, start, end-start+1))
		f.transferred.Add(n)
	}))
	t.Cleanup(f.server.Close)
	f.setMetadata(t, source)
	minted, _ := json.Marshal(map[string]any{"url": f.server.URL, "file_id": 7, "source": source})
	pf.callAppResponses["storage:files_get_url"] = minted
	return f
}

func (f *rangeVideoFixture) setMetadata(t *testing.T, source storageVideoSource) {
	t.Helper()
	metadata, err := json.Marshal(map[string]any{"id": 7, "content_type": "video/mp4", "size_bytes": source.SizeBytes, "sha256": source.Checksum.Value, "source": source})
	if err != nil {
		t.Fatal(err)
	}
	f.platform.callAppResponses["storage:files_get"] = metadata
}

func TestStorageRangeSourceIsSeekableAndBoundsRecoveryValidation(t *testing.T) {
	f := newRangeVideoFixture(t, 9<<20)
	// No usable staging directory or system temp directory is available.
	t.Setenv("APTEVA_DATA_DIR", "")
	t.Setenv("DB_PATH", "")
	t.Setenv("TMPDIR", "/unavailable-social-test-temp")
	app := &App{}
	p, err := app.prepareVideoSource(f.ctx, publishJob{mediaProjectID: "test-proj"}, f.item, "RECOVERY_VALIDATION")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.File != nil || p.source == nil || p.SHA256 != f.source.Checksum.Value {
		t.Fatal("verified Storage source was staged or its checksum was replaced by an ETag")
	}
	if f.gets.Load() != 1 || f.transferred.Load() > 4<<20 {
		t.Fatalf("recovery validation downloaded the whole video: requests=%d bytes=%d", f.gets.Load(), f.transferred.Load())
	}
	for _, offset := range []int64{7 << 20, 0, 8 << 20, 12} {
		actual := make([]byte, 12)
		want := make([]byte, 12)
		_, err := p.readerAt().ReadAt(actual, offset)
		_, _ = (virtualVideo{f.source.SizeBytes}).ReadAt(want, offset)
		if err != nil || !bytes.Equal(actual, want) {
			t.Fatalf("reopen at %d: %v", offset, err)
		}
		if cap(p.source.cache) > 4<<20 {
			t.Fatal("range buffer exceeded configured chunk size")
		}
	}
	for _, call := range f.platform.callAppCalls {
		if call.Tool == "files_get_url" && call.Input["expected_content_revision"] != "approved-v1" {
			t.Fatal("URL omitted immutable binding")
		}
	}
}

func TestStorageRangeSourceRejectsChangedAndMalformedResponses(t *testing.T) {
	for _, scenario := range []string{"wrong_revision", "wrong_file", "wrong_range", "wrong_size", "truncated", "ignored_range", "changed_metadata", "changed_renewal"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRangeVideoFixture(t, 24)
			f.setResponse(func(w http.ResponseWriter, r *http.Request, start, end int64) bool {
				if scenario == "changed_metadata" || scenario == "changed_renewal" {
					return false
				}
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("X-Storage-Content-Revision", "approved-v1")
				w.Header().Set("X-Storage-File-ID", "7")
				w.Header().Set("Content-Range", "bytes 0-23/24")
				w.Header().Set("Content-Length", "24")
				switch scenario {
				case "wrong_revision":
					w.Header().Set("X-Storage-Content-Revision", "replacement")
				case "wrong_file":
					w.Header().Set("X-Storage-File-ID", "8")
				case "wrong_range":
					w.Header().Set("Content-Range", "bytes 1-24/24")
				case "wrong_size":
					w.Header().Set("Content-Range", "bytes 0-23/25")
				case "ignored_range":
					w.WriteHeader(200)
					w.Write(testMP4)
					return true
				}
				w.WriteHeader(206)
				if scenario == "truncated" {
					w.Write(testMP4[:12])
				} else {
					w.Write(testMP4)
				}
				return true
			})
			if scenario == "changed_metadata" {
				changed := f.source
				changed.ContentRevision = "replacement"
				f.setMetadata(t, changed)
			}
			if scenario == "changed_renewal" {
				changed := f.source
				changed.ContentRevision = "replacement"
				f.platform.callAppResponses["storage:files_get_url"], _ = json.Marshal(map[string]any{"url": f.server.URL, "file_id": 7, "source": changed})
			}
			p, err := (&App{}).prepareVideoSource(f.ctx, publishJob{mediaProjectID: "test-proj"}, f.item, "FILE_UPLOAD")
			if p != nil {
				p.Close()
			}
			if err == nil || len(f.platform.executeCalls) != 0 {
				t.Fatalf("invalid source reached publisher: %v", err)
			}
			files, _ := filepath.Glob(filepath.Join(f.ctx.DataDir(), "video-staging", "*.spool"))
			if len(files) > 0 {
				t.Fatal("invalid streaming proof silently fell back to staging")
			}
		})
	}
}

func TestStorageRangeURLRenewalKeepsExactSource(t *testing.T) {
	f := newRangeVideoFixture(t, 24)
	var expired atomic.Bool
	f.setResponse(func(w http.ResponseWriter, r *http.Request, start, end int64) bool {
		if !expired.Swap(true) {
			w.WriteHeader(403)
			return true
		}
		return false
	})
	p, err := (&App{}).prepareVideoSource(f.ctx, publishJob{mediaProjectID: "test-proj"}, f.item, "FILE_UPLOAD")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	urls := 0
	for _, call := range f.platform.callAppCalls {
		if call.Tool == "files_get_url" {
			urls++
			if call.Input["id"] != int64(7) || call.Input["expected_content_revision"] != "approved-v1" {
				t.Fatal(call)
			}
		}
	}
	if urls != 2 || f.gets.Load() != 2 {
		t.Fatal("URL renewal did not retain exact source")
	}
}

func TestStorageRangeVideoUploads2GBWithoutStaging(t *testing.T) {
	if testing.Short() {
		t.Skip("2 GB HTTP transfer acceptance test")
	}
	const size int64 = 2 << 30
	f := newRangeVideoFixture(t, size)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "unavailable"))
	t.Setenv("APTEVA_DATA_DIR", "")
	t.Setenv("DB_PATH", "")
	var init, uploads atomic.Int64
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads.Add(1)
		var start, end, total int64
		if n, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil || n != 3 || total != size || start != received.Load() {
			t.Errorf("invalid provider range: %q", r.Header.Get("Content-Range"))
			w.WriteHeader(400)
			return
		}
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil || n != end-start+1 {
			t.Errorf("incomplete provider body: %d %v", n, err)
			return
		}
		received.Add(n)
		if received.Load() == size {
			io.WriteString(w, `{"id":"two-gb-video"}`)
			return
		}
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", end))
		w.WriteHeader(308)
	}))
	defer server.Close()
	destination, _ := url.Parse(server.URL)
	old := youtubeVideoUploadClient
	youtubeVideoUploadClient = &http.Client{Transport: rewriteUploadTransport{http.DefaultTransport, destination}, Timeout: 5 * time.Minute}
	t.Cleanup(func() { youtubeVideoUploadClient = old })
	f.platform.executeResponses["upload_video_init"] = &sdk.ExecuteResult{Success: true, Status: 200, Headers: map[string]string{"Location": testYouTubeSession}}
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	p, err := (&App{}).prepareVideoSource(f.ctx, publishJob{mediaProjectID: "test-proj"}, f.item, "FILE_UPLOAD")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	id, err := (&App{}).uploadPreparedYouTube(f.ctx, platforms["youtube"], publishJob{connID: 42}, p, map[string]any{"upload_content_length": size})
	for _, call := range f.platform.executeCalls {
		if call.Tool == "upload_video_init" {
			init.Add(1)
		}
	}
	if err != nil || id != "two-gb-video" || received.Load() != size || init.Load() != 1 || p.File != nil {
		t.Fatalf("upload id=%s bytes=%d initializations=%d err=%v", id, received.Load(), init.Load(), err)
	}
	if cap(p.source.cache) > 4<<20 || f.peakHeap.Load() > baseline.HeapAlloc+(96<<20) {
		t.Fatalf("unbounded upload memory: cache=%d baseline=%d peak=%d", cap(p.source.cache), baseline.HeapAlloc, f.peakHeap.Load())
	}
	t.Logf("Uploaded %d bytes with one initialization, %d provider chunks, %d Storage ranges, %d-byte source buffer, peak Go heap %d bytes", received.Load(), uploads.Load(), f.gets.Load(), cap(p.source.cache), f.peakHeap.Load())
}

func TestVideoStagingPrunesOnlyUnlockedOwnedOrphans(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	create := func(name string) *os.File {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f.Name(), old, old); err != nil {
			t.Fatal(err)
		}
		return f
	}
	stale := create("social-video-stale.spool")
	stale.Close()
	active := create("social-video-active.spool")
	defer active.Close()
	if err := lockVideoStaging(active); err != nil {
		t.Fatal(err)
	}
	unrelated := create("other-data")
	unrelated.Close()
	pruneVideoStaging(dir)
	if _, err := os.Stat(stale.Name()); !os.IsNotExist(err) {
		t.Fatal("owned orphan retained")
	}
	for _, path := range []string{active.Name(), unrelated.Name()} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("active or unrelated file removed: %s %v", path, err)
		}
	}
}

func TestStorageStreamingYouTubeResumesSameTargetAndSession(t *testing.T) {
	f := newRangeVideoFixture(t, 24)
	var puts, probes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Range") == "bytes */24" {
			probes.Add(1)
			w.Header().Set("Range", "bytes=0-11")
			w.WriteHeader(308)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if puts.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("Content-Range") != "bytes 12-23/24" || !bytes.Equal(body, testMP4[12:]) {
			t.Errorf("resumed the wrong source range: %q %q", r.Header.Get("Content-Range"), body)
		}
		io.WriteString(w, `{"id":"same-video"}`)
	}))
	defer server.Close()
	destination, _ := url.Parse(server.URL)
	old := youtubeVideoUploadClient
	youtubeVideoUploadClient = &http.Client{Transport: rewriteUploadTransport{http.DefaultTransport, destination}}
	t.Cleanup(func() { youtubeVideoUploadClient = old })
	f.platform.executeResponses["upload_video_init"] = &sdk.ExecuteResult{Success: true, Status: 200, Headers: map[string]string{"Location": testYouTubeSession}}
	result, err := f.ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj','youtube',42,'Channel','active')`)
	if err != nil {
		t.Fatal(err)
	}
	account, _ := result.LastInsertId()
	app := &App{}
	out, err := app.toolPostCreate(f.ctx, map[string]any{"mode": "publish", "body": "Original", "social_account_ids": []any{account}, "media_storage_ids": []any{int64(7)}})
	if err != nil {
		t.Fatal(err)
	}
	post := out.(map[string]any)["post_id"].(int64)
	var target int64
	var session string
	if err := f.ctx.AppDB().QueryRow(`SELECT id,youtube_upload_session FROM post_targets WHERE post_id=?`, post).Scan(&target, &session); err != nil {
		t.Fatal(err)
	}
	if session != testYouTubeSession {
		t.Fatal("existing session was lost")
	}
	if _, err := app.toolPostRetry(f.ctx, map[string]any{"post_id": post, "target_id": target}); err != nil {
		t.Fatal(err)
	}
	initializations := 0
	for _, call := range f.platform.executeCalls {
		if call.Tool == "upload_video_init" {
			initializations++
		}
	}
	var rows int
	f.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE post_id=?`, post).Scan(&rows)
	targets := app.loadTargets(f.ctx, post)
	if rows != 1 || len(targets) != 1 || targets[0]["id"] != target || targets[0]["platform_post_id"] != "same-video" || initializations != 1 || probes.Load() != 1 || puts.Load() != 2 {
		t.Fatalf("delivery was duplicated or not resumed: targets=%+v initializations=%d probes=%d puts=%d", targets, initializations, probes.Load(), puts.Load())
	}
	d := loadDeliveryDiagnostics(f.ctx, target)
	if d.Source == nil || *d.Source != f.source || d.AcknowledgedBytes == nil || *d.AcknowledgedBytes != 24 {
		t.Fatalf("confirmed offset or source binding lost: %+v", d)
	}
}

func TestStorageStreamingPromotesLegacySessionWithoutReinitializing(t *testing.T) {
	f := newRangeVideoFixture(t, 9<<20)
	app := &App{}
	p, err := app.prepareVideoSource(f.ctx, publishJob{mediaProjectID: "test-proj"}, f.item, "FILE_UPLOAD")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	accountResult, err := f.ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj','youtube',42,'Channel','active')`)
	if err != nil {
		t.Fatal(err)
	}
	account, _ := accountResult.LastInsertId()
	postResult, err := f.ctx.AppDB().Exec(`INSERT INTO posts(project_id,body,status) VALUES('test-proj','Original','failed')`)
	if err != nil {
		t.Fatal(err)
	}
	post, _ := postResult.LastInsertId()
	input := map[string]any{"upload_content_length": f.source.SizeBytes}
	legacy := sha256.New()
	io.Copy(legacy, io.NewSectionReader(virtualVideo{f.source.SizeBytes}, 0, f.source.SizeBytes))
	metadata, _ := json.Marshal(input)
	fmt.Fprintf(legacy, "\n%d\n%s", int64(42), metadata)
	oldFingerprint := hex.EncodeToString(legacy.Sum(nil))
	targetResult, err := f.ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status,youtube_upload_session,youtube_upload_fingerprint) VALUES(?,?,'failed',?,?)`, post, account, testYouTubeSession, oldFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := targetResult.LastInsertId()
	job := publishJob{targetID: target, connID: 42}
	current, err := prepareYouTubeFingerprint(f.ctx, job, p, input, testYouTubeSession, oldFingerprint)
	if err != nil || current == oldFingerprint {
		t.Fatalf("legacy source was not verified and promoted: %s %v", current, err)
	}
	var retainedSession, stored string
	f.ctx.AppDB().QueryRow(`SELECT youtube_upload_session,youtube_upload_fingerprint FROM post_targets WHERE id=?`, target).Scan(&retainedSession, &stored)
	if retainedSession != testYouTubeSession || stored != current || len(f.platform.executeCalls) != 0 {
		t.Fatal("session replaced or provider initialized")
	}
	before := f.gets.Load()
	if _, err := prepareYouTubeFingerprint(f.ctx, job, p, input, retainedSession, stored); err != nil || f.gets.Load() != before {
		t.Fatal("promoted session performed another whole-file read", err)
	}
}

func TestStorageStreamingTikTokUsesSameBoundedReader(t *testing.T) {
	f := newRangeVideoFixture(t, 24)
	var uploads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, testMP4) || r.Header.Get("Content-Range") != "bytes 0-23/24" {
			t.Error("incorrect TikTok video bytes")
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	p, err := (&App{}).prepareVideoSource(f.ctx, publishJob{mediaProjectID: "test-proj"}, f.item, "FILE_UPLOAD")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	d := p.Diagnostics
	d.AttemptedBytes = bytePointer(0)
	d.AcknowledgedBytes = bytePointer(0)
	d.UploadURLSHA256 = uploadURLHash(server.URL)
	if err := uploadTikTokChunks(f.ctx, 0, p, &d, server.URL, 24, 1); err != nil {
		t.Fatal(err)
	}
	if uploads.Load() != 1 || *d.AcknowledgedBytes != 24 || p.File != nil {
		t.Fatal("TikTok did not consume the streaming source")
	}
}

func TestStorageUnverifiedProofUsesFullDiskVerification(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	configureTestVideo(t, pf, 7, 24)
	item := testSourceItem()
	hash := sha256.Sum256(testMP4)
	item.SHA256 = hex.EncodeToString(hash[:])
	item.Source = &storageVideoSource{Schema: "storage-source/v1", Checksum: storageVideoChecksum{Algorithm: "sha256", Status: "pending"}}
	p, err := (&App{}).prepareVideoSource(ctx, publishJob{mediaProjectID: "test-proj"}, item, "FILE_UPLOAD")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.File == nil || p.source != nil || p.SHA256 != item.SHA256 || p.Diagnostics.Source != nil {
		t.Fatal("unverified descriptor bypassed full checksum staging")
	}
}

func TestStorageStreamingRejectsSourceChangeAfterRecoveryAuthorization(t *testing.T) {
	f := newRangeVideoFixture(t, 24)
	accountResult, err := f.ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj','youtube',42,'Channel','active')`)
	if err != nil {
		t.Fatal(err)
	}
	account, _ := accountResult.LastInsertId()
	postResult, err := f.ctx.AppDB().Exec(`INSERT INTO posts(project_id,body,status) VALUES('test-proj','Original','failed')`)
	if err != nil {
		t.Fatal(err)
	}
	post, _ := postResult.LastInsertId()
	targetResult, err := f.ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status,attempts) VALUES(?,?,'publishing',2)`, post, account)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := targetResult.LastInsertId()
	approved := f.source
	approved.ContentRevision = "different-approved-revision"
	evidence, _ := json.Marshal(map[string]any{"source_sha256": f.source.Checksum.Value, "source": approved})
	_, err = f.ctx.AppDB().Exec(`INSERT INTO delivery_recovery_requests(id,project_id,post_id,target_id,source_attempt,revision,fingerprint,decision,status,evidence,actor,expires_at) VALUES('source-grant','test-proj',?,?,1,1,'proof','resume','consumed',?,'test',datetime('now','+1 hour'))`, post, target, string(evidence))
	if err != nil {
		t.Fatal(err)
	}
	p, err := (&App{}).prepareVideoSource(f.ctx, publishJob{targetID: target, mediaProjectID: "test-proj"}, f.item, "FILE_UPLOAD")
	if p != nil {
		p.Close()
	}
	if sourceErrorCode(err) != "source_changed" || len(f.platform.executeCalls) != 0 {
		t.Fatalf("changed authorized object was accepted: %v", err)
	}
}
