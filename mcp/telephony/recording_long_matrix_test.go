package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type recordingStorageFixture struct {
	t        *testing.T
	root     string
	mu       sync.Mutex
	next     int64
	files    map[int64]string
	uploads  map[string]*recordingFixtureUpload
	badHash  bool
	badSize  bool
	tooLarge bool
	partSize int64
}
type recordingFixtureUpload struct {
	path, sha string
	size, id  int64
}

func newRecordingStorageFixture(t *testing.T) *recordingStorageFixture {
	return &recordingStorageFixture{t: t, root: t.TempDir(), files: map[int64]string{}, uploads: map[string]*recordingFixtureUpload{}, partSize: 8 << 20}
}
func (s *recordingStorageFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test" || r.URL.Query().Get("project_id") != "project-a" {
		http.Error(w, "private", 403)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, storageRecordingProxyPath)
	if path == "/health" {
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if path == "/uploads" && r.Method == "POST" {
		if s.tooLarge {
			http.Error(w, "file too large", 413)
			return
		}
		var meta struct {
			Size        int64  `json:"size"`
			SHA         string `json:"sha256"`
			Visibility  string `json:"visibility"`
			Filename    string `json:"filename"`
			ContentType string `json:"content_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&meta); err != nil || meta.Visibility != "private" || meta.Size <= 0 {
			s.t.Error("incorrect private upload contract", meta, err)
			http.Error(w, "bad", 400)
			return
		}
		if (strings.HasSuffix(meta.Filename, ".mp3") && meta.ContentType != "audio/mpeg") || (strings.HasSuffix(meta.Filename, ".wav") && meta.ContentType != "audio/wav") {
			s.t.Error("incorrect upload MIME", meta)
		}
		file, err := os.CreateTemp(s.root, "stored-")
		if err != nil {
			s.t.Error(err)
			http.Error(w, "disk", 500)
			return
		}
		file.Close()
		s.mu.Lock()
		s.next++
		id := s.next
		upload := strconv.FormatInt(id, 10)
		s.uploads[upload] = &recordingFixtureUpload{path: file.Name(), sha: meta.SHA, size: meta.Size, id: id}
		s.mu.Unlock()
		writeJSON(w, map[string]any{"upload_id": upload, "part_size": s.partSize, "max_parts": 2048})
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "uploads" {
		s.mu.Lock()
		up := s.uploads[parts[1]]
		s.mu.Unlock()
		if up == nil {
			http.NotFound(w, r)
			return
		}
		if r.Method == "DELETE" {
			os.Remove(up.path)
			writeJSON(w, map[string]any{"ok": true})
			return
		}
		if len(parts) == 4 && parts[2] == "parts" && r.Method == "PUT" {
			part, _ := strconv.ParseInt(parts[3], 10, 64)
			offset := (part - 1) * s.partSize
			size := min(s.partSize, up.size-offset)
			if part < 1 || size <= 0 || r.ContentLength != size {
				s.t.Error("part not streamed with correct length", part, r.ContentLength, size)
				http.Error(w, "bad", 400)
				return
			}
			file, err := os.OpenFile(up.path, os.O_WRONLY, 0600)
			if err != nil {
				s.t.Error(err)
				http.Error(w, "disk", 500)
				return
			}
			defer file.Close()
			file.Seek(offset, io.SeekStart)
			written, err := io.Copy(file, io.LimitReader(r.Body, size+1))
			if err != nil || written != size {
				s.t.Error("incomplete part", written, size, err)
				http.Error(w, "size", 400)
				return
			}
			writeJSON(w, map[string]any{"ok": true})
			return
		}
		if len(parts) == 3 && parts[2] == "complete" {
			sha, err := fileSHA256(up.path)
			info, statErr := os.Stat(up.path)
			if err != nil || statErr != nil || info.Size() != up.size || sha != up.sha {
				s.t.Error("server size/hash mismatch", err, statErr)
				http.Error(w, "mismatch", 400)
				return
			}
			var completion struct {
				SHA string `json:"sha256"`
			}
			json.NewDecoder(r.Body).Decode(&completion)
			if completion.SHA != sha {
				s.t.Error("completion hash mismatch")
			}
			s.mu.Lock()
			s.files[up.id] = up.path
			s.mu.Unlock()
			size := info.Size()
			if s.badHash {
				sha = strings.Repeat("0", 64)
			}
			if s.badSize {
				size++
			}
			writeJSON(w, map[string]any{"file": storedRecordingFile{ID: up.id, SizeBytes: size, SHA256: sha}})
			return
		}
	}
	if len(parts) == 3 && parts[0] == "files" && parts[2] == "content" {
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		s.content(w, r, id)
		return
	}
	http.NotFound(w, r)
}
func (s *recordingStorageFixture) content(w http.ResponseWriter, r *http.Request, id int64) {
	s.mu.Lock()
	path := s.files[id]
	s.mu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "recording.wav", info.ModTime(), f)
}
func captureAcceleratedRecording(t *testing.T, app *App, direction string, minutes int) (*recordingRow, string) {
	t.Helper()
	call := testCall(fmt.Sprintf("record-%s-%d-%s", direction, minutes, newCallID()), "completed")
	call.ProjectID = "project-a"
	call.Direction = direction
	call.IngressPath = inboundTransportSIPDirect
	call.CarrierSlug = "telnyx"
	call.AgentID = 7
	call.RecordingMode = recordingModeAlways
	call.RecordingChannels = "dual"
	call.RecordingStorageMode = recordingStorageCopy
	if err := app.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	recorder, err := app.newLocalRecording(&call, false)
	if err != nil {
		t.Fatal(err)
	}
	path := recorder.file.Name()
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(path)) })
	duration := time.Duration(minutes) * time.Minute
	for _, offset := range []time.Duration{0, duration / 2, duration - 20*time.Millisecond} {
		for direction := 0; direction < 2; direction++ {
			samples := make([]int16, 480)
			for i := range samples {
				samples[i] = int16(1500 * (direction + 1))
			}
			recorder.queue <- localRecordingFrame{direction: direction, pcm: samples, at: recorder.started.Add(offset)}
		}
	}
	recorder.Close()
	row, err := app.db().findRecording(recorder.row.ID)
	if err != nil || row.ProviderStatus != "completed" || row.DurationMS != duration.Milliseconds() || row.SizeBytes != 44+int64(minutes*60)*96000 {
		t.Fatal("capture did not preserve duration", row, err)
	}
	return row, path
}
func clearPlaybackForRecording(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		busy := false
		playbackCache.Lock()
		for key, e := range playbackCache.entries {
			if strings.Contains(key, ":"+id+":") {
				if e.readers > 0 {
					busy = true
				} else {
					removeRecordingCacheEntry(key, e)
				}
			}
		}
		playbackCache.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("playback reader did not close")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestLongSIPRecordingImportAndPlaybackMatrix(t *testing.T) {
	if os.Getenv("TELEPHONY_LONG_RECORDING_TEST") != "1" {
		t.Skip("opt-in disk/CPU test of 30/60/120-minute recording files")
	}
	var results []map[string]any
	for _, direction := range []string{"inbound", "outbound"} {
		for _, minutes := range []int{30, 60, 120} {
			t.Run(fmt.Sprintf("%s-%d-minute", direction, minutes), func(t *testing.T) {
				started := time.Now()
				app, ctx := withTelephonyTestContext(t, &answerPlatform{})
				settings := defaultRecordingSettings("project-a")
				settings.StorageFormat = "wav" // retain coverage of the lossless opt-in path
				if err := app.db().saveRecordingSettings(settings); err != nil {
					t.Fatal(err)
				}
				rec, original := captureAcceleratedRecording(t, app, direction, minutes)
				if recordingPublic(*rec)["storage_state_label"] != "Recorded locally" {
					t.Fatal("local capture state missing")
				}
				fixture := newRecordingStorageFixture(t)
				storage := httptest.NewServer(fixture)
				defer storage.Close()
				t.Setenv("APTEVA_GATEWAY_URL", storage.URL)
				t.Setenv("APTEVA_OUTBOUND_TOKEN", "test")
				claim, err := app.db().claimRecordingImport("project-a")
				if err != nil || claim == nil {
					t.Fatal(claim, err)
				}
				if err := app.importRecording(t.Context(), ctx, claim); err != nil {
					t.Fatal(err)
				}
				stored, err := app.db().findRecording(rec.ID)
				if err != nil || stored.StorageStatus != "stored" || stored.SizeBytes != rec.SizeBytes {
					t.Fatal("not stored", stored, err)
				}
				if _, err = os.Stat(original); err != nil {
					t.Fatal("copy mode lost original", err)
				}
				if recordingPublic(*stored)["storage_state_label"] != "Stored successfully" {
					t.Fatal("stored state missing")
				}
				mux := http.NewServeMux()
				for _, route := range app.HTTPRoutes() {
					mux.HandleFunc(route.Pattern, route.Handler)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer operator" {
						http.Error(w, "gateway authentication required", 401)
						return
					}
					if strings.HasPrefix(r.URL.Path, "/api/apps/storage/files/") {
						if r.URL.Query().Get("project_id") != "project-a" {
							http.Error(w, "private", 403)
							return
						}
						id, _ := strconv.ParseInt(strings.Split(strings.TrimPrefix(r.URL.Path, "/api/apps/storage/files/"), "/")[0], 10, 64)
						fixture.content(w, r, id)
						return
					}
					mux.ServeHTTP(w, r)
				}))
				defer server.Close()
				root, _ := recordingTemporaryRoot()
				before, _ := os.ReadDir(root)
				runtime.GC()
				var memBefore, memAfter runtime.MemStats
				runtime.ReadMemStats(&memBefore)
				for _, variant := range []string{"original", "mix", "caller", "agent"} {
					channels := 1
					value := int16(6000)
					if variant == "original" {
						channels = 2
						value = 1500
					}
					if variant == "mix" {
						value = 12000
					}
					frameOffset := int64(minutes*60/2)*24000*int64(channels*2) + 44
					endOffset := int64(minutes*60)*24000*int64(channels*2) + 44 - 480*int64(channels*2)
					req, _ := http.NewRequest("GET", server.URL+"/recordings/"+rec.ID+"/content?project_id=project-a&variant="+variant, nil)
					req.Header.Set("Authorization", "Bearer operator")
					req.Header.Set("Range", fmt.Sprintf("bytes=0-47,%d-%d,%d-%d", frameOffset, frameOffset+3, endOffset, endOffset+3))
					resp, err := server.Client().Do(req)
					if err != nil {
						t.Fatal(variant, err)
					}
					if resp.StatusCode != 206 {
						body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
						resp.Body.Close()
						t.Fatal(variant, resp.StatusCode, string(body))
					}
					_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
					if err != nil {
						t.Fatal(err)
					}
					reader := multipart.NewReader(resp.Body, params["boundary"])
					for section := 0; section < 3; section++ {
						part, err := reader.NextPart()
						if err != nil {
							t.Fatal(variant, section, err)
						}
						data, err := io.ReadAll(io.LimitReader(part, 4096))
						if err != nil {
							t.Fatal(err)
						}
						if section == 0 {
							if len(data) != 48 || string(data[:4]) != "RIFF" || binary.LittleEndian.Uint16(data[22:24]) != uint16(channels) {
								t.Fatal("header/format", variant, data)
							}
							data = data[44:]
						}
						if len(data) != 4 || int16(binary.LittleEndian.Uint16(data[:2])) != value {
							t.Fatal("incorrect seek/channel PCM", variant, section, data, value)
						}
						if variant == "original" && int16(binary.LittleEndian.Uint16(data[2:])) != 3000 {
							t.Fatal("lost adviser channel")
						}
					}
					resp.Body.Close()
				}
				runtime.ReadMemStats(&memAfter)
				allocated := memAfter.TotalAlloc - memBefore.TotalAlloc
				if allocated > 128<<20 {
					t.Fatalf("playback allocated %d bytes; appears file-sized instead of streaming", allocated)
				}
				req, _ := http.NewRequest("GET", server.URL+"/recordings/"+rec.ID+"/content?project_id=other", nil)
				req.Header.Set("Authorization", "Bearer operator")
				resp, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != 403 {
					t.Fatal("cross-project recording disclosed", resp.StatusCode)
				}
				identity := phoneTestIdentity("alice")
				if w := phoneTestRequest(app, &identity, "GET", "/recordings/"+rec.ID+"/content", nil); w.Code < 400 {
					t.Fatal("application-user bypassed private recording authorization", w.Code)
				}
				other := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 8})
				out, err := app.toolRecordingGet(other, ctx, map[string]any{"recording_id": rec.ID})
				if err != nil || out.(map[string]any)["isError"] != true {
					t.Fatal("other agent obtained recording", out, err)
				}
				clearPlaybackForRecording(t, rec.ID)
				after, _ := os.ReadDir(root)
				if len(before) != len(after) || len(playbackCache.slots) != 0 {
					t.Fatal("temporary playback/import files or permits leaked", len(before), len(after), len(playbackCache.slots))
				}
				results = append(results, map[string]any{"direction": direction, "minutes": minutes, "original_bytes": rec.SizeBytes, "storage_verified": true, "variants": "original/mix/caller/agent", "seeks": "start/middle/end", "private_access": true, "temporary_cleanup": true, "playback_total_allocated_bytes": allocated, "elapsed_seconds": time.Since(started).Seconds()})
				t.Logf("%s %dm: original %d bytes, all variants/seeks/private access/cleanup passed; playback allocated %.1f MiB", direction, minutes, rec.SizeBytes, float64(allocated)/(1<<20))
			})
		}
	}
	if path := os.Getenv("TELEPHONY_LONG_RECORDING_OUTPUT"); path != "" {
		data, _ := json.MarshalIndent(map[string]any{"scope": "accelerated actual SIP recorder with sparse PCM markers at start/middle/end; HTTP Storage substitute and production import/playback; no carrier calls", "results": results}, "", "  ")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestLocalOriginalSurvivesStorageVerificationFailure(t *testing.T) {
	for _, kind := range []string{"hash", "size", "413"} {
		t.Run(kind, func(t *testing.T) {
			app, ctx := withTelephonyTestContext(t, &answerPlatform{})
			rec, path := captureAcceleratedRecording(t, app, "outbound", 1)
			ctx.AppDB().Exec(`UPDATE calls SET recording_storage_mode=? WHERE id=?`, recordingStorageMove, rec.CallID)
			fixture := newRecordingStorageFixture(t)
			fixture.badHash = kind == "hash"
			fixture.badSize = kind == "size"
			fixture.tooLarge = kind == "413"
			fixture.partSize = 1 << 30 // proves negotiated parts do not allocate gigabyte buffers
			server := httptest.NewServer(fixture)
			defer server.Close()
			t.Setenv("APTEVA_GATEWAY_URL", server.URL)
			t.Setenv("APTEVA_OUTBOUND_TOKEN", "test")
			claim, err := app.db().claimRecordingImport("project-a")
			if err != nil || claim == nil {
				t.Fatal(claim, err)
			}
			err = app.importRecording(t.Context(), ctx, claim)
			if err == nil {
				t.Fatal("bad stored metadata accepted")
			}
			app.db().failRecordingImport(rec.ID, err, claim.ImportStartedAt)
			if _, err = os.Stat(path); err != nil {
				t.Fatal("unverified upload destroyed local original", err)
			}
			failed, _ := app.db().findRecording(rec.ID)
			if failed.StorageStatus != "failed" || failed.StorageFileID != 0 || failed.ProviderDeletedAt != "" {
				t.Fatal(failed)
			}
			if kind == "413" {
				if next, err := app.db().claimRecordingImport("project-a"); err != nil || next != nil {
					t.Fatal("automatic retry after actual 413", next, err)
				}
			}
		})
	}
}
