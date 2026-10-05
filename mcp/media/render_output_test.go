package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderOutputNamesAcrossOperations(t *testing.T) {
	cases := []struct{ op, params, ext, name, want, ct string }{
		{"crop", `{"width":32,"height":32}`, ".png", "portrait", "portrait.png", "image/png"},
		{"resize", `{"width":32,"keep_aspect":true}`, ".jpg", "scaled", "scaled.jpg", "image/jpeg"},
		{"crop", `{"width":32,"height":32}`, ".png", "portrait.JPG", "portrait.JPG", "image/jpeg"},
		{"crop", `{"width":32,"height":32}`, "", "image.webp", "image.webp", "image/webp"},
		{"trim", `{"start_ms":0,"end_ms":1000}`, ".mov", "clip", "clip.mp4", "video/mp4"},
		{"concat", `{}`, "", "combined", "combined.mp4", "video/mp4"},
		{"transcode", `{"format":"webm"}`, ".mp4", "clip", "clip.webm", "video/webm"},
		{"audio_extract", `{"format":"mp3"}`, ".mp4", "sound", "sound.mp3", "audio/mpeg"},
		{"audio_filter", `{"mode":"volume","gain_db":-2}`, ".wav", "quiet", "quiet.wav", "audio/wav"},
		{"extract_frame", `{"at_ms":0}`, ".mp4", "portrait", "portrait.png", "image/png"},
		{"extract_reel", `{"start_ms":0,"end_ms":1000}`, ".mp4", "reel", "reel.mp4", "video/mp4"},
	}
	for _, c := range cases {
		t.Run(c.op+"/"+c.name, func(t *testing.T) {
			sources := []string{"1"}
			if c.op == "concat" {
				sources = append(sources, "2")
			}
			p, err := buildPlan(c.op, sources, json.RawMessage(c.params), c.name, c.ext)
			if err != nil {
				t.Fatal(err)
			}
			if p.Filename != c.want || p.ContentType != c.ct {
				t.Fatalf("plan=%+v want %s (%s)", p, c.want, c.ct)
			}
			if strings.HasPrefix(c.ct, "image/") && c.op != "extract_frame" && !argPair(p.Args, "-frames:v", "1") {
				t.Fatalf("missing image encoder flags: %v", p.Args)
			}
		})
	}
}

