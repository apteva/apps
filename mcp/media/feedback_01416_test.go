package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdaptivePeakRetryPolicy(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python unavailable")
	}
	helper := filepath.Join(t.TempDir(), "runtime.py")
	if err := os.WriteFile(helper, []byte(renderRuntimePython), 0600); err != nil {
		t.Fatal(err)
	}
	script := `import importlib.util,sys
s=importlib.util.spec_from_file_location("runtime",sys.argv[1]);m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
for peaks,loudness,expected,passes in [([-1.32,-2.89],-19.92,True,2),([-1.32]*3,-19.92,False,3),([-3],-17,False,1)]:
 req={"source":"source.wav","output":"out.wav","params":{"target_lufs":-20,"target_peak_dbtp":-1.5},"args":["-i","source.wav","-af","","-ar","48000","-c:a","aac","out.wav"],"ffmpeg":"ffmpeg","ffprobe":"ffprobe"}
 info={"streams":[{"codec_type":"audio","sample_rate":"48000","duration":"5"}]}
 calls=[];measured=[];d={}
 def measure(req,source,prefix=""):
  peak=-5 if source==req["source"] else peaks[len(measured)]
  result={"input_i":str(-20 if source==req["source"] else loudness),"input_tp":str(peak),"input_lra":"1","input_thresh":"-30","target_offset":"0"}
  if source!=req["source"]:measured.append(result)
  return result
 m.measurement=measure;m.encode=lambda binary,args:calls.append(list(args));m.runtime_event=lambda *args:None;m.probe=lambda *args:info;m.run=lambda *args:("","")
 try:m.normalized_audio(req,info,d);succeeded=True
 except ValueError as e:
  assert "audio_normalization_failed: after" in str(e),str(e)
  succeeded=False
 assert succeeded==expected and len(calls)==passes,(calls,d)
 assert len(d["audio_normalization"]["attempts"])==passes,d
 for a in calls:assert a[a.index("-i")+1]=="source.wav" and "loudnorm=I=-20:" in a[a.index("-af")+1],a
 if passes>1:assert "TP=-4.5:" in calls[1][calls[1].index("-af")+1],calls
 assert d["audio_normalization"]["validated"]==expected,d
`
	out, err := exec.Command("python3", "-c", script, helper).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
}

func TestAdaptivePeakRetryRetainsVideoAndReusesProof(t *testing.T) {
	source := guardedFixture(t, false)
	realFFmpeg, _ := exec.LookPath("ffmpeg")
	realProbe, _ := exec.LookPath("ffprobe")
	dir := t.TempDir()
	trace := filepath.Join(dir, "trace")
	forced := filepath.Join(dir, "forced")
	// Inject the reported first-pass peak; encoding and final verification use real FFmpeg.
	wrapper := fmt.Sprintf(`#!/usr/bin/env python3
import subprocess,sys,json,re,os
args=sys.argv[1:]
with open(%q,"a") as f:f.write(json.dumps(args)+"\n")
if "-af" in args and "print_format=json" in args[args.index("-af")+1] and args[args.index("-af")+1].startswith("loudnorm=") and os.path.basename(args[args.index("-i")+1])=="out.mov" and not os.path.exists(%q):
 p=subprocess.run([%q]+args,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
 with open(%q,"w") as f:f.write("forced first encoded peak")
 err=re.sub(r'"input_tp"\s*:\s*"[^"]+"','"input_tp" : "-1.32"',p.stderr)
 err=re.sub(r'"input_i"\s*:\s*"[^"]+"','"input_i" : "-19.92"',err)
 sys.stdout.write(p.stdout);sys.stderr.write(err);sys.exit(p.returncode)
os.execv(%q,[%q]+args)
`, trace, forced, realFFmpeg, forced, realFFmpeg, realFFmpeg)
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realProbe, filepath.Join(dir, "ffprobe")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	copy, result, err := runGuardedFixture(t, source, "trim", map[string]any{"start_ms": 1005, "end_ms": 2990, "trim_mode": "auto", "trim_diagnostics": map[string]any{}}, false)
	if err != nil {
		t.Fatalf("%v %v", err, result)
	}
	for _, remote := range []bool{false, true} {
		os.Remove(forced)
		os.WriteFile(trace, nil, 0600)
		output, normalized, err := runGuardedFixture(t, copy, "audio_filter", map[string]any{"target_lufs": -20, "target_peak_dbtp": -1.5, "_validated_video_evidence": result["video_evidence"]}, remote)
		if err != nil {
			t.Fatalf("remote=%v %v %v", remote, err, normalized)
		}
		d := normalized["audio_normalization"].(map[string]any)
		if d["retry_count"] != float64(1) || d["video_validation_reused"] != true || d["video_frames_checked"] != float64(60) || d["timeline_validated"] != true {
			t.Fatal(d)
		}
		loudness, peak := measureLoudness(t, output)
		if loudness < -20.5 || loudness > -19.5 || peak > -1.4 {
			t.Fatalf("%.2f %.2f", loudness, peak)
		}
		raw, _ := os.ReadFile(trace)
		if strings.Contains(string(raw), "framemd5") {
			t.Fatalf("retry decoded video: %s", raw)
		}
	}
}

