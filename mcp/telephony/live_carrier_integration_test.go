//go:build integration && livecarrier

package main

// This opt-in Tier-2 test crosses a real carrier network without involving an
// LLM or a human tester. One dedicated number calls a second number whose
// inbound route points back to the same Telephony install. The test publishes
// a real IVR, selects its browser destination through a timed fallback, then two
// deterministic softphone protocol clients answer the resulting call legs and
// exchange known PCM tones in both directions.
//
// Required environment:
//
//	RUN_TELEPHONY_LIVE_CARRIER=I_UNDERSTAND_THIS_PLACES_A_BILLABLE_CALL
//	APTEVA_LIVE_BASE_URL=https://public-apteva.example
//	APTEVA_LIVE_API_KEY=...
//	APTEVA_LIVE_PROJECT_ID=...
//	TELEPHONY_LIVE_FROM_NUMBER=+...
//	TELEPHONY_LIVE_TO_NUMBER=+...
//
// FROM must be voice-capable with an outbound profile. TO must be a different,
// dedicated number with a healthy inbound route to this Telephony install. The
// test temporarily assigns that route to its deterministic IVR and restores
// the previous flow afterward. It places one billable call and hangs it up
// after the routing, audio, and recording assertions. Both numbers must belong
// to the Telnyx connection bound to the target Telephony installation.

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
	ID                   string         `json:"id"`
	Direction            string         `json:"direction"`
	ToNumber             string         `json:"to_number"`
	FromNumber           string         `json:"from_number"`
	Status               string         `json:"status"`
	AnsweredAt           string         `json:"answered_at"`
	MediaStatus          string         `json:"media_status"`
	PeerKind             string         `json:"peer_kind"`
	RoutingFlowID        string         `json:"routing_flow_id"`
	RoutingFlowVersionID string         `json:"routing_flow_version_id"`
	RoutingDestinationID string         `json:"routing_destination_id"`
	Error                string         `json:"error_message"`
	MediaError           string         `json:"media_error_message"`
	BrowserDiag          map[string]any `json:"browser_audio_diagnostics"`
	CarrierDiag          map[string]any `json:"carrier_audio_diagnostics"`
}

type liveConnectedNumber struct {
	PhoneNumber   string `json:"phone_number"`
	Provider      string `json:"provider"`
	RoutingHealth string `json:"routing_health"`
	Route         *struct {
		Enabled    bool   `json:"enabled"`
		AnswerMode string `json:"answer_mode"`
	} `json:"route"`
	Outbound struct {
		Status string `json:"status"`
	} `json:"outbound"`
}

type liveRecording struct {
	ID                  string `json:"id"`
	CallID              string `json:"call_id"`
	Provider            string `json:"provider"`
	ProviderStatus      string `json:"provider_status"`
	StorageStatus       string `json:"storage_status"`
	PlaybackURL         string `json:"playback_url"`
	ProviderRecordingID string `json:"provider_recording_id"`
}

type liveCarrierEvidence struct {
	Provider        string                                  `json:"provider"`
	OutboundCallID  string                                  `json:"outbound_call_id"`
	InboundCallID   string                                  `json:"inbound_call_id"`
	From            string                                  `json:"from"`
	To              string                                  `json:"to"`
	ToneResults     map[string]liveToneMetrics              `json:"tone_results"`
	LifecycleTopics map[string][]string                     `json:"lifecycle_topics"`
	RecordingID     string                                  `json:"recording_id"`
	RecordingSource string                                  `json:"recording_source"`
	RecordingTones  map[string]liveToneMetrics              `json:"recording_tones"`
	RecordingSpeech map[string]liveSpeechRecordingMetrics   `json:"recording_speech"`
	BrowserSpeech   map[string]liveBrowserDirectionEvidence `json:"browser_speech"`
	Diagnostics     map[string]liveCallDiagnostics          `json:"diagnostics"`
	IVR             liveIVREvidence                         `json:"ivr"`
}

type liveIVREvidence struct {
	FlowID          string   `json:"flow_id"`
	FlowVersionID   string   `json:"flow_version_id"`
	DestinationID   string   `json:"destination_id"`
	RouteID         string   `json:"route_id"`
	Selection       string   `json:"selection"`
	AssignedNumbers []string `json:"assigned_numbers"`
}