func TestRenderOutputRejectsUnsupportedAndConflictingFormats(t *testing.T) {
	for _, c := range []struct{ op, params, name string }{
		{"crop", `{"width":32,"height":32}`, "portrait.xyz"},
		{"crop", `{"width":32,"height":32}`, "portrait.heic"},
		{"resize", `{"width":32,"keep_aspect":true}`, "image.mp3"},
		{"audio_filter", `{"mode":"mute"}`, "sound.png"},
		{"extract_frame", `{"at_ms":0}`, "portrait.jpg"},
		{"extract_reel", `{"start_ms":0,"end_ms":1000}`, "clip.webm"},
		{"transcode", `{"format":"mp4"}`, "clip.webm"},
		{"transcode", `{"format":"bogus"}`, "clip"},
		{"audio_extract", `{"format":"mp3"}`, "sound.wav"},
	} {
		t.Run(c.op+"/"+c.name, func(t *testing.T) {
			_, err := buildPlan(c.op, []string{"1"}, json.RawMessage(c.params), c.name, ".png")
			if err == nil || renderFailureCode(err.Error()) != "invalid_output_format" {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestSubmitNormalizesPNGAndRejectsBeforeQueueing(t *testing.T) {
	app := newTestCtx(t)
	if err := upsertMedia(app.AppDB(), testProj, "1", sampleImageProbe(), "sha", "/", "source.png"); err != nil {
		t.Fatal(err)
	}
	handler := (&App{}).toolSubmitRender("crop", []string{"width", "height"}, []string{"file_id"})
	for _, name := range []string{"portrait", "portrait.png", "portrait.xyz"} {
		out, err := handler(app, map[string]any{"_project_id": testProj, "file_id": "1", "width": 32, "height": 32, "output_name": name})
		if name == "portrait.xyz" {
			if err == nil {
				t.Fatal("invalid output queued")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		row, err := getRender(app.AppDB(), testProj, out.(map[string]any)["render_id"].(int64))
		if err != nil || row.OutputName != "portrait.png" {
			t.Fatalf("row=%+v err=%v", row, err)
		}
	}
	rows, _ := listRenders(app.AppDB(), testProj, RenderFilters{})
	if len(rows) != 2 {
		t.Fatalf("queued %d rows", len(rows))
	}
	// Both public submission surfaces use the same early contract.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/renders?project_id="+testProj, strings.NewReader(`{"operation":"crop","file_id":"1","output_name":"bad.xyz","params":{"width":32,"height":32}}`))
	(&App{}).handleRendersCollection(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"error_code":"invalid_output_format"`) {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/renders?project_id="+testProj, strings.NewReader(`{"operation":"crop","file_id":"1","output_name":"http-portrait","params":{"width":32,"height":32}}`))
	(&App{}).handleRendersCollection(w, r)
	if w.Code != 202 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	rows, _ = listRenders(app.AppDB(), testProj, RenderFilters{})
	if rows[0].OutputName != "http-portrait.png" {
		t.Fatalf("HTTP queued name=%s", rows[0].OutputName)
	}
}

func TestRemoteCacheTelemetryIsNotPrimaryFailure(t *testing.T) {
	out := "REMOTE_SOURCE_CACHE_MISS file_id=1 path=/tmp/source.png\nREMOTE_SOURCE_CACHE_HIT file_id=2 path=/tmp/source.png\nUnable to choose an output format for './portrait'\nREMOTE_SOURCE_CACHE_DOWNLOAD_INVALID file_id=3\n"
	primary, hits, misses := splitRemoteRenderDiagnostics(out)
	if hits != 1 || misses != 1 || strings.Contains(primary, "CACHE_MISS") || strings.Contains(primary, "CACHE_HIT") || !strings.Contains(primary, "DOWNLOAD_INVALID") {
		t.Fatalf("primary=%s hits=%d misses=%d", primary, hits, misses)
	}
	app := newTestCtx(t)
	id, err := insertRender(app.AppDB(), testProj, "crop", []string{"1"}, nil, "portrait", "/", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = renderMarkFailed(app.AppDB(), id, primary); err != nil {
		t.Fatal(err)
	}
	row, err := getRender(app.AppDB(), testProj, id)
	if err != nil || row.ErrorCode != "invalid_output_format" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}

func TestSubmitResolvesSourceContentTypeBeforeNormalizing(t *testing.T) {
	app := newTestCtx(t)
	sc := newStorageStubClient(t)
	t.Setenv("APTEVA_GATEWAY_URL", sc.base)
	for _, id := range []string{"136", "200"} {
		plan, err := prepareRenderSubmission(app, testProj, "crop", []string{id}, json.RawMessage(`{"width":32,"height":32}`), "portrait")
		if err != nil || plan.Filename != "portrait.png" || plan.ContentType != "image/png" {
			t.Fatalf("id=%s plan=%+v err=%v", id, plan, err)
		}
	}
	if _, err := prepareRenderSubmission(app, testProj, "crop", []string{"999"}, json.RawMessage(`{"width":32,"height":32}`), "portrait"); err == nil || renderFailureCode(err.Error()) != "invalid_output_format" {
		t.Fatalf("unknown source format guessed: %v", err)
	}
}

// Execute the complete production remote Bash program, including real FFmpeg,
// cache materialization and HTTP upload. Only Linux df/hash utilities are adapted
// for the test host; no SSH or production host is needed.
func TestRemotePNGOutputNormalizationWithRealFFmpeg(t *testing.T) {
	skipIfNoFFmpeg(t)
	for _, binary := range []string{"bash", "curl", "python3"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip(binary + " unavailable")
		}
	}
	ffmpeg, _ := exec.LookPath("ffmpeg")
	var src bytes.Buffer
	if err := png.Encode(&src, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"portrait", "portrait.png"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			work := filepath.Join(root, "work")
			plan, err := buildPlan("crop", []string{"1"}, json.RawMessage(`{"x":0,"y":0,"width":32,"height":32}`), name, ".png")
			if err != nil {
				t.Fatal(err)
			}
			metadata := make(chan map[string]any, 1)
			uploaded := make(chan []byte, 1)
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/source":
					w.Write(src.Bytes())
				case strings.HasSuffix(r.URL.Path, "/files/init"):
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						http.Error(w, err.Error(), 400)
						return
					}
					metadata <- body
					json.NewEncoder(w).Encode(map[string]any{"upload_url": srv.URL + "/put", "upload_id": 99})
				case r.URL.Path == "/put":
					body, _ := io.ReadAll(r.Body)
					uploaded <- body
					w.WriteHeader(200)
				case strings.HasSuffix(r.URL.Path, "/files/99/finalize"):
					json.NewEncoder(w).Encode(map[string]any{"file": map[string]any{"id": 99}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			row := &RenderRow{ID: 1, ProjectID: testProj, Operation: "crop", SourceFileIDs: []string{"1"}, WorkDir: work}
			e := &remoteExecutor{storageToken: "test-only"}
			script, err := e.buildScript(row, plan, ffmpeg, []string{srv.URL + "/source"}, []string{"source.png"}, []int64{int64(src.Len())}, []string{fmt.Sprintf("%x", sha256.Sum256(src.Bytes()))}, "/portraits/", srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			script = strings.ReplaceAll(script, remoteRenderRoot, root)
			script = strings.ReplaceAll(script, remoteSourceCacheRoot, filepath.Join(root, "cache"))
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "df"), []byte("#!/bin/sh\nprintf 'Filesystem blocks used available capacity mounted\\nfixture 99999999999 0 99999999999 0%% /\\n'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := exec.LookPath("sha256sum"); err != nil {
				shasum, e := exec.LookPath("shasum")
				if e != nil {
					t.Skip("hash utility unavailable")
				}
				if e = os.WriteFile(filepath.Join(bin, "sha256sum"), []byte("#!/bin/sh\nexec "+shellQuote(shasum)+" -a 256 \"$@\"\n"), 0700); e != nil {
					t.Fatal(e)
				}
			}
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("remote script: %v\n%s", err, output)
			}
			result, err := parseAptevaResult(string(output))
			if err != nil || result.FileID != 99 {
				t.Fatalf("result=%+v err=%v output=%s", result, err, output)
			}
			meta := <-metadata
			data := <-uploaded
			if meta["name"] != "portrait.png" || meta["content_type"] != "image/png" {
				t.Fatalf("upload metadata=%v", meta)
			}
			img, err := png.Decode(bytes.NewReader(data))
			if err != nil || img.Bounds().Dx() != 32 || img.Bounds().Dy() != 32 {
				t.Fatalf("invalid PNG err=%v", err)
			}
		})
	}
}