func TestRemoteUploadInitPreservesBlockerAndBoundsRetries(t *testing.T) {
	for _, tc := range []struct {
		name, phase, body string
		status, wantCalls int
		recover           bool
	}{
		{"quota", "/uploads", `{"error":"pending quota exhausted"}`, 429, 1, false},
		{"directQuota", "/files/init", `{"error":"pending quota exhausted"}`, 429, 1, false},
		{"transient", "/uploads", `{"error":"rate limited"}`, 429, 2, true},
		{"exhausted", "/uploads", `{"error":"rate limited"}`, 429, 4, false},
		{"serverError", "/uploads", `{"error":"unavailable"}`, 503, 4, false},
		{"auth", "/uploads", `{"error":"permission denied"}`, 403, 1, false},
		{"invalidSuccess", "/uploads", `{"unexpected":true}`, 200, 1, false},
		{"invalidDirectSuccess", "/files/init", `<html>unexpected gateway response</html>`, 200, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls, whole atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.phase {
					n := calls.Add(1)
					if tc.recover && n >= 2 {
						fmt.Fprint(w, `{"was_existing":true,"file":{"id":42}}`)
						return
					}
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
					return
				}
				if r.URL.Path == "/files" {
					whole.Add(1)
					w.WriteHeader(413)
					return
				}
				w.WriteHeader(501)
			}))
			defer srv.Close()
			script := "set -euo pipefail\ncurl_retry() { curl -sS \"$@\"; }\nsleep() { :; }\n"
			for k, v := range map[string]string{"STORAGE_BASE": srv.URL, "STORAGE_TOKEN": "test", "PROJECT_ID": "p1", "NAME_JSON": `"out.mov"`, "FOLDER_JSON": `"/"`, "CT_JSON": `"video/quicktime"`, "SIZE": "2000000000", "SHA": strings.Repeat("a", 64)} {
				script += k + "=" + shellQuote(v) + "\n"
			}
			cmd := exec.Command("bash", "-c", script+uploadScriptFragment+"\nprintf '%s' \"$FILE_ID\"")
			cmd.Dir = t.TempDir()
			raw, err := cmd.CombinedOutput()
			if tc.recover {
				if err != nil || !strings.HasSuffix(string(raw), "42") {
					t.Fatalf("%v %s", err, raw)
				}
			} else if err == nil || !strings.Contains(string(raw), tc.body) || !strings.Contains(string(raw), fmt.Sprintf("http_status=%d", tc.status)) {
				t.Fatalf("%v %s", err, raw)
			}
			if int(calls.Load()) != tc.wantCalls || whole.Load() != 0 {
				t.Fatalf("calls=%d whole=%d", calls.Load(), whole.Load())
			}
		})
	}
}

