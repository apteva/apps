package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// toolLoadTest spawns N goroutine "viewers" against the playback URL,
// each refreshing the manifest every half-segment-duration and fetching
// every new .ts segment. Bytes are discarded. Returns latency
// percentiles, served bitrate, refusal/5xx counts, and segment-late
// counts.
//
// Two ways to use it:
//
//   - Bisect for capacity: agent runs viewers=100 → ok, 500 → 5xx,
//     250 → ok, 375 → ok, 437 → 5xx, narrowing to the knee.
//   - Sustained read: viewers=100, duration_seconds=300 to confirm a
//     known-good level holds without degradation.
//
// The load gen runs IN-PROCESS in the streaming sidecar — fine for a
// local box, but means the loadgen and the server share CPU. For
// realistic numbers run the test from a separate machine using wrk
// against the same playback URL.
func (a *App) toolLoadTest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	viewers := intArg(args, "viewers", 50)
	if viewers <= 0 {
		viewers = 50
	}
	if viewers > 2000 {
		return nil, errors.New("viewers capped at 2000 — run remote wrk for higher")
	}
	duration := intArg(args, "duration_seconds", 30)
	if duration <= 0 {
		duration = 30
	}
	if duration > 300 {
		duration = 300
	}

	s, err := a.dbGet(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("stream not found")
	}

	manifestURL := a.loopbackPlaybackURL(s, indexPlaylistFile)
	res, err := runLoadTest(ctx, manifestURL, viewers, duration)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// loopbackPlaybackURL builds the load generator's target: this
// sidecar's own HTTP route, on loopback, never the public host.
//
// v0.1 load-tested s.PlaybackURL and only rewrote it when it was
// relative — so with PUBLIC_URL set (i.e. in production) up to 2000
// goroutines for up to 300s hammered https://<public-host>/… through
// apteva-server's reverse proxy: a self-inflicted DoS of prod that
// degrades every other app on the box, measured through a proxy hop
// that isn't part of what we're trying to measure. And in dev the
// rewrite produced http://localhost:<port>/api/apps/streaming/... —
// the proxy's path, which the sidecar itself doesn't serve, so every
// request 404'd and the tool reported refusals.
func (a *App) loopbackPlaybackURL(s *Stream, file string) string {
	q := url.Values{}
	q.Set("t", s.PlaybackToken)
	if v := urlProjectID(s.ProjectID); v != "" {
		q.Set("project_id", v)
	}
	port := strings.TrimSpace(getenv("APTEVA_LISTEN_PORT", "8080"))
	return fmt.Sprintf("http://127.0.0.1:%s/streams/%d/%s?%s", port, s.ID, file, q.Encode())
}

// assertLoopback refuses to point the load generator at anything but
// this machine. Belt to loopbackPlaybackURL's braces — the load test
// is a capacity probe of the local sidecar, never a traffic generator
// aimed at a remote host.
func assertLoopback(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("load test target %q: %w", raw, err)
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("load test target must be loopback, got %q", host)
}

type loadResult struct {
	TargetViewers    int     `json:"target_viewers"`
	DurationSeconds  int     `json:"duration_seconds"`
	ManifestRequests int     `json:"manifest_requests"`
	SegmentRequests  int     `json:"segment_requests"`
	BytesServed      int64   `json:"bytes_served"`
	ServedMbps       float64 `json:"served_mbps"`
	P50TTFBMillis    float64 `json:"p50_ttfb_ms"`
	P95TTFBMillis    float64 `json:"p95_ttfb_ms"`
	P99TTFBMillis    float64 `json:"p99_ttfb_ms"`
	HTTP5xx          int     `json:"http_5xx"`
	Refusals         int     `json:"refusals"`
	SegmentsLate     int     `json:"segments_late"`
	// Failures counts EVERY non-2xx response. v0.1 only counted 404
	// and 5xx, so a deployment answering 400 or 403 to every request
	// reported a perfectly clean run.
	Failures        int            `json:"failures"`
	StatusBreakdown map[string]int `json:"status_breakdown"`
	WallSeconds     float64        `json:"wall_seconds"`
}

// probeTimeout bounds the one-shot manifest probe. v0.1 used
// http.Get + http.DefaultClient: no timeout, no context, so a wedged
// server hung the MCP tool call forever.
const probeTimeout = 10 * time.Second

