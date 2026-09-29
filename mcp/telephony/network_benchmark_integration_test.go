//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	bench "github.com/apteva/apps/mcp/telephony/benchmarks/softphone"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type benchmarkDirection struct {
	Expected        int     `json:"expected_markers"`
	Received        int     `json:"received_markers"`
	MissingPct      float64 `json:"missing_markers_pct"`
	Duplicates      int     `json:"duplicate_markers"`
	OutOfOrder      int     `json:"out_of_order_markers"`
	P50             float64 `json:"p50_ms"`
	P95             float64 `json:"p95_ms"`
	P99             float64 `json:"p99_ms"`
	Max             float64 `json:"max_ms"`
	MedianLevelDBFS float64 `json:"median_level_dbfs"`
	TailReceived    int     `json:"final_two_seconds_markers"`
}
type benchmarkBrowserResult struct {
	WireDiagnostics []json.RawMessage `json:"wire_diagnostics"`
	Markers         []bench.Marker    `json:"markers"`
	States          []map[string]any  `json:"states"`
	Diagnostics     []map[string]any  `json:"diagnostics"`
	PageErrors      []string          `json:"page_errors"`
	BrowserVersion  string            `json:"browser_version"`
	StartAt         int64             `json:"start_at"`
}
type benchmarkResult struct {
	Profile           bench.Profile              `json:"profile"`
	Outcome           string                     `json:"outcome"`
	Errors            []string                   `json:"errors,omitempty"`
	Up                benchmarkDirection         `json:"adviser_to_carrier"`
	Down              benchmarkDirection         `json:"carrier_to_adviser"`
	Network           map[string]bench.LinkStats `json:"network"`
	Browser           benchmarkBrowserResult     `json:"browser"`
	CarrierMarkers    []bench.Marker             `json:"carrier_markers"`
	ServerDiagnostics map[string]any             `json:"server_diagnostics,omitempty"`
}

func benchmarkPercentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	return values[min(len(values)-1, int(math.Ceil(float64(len(values))*p))-1)]
}
func benchmarkAnalyze(markers []bench.Marker, offset int, start int64, duration int) benchmarkDirection {
	end := duration/500 - 1
	out := benchmarkDirection{Expected: end - 1}
	seen := map[int]bool{}
	var delays, levels []float64
	last := -1
	for _, marker := range markers {
		index := marker.ID - offset
		if index < 1 || index >= end {
			continue
		}
		if seen[index] {
			out.Duplicates++
			continue
		}
		seen[index] = true
		if index < last {
			out.OutOfOrder++
		}
		last = index
		out.Received++
		delay := marker.AtMS - float64(start) - float64(index*500)
		delays = append(delays, delay)
		levels = append(levels, marker.LevelDBFS)
		if index*500 >= duration-2500 {
			out.TailReceived++
		}
	}
	out.MissingPct = float64(out.Expected-out.Received) * 100 / float64(out.Expected)
	out.P50 = benchmarkPercentile(delays, .5)
	out.P95 = benchmarkPercentile(delays, .95)
	out.P99 = benchmarkPercentile(delays, .99)
	out.Max = benchmarkPercentile(delays, 1)
	out.MedianLevelDBFS = benchmarkPercentile(levels, .5)
	return out
}
func TestSoftphoneNetworkBenchmark(t *testing.T) {
	output := os.Getenv("TELEPHONY_BENCHMARK_OUTPUT")
	if output == "" {
		t.Skip("opt-in local benchmark: bun run benchmark:softphone")
	}
	profiles, err := bench.Profiles("benchmarks/softphone/profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	seconds, err := strconv.Atoi(os.Getenv("TELEPHONY_BENCHMARK_SECONDS"))
	if err != nil || seconds < 8 || seconds > 60 {
		t.Fatal("benchmark duration must be 8..60 seconds")
	}
	seed, err := strconv.ParseInt(os.Getenv("TELEPHONY_BENCHMARK_SEED"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	selection := os.Getenv("TELEPHONY_BENCHMARK_PROFILES")
	selected := map[string]bool{}
	for _, name := range strings.Split(selection, ",") {
		selected[name] = true
	}
	if selection != "all" {
		for name := range selected {
			found := false
			for _, profile := range profiles {
				found = found || profile.Name == name
			}
			if !found {
				t.Fatalf("unknown benchmark profile %q", name)
			}
		}
	}
	metadata := benchmarkSourceMetadata(t)
	var results []benchmarkResult
	for _, profile := range profiles {
		if selection != "all" && !selected[profile.Name] {
			continue
		}
		result := benchmarkResult{Profile: profile, Outcome: "error"}
		t.Run(profile.Name, func(t *testing.T) { result = runSoftphoneBenchmark(t, profile, seed, seconds*1000, output) })
		results = append(results, result)
	}
	if len(results) == 0 {
		t.Fatal("no benchmark profiles selected")
	}
	git := exec.Command("git", "rev-parse", "HEAD")
	revision, _ := git.Output()
	report := map[string]any{"schema": "telephony-softphone-network-benchmark/v1", "created_at": time.Now().UTC().Format(time.RFC3339), "revision": strings.TrimSpace(string(revision)), "source": metadata, "seed": seed, "duration_ms": seconds * 1000, "clock_uncertainty_ms": 20, "scope": "real Chromium + production SoftphoneSession/worklets + compiled Telephony + local Telnyx L16 carrier substitute; shaped browser TCP link; synthetic acoustic markers; no live provider", "results": results}
	raw, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(output, "results.json"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# Softphone network benchmark\n\nLocal run: %s. Source revision: `%s` plus local changes. Seed: %d. Measurement: %ds/profile, plus warmup/drain.\n\n", time.Now().Format(time.RFC3339), strings.TrimSpace(string(revision)), seed, seconds)
	md.WriteString("Actual Chromium, production audio pipeline, compiled Telephony and a local Telnyx L16 substitute. Only the browser TCP link is impaired. No production/staging traffic or real calls. Timing uncertainty is approximately ±20ms.\n\n| Profile | Outcome | Adviser → carrier p95 | Missing markers | Carrier → adviser p95 | Missing markers |\n|---|---|---:|---:|---:|---:|\n")
	for _, r := range results {
		fmt.Fprintf(&md, "| %s | %s | %.0f ms | %.1f%% | %.0f ms | %.1f%% |\n", r.Profile.Name, r.Outcome, r.Up.P95, r.Up.MissingPct, r.Down.P95, r.Down.MissingPct)
	}
	md.WriteString("\nMissing markers measure corruption/loss of identifiable tone sequences, not a percentage of speech samples or a MOS score. Zero latency with zero received markers means no measurement. TCP recovery profiles delay ordered bytes; they do not emulate kernel packet loss/congestion control. Degradation is expected below the raw PCM payload requirement of 384 kbit/s per direction (framing adds overhead). A `usable` profile failing its thresholds fails the command. Adverse profiles report degradation honestly; a `recovery` profile must deliver markers again in the final two seconds.\n")
	if err := os.WriteFile(filepath.Join(output, "REPORT.md"), []byte(md.String()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Outcome == "error" || r.Outcome == "failed" {
			t.Errorf("benchmark gate failed: %s: %v", r.Profile.Name, r.Errors)
		}
	}
	t.Logf("benchmark report: %s", filepath.Join(output, "REPORT.md"))
}
func runSoftphoneBenchmark(t *testing.T, profile bench.Profile, seed int64, duration int, output string) benchmarkResult {
	t.Helper()
	result := benchmarkResult{Profile: profile, Outcome: "error"}
	platform := newTier2PlatformGateway(t)
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(tier2Project), tk.WithEnv("APTEVA_GATEWAY_URL", platform.server.URL))
	created := tier2MCPAs(t, sc, "telephony_routes_create", map[string]any{"phone_number": tier2Number, "answer_mode": "human_browser", "recording_mode": "off"})
	route := created["route"].(map[string]any)
	tier2MCPAs(t, sc, "telephony_routes_configure_carrier", map[string]any{"route_id": route["id"]})
	incoming, _ := json.Marshal(map[string]any{"data": map[string]any{"id": "benchmark-incoming", "event_type": "call.initiated", "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "payload": map[string]any{"call_control_id": "benchmark-carrier-call", "connection_id": "application-test-1", "direction": "incoming", "from": tier2Caller, "to": tier2Number}}})
	response := tier2SignedPOST(t, platform, localSidecarURL(t, sc, created["inbound_url"].(string)), incoming)
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("benchmark inbound: %d", response.StatusCode)
	}
	calls := tier2CallList(t, sc)
	if len(calls) != 1 {
		t.Fatalf("benchmark calls: %v", calls)
	}
	id := calls[0]["id"].(string)
	var session softphoneSession
	status, body := tier2Request(t, sc, "POST", "/softphone/answer/"+id+"?project_id="+tier2Project, map[string]any{}, &session, tier2Headers())
	if status != 200 {
		t.Fatalf("benchmark answer: %d %s", status, body)
	}
	target, _ := url.Parse(sc.URL())
	proxy, err := bench.NewProxy(target.Host, profile, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	var epoch atomic.Int64
	var markerMu sync.Mutex
	decoder := bench.NewDecoder(16000)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			io.WriteString(w, `<!doctype html><title>Local Telephony network benchmark</title><script type="module" src="/entry.js"></script>`)
		case "/config":
			writeTier2JSON(w, map[string]any{"media_url": "ws://" + proxy.Addr() + "/softphone/media/" + id + "/" + session.SessionToken, "duration_ms": duration, "drain_ms": 2500})
		case "/arm":
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			at := time.Now().Add(time.Second).Truncate(time.Millisecond)
			if epoch.CompareAndSwap(0, at.UnixMilli()) {
				proxy.Arm(at)
			}
			writeTier2JSON(w, map[string]any{"start_at": epoch.Load()})
		default:
			files := map[string]string{"/entry.js": filepath.Join(output, "browser-entry.js"), "/probe.js": "benchmarks/softphone/probe-worklet.js", "/worklet.js": "ui/softphone-worklet.js", "/worker.js": "ui/softphone-worker.js"}
			path, ok := files[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/javascript")
			http.ServeFile(w, r, path)
		}
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(duration)*time.Millisecond+45*time.Second)
	defer cancel()
	carrierDone := make(chan struct{})
	carrierErrors := make(chan error, 1)
	go func() {
		defer close(carrierDone)
		var command tier2CarrierCall
	selectLoop:
		for {
			select {
			case <-ctx.Done():
				return
			case command = <-platform.carrierCalls:
				if command.Tool == "answer_call" {
					break selectLoop
				}
			case <-time.After(15 * time.Second):
				carrierErrors <- fmt.Errorf("carrier answer command timeout")
				return
			}
		}
		carrierURL := strings.Replace(rawSidecarMediaURL(t, sc, command.Input["stream_url"].(string)), "http://", "ws://", 1)
		carrier, buffered, _, err := (ws.Dialer{}).Dial(ctx, carrierURL)
		if err != nil {
			carrierErrors <- err
			return
		}
		if buffered != nil {
			carrier = hijackedConn{Conn: carrier, reader: buffered}
		}
		defer carrier.Close()
		start, _ := json.Marshal(map[string]any{"event": "start", "stream_id": "benchmark-stream", "sequence_number": "1", "start": map[string]any{"call_control_id": "benchmark-carrier-call", "stream_id": "benchmark-stream"}})
		if err = wsutil.WriteClientText(carrier, start); err != nil {
			carrierErrors <- err
			return
		}
		var writing sync.WaitGroup
		writing.Add(1)
		go func() {
			defer writing.Done()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for epoch.Load() == 0 {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
			startAt := time.UnixMilli(epoch.Load())
			timer := time.NewTimer(max(0, time.Until(startAt)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			cadence := time.NewTicker(20 * time.Millisecond)
			defer cadence.Stop()
			for frame := 0; frame < duration/20; frame++ {
				pcm := make([]int16, 320)
				for i := range pcm {
					pcm[i] = bench.Sample(frame*320+i, 16000, 128)
				}
				payload := base64.StdEncoding.EncodeToString(pcm16ToBytes(pcm))
				raw, _ := json.Marshal(map[string]any{"event": "media", "sequence_number": frame + 2, "media": map[string]any{"payload": payload, "timestamp": frame * 20, "chunk": frame + 1}})
				if wsutil.WriteClientText(carrier, raw) != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-cadence.C:
				}
			}
		}()
		stopCloser := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				carrier.Close()
			case <-stopCloser:
			}
		}()
		defer close(stopCloser)
		defer writing.Wait()
		for {
			data, op, err := wsutil.ReadServerData(carrier)
			if err != nil {
				return
			}
			if op != ws.OpText {
				continue
			}
			var frame carrierMediaFrame
			if json.Unmarshal(data, &frame) != nil || frame.Event != "media" || frame.Media == nil {
				continue
			}
			pcm, err := decodeCarrierPCM(frame.Media.Payload, carrierCodecL16_16)
			if err != nil {
				continue
			}
			markerMu.Lock()
			decoder.Push(pcm, float64(time.Now().UnixMicro())/1000)
			markerMu.Unlock()
		}
	}()
	browserFile := filepath.Join(output, profile.Name+"-browser.json")
	cmd := exec.CommandContext(ctx, "bun", "benchmarks/softphone/browser.ts")
	cmd.Env = append(os.Environ(), "TELEPHONY_BENCHMARK_ORIGIN="+fixture.URL, "TELEPHONY_BENCHMARK_BROWSER_RESULT="+browserFile)
	var log bytes.Buffer
	cmd.Stdout = &log
	cmd.Stderr = &log
	if err = cmd.Run(); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("browser: %v: %s", err, log.String()))
	} else {
		raw, readErr := os.ReadFile(browserFile)
		if readErr != nil {
			result.Errors = append(result.Errors, readErr.Error())
		} else if err = json.Unmarshal(raw, &result.Browser); err != nil {
			result.Errors = append(result.Errors, err.Error())
		}
	}
	// Read persisted telemetry while the local sidecar remains alive.
	var detail map[string]any
	tier2Request(t, sc, "GET", "/calls/"+id+"?project_id="+tier2Project, nil, &detail, tier2Headers())
	if row, ok := detail["call"].(map[string]any); ok {
		result.ServerDiagnostics = map[string]any{"browser": row["browser_audio_diagnostics"], "carrier": row["carrier_audio_diagnostics"]}
	}
	cancel()
	<-carrierDone
	select {
	case e := <-carrierErrors:
		result.Errors = append(result.Errors, e.Error())
	default:
	}
	result.Network = proxy.Stats()
	markerMu.Lock()
	result.CarrierMarkers = append([]bench.Marker(nil), decoder.Markers...)
	markerMu.Unlock()
	result.Up = benchmarkAnalyze(result.CarrierMarkers, 0, epoch.Load(), duration)
	result.Down = benchmarkAnalyze(result.Browser.Markers, 128, epoch.Load(), duration)
	for _, wire := range result.Browser.WireDiagnostics {
		var control struct {
			Diagnostics *browserAudioDiagnostics `json:"diagnostics"`
		}
		if err := json.Unmarshal(wire, &control); err != nil {
			result.Errors = append(result.Errors, "browser diagnostics protocol: "+err.Error())
			break
		}
	}
	result.Errors = append(result.Errors, result.Browser.PageErrors...)
	result.Outcome, result.Errors = benchmarkEvaluate(profile, result.Up, result.Down, result.Errors)

	t.Logf("%s: %s up p95 %.0fms missing %.1f%%; down p95 %.0fms missing %.1f%%", profile.Name, result.Outcome, result.Up.P95, result.Up.MissingPct, result.Down.P95, result.Down.MissingPct)
	return result
}

// Snapshot source before the run so the report identifies uncommitted app and
// harness changes as well as the Git revision. Generated assets are included.
func benchmarkSourceMetadata(t *testing.T) map[string]any {
	t.Helper()
	hashes := map[string]string{}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", "results", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".ts", ".js", ".mjs", ".json":
		default:
			if path != "go.mod" && path != "go.sum" {
				return nil
			}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hashes[path] = fmt.Sprintf("%x", sha256.Sum256(raw))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(hashes)
	if err != nil {
		t.Fatal(err)
	}
	status, err := exec.Command("git", "status", "--porcelain", "--", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"working_tree_dirty": len(bytes.TrimSpace(status)) > 0,
		"manifest_sha256":    fmt.Sprintf("%x", sha256.Sum256(encoded)),
		"files_sha256":       hashes,
		"go_version":         runtime.Version(), "os": runtime.GOOS,
		"arch": runtime.GOARCH, "logical_cpus": runtime.NumCPU(),
	}
}

func benchmarkEvaluate(profile bench.Profile, up, down benchmarkDirection, errors []string) (string, []string) {
	if len(errors) > 0 {
		return "error", errors
	}
	good := true
	for _, d := range []benchmarkDirection{up, down} {
		// Identity/order violations are infrastructure or app failures, even
		// on a deliberately adverse network. Never label them expected loss.
		if d.Duplicates > 0 || d.OutOfOrder > 0 || (d.Received > 0 && d.P50 < -20) {
			return "failed", append(errors, "marker ordering, duplication or clock invariant violated")
		}
		if d.Received == 0 || d.P95 > profile.MaxP95MS || d.MissingPct > profile.MaxMissingPct {
			good = false
		}
	}
	if profile.Expectation == "recovery" && (up.TailReceived < 2 || down.TailReceived < 2) {
		return "failed", append(errors, "audio did not recover in final two seconds")
	}
	if good {
		return "pass", errors
	}
	if profile.Expectation == "usable" {
		return "failed", append(errors, "usable-profile audio thresholds exceeded")
	}
	return "degraded", errors
}

func TestBenchmarkMeasurementDetectsDelayLossAndDuplicates(t *testing.T) {
	const epoch = 100000
	var markers []bench.Marker
	for i := 1; i < 15; i++ {
		if i == 7 {
			continue
		}
		markers = append(markers, bench.Marker{ID: i, AtMS: epoch + float64(i*500) + 180, LevelDBFS: -15})
	}
	markers = append(markers, markers[len(markers)-1])
	d := benchmarkAnalyze(markers, 0, epoch, 8000)
	if d.Expected != 14 || d.Received != 13 || d.Duplicates != 1 || d.P95 != 180 || math.Abs(d.MissingPct-100.0/14) > .001 || d.TailReceived != 4 {
		t.Fatalf("incorrect measurement: %+v", d)
	}
	empty := benchmarkAnalyze(nil, 128, epoch, 8000)
	if empty.Received != 0 || empty.MissingPct != 100 {
		t.Fatalf("empty direction looked healthy: %+v", empty)
	}
}

func TestBenchmarkGatesRejectFailuresAndSeparateExpectedDegradation(t *testing.T) {
	healthy := benchmarkDirection{Received: 14, P50: 50, P95: 60, TailReceived: 4}
	for _, tc := range []struct {
		name, expectation, want string
		direction               benchmarkDirection
		errors                  []string
	}{
		{"healthy", "usable", "pass", healthy, nil},
		{"missing", "usable", "failed", benchmarkDirection{MissingPct: 100}, nil},
		{"delayed", "usable", "failed", benchmarkDirection{Received: 14, P95: 1000}, nil},
		{"constrained", "degradation", "degraded", benchmarkDirection{MissingPct: 100}, nil},
		{"outage-no-recovery", "recovery", "failed", benchmarkDirection{Received: 10, TailReceived: 1}, nil},
		{"outage-recovered", "recovery", "degraded", benchmarkDirection{Received: 10, MissingPct: 30, TailReceived: 3}, nil},
		{"duplicates-even-on-adverse", "degradation", "failed", benchmarkDirection{Received: 14, Duplicates: 1}, nil},
		{"reordered", "usable", "failed", benchmarkDirection{Received: 14, OutOfOrder: 1}, nil},
		{"clock", "usable", "failed", benchmarkDirection{Received: 14, P50: -100}, nil},
		{"infrastructure", "degradation", "error", healthy, []string{"browser crashed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := bench.Profile{Expectation: tc.expectation, MaxP95MS: 250, MaxMissingPct: 5}
			got, _ := benchmarkEvaluate(profile, tc.direction, healthy, tc.errors)
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
