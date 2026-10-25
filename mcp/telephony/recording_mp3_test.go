package main

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeMP3TestWAV(t *testing.T, channels, rate, seconds int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "speech.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	size := int64(rate * seconds * channels * 2)
	if channels == 2 {
		err = writeDualLocalWAVHeader(f, rate, size)
	} else {
		err = writeMonoWAVHeader(f, rate, size)
	}
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, rate*channels*2)
	for i := 0; i < rate; i++ {
		for c := 0; c < channels; c++ {
			frequency := float64(440 + c*660)
			value := int16(10000 * math.Sin(2*math.Pi*frequency*float64(i)/float64(rate)))
			binary.LittleEndian.PutUint16(buf[(i*channels+c)*2:], uint16(value))
		}
	}
	for i := 0; i < seconds; i++ {
		if _, err = f.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestRecordingMP3RoundTrip(t *testing.T) {
	for _, rate := range []int{8000, 24000, 48000} {
		for _, channels := range []int{1, 2} {
			t.Run(fmt.Sprintf("%d-%d", rate, channels), func(t *testing.T) {
				source := writeMP3TestWAV(t, channels, rate, 2)
				start := time.Now()
				encoded, err := encodeRecordingMP3(t.Context(), source)
				if err != nil {
					t.Fatal(err)
				}
				defer os.Remove(encoded)
				if rate == 24000 && channels == 2 && os.Getenv("TELEPHONY_MP3_BROWSER_FIXTURE") != "" {
					data, err := os.ReadFile(encoded)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(os.Getenv("TELEPHONY_MP3_BROWSER_FIXTURE"), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				info, _ := os.Stat(encoded)
				if info.Size() > 40000 || info.Size() < 15000 {
					t.Fatalf("unexpected MP3 size %d", info.Size())
				}
				decoded, err := decodeRecordingMP3(t.Context(), encoded, channels)
				if err != nil {
					t.Fatal(err)
				}
				defer os.Remove(decoded)
				f, err := os.Open(decoded)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				wav, err := inspectPCM16WAV(f)
				if err != nil {
					t.Fatal(err)
				}
				duration := float64(wav.DataSize) / float64(wav.SampleRate*channels*2)
				if duration < 2 || duration > 2.3 || wav.Channels != channels || wav.SampleRate != max(rate, 16000) {
					t.Fatalf("bad duration/channels/rate %+v duration=%f", wav, duration)
				}
				// Spectral separation proves left/right are not mixed by MP3 conversion.
				data, err := io.ReadAll(f)
				if err != nil {
					t.Fatal(err)
				}
				for c := 0; c < channels; c++ {
					power := func(freq float64) float64 {
						var re, im float64
						for i := wav.SampleRate / 2; i < wav.SampleRate; i++ {
							v := float64(int16(binary.LittleEndian.Uint16(data[(i*channels+c)*2:])))
							angle := 2 * math.Pi * freq * float64(i) / float64(wav.SampleRate)
							re += v * math.Cos(angle)
							im += v * math.Sin(angle)
						}
						return re*re + im*im
					}
					if power(float64(440+c*660)) < power(float64(1100-c*660))*100 {
						t.Fatalf("channel %d lost separation", c)
					}
				}
				for _, variant := range []string{"mix", "caller", "agent"} {
					v, err := buildRecordingVariant(decoded, variant, t.Context())
					if channels == 1 && variant != "mix" {
						if err == nil {
							os.Remove(v)
							t.Fatal("mono accepted channel variant")
						}
						continue
					}
					if err != nil {
						t.Fatal(err)
					}
					os.Remove(v)
				}
				t.Logf("2s source, MP3 %d bytes, conversion/decoding %s", info.Size(), time.Since(start))
			})
		}
	}
}

func TestRecordingMP3DefaultsAndImport(t *testing.T) {
	for _, format := range []string{"mp3", "wav"} {
		for _, mode := range []string{recordingStorageCopy, recordingStorageMove} {
			t.Run(format+"-"+mode, func(t *testing.T) {
				app, ctx := withTelephonyTestContext(t, &answerPlatform{})
				settings, err := app.db().recordingSettings("project-a")
				if err != nil || settings.StorageFormat != "mp3" {
					t.Fatal(settings, err)
				}
				settings.StorageFormat = format
				if err = app.db().saveRecordingSettings(settings); err != nil {
					t.Fatal(err)
				}
				settings, err = app.db().recordingSettings("project-a")
				if err != nil || settings.StorageFormat != format {
					t.Fatal(settings, err)
				}
				rec, original := captureAcceleratedRecording(t, app, "outbound", 1)
				if _, err = ctx.AppDB().Exec(`UPDATE calls SET recording_storage_mode=? WHERE id=?`, mode, rec.CallID); err != nil {
					t.Fatal(err)
				}
				fixture := newRecordingStorageFixture(t)
				server := httptest.NewServer(fixture)
				defer server.Close()
				t.Setenv("APTEVA_GATEWAY_URL", server.URL)
				t.Setenv("APTEVA_OUTBOUND_TOKEN", "test")
				claim, err := app.db().claimRecordingImport("project-a")
				if err != nil || claim == nil {
					t.Fatal(claim, err)
				}
				if err = app.importRecording(t.Context(), ctx, claim); err != nil {
					t.Fatal(err)
				}
				stored, err := app.db().findRecording(rec.ID)
				if err != nil {
					t.Fatal(err)
				}
				if stored.StorageStatus != "stored" || stored.StorageFormat != format || stored.Format != "wav" {
					t.Fatal(stored)
				}
				if format == "mp3" && stored.SizeBytes > rec.SizeBytes/4 {
					t.Fatal("no storage saving", stored.SizeBytes, rec.SizeBytes)
				}
				_, statErr := os.Stat(original)
				if mode == recordingStorageCopy && statErr != nil {
					t.Fatal("lost retained WAV", statErr)
				}
				if stored.SourceSizeBytes != rec.SizeBytes {
					t.Fatal("source size was replaced by compressed size", stored)
				}
				if mode == recordingStorageMove && !os.IsNotExist(statErr) {
					t.Fatal("verified move retained WAV", statErr)
				}
				clearPlaybackForRecording(t, stored.ID)
				defer clearPlaybackForRecording(t, stored.ID)
				for _, variant := range []string{"original", "mix", "caller", "agent"} {
					req := httptest.NewRequest("GET", "/?variant="+variant, nil)
					req.Header.Set("Range", "bytes=0-43")
					w := httptest.NewRecorder()
					app.serveRecordingContent(w, req, ctx, stored)
					if variant == "original" {
						if w.Code != 307 {
							t.Fatal(w.Code, w.Body.String())
						}
						continue
					}
					if w.Code != http.StatusPartialContent || w.Header().Get("Cache-Control") != "private, no-store" || w.Body.String()[:4] != "RIFF" {
						t.Fatal(variant, w.Code, w.Body.String())
					}
				}
			})
		}
	}
}

func TestRecordingMP3FailureAndCancellation(t *testing.T) {
	source := writeMP3TestWAV(t, 2, 24000, 2)
	if path, _, err := prepareRecordingStorage(t.Context(), source, "wav", "mp3", 1); err == nil {
		os.Remove(path)
		t.Fatal("mismatched channel metadata accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if p, err := encodeRecordingMP3(ctx, source); err == nil {
		os.Remove(p)
		t.Fatal("cancelled encoding succeeded")
	}
	root, _ := recordingTemporaryRoot()
	before, _ := os.ReadDir(root)
	longSource := writeMP3TestWAV(t, 2, 24000, 60)
	running, stop := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer stop()
	if p, err := encodeRecordingMP3(running, longSource); err == nil {
		os.Remove(p)
		t.Fatal("in-flight cancellation ignored")
	}
	after, _ := os.ReadDir(root)
	if len(before) != len(after) {
		t.Fatal("cancelled conversion leaked temporary files")
	}
	limited := context.WithValue(t.Context(), playbackDownloadContextKey{}, int64(100))
	if p, err := encodeRecordingMP3(limited, source); err == nil {
		os.Remove(p)
		t.Fatal("size limit ignored")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("original lost", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.wav")
	os.WriteFile(bad, []byte("bad"), 0600)
	if p, err := encodeRecordingMP3(t.Context(), bad); err == nil {
		os.Remove(p)
		t.Fatal("invalid WAV accepted")
	}
	if err := validateRecordingSettings(recordingSettings{StorageFormat: "bad"}); err == nil {
		t.Fatal("invalid format accepted")
	}
}

func TestLongSIPMP3RecordingMatrix(t *testing.T) {
	if os.Getenv("TELEPHONY_LONG_RECORDING_TEST") != "1" {
		t.Skip("opt-in 30/60/120-minute MP3 disk/CPU matrix")
	}
	for _, direction := range []string{"inbound", "outbound"} {
		for _, minutes := range []int{30, 60, 120} {
			t.Run(fmt.Sprintf("%s-%d", direction, minutes), func(t *testing.T) {
				started := time.Now()
				app, ctx := withTelephonyTestContext(t, &answerPlatform{})
				rec, original := captureAcceleratedRecording(t, app, direction, minutes)
				fixture := newRecordingStorageFixture(t)
				server := httptest.NewServer(fixture)
				defer server.Close()
				t.Setenv("APTEVA_GATEWAY_URL", server.URL)
				t.Setenv("APTEVA_OUTBOUND_TOKEN", "test")
				root, _ := recordingTemporaryRoot()
				before, _ := os.ReadDir(root)
				claim, err := app.db().claimRecordingImport("project-a")
				if err != nil || claim == nil {
					t.Fatal(claim, err)
				}
				if err = app.importRecording(t.Context(), ctx, claim); err != nil {
					t.Fatal(err)
				}
				stored, err := app.db().findRecording(rec.ID)
				if err != nil || stored.StorageStatus != "stored" || stored.StorageFormat != "mp3" {
					t.Fatal(stored, err)
				}
				if stored.SizeBytes > int64(minutes*60)*16100 || stored.SizeBytes < int64(minutes*60)*15900 {
					t.Fatal("unexpected compressed size", stored.SizeBytes)
				}
				if _, err = os.Stat(original); err != nil {
					t.Fatal("original removed", err)
				}
				for _, variant := range []string{"original", "mix", "caller", "agent"} {
					// One multi-range request also tests oversized transient files
					// without re-decoding the entire recording for each range.
					middle := int64(minutes*60/2)*48000 + 44
					if variant == "original" {
						middle = stored.SizeBytes / 2
					}
					req := httptest.NewRequest("GET", "/?variant="+variant, nil)
					req.Header.Set("Range", fmt.Sprintf("bytes=0-43,%d-%d,-44", middle, middle+43))
					w := httptest.NewRecorder()
					if variant == "original" {
						fixture.content(w, req, stored.StorageFileID)
					} else {
						app.serveRecordingContent(w, req, ctx, stored)
					}
					if w.Code != 206 || w.Header().Get("Cache-Control") != "private, no-store" {
						t.Fatal(variant, w.Code, w.Body.String())
					}
					_, params, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
					if err != nil {
						t.Fatal(err)
					}
					reader := multipart.NewReader(w.Body, params["boundary"])
					for partIndex := 0; partIndex < 3; partIndex++ {
						part, err := reader.NextPart()
						if err != nil {
							t.Fatal(err)
						}
						data, err := io.ReadAll(part)
						if err != nil || len(data) != 44 {
							t.Fatal(variant, partIndex, len(data), err)
						}
						if partIndex == 0 && variant != "original" {
							if string(data[:4]) != "RIFF" || binary.LittleEndian.Uint16(data[22:]) != 1 {
								t.Fatal("bad variant WAV", variant)
							}
							length, err := strconv.ParseInt(strings.Split(part.Header.Get("Content-Range"), "/")[1], 10, 64)
							if err != nil {
								t.Fatal(err)
							}
							duration := float64(length-44) / 48000
							if math.Abs(duration-float64(minutes*60)) > 0.2 {
								t.Fatal("duration changed", variant, duration)
							}
						}
					}
					clearPlaybackForRecording(t, stored.ID)
				}
				identity := phoneTestIdentity("unrelated-user")
				if w := phoneTestRequest(app, &identity, "GET", "/recordings/"+rec.ID+"/content", nil); w.Code < 400 {
					t.Fatal("private recording disclosed", w.Code)
				}
				after, _ := os.ReadDir(root)
				if len(before) != len(after) || len(playbackCache.slots) != 0 {
					t.Fatal("temporary file/permit leak", len(before), len(after), len(playbackCache.slots))
				}
				t.Logf("%s %dm: WAV=%d MP3=%d reduction=%.2f%%; all variants/seeks/private access/cleanup passed in %s", direction, minutes, rec.SizeBytes, stored.SizeBytes, 100*(1-float64(stored.SizeBytes)/float64(rec.SizeBytes)), time.Since(started))
			})
		}
	}
}

func TestRecordingMP3Migration(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE recording_settings(project_id TEXT); INSERT INTO recording_settings VALUES('existing'); CREATE TABLE recordings(format TEXT); INSERT INTO recordings VALUES('wav')`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/062_recording_mp3.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var format string
	if err = db.QueryRow(`SELECT storage_format FROM recording_settings`).Scan(&format); err != nil || format != "mp3" {
		t.Fatal(format, err)
	}
	if err = db.QueryRow(`SELECT storage_format FROM recordings`).Scan(&format); err != nil || format != "" {
		t.Fatal(format, err)
	}
	if got := recordingPlaybackFormat(&recordingRow{Format: "wav", StorageStatus: "stored", StorageFileID: 1}); got != "wav" {
		t.Fatal("legacy format changed", got)
	}
}

func TestRecordingMP3ConcurrentIsolation(t *testing.T) {
	source := writeMP3TestWAV(t, 2, 24000, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := encodeRecordingMP3(t.Context(), source)
			if err != nil {
				t.Error(err)
				return
			}
			defer os.Remove(path)
			f, err := decodeRecordingMP3(t.Context(), path, 2)
			if err != nil {
				t.Error(err)
				return
			}
			os.Remove(f)
		}()
	}
	wg.Wait()
}

func TestRecordingMP3ImportConversionFailureRetainsOriginal(t *testing.T) {
	app, ctx := withTelephonyTestContext(t, &answerPlatform{})
	rec, path := captureAcceleratedRecording(t, app, "inbound", 1)
	if _, err := ctx.AppDB().Exec(`UPDATE calls SET recording_storage_mode=? WHERE id=?`, recordingStorageMove, rec.CallID); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 44); err != nil {
		t.Fatal(err)
	}
	fixture := newRecordingStorageFixture(t)
	server := httptest.NewServer(fixture)
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "test")
	root, _ := recordingTemporaryRoot()
	before, _ := os.ReadDir(root)
	claim, err := app.db().claimRecordingImport("project-a")
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	err = app.importRecording(t.Context(), ctx, claim)
	if err == nil {
		t.Fatal("truncated recording accepted")
	}
	app.db().failRecordingImport(rec.ID, err, claim.ImportStartedAt)
	failed, err := app.db().findRecording(rec.ID)
	if err != nil || failed.StorageStatus != "failed" || failed.StorageFormat != "" || failed.StorageFileID != 0 {
		t.Fatal(failed, err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("failed conversion deleted original", err)
	}
	after, _ := os.ReadDir(root)
	if len(before) != len(after) || len(playbackCache.slots) != 0 {
		t.Fatal("failed conversion leaked files or permits")
	}
}
