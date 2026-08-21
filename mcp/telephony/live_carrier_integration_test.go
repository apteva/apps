//go:build integration && livecarrier

package main

// This opt-in Tier-2 test crosses a real carrier network without involving an
// LLM or a human tester. One dedicated number calls a second number whose
// inbound route points back to the same Telephony install. Two deterministic
// softphone protocol clients answer the resulting call legs and exchange known
// PCM tones in both directions.
//
// Required environment:
//
//	RUN_TELEPHONY_LIVE_CARRIER=1
//	APTEVA_LIVE_BASE_URL=https://public-apteva.example
//	APTEVA_LIVE_API_KEY=...
//	APTEVA_LIVE_PROJECT_ID=...
//	TELEPHONY_LIVE_FROM_NUMBER=+...
//	TELEPHONY_LIVE_TO_NUMBER=+...
//
// FROM must be voice-capable with an outbound profile. TO must be a different,
// dedicated number routed to this Telephony install with answer_mode set to
// human_browser. The test places a billable call and hangs it up after the
// audio assertions.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type liveCarrierConfig struct {
	baseURL   string
	apiKey    string
	projectID string
	from      string
	to        string
}

type liveCarrierCall struct {
	ID          string `json:"id"`
	Direction   string `json:"direction"`
	ToNumber    string `json:"to_number"`
	FromNumber  string `json:"from_number"`
	Status      string `json:"status"`
	MediaStatus string `json:"media_status"`
	PeerKind    string `json:"peer_kind"`
	Error       string `json:"error_message"`
}

type liveToneResult struct {
	pcm          []int16
	firstArrival time.Duration
	err          error
}

var liveCarrierHTTPClient = &http.Client{Timeout: 30 * time.Second}

func TestLiveCarrierToneDetector(t *testing.T) {
	fixture := append(make([]int16, 4800), sinePCM(24000, 523, 12000)...)
	rms, score := strongestLiveToneWindow(fixture, 24000, 523)
	_, wrongScore := strongestLiveToneWindow(fixture, 24000, 941)
	if rms < 5000 || score < 0.8 || wrongScore > 0.05 {
		t.Fatalf("tone detector rejected deterministic fixture: rms=%.1f score=%.3f wrong_score=%.3f", rms, score, wrongScore)
	}
}

func TestTier2LiveCarrierLoopback(t *testing.T) {
	cfg := requireLiveCarrierConfig(t)
	baseline := liveCarrierCalls(t, cfg)
	known := make(map[string]bool, len(baseline))
	for _, call := range baseline {
		known[call.ID] = true
	}

	var outbound softphoneSession
	liveCarrierJSON(t, cfg, http.MethodPost, "/softphone/place", map[string]any{
		"to": cfg.to, "from": cfg.from, "timeout_sec": 30, "recording": false,
	}, &outbound)
	if outbound.CallID == "" || outbound.MediaURL == "" {
		t.Fatalf("outbound softphone session is incomplete: call_id=%q media_url=%q", outbound.CallID, outbound.MediaURL)
	}
	callActive := true
	t.Cleanup(func() {
		if callActive {
			if err := liveCarrierPost(cfg, "/calls/"+url.PathEscape(outbound.CallID)+"/hangup", nil, nil); err != nil {
				t.Logf("live carrier cleanup: %v", err)
			}
		}
	})

	outboundBrowser := dialLiveCarrierWS(t, cfg, outbound.MediaURL)
	waitLiveSoftphoneEvent(t, outboundBrowser, "peer.connected", 20*time.Second)

	inboundCall := waitLiveInboundCall(t, cfg, known, 30*time.Second)
	if inboundCall.PeerKind != peerKindHuman || inboundCall.Status != "pending" {
		t.Fatalf("destination route must use answer_mode=human_browser; inbound call=%+v", inboundCall)
	}
	var inbound softphoneSession
	liveCarrierJSON(t, cfg, http.MethodPost, "/softphone/answer/"+url.PathEscape(inboundCall.ID), map[string]any{}, &inbound)
	if inbound.CallID != inboundCall.ID || inbound.MediaURL == "" {
		t.Fatalf("inbound softphone session is incomplete: %+v", inbound)
	}
	inboundBrowser := dialLiveCarrierWS(t, cfg, inbound.MediaURL)
	waitLiveSoftphoneEvent(t, inboundBrowser, "peer.connected", 20*time.Second)

	waitLiveCallConnected(t, cfg, outbound.CallID, 30*time.Second)
	waitLiveCallConnected(t, cfg, inbound.CallID, 30*time.Second)
	drainLiveCarrierAudio(outboundBrowser)
	drainLiveCarrierAudio(inboundBrowser)

	assertLiveToneExchange(t, outboundBrowser, inboundBrowser, 523, "outbound-to-inbound")
	assertLiveToneExchange(t, inboundBrowser, outboundBrowser, 941, "inbound-to-outbound")

	if err := liveCarrierPost(cfg, "/calls/"+url.PathEscape(outbound.CallID)+"/hangup", nil, nil); err != nil {
		t.Fatal(err)
	}
	callActive = false
	waitLiveCallTerminal(t, cfg, outbound.CallID, 30*time.Second)
	waitLiveCallTerminal(t, cfg, inbound.CallID, 30*time.Second)
}