func runLoadTest(ctx *sdk.AppCtx, manifestURL string, viewers, duration int) (*loadResult, error) {
	res := &loadResult{
		TargetViewers:   viewers,
		DurationSeconds: duration,
		StatusBreakdown: map[string]int{},
	}
	if err := assertLoopback(manifestURL); err != nil {
		return nil, err
	}

	manifestURLParsed, err := url.Parse(manifestURL)
	if err != nil {
		return nil, fmt.Errorf("parse manifest url: %w", err)
	}

	// Probe the manifest once to learn the segment URL pattern + the
	// segment duration the server is using. Bail — loudly — if it
	// doesn't answer: v0.1 returned an all-zero result with a nil
	// error here, which is indistinguishable from "ran fine, served
	// nothing".
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), probeTimeout)
	defer cancelProbe()
	probeReq, err := http.NewRequestWithContext(probeCtx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, err
	}
	probeClient := &http.Client{Timeout: probeTimeout}
	probe, err := probeClient.Do(probeReq)
	if err != nil {
		return nil, fmt.Errorf("probe manifest: %w", err)
	}
	probeBody, _ := io.ReadAll(probe.Body)
	probe.Body.Close()
	if probe.StatusCode < 200 || probe.StatusCode >= 300 {
		return nil, fmt.Errorf("probe manifest: HTTP %d from %s", probe.StatusCode, manifestURL)
	}
	segDur, segNames := parseManifest(string(probeBody))
	if segDur <= 0 {
		segDur = 4 * time.Second
	}
	if len(segNames) == 0 {
		// Manifest is empty — stream hasn't started yet. We can still
		// load-test the manifest endpoint itself though.
	}

	// Pre-compute the auth query suffix to append to segment URLs. The
	// server now rewrites manifest URIs to carry the request's own
	// credentials (rewriteManifestQuery), so only append when the name
	// came back bare — double-appending would produce "?t=…?t=…".
	authQuery := manifestURLParsed.RawQuery
	segmentBaseURL := func(name string) string {
		base := strings.TrimSuffix(
			manifestURL[:len(manifestURL)-len(manifestURLParsed.RequestURI())]+manifestURLParsed.Path,
			"/"+path.Base(manifestURLParsed.Path))
		u := base + "/" + name
		if authQuery != "" && !strings.Contains(name, "?") {
			u += "?" + authQuery
		}
		return u
	}

	cnt := newLoadCounters()

	manifestPoll := segDur / 2
	if manifestPoll < 1*time.Second {
		manifestPoll = 1 * time.Second
	}

	// One client per viewer — keep-alive within, but no shared connection
	// pool across viewers (more realistic).
	client := func() *http.Client {
		return &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        2,
				MaxIdleConnsPerHost: 2,
				DisableCompression:  true,
				IdleConnTimeout:     30 * time.Second,
			},
		}
	}

	deadline := time.Now().Add(time.Duration(duration) * time.Second)
	rootCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	startWall := time.Now()

	var wg sync.WaitGroup
	for i := 0; i < viewers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c := client()
			seen := map[string]bool{}
			tick := time.NewTicker(manifestPoll)
			defer tick.Stop()

			fetchManifest := func(u string) {
				req, _ := http.NewRequestWithContext(rootCtx, http.MethodGet, u, nil)
				start := time.Now()
				resp, err := c.Do(req)
				if err != nil {
					if rootCtx.Err() != nil {
						return
					}
					cnt.refusals.Add(1)
					cnt.failures.Add(1)
					cnt.recordStatus("transport_error")
					return
				}
				defer resp.Body.Close()
				cnt.recordTTFB(float64(time.Since(start).Milliseconds()))
				cnt.recordStatus(strconv.Itoa(resp.StatusCode))
				if !classifyResponse(cnt, resp.StatusCode, true) {
					_, _ = io.Copy(io.Discard, resp.Body)
					return
				}
				cnt.manifestReqs.Add(1)
				body, _ := io.ReadAll(resp.Body)
				cnt.bytes.Add(int64(len(body)))
				// Discover new segments + queue them.
				_, names := parseManifest(string(body))
				for _, n := range names {
					if !seen[n] {
						seen[n] = true
						// Fetch in this same goroutine so the viewer's
						// bandwidth budget mirrors a real client:
						// serial, not parallel.
						fetchSegment(rootCtx, c, segmentBaseURL(n), cnt)
					}
				}
			}

			// Initial manifest fetch immediately, then on tick.
			fetchManifest(manifestURL)
			for {
				select {
				case <-rootCtx.Done():
					return
				case <-tick.C:
					fetchManifest(manifestURL)
				}
			}
		}(i)
	}
	wg.Wait()

	wall := time.Since(startWall).Seconds()
	res.WallSeconds = wall
	res.ManifestRequests = int(cnt.manifestReqs.Load())
	res.SegmentRequests = int(cnt.segReqs.Load())
	res.BytesServed = cnt.bytes.Load()
	res.HTTP5xx = int(cnt.fivexx.Load())
	res.Refusals = int(cnt.refusals.Load())
	res.SegmentsLate = int(cnt.late.Load())
	res.Failures = int(cnt.failures.Load())
	if wall > 0 {
		res.ServedMbps = float64(cnt.bytes.Load()*8) / 1_000_000.0 / wall
	}

	cnt.mu.Lock()
	defer cnt.mu.Unlock()
	res.StatusBreakdown = cnt.statuses
	if len(cnt.ttfbs) > 0 {
		sort.Float64s(cnt.ttfbs)
		res.P50TTFBMillis = pct(cnt.ttfbs, 0.50)
		res.P95TTFBMillis = pct(cnt.ttfbs, 0.95)
		res.P99TTFBMillis = pct(cnt.ttfbs, 0.99)
	}
	return res, nil
}