type liveRoutingRoute struct {
	ID                     string `json:"id"`
	PhoneNumber            string `json:"phone_number"`
	FlowID                 string `json:"flow_id"`
	PublishedFlowVersionID string `json:"published_flow_version_id"`
}

type liveIVRFixture struct {
	FlowID          string
	FlowVersionID   string
	DestinationID   string
	RouteID         string
	PreviousFlowID  string
	AssignedNumbers []string
}

type liveCallDiagnostics struct {
	Browser map[string]any `json:"browser"`
	Carrier map[string]any `json:"carrier"`
}

type liveToneMetrics struct {
	Samples        int     `json:"samples"`
	PeakRMS        float64 `json:"peak_rms"`
	PeakScore      float64 `json:"peak_score"`
	Coverage       float64 `json:"coverage"`
	LongestCutMS   int64   `json:"longest_cut_ms"`
	FirstArrivalMS int64   `json:"first_arrival_ms"`
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
	withCut := append(sinePCM(24000, 523, 12000), make([]int16, 2400)...)
	withCut = append(withCut, sinePCM(24000, 523, 12000)...)
	metrics := analyzeLiveTone(withCut, 24000, 523)
	if metrics.Coverage < 0.85 || metrics.LongestCutMS < 80 || metrics.LongestCutMS > 120 {
		t.Fatalf("continuity detector missed a 100ms cut: %+v", metrics)
	}
}