func requireLiveCarrierConfig(t *testing.T) liveCarrierConfig {
	t.Helper()
	if os.Getenv("RUN_TELEPHONY_LIVE_CARRIER") != "1" {
		t.Fatal("live-carrier profile places a billable call; set RUN_TELEPHONY_LIVE_CARRIER=1 to confirm")
	}
	cfg := liveCarrierConfig{
		baseURL:   strings.TrimRight(strings.TrimSpace(os.Getenv("APTEVA_LIVE_BASE_URL")), "/"),
		apiKey:    strings.TrimSpace(os.Getenv("APTEVA_LIVE_API_KEY")),
		projectID: strings.TrimSpace(os.Getenv("APTEVA_LIVE_PROJECT_ID")),
		from:      strings.TrimSpace(os.Getenv("TELEPHONY_LIVE_FROM_NUMBER")),
		to:        strings.TrimSpace(os.Getenv("TELEPHONY_LIVE_TO_NUMBER")),
	}
	missing := []string{}
	for name, value := range map[string]string{
		"APTEVA_LIVE_BASE_URL": cfg.baseURL, "APTEVA_LIVE_API_KEY": cfg.apiKey,
		"APTEVA_LIVE_PROJECT_ID": cfg.projectID, "TELEPHONY_LIVE_FROM_NUMBER": cfg.from,
		"TELEPHONY_LIVE_TO_NUMBER": cfg.to,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("live-carrier profile is missing: %s", strings.Join(missing, ", "))
	}
	parsed, err := url.Parse(cfg.baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		t.Fatalf("APTEVA_LIVE_BASE_URL must be a public https URL, got %q", cfg.baseURL)
	}
	if !validE164(cfg.from) || !validE164(cfg.to) || cfg.from == cfg.to {
		t.Fatal("TELEPHONY_LIVE_FROM_NUMBER and TELEPHONY_LIVE_TO_NUMBER must be different E.164 numbers")
	}
	return cfg
}

func liveCarrierAppURL(cfg liveCarrierConfig, path string) string {
	endpoint, _ := url.Parse(cfg.baseURL + "/api/apps/telephony" + path)
	query := endpoint.Query()
	query.Set("project_id", cfg.projectID)
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func liveCarrierJSON(t *testing.T, cfg liveCarrierConfig, method, path string, body, out any) {
	t.Helper()
	var encoded io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		encoded = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, liveCarrierAppURL(cfg, path), encoded)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := liveCarrierHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("live carrier %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("live carrier %s %s: status=%d body=%s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode live carrier %s %s: %v body=%s", method, path, err, raw)
		}
	}
}