// loadCounters is the tally every viewer goroutine writes into.
type loadCounters struct {
	manifestReqs atomic.Int64
	segReqs      atomic.Int64
	bytes        atomic.Int64
	fivexx       atomic.Int64
	refusals     atomic.Int64
	late         atomic.Int64
	failures     atomic.Int64

	mu       sync.Mutex
	statuses map[string]int
	ttfbs    []float64
}

func newLoadCounters() *loadCounters {
	return &loadCounters{statuses: map[string]int{}}
}

func (c *loadCounters) recordStatus(key string) {
	c.mu.Lock()
	c.statuses[key]++
	c.mu.Unlock()
}

func (c *loadCounters) recordTTFB(ms float64) {
	c.mu.Lock()
	c.ttfbs = append(c.ttfbs, ms)
	c.mu.Unlock()
}

// classifyResponse tallies one response and reports whether it counts
// as served. ANY non-2xx is a failure — v0.1's else-branch treated
// 400/403/3xx as success, so a fully broken deployment produced a
// clean-looking report.
func classifyResponse(c *loadCounters, code int, isManifest bool) bool {
	switch {
	case code >= 200 && code < 300:
		return true
	case code >= 500:
		c.fivexx.Add(1)
	case code == http.StatusNotFound:
		// A missing segment is the classic "server can't keep up"
		// signal; a missing manifest means we were refused outright.
		if isManifest {
			c.refusals.Add(1)
		} else {
			c.late.Add(1)
		}
	case code == http.StatusTooManyRequests, code == http.StatusForbidden, code == http.StatusUnauthorized:
		c.refusals.Add(1)
	}
	c.failures.Add(1)
	return false
}

func fetchSegment(ctx context.Context, c *http.Client, u string, cnt *loadCounters) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		cnt.late.Add(1)
		cnt.failures.Add(1)
		cnt.recordStatus("transport_error")
		return
	}
	defer resp.Body.Close()
	cnt.recordTTFB(float64(time.Since(start).Milliseconds()))
	cnt.recordStatus(strconv.Itoa(resp.StatusCode))
	if !classifyResponse(cnt, resp.StatusCode, false) {
		_, _ = io.Copy(io.Discard, resp.Body)
		return
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	cnt.bytes.Add(n)
	cnt.segReqs.Add(1)
}

// parseManifest pulls #EXT-X-TARGETDURATION + segment filenames from
// an HLS manifest body. Lenient — non-#-lines that look like filenames
// are accepted as segments.
func parseManifest(body string) (time.Duration, []string) {
	var (
		segDur time.Duration
		names  []string
	)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			vs := strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:")
			if v, err := time.ParseDuration(vs + "s"); err == nil {
				segDur = v
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, line)
	}
	return segDur, names
}

// pct returns the q-th percentile (0..1) of a sorted slice. Linear
// interpolation between adjacent ranks. Matches the "type 7" definition
// most stats libraries use.
func pct(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	rank := q * float64(len(sorted)-1)
	lo := int(rank)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}