func TestTier2LiveCarrierIVRConversation(t *testing.T) {
	cfg := requireLiveCarrierConfig(t)
	requireLiveTelnyxPreflight(t, cfg)
	ivr := configureLiveCarrierIVR(t, cfg)
	baseline := liveCarrierCalls(t, cfg)
	known := make(map[string]bool, len(baseline))
	for _, call := range baseline {
		known[call.ID] = true
	}

	var outbound softphoneSession
	liveCarrierJSON(t, cfg, http.MethodPost, "/softphone/place", map[string]any{
		"to": cfg.to, "from": cfg.from, "timeout_sec": 30, "recording": true,
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

	inboundCall := waitLiveInboundCall(t, cfg, known, 30*time.Second)
	waitLiveCallStatus(t, cfg, outbound.CallID, "answered", 30*time.Second)
	inboundCall = waitLiveCarrierAnswered(t, cfg, inboundCall.ID, 30*time.Second)
	if inboundCall.Status != "pending" || inboundCall.RoutingFlowVersionID != ivr.FlowVersionID || inboundCall.RoutingDestinationID != "" {
		t.Fatalf("inbound IVR did not remain offerable while waiting for a digit: %+v", inboundCall)
	}
	// A second controlled Telnyx leg cannot emulate caller-originated DTMF: both
	// send_dtmf and audio injected through its media stream bypass the far leg's
	// gather detector. Exercise the real IVR timeout branch here; signed DTMF
	// webhooks and browser keypad commands are covered by integration tests.
	inboundCall = waitLiveIVRSelection(t, cfg, inboundCall.ID, ivr, 20*time.Second)
	if inboundCall.PeerKind != peerKindHuman || inboundCall.Status != "pending" {
		t.Fatalf("IVR did not offer its browser destination: %+v", inboundCall)
	}
	var inbound softphoneSession
	liveCarrierJSON(t, cfg, http.MethodPost, "/softphone/answer/"+url.PathEscape(inboundCall.ID), map[string]any{}, &inbound)
	if inbound.CallID != inboundCall.ID || inbound.MediaURL == "" {
		t.Fatalf("inbound softphone session is incomplete: %+v", inbound)
	}
	inboundBrowser := dialLiveCarrierWS(t, cfg, inbound.MediaURL)
	// Do not wait for outbound media before answering the destination. Some
	// carriers open the media stream only after the far leg answers.
	waitLiveSoftphoneEvent(t, outboundBrowser, "peer.connected", 20*time.Second)
	waitLiveSoftphoneEvent(t, inboundBrowser, "peer.connected", 20*time.Second)

	waitLiveCallConnected(t, cfg, outbound.CallID, 30*time.Second)
	waitLiveCallConnected(t, cfg, inbound.CallID, 30*time.Second)
	drainLiveCarrierAudio(outboundBrowser)
	drainLiveCarrierAudio(inboundBrowser)

	evidence := liveCarrierEvidence{
		Provider: "telnyx", OutboundCallID: outbound.CallID, InboundCallID: inbound.CallID,
		From: cfg.from, To: cfg.to, ToneResults: map[string]liveToneMetrics{},
		LifecycleTopics: map[string][]string{}, Diagnostics: map[string]liveCallDiagnostics{},
		IVR: liveIVREvidence{FlowID: ivr.FlowID, FlowVersionID: ivr.FlowVersionID, DestinationID: ivr.DestinationID,
			RouteID: ivr.RouteID, Selection: "timeout", AssignedNumbers: ivr.AssignedNumbers},
	}
	evidence.ToneResults["outbound_to_inbound"] = assertLiveToneExchange(t, outboundBrowser, inboundBrowser, 523, "outbound-to-inbound")
	evidence.ToneResults["inbound_to_outbound"] = assertLiveToneExchange(t, inboundBrowser, outboundBrowser, 941, "inbound-to-outbound")
	speech := runLiveBrowserSpeech(t, cfg, outbound, inbound)
	evidence.BrowserSpeech = speech.Evidence

	if err := liveCarrierPost(cfg, "/calls/"+url.PathEscape(outbound.CallID)+"/hangup", nil, nil); err != nil {
		t.Fatal(err)
	}
	callActive = false
	waitLiveCallTerminal(t, cfg, outbound.CallID, 30*time.Second)
	waitLiveCallTerminal(t, cfg, inbound.CallID, 30*time.Second)

	for _, callID := range []string{outbound.CallID, inbound.CallID} {
		final := waitLiveCallDiagnostics(t, cfg, callID, 10*time.Second)
		if final.MediaError != "" || final.Error != "" {
			t.Fatalf("final state is not a clean Telnyx call: %+v", final)
		}
		requireCleanLiveDiagnostics(t, callID, final.CarrierDiag)
		evidence.Diagnostics[callID] = liveCallDiagnostics{Browser: final.BrowserDiag, Carrier: final.CarrierDiag}
	}
	evidence.LifecycleTopics[outbound.CallID] = requireLiveLifecycle(t, cfg, outbound.CallID, "call.initiated", "call.answered", "call.completed")
	evidence.LifecycleTopics[inbound.CallID] = requireLiveLifecycle(t, cfg, inbound.CallID, "call.incoming", "call.answered", "call.completed")
	recording := waitLiveRecording(t, cfg, outbound.CallID, 90*time.Second)
	evidence.RecordingID = recording.ID
	evidence.RecordingSource = firstNonEmpty(recording.StorageStatus, recording.ProviderStatus)
	if recording.Provider != "telnyx" || recording.ProviderStatus != "completed" || recording.PlaybackURL == "" {
		t.Fatalf("Telnyx recording did not become playable: %+v", recording)
	}
	evidence.RecordingTones, evidence.RecordingSpeech = assertLiveRecordingAudio(t, cfg, recording, speech.Fixtures)

	encoded, _ := json.MarshalIndent(evidence, "", "  ")
	t.Logf("LIVE_CARRIER_EVIDENCE\n%s", encoded)
}

func requireLiveCarrierConfig(t *testing.T) liveCarrierConfig {
	t.Helper()
	if os.Getenv("RUN_TELEPHONY_LIVE_CARRIER") != "I_UNDERSTAND_THIS_PLACES_A_BILLABLE_CALL" {
		t.Fatal("live-carrier profile places a billable call; set RUN_TELEPHONY_LIVE_CARRIER=I_UNDERSTAND_THIS_PLACES_A_BILLABLE_CALL to confirm")
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

func requireLiveTelnyxPreflight(t *testing.T, cfg liveCarrierConfig) {
	t.Helper()
	var response struct {
		Provider string                `json:"provider"`
		Numbers  []liveConnectedNumber `json:"numbers"`
	}
	liveCarrierJSON(t, cfg, http.MethodPost, "/numbers/connected", map[string]any{}, &response)
	if response.Provider != "telnyx" {
		t.Fatalf("live carrier profile requires a Telnyx binding, got %q", response.Provider)
	}
	byNumber := make(map[string]liveConnectedNumber, len(response.Numbers))
	for _, number := range response.Numbers {
		byNumber[compactPhoneNumber(number.PhoneNumber)] = number
	}
	from, fromOK := byNumber[compactPhoneNumber(cfg.from)]
	to, toOK := byNumber[compactPhoneNumber(cfg.to)]
	if !fromOK || !toOK {
		t.Fatalf("both live-test numbers must belong to the bound Telnyx connection: from_owned=%t to_owned=%t", fromOK, toOK)
	}
	if from.Provider != "telnyx" || from.Outbound.Status != outboundReady {
		t.Fatalf("Telnyx source number is not outbound-ready: number=%s provider=%s status=%s", from.PhoneNumber, from.Provider, from.Outbound.Status)
	}
	if to.Provider != "telnyx" || to.Route == nil || !to.Route.Enabled {
		t.Fatalf("Telnyx destination must have an enabled inbound route: %+v", to)
	}
	if to.RoutingHealth != "healthy" {
		t.Fatalf("Telnyx destination routing is not healthy: number=%s health=%s", to.PhoneNumber, to.RoutingHealth)
	}
	t.Logf("Telnyx preflight passed: from=%s outbound=%s to=%s route=%s", cfg.from, from.Outbound.Status, cfg.to, to.RoutingHealth)
}

func configureLiveCarrierIVR(t *testing.T, cfg liveCarrierConfig) liveIVRFixture {
	t.Helper()
	var snapshot struct {
		Routes []liveRoutingRoute `json:"routes"`
	}
	liveCarrierJSON(t, cfg, http.MethodGet, "/routing/snapshot", nil, &snapshot)
	var route liveRoutingRoute
	for _, candidate := range snapshot.Routes {
		if compactPhoneNumber(candidate.PhoneNumber) == compactPhoneNumber(cfg.to) {
			route = candidate
			break
		}
	}
	if route.ID == "" {
		t.Fatalf("live IVR destination number %s has no Telephony inbound route", cfg.to)
	}

	suffix := strings.TrimPrefix(compactPhoneNumber(cfg.to), "+")
	fixture := liveIVRFixture{
		FlowID: "live_carrier_ivr_" + suffix, DestinationID: "live_carrier_browser_" + suffix,
		RouteID: route.ID, PreviousFlowID: route.FlowID,
	}
	liveCarrierJSON(t, cfg, http.MethodPost, "/routing/destinations/save", map[string]any{
		"id": fixture.DestinationID, "name": "Live carrier browser " + cfg.to, "kind": "browser", "enabled": true,
		"config": map[string]any{"hold_prompt": "Level two IVR test is connecting the browser operator.", "timeout_sec": 30},
	}, nil)
	draft := map[string]any{
		"entry": "menu",
		"nodes": []map[string]any{
			{"id": "menu", "type": "dtmf_menu", "label": "Automated Level 2 choice", "config": map[string]any{"prompt": "Automated routing test. The browser conversation will begin after this prompt."}, "branches": map[string]string{"1": "browser", "default": "end", "timeout": "browser"}},
			{"id": "browser", "type": "destination", "label": "Browser conversation", "config": map[string]any{"destination_id": fixture.DestinationID}},
			{"id": "end", "type": "hangup", "label": "Invalid or missing choice"},
		},
	}
	liveCarrierJSON(t, cfg, http.MethodPost, "/routing/flows/save", map[string]any{
		"id": fixture.FlowID, "name": "Live carrier IVR " + cfg.to,
		"description": "Deterministic Level 2 carrier, IVR, browser audio, and recording proof.", "draft": draft,
	}, nil)
	var published struct {
		Valid   bool `json:"valid"`
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
	}
	liveCarrierJSON(t, cfg, http.MethodPost, "/routing/flows/publish", map[string]any{"id": fixture.FlowID}, &published)
	if !published.Valid || published.Version.ID == "" {
		t.Fatalf("live IVR publication returned incomplete evidence: %+v", published)
	}
	fixture.FlowVersionID = published.Version.ID
	var assigned struct {
		OK        bool   `json:"ok"`
		Valid     bool   `json:"valid"`
		VersionID string `json:"version_id"`
	}
	liveCarrierJSON(t, cfg, http.MethodPost, "/routing/flows/numbers/assign", map[string]any{
		"flow_id": fixture.FlowID, "route_ids": []string{fixture.RouteID},
	}, &assigned)
	if !assigned.OK || !assigned.Valid || assigned.VersionID != fixture.FlowVersionID {
		t.Fatalf("live IVR assignment did not pin the published version: %+v", assigned)
	}
	var listed struct {
		Numbers []struct {
			PhoneNumber string `json:"phone_number"`
		} `json:"numbers"`
	}
	liveCarrierJSON(t, cfg, http.MethodGet, "/routing/flows/numbers?flow_id="+url.QueryEscape(fixture.FlowID), nil, &listed)
	for _, number := range listed.Numbers {
		fixture.AssignedNumbers = append(fixture.AssignedNumbers, number.PhoneNumber)
	}
	if len(fixture.AssignedNumbers) != 1 || compactPhoneNumber(fixture.AssignedNumbers[0]) != compactPhoneNumber(cfg.to) {
		t.Fatalf("live IVR assignment list=%v, want [%s]", fixture.AssignedNumbers, cfg.to)
	}

	t.Cleanup(func() {
		if fixture.PreviousFlowID != "" {
			if err := liveCarrierPost(cfg, "/routing/flows/numbers/assign", map[string]any{
				"flow_id": fixture.PreviousFlowID, "route_ids": []string{fixture.RouteID},
			}, nil); err != nil {
				t.Logf("restore previous live IVR assignment: %v", err)
			}
			return
		}
		if err := liveCarrierPost(cfg, "/routing/flows/numbers/unassign", map[string]any{
			"flow_id": fixture.FlowID, "route_ids": []string{fixture.RouteID},
		}, nil); err != nil {
			t.Logf("remove temporary live IVR assignment: %v", err)
		}
	})
	t.Logf("Live IVR ready: flow=%s version=%s route=%s number=%s", fixture.FlowID, fixture.FlowVersionID, fixture.RouteID, cfg.to)
	return fixture
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

func liveCarrierCallByID(t *testing.T, cfg liveCarrierConfig, callID string) liveCarrierCall {
	t.Helper()
	for _, call := range liveCarrierCalls(t, cfg) {
		if call.ID == callID {
			return call
		}
	}
	t.Fatalf("call %s disappeared from the live Telephony call list", callID)
	return liveCarrierCall{}
}

func waitLiveCallDiagnostics(t *testing.T, cfg liveCarrierConfig, callID string, timeout time.Duration) liveCarrierCall {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		call := liveCarrierCallByID(t, cfg, callID)
		if call.CarrierDiag["provider"] != nil {
			return call
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("call %s did not persist carrier audio diagnostics", callID)
	return liveCarrierCall{}
}

func requireCleanLiveDiagnostics(t *testing.T, callID string, diagnostics map[string]any) {
	t.Helper()
	provider, _ := diagnostics["provider"].(string)
	codec, _ := diagnostics["codec"].(string)
	pacer, _ := diagnostics["pacer_mode"].(string)
	sampleRate, _ := diagnostics["sample_rate"].(float64)
	droppedMS, _ := diagnostics["dropped_stale_ms"].(float64)
	sequenceGaps, _ := diagnostics["sequence_gaps"].(float64)
	if provider != "telnyx" || codec != carrierCodecL16_16 || pacer != "live_human" || int(sampleRate) != 16000 {
		t.Fatalf("call %s negotiated an unexpected carrier audio path: %+v", callID, diagnostics)
	}
	if droppedMS > 200 || sequenceGaps > 10 {
		t.Fatalf("call %s exceeded live audio loss limits: dropped_ms=%.0f sequence_gaps=%.0f diagnostics=%+v", callID, droppedMS, sequenceGaps, diagnostics)
	}
}

func liveCarrierMCP(t *testing.T, cfg liveCarrierConfig, tool string, args map[string]any) map[string]any {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := cfg.baseURL + "/api/apps/telephony/mcp?project_id=" + url.QueryEscape(cfg.projectID)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := liveCarrierHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("live MCP %s: %v", tool, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("live MCP %s: status=%d body=%s", tool, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Error != nil || len(envelope.Result.Content) == 0 {
		t.Fatalf("live MCP %s returned an invalid envelope: error=%v decode=%v body=%s", tool, envelope.Error, err, raw)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &result); err != nil || envelope.Result.IsError {
		t.Fatalf("live MCP %s tool result failed: decode=%v result=%s", tool, err, envelope.Result.Content[0].Text)
	}
	return result
}

func requireLiveLifecycle(t *testing.T, cfg liveCarrierConfig, callID string, required ...string) []string {
	t.Helper()
	result := liveCarrierMCP(t, cfg, "telephony_call_events_list", map[string]any{"call_id": callID, "limit": 200})
	rawEvents, _ := result["events"].([]any)
	topics := make([]string, 0, len(rawEvents))
	present := make(map[string]bool, len(rawEvents))
	for _, raw := range rawEvents {
		event, _ := raw.(map[string]any)
		topic, _ := event["topic"].(string)
		if topic != "" {
			topics = append(topics, topic)
			present[topic] = true
		}
	}
	for _, topic := range required {
		if !present[topic] {
			t.Fatalf("call %s lifecycle is missing %s; got %v", callID, topic, topics)
		}
	}
	return topics
}

func waitLiveRecording(t *testing.T, cfg liveCarrierConfig, callID string, timeout time.Duration) liveRecording {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var latest []liveRecording
	for time.Now().Before(deadline) {
		var response struct {
			Recordings []liveRecording `json:"recordings"`
		}
		liveCarrierJSON(t, cfg, http.MethodGet, "/recordings/?call_id="+url.QueryEscape(callID), nil, &response)
		latest = response.Recordings
		for _, recording := range latest {
			if recording.ProviderStatus == "completed" && recording.PlaybackURL != "" {
				return recording
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("call %s did not produce a playable recording within %s; recordings=%+v", callID, timeout, latest)
	return liveRecording{}
}

func assertLiveRecordingAudio(t *testing.T, cfg liveCarrierConfig, recording liveRecording, fixtures map[string]liveSpeechFixture) (map[string]liveToneMetrics, map[string]liveSpeechRecordingMetrics) {
	t.Helper()
	endpoint, err := url.Parse(recording.PlaybackURL)
	if err != nil {
		t.Fatalf("parse recording playback URL: %v", err)
	}
	if !endpoint.IsAbs() {
		base, _ := url.Parse(cfg.baseURL)
		endpoint = base.ResolveReference(endpoint)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)
	resp, err := liveCarrierHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("download live recording: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		t.Fatalf("download live recording: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	file, err := os.CreateTemp(t.TempDir(), "telnyx-loopback-*.wav")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	written, err := io.Copy(file, io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		t.Fatalf("save live recording: %v", err)
	}
	if written < 44 {
		t.Fatalf("live recording is unexpectedly short: %d bytes", written)
	}
	info, err := inspectPCM16WAV(file)
	if err != nil {
		t.Fatalf("inspect Telnyx recording: %v", err)
	}
	mixed := make([]int16, 0, info.DataSize/int64(info.Channels*2))
	if err := forEachWAVFrame(file, info, func(frame []int16) error {
		var sum int64
		for _, sample := range frame {
			sum += int64(sample)
		}
		mixed = append(mixed, int16(sum/int64(len(frame))))
		return nil
	}); err != nil {
		t.Fatalf("read Telnyx recording audio: %v", err)
	}
	toneResults := map[string]liveToneMetrics{
		"523_hz": analyzeLiveTone(mixed, info.SampleRate, 523),
		"941_hz": analyzeLiveTone(mixed, info.SampleRate, 941),
	}
	for frequency, metrics := range toneResults {
		if metrics.PeakRMS < 300 || metrics.PeakScore < 0.08 {
			t.Fatalf("Telnyx recording does not contain the %s test signal: %+v", frequency, metrics)
		}
	}
	speechResults := make(map[string]liveSpeechRecordingMetrics, len(fixtures))
	for direction, fixture := range fixtures {
		metrics := analyzeRecordedSpeech(mixed, info.SampleRate, fixture)
		if metrics.RecordedRMS < 300 || metrics.EnvelopeCorrelation < 0.35 {
			t.Fatalf("Telnyx recording does not preserve the %s speech cadence: %+v", direction, metrics)
		}
		speechResults[direction] = metrics
	}
	t.Logf("Telnyx recording verified: id=%s bytes=%d sample_rate=%d channels=%d", recording.ID, written, info.SampleRate, info.Channels)
	return toneResults, speechResults
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

func waitLiveCallStatus(t *testing.T, cfg liveCarrierConfig, callID, status string, timeout time.Duration) liveCarrierCall {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		call := liveCarrierCallByID(t, cfg, callID)
		if call.Error != "" {
			t.Fatalf("call %s failed while waiting for %s: %s", callID, status, call.Error)
		}
		if call.Status == status || (status == "answered" && call.Status == "in-progress") {
			return call
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("call %s did not reach %s within %s", callID, status, timeout)
	return liveCarrierCall{}
}

func waitLiveCarrierAnswered(t *testing.T, cfg liveCarrierConfig, callID string, timeout time.Duration) liveCarrierCall {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		call := liveCarrierCallByID(t, cfg, callID)
		if call.Error != "" {
			t.Fatalf("call %s failed while waiting for carrier answer: %s", callID, call.Error)
		}
		// An inbound IVR is carrier-answered but intentionally remains pending
		// in Telephony until its selected browser operator accepts the offer.
		if call.AnsweredAt != "" {
			return call
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("call %s did not receive a carrier answer within %s", callID, timeout)
	return liveCarrierCall{}
}

func waitLiveIVRSelection(t *testing.T, cfg liveCarrierConfig, callID string, fixture liveIVRFixture, timeout time.Duration) liveCarrierCall {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		call := liveCarrierCallByID(t, cfg, callID)
		if call.Error != "" {
			t.Fatalf("IVR call %s failed: %s", callID, call.Error)
		}
		if call.Status == "pending" && call.PeerKind == peerKindHuman && call.RoutingFlowID == fixture.FlowID &&
			call.RoutingFlowVersionID == fixture.FlowVersionID && call.RoutingDestinationID == fixture.DestinationID {
			return call
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("call %s did not select IVR destination %s within %s", callID, fixture.DestinationID, timeout)
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

func assertLiveToneExchange(t *testing.T, source, destination net.Conn, frequency int, label string) liveToneMetrics {
	t.Helper()
	// Telnyx continuously delivers 20 ms media frames, including silence. Drain
	// frames accumulated while the previous direction was under test so this
	// assertion measures newly transmitted audio instead of immediately filling
	// its sample window from stale silence.
	drainLiveCarrierAudio(destination)
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

	// Send exactly the 36,000 samples collected by the reader. Sending a longer
	// tone leaves its tail queued for the following direction and makes the
	// bidirectional assertion order-dependent.
	tone := sinePCM(24000, frequency, 480*75)
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
	metrics := analyzeLiveTone(received.pcm, 24000, frequency)
	metrics.FirstArrivalMS = received.firstArrival.Milliseconds()
	if metrics.PeakRMS < 500 || metrics.PeakScore < 0.12 {
		t.Fatalf("%s audio quality failed: %+v", label, metrics)
	}
	if received.firstArrival <= 0 || received.firstArrival > 5*time.Second {
		t.Fatalf("%s audio latency out of bounds: %s", label, received.firstArrival)
	}
	if metrics.Coverage < 0.7 || metrics.LongestCutMS > 200 {
		t.Fatalf("%s audio continuity failed: %+v", label, metrics)
	}
	t.Logf("%s passed: samples=%d peak_rms=%.1f tone_score=%.3f coverage=%.2f longest_cut=%dms first_audio=%s",
		label, metrics.Samples, metrics.PeakRMS, metrics.PeakScore, metrics.Coverage,
		metrics.LongestCutMS, received.firstArrival.Round(time.Millisecond))
	return metrics
}

func analyzeLiveTone(samples []int16, sampleRate, frequency int) liveToneMetrics {
	metrics := liveToneMetrics{Samples: len(samples)}
	window := sampleRate / 50 // 20 ms, matching carrier media packet cadence.
	if window <= 0 || len(samples) < window {
		return metrics
	}
	type windowResult struct {
		rms, score float64
		strong     bool
	}
	windows := make([]windowResult, 0, len(samples)/window)
	firstStrong, lastStrong := -1, -1
	for start := 0; start+window <= len(samples); start += window {
		rms, score := strongestLiveToneWindow(samples[start:start+window], sampleRate, frequency)
		strong := rms >= 300 && score >= 0.08
		windows = append(windows, windowResult{rms: rms, score: score, strong: strong})
		metrics.PeakRMS = math.Max(metrics.PeakRMS, rms)
		metrics.PeakScore = math.Max(metrics.PeakScore, score)
		if strong {
			if firstStrong < 0 {
				firstStrong = len(windows) - 1
			}
			lastStrong = len(windows) - 1
		}
	}
	if firstStrong < 0 {
		return metrics
	}
	strongCount, weakRun, maxWeakRun := 0, 0, 0
	for _, result := range windows[firstStrong : lastStrong+1] {
		if result.strong {
			strongCount++
			weakRun = 0
			continue
		}
		weakRun++
		maxWeakRun = max(maxWeakRun, weakRun)
	}
	activeWindows := lastStrong - firstStrong + 1
	metrics.Coverage = float64(strongCount) / float64(activeWindows)
	metrics.LongestCutMS = int64(maxWeakRun * 20)
	return metrics
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