func liveCarrierPost(cfg liveCarrierConfig, path string, body, out any) error {
	var encoded io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		encoded = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(http.MethodPost, liveCarrierAppURL(cfg, path), encoded)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := liveCarrierHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func liveCarrierCalls(t *testing.T, cfg liveCarrierConfig) []liveCarrierCall {
	t.Helper()
	var response struct {
		Calls []liveCarrierCall `json:"calls"`
	}
	liveCarrierJSON(t, cfg, http.MethodGet, "/calls", nil, &response)
	return response.Calls
}

func waitLiveInboundCall(t *testing.T, cfg liveCarrierConfig, known map[string]bool, timeout time.Duration) liveCarrierCall {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, call := range liveCarrierCalls(t, cfg) {
			if !known[call.ID] && call.Direction == "inbound" && call.ToNumber == cfg.to && call.FromNumber == cfg.from {
				return call
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("real carrier did not deliver an inbound call from %s to %s within %s", cfg.from, cfg.to, timeout)
	return liveCarrierCall{}
}

func waitLiveCallConnected(t *testing.T, cfg liveCarrierConfig, callID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, call := range liveCarrierCalls(t, cfg) {
			if call.ID != callID {
				continue
			}
			if call.Error != "" {
				t.Fatalf("call %s failed while connecting: %s", callID, call.Error)
			}
			if (call.Status == "answered" || call.Status == "in-progress") && call.MediaStatus == "connected" {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("call %s did not reach answered + media connected", callID)
}

func waitLiveCallTerminal(t *testing.T, cfg liveCarrierConfig, callID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, call := range liveCarrierCalls(t, cfg) {
			if call.ID == callID && isTerminalStatus(call.Status) {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("call %s did not receive a terminal carrier lifecycle event", callID)
}

func dialLiveCarrierWS(t *testing.T, cfg liveCarrierConfig, mediaURL string) net.Conn {
	t.Helper()
	endpoint, err := url.Parse(mediaURL)
	if err != nil {
		t.Fatal(err)
	}
	if !endpoint.IsAbs() {
		base, _ := url.Parse(cfg.baseURL)
		endpoint = base.ResolveReference(endpoint)
	}
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	conn, buffered, _, err := ws.Dial(t.Context(), endpoint.String())
	if err != nil {
		t.Fatalf("dial live softphone media: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if buffered != nil {
		return hijackedConn{Conn: conn, reader: buffered}
	}
	return conn
}

func waitLiveSoftphoneEvent(t *testing.T, conn net.Conn, kind string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		_ = conn.SetReadDeadline(deadline)
		data, op, err := wsutil.ReadServerData(conn)
		if err != nil {
			t.Fatalf("wait for softphone %s: %v", kind, err)
		}
		if op != ws.OpText {
			continue
		}
		var event map[string]any
		if json.Unmarshal(data, &event) == nil && event["type"] == kind {
			_ = conn.SetReadDeadline(time.Time{})
			return
		}
		if event["type"] == "call.error" {
			t.Fatalf("softphone call error: %v", event["detail"])
		}
	}
}

func drainLiveCarrierAudio(conn net.Conn) {
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		readDeadline := time.Now().Add(50 * time.Millisecond)
		if readDeadline.After(deadline) {
			readDeadline = deadline
		}
		_ = conn.SetReadDeadline(readDeadline)
		if _, _, err := wsutil.ReadServerData(conn); err != nil {
			_ = conn.SetReadDeadline(time.Time{})
			return
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
}

func assertLiveToneExchange(t *testing.T, source, destination net.Conn, frequency int, label string) {
	t.Helper()
	started := time.Now()
	result := make(chan liveToneResult, 1)
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		pcm := make([]int16, 0, 48000)
		first := time.Duration(0)
		for len(pcm) < 36000 {
			_ = destination.SetReadDeadline(deadline)
			data, op, err := wsutil.ReadServerData(destination)
			if err != nil {
				result <- liveToneResult{pcm: pcm, firstArrival: first, err: err}
				return
			}
			if op != ws.OpBinary || len(data) == 0 {
				continue
			}
			if first == 0 {
				first = time.Since(started)
			}
			pcm = append(pcm, bytesToPCM16(data)...)
		}
		_ = destination.SetReadDeadline(time.Time{})
		result <- liveToneResult{pcm: pcm, firstArrival: first}
	}()

	tone := sinePCM(24000, frequency, 480*100)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for offset := 0; offset < len(tone); offset += 480 {
		if err := wsutil.WriteClientBinary(source, pcm16ToBytes(tone[offset:offset+480])); err != nil {
			t.Fatalf("%s write audio: %v", label, err)
		}
		<-ticker.C
	}

	var received liveToneResult
	select {
	case received = <-result:
	case <-time.After(12 * time.Second):
		t.Fatalf("%s timed out waiting for real carrier audio", label)
	}
	if received.err != nil && len(received.pcm) < 12000 {
		t.Fatalf("%s carrier audio ended early: samples=%d err=%v", label, len(received.pcm), received.err)
	}
	peakRMS, toneScore := strongestLiveToneWindow(received.pcm, 24000, frequency)
	if peakRMS < 500 || toneScore < 0.12 {
		t.Fatalf("%s audio quality failed: samples=%d peak_rms=%.1f tone_score=%.3f latency=%s", label, len(received.pcm), peakRMS, toneScore, received.firstArrival)
	}
	if received.firstArrival <= 0 || received.firstArrival > 5*time.Second {
		t.Fatalf("%s audio latency out of bounds: %s", label, received.firstArrival)
	}
	t.Logf("%s passed: samples=%d peak_rms=%.1f tone_score=%.3f first_audio=%s", label, len(received.pcm), peakRMS, toneScore, received.firstArrival.Round(time.Millisecond))
}

func strongestLiveToneWindow(samples []int16, sampleRate, frequency int) (peakRMS, peakScore float64) {
	window := sampleRate / 5
	if len(samples) < window {
		window = len(samples)
	}
	if window == 0 {
		return 0, 0
	}
	step := window / 2
	if step == 0 {
		step = 1
	}
	for start := 0; start+window <= len(samples); start += step {
		var energy, sinSum, cosSum float64
		for index, sample := range samples[start : start+window] {
			value := float64(sample)
			phase := 2 * math.Pi * float64(frequency*index) / float64(sampleRate)
			energy += value * value
			sinSum += value * math.Sin(phase)
			cosSum += value * math.Cos(phase)
		}
		rms := math.Sqrt(energy / float64(window))
		score := 0.0
		if energy > 0 {
			score = 2 * (sinSum*sinSum + cosSum*cosSum) / (float64(window) * energy)
		}
		peakRMS = math.Max(peakRMS, rms)
		peakScore = math.Max(peakScore, score)
	}
	return peakRMS, peakScore
}
