package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
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
	a.materializeURLs(ctx, s)
	if s.PlaybackURL == "" {
		return nil, errors.New("stream has no playback URL")
	}

	manifestURL := s.PlaybackURL
	// In dev (PUBLIC_URL unset), the materialized URL starts with
	// /api/apps/streaming/... Resolve it against http://localhost
	// + the sidecar's listen port. Pull the port from the env var
	// the SDK sets.
	if strings.HasPrefix(manifestURL, "/") {
		port := strings.TrimSpace(getenv("APTEVA_LISTEN_PORT", "8080"))
		manifestURL = "http://localhost:" + port + manifestURL
	}

	res := runLoadTest(ctx, manifestURL, viewers, duration)
	return res, nil
}

type loadResult struct {
	TargetViewers     int     `json:"target_viewers"`
	DurationSeconds   int     `json:"duration_seconds"`
	ManifestRequests  int     `json:"manifest_requests"`
	SegmentRequests   int     `json:"segment_requests"`
	BytesServed       int64   `json:"bytes_served"`
	ServedMbps        float64 `json:"served_mbps"`
	P50TTFBMillis     float64 `json:"p50_ttfb_ms"`
	P95TTFBMillis     float64 `json:"p95_ttfb_ms"`
	P99TTFBMillis     float64 `json:"p99_ttfb_ms"`
	HTTP5xx           int     `json:"http_5xx"`
	Refusals          int     `json:"refusals"`
	SegmentsLate      int     `json:"segments_late"`
	WallSeconds       float64 `json:"wall_seconds"`
}

func runLoadTest(ctx *sdk.AppCtx, manifestURL string, viewers, duration int) *loadResult {
	res := &loadResult{
		TargetViewers:   viewers,
		DurationSeconds: duration,
	}

	// Probe the manifest once to learn the segment URL pattern + the
	// segment duration the server is using. Bail fast if it 404s.
	probe, err := http.Get(manifestURL)
	if err != nil {
		return res
	}
	probeBody, _ := io.ReadAll(probe.Body)
	probe.Body.Close()
	segDur, segNames := parseManifest(string(probeBody))
	if segDur <= 0 {
		segDur = 4 * time.Second
	}
	if len(segNames) == 0 {
		// Manifest is empty — stream hasn't started yet. We can still
		// load-test the manifest endpoint itself though.
	}

	manifestURLParsed, err := url.Parse(manifestURL)
	if err != nil {
		return res
	}

	// Pre-compute the auth query suffix to append to segment URLs.
	authQuery := manifestURLParsed.RawQuery
	segmentBaseURL := func(name string) string {
		base := strings.TrimSuffix(manifestURL[:len(manifestURL)-len(manifestURLParsed.RequestURI())]+manifestURLParsed.Path, "/index.m3u8")
		u := base + "/" + name
		if authQuery != "" {
			u += "?" + authQuery
		}
		return u
	}

	var (
		manifestReqs atomic.Int64
		segReqs      atomic.Int64
		bytes        atomic.Int64
		fivexx       atomic.Int64
		refusals     atomic.Int64
		late         atomic.Int64
		ttfbMu       sync.Mutex
		ttfbs        []float64
	)

	recordTTFB := func(ms float64) {
		ttfbMu.Lock()
		ttfbs = append(ttfbs, ms)
		ttfbMu.Unlock()
	}

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

			fetch := func(u string, isManifest bool) {
				req, _ := http.NewRequestWithContext(rootCtx, http.MethodGet, u, nil)
				start := time.Now()
				resp, err := c.Do(req)
				if err != nil {
					if rootCtx.Err() != nil {
						return
					}
					refusals.Add(1)
					return
				}
				ttfb := float64(time.Since(start).Milliseconds())
				recordTTFB(ttfb)
				defer resp.Body.Close()
				if resp.StatusCode >= 500 {
					fivexx.Add(1)
					_, _ = io.Copy(io.Discard, resp.Body)
					return
				}
				if resp.StatusCode == http.StatusNotFound {
					if isManifest {
						refusals.Add(1)
					} else {
						late.Add(1)
					}
					_, _ = io.Copy(io.Discard, resp.Body)
					return
				}
				if isManifest {
					manifestReqs.Add(1)
					body, _ := io.ReadAll(resp.Body)
					bytes.Add(int64(len(body)))
					// Discover new segments + queue them.
					_, names := parseManifest(string(body))
					for _, n := range names {
						if !seen[n] {
							seen[n] = true
							// Fetch in this same goroutine so the
							// viewer's bandwidth budget mirrors a real
							// client: serial, not parallel.
							fetchSegment(c, segmentBaseURL(n), &segReqs, &bytes, &fivexx, &late, recordTTFB, rootCtx)
						}
					}
				} else {
					n, _ := io.Copy(io.Discard, resp.Body)
					bytes.Add(n)
					segReqs.Add(1)
				}
			}

			// Initial manifest fetch immediately, then on tick.
			fetch(manifestURL, true)
			for {
				select {
				case <-rootCtx.Done():
					return
				case <-tick.C:
					fetch(manifestURL, true)
				}
			}
		}(i)
	}
	wg.Wait()

	wall := time.Since(startWall).Seconds()
	res.WallSeconds = wall
	res.ManifestRequests = int(manifestReqs.Load())
	res.SegmentRequests = int(segReqs.Load())
	res.BytesServed = bytes.Load()
	res.HTTP5xx = int(fivexx.Load())
	res.Refusals = int(refusals.Load())
	res.SegmentsLate = int(late.Load())
	if wall > 0 {
		res.ServedMbps = float64(bytes.Load()*8) / 1_000_000.0 / wall
	}

	ttfbMu.Lock()
	defer ttfbMu.Unlock()
	if len(ttfbs) > 0 {
		sort.Float64s(ttfbs)
		res.P50TTFBMillis = pct(ttfbs, 0.50)
		res.P95TTFBMillis = pct(ttfbs, 0.95)
		res.P99TTFBMillis = pct(ttfbs, 0.99)
	}
	return res
}

func fetchSegment(c *http.Client, u string, segReqs, bytes, fivexx, late *atomic.Int64, recordTTFB func(float64), ctx context.Context) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		late.Add(1)
		return
	}
	defer resp.Body.Close()
	ttfb := float64(time.Since(start).Milliseconds())
	recordTTFB(ttfb)
	if resp.StatusCode >= 500 {
		fivexx.Add(1)
		_, _ = io.Copy(io.Discard, resp.Body)
		return
	}
	if resp.StatusCode == http.StatusNotFound {
		late.Add(1)
		_, _ = io.Copy(io.Discard, resp.Body)
		return
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	bytes.Add(n)
	segReqs.Add(1)
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