func TestLocalUploadQuotaAndRateLimit(t *testing.T) {
	for _, quota := range []bool{true, false} {
		t.Run(fmt.Sprint(quota), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if !quota && n == 2 {
					fmt.Fprint(w, `{"was_existing":true,"file":{"id":42}}`)
					return
				}
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(429)
				if quota {
					fmt.Fprint(w, `{"error":"pending quota exhausted"}`)
				} else {
					fmt.Fprint(w, `{"error":"rate limited"}`)
				}
			}))
			defer srv.Close()
			c := &storageClient{base: srv.URL, httpClient: srv.Client()}
			file, err := c.uploadFileChunked(context.Background(), "p1", "/", "out.mov", "video/quicktime", "unused", 10, strings.Repeat("a", 64))
			if quota {
				var e *storageUploadError
				if !errors.As(err, &e) || e.Code != "storage_upload_quota_exhausted" || e.Phase != "multipart_init" || e.Attempts != 1 || calls.Load() != 1 {
					t.Fatalf("%v calls=%d", err, calls.Load())
				}
			} else if err != nil || file.ID != 42 || calls.Load() != 2 {
				t.Fatalf("%v %v calls=%d", file, err, calls.Load())
			}
		})
	}
	if storageRetryDelay("120", 1) != 10*time.Second || storageRetryDelay("", 3) != 4*time.Second {
		t.Fatal("unbounded retry delay")
	}
}

func TestValidationProgressCannotAppearFinished(t *testing.T) {
	app := newTestCtx(t)
	id, err := insertRender(app.AppDB(), testProj, "trim", []string{"1"}, map[string]any{}, "out.mov", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	claimNextPending(app.AppDB())
	forwardProgress(io.NopCloser(strings.NewReader("out_time_ms=2000000\nAPTEVA_STATUS:{\"stage\":\"validation\"}\nout_time_ms=2000000\n")), app.AppDB(), id, app, testProj, 2000)
	row, _ := getRender(app.AppDB(), testProj, id)
	var metrics map[string]any
	json.Unmarshal(row.Metrics, &metrics)
	if row.ProgressPct != 80 || metrics["stage"] != "validation" || metrics["stage_progress_pct"] != nil {
		t.Fatalf("%+v %v", row, metrics)
	}
	failure := "storage_upload_quota_exhausted: phase=multipart_init http_status=429 pending quota exhausted"
	compact := truncateRenderFailure(strings.Repeat("ffmpeg noise\n", 10000)+failure, 1500)
	if !strings.HasSuffix(compact, failure) || len(compact) > 1500 || renderFailureCode(compact) != "storage_upload_quota_exhausted" {
		t.Fatal(compact)
	}
}

func TestRemoteLegacyUploadRejectsOversizedBody(t *testing.T) {
	var whole atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files" {
			whole.Add(1)
		}
		w.WriteHeader(501)
	}))
	defer srv.Close()
	script := "set -euo pipefail\ncurl_retry() { curl -sS \"$@\"; }\n"
	for k, v := range map[string]string{"STORAGE_BASE": srv.URL, "STORAGE_TOKEN": "test", "PROJECT_ID": "p1", "NAME_JSON": `"out.mov"`, "FOLDER_JSON": `"/"`, "CT_JSON": `"video/quicktime"`, "SIZE": "2000000000", "SHA": strings.Repeat("a", 64)} {
		script += k + "=" + shellQuote(v) + "\n"
	}
	cmd := exec.Command("bash", "-c", script+uploadScriptFragment)
	cmd.Dir = t.TempDir()
	raw, err := cmd.CombinedOutput()
	if err == nil || whole.Load() != 0 || !strings.Contains(string(raw), "phase=legacy_upload") {
		t.Fatalf("%v %s whole=%d", err, raw, whole.Load())
	}
}
