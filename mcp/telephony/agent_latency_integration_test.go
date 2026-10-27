//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Matched PCM replay through real local Core and Server processes. The baseline
// uses Conversations' wire format, not a simulated model response in Telephony.
// Default mode responds at a known input sample position, 500ms after the clip;
// --live additionally measures real model latency without placing PSTN calls.
func TestAgentAudioMatchedLatency(t *testing.T) {
	if os.Getenv("TELEPHONY_AGENT_AUDIO_LATENCY") != "1" {
		t.Skip("run benchmark:agent-audio --latency true")
	}
	live := os.Getenv("TELEPHONY_AGENT_AUDIO_LIVE") == "1"
	pauseMS, err := strconv.Atoi(os.Getenv("TELEPHONY_LATENCY_PAUSE_MS"))
	if os.Getenv("TELEPHONY_LATENCY_PAUSE_MS") != "" && (err != nil || pauseMS < 0 || pauseMS > 2000) {
		t.Fatal("input pause must be 0..2000ms")
	}
	speech, err := readLocalPromptWAV("testdata/agent-audio/caller-interrupt-fr.wav")
	if err != nil {
		t.Fatal(err)
	}
	// Trim only recorded leading/trailing silence, once for all transports.
	start, end := 0, len(speech)
	for start < end && speech[start] < 1000 && speech[start] > -1000 {
		start++
	}
	for end > start && speech[end-1] < 1000 && speech[end-1] > -1000 {
		end--
	}
	speech = speech[start:end]
	t.Setenv("TELEPHONY_LATENCY_REPLY_AFTER_MS", strconv.Itoa(500+((len(speech)+479)/480)*20+500))
	paths := []struct{ provider, direction string }{{"conversations-pcm", "inbound"}, {"twilio", "inbound"}, {"twilio", "outbound"}}
	if live {
		paths = append(paths, struct{ provider, direction string }{"conversations-pcm-pcmu", "inbound"})
	}
	if !live || os.Getenv("TELEPHONY_LATENCY_SIP") == "1" {
		paths = append(paths, struct{ provider, direction string }{"sip-PCMA", "inbound"}, struct{ provider, direction string }{"sip-PCMA", "outbound"}, struct{ provider, direction string }{"sip-G722", "inbound"}, struct{ provider, direction string }{"sip-G722", "outbound"})
	}
	samples, _ := strconv.Atoi(os.Getenv("TELEPHONY_LATENCY_SAMPLES"))
	if samples < 1 {
		samples = 1
	}
	if samples > 5 {
		t.Fatal("latency samples must be 1..5")
	}
	root := os.Getenv("TELEPHONY_INTERNAL_OUTPUT")
	for sample := 1; sample <= samples; sample++ {
		for _, path := range paths {
			t.Run(fmt.Sprintf("sample-%d/%s/%s", sample, path.provider, path.direction), func(t *testing.T) {
				t.Setenv("TELEPHONY_INTERNAL_OUTPUT", filepath.Join(root, fmt.Sprintf("sample-%d", sample)))
				a, row, core, probe, output := newAgentPipeline(t, path.provider, path.direction, live)
				pcm := newPCMResampler(24000, probe.sampleRate).Process(speech)
				var stats struct {
					Events []struct {
						Type string         `json:"type"`
						Time time.Time      `json:"time"`
						Data map[string]any `json:"data"`
					} `json:"events"`
				}
				stop, done := make(chan struct{}), make(chan struct{})
				callerEnd := make(chan time.Time, 1)
				defer func() { close(stop); <-done }()
				go func() {
					defer close(done)
					ticker := time.NewTicker(20 * time.Millisecond)
					defer ticker.Stop()
					position := -probe.sampleRate / 2 // identical 500ms warmup for each path
					ended := false
					for {
						select {
						case <-stop:
							return
						case <-ticker.C:
							frame := make([]int16, probe.sampleRate/50)
							if position >= 0 && position < len(pcm) {
								copy(frame, pcm[position:min(position+len(frame), len(pcm))])
							}
							position += len(frame)
							if probe.send(frame) != nil {
								return
							}
							if !ended && position >= len(pcm) {
								ended = true
								callerEnd <- time.Now()
							}
						}
					}
				}()
				var ended, first, firstAny, expectedText time.Time
				deadline := time.NewTimer(35 * time.Second)
				defer deadline.Stop()
				poll := time.NewTicker(100 * time.Millisecond)
				defer poll.Stop()
				for first.IsZero() || ended.IsZero() || live && expectedText.IsZero() {
					select {
					case ended = <-callerEnd:
					case at := <-probe.audible:
						if firstAny.IsZero() {
							firstAny = at
						}
						if !ended.IsZero() && !at.Before(ended) && first.IsZero() {
							first = at
						}
					case <-poll.C:
						if live && !ended.IsZero() {
							agentFixtureJSON(t, "GET", core.url+"/stats", nil, &stats)
							for _, e := range stats.Events {
								text, _ := e.Data["text"].(string)
								if e.Type == "realtime.assistant" && strings.Contains(strings.ToLower(text), "mardi") {
									expectedText = e.Time
									break
								}
							}
						}
					case <-deadline.C:
						t.Fatal("no speech reply within 35s; see fixture logs")
					}
				}
				// Let telemetry's bounded 1s sampling interval close before reading.
				time.Sleep(1100 * time.Millisecond)
				agentFixtureJSON(t, "GET", core.url+"/stats", nil, &stats)
				maxInput, maxOutput := 0.0, 0.0
				for _, e := range stats.Events {
					if e.Type == "realtime.input_flow" {
						if n, ok := e.Data["input_wait_max_ms"].(float64); ok {
							maxInput = max(maxInput, n)
						}
					}
					if e.Type == "realtime.utterance_timing" {
						if n, ok := e.Data["provider_audio_to_bridge_write_ms"].(float64); ok {
							maxOutput = max(maxOutput, n)
						}
					}
				}
				result := map[string]any{"live": live, "provider": path.provider, "direction": path.direction, "injected_input_pause_ms": pauseMS, "caller_end_to_first_reply_ms": float64(first.Sub(ended)) / float64(time.Millisecond), "caller_end_to_first_output_ms": float64(firstAny.Sub(ended)) / float64(time.Millisecond), "max_core_input_wait_ms": maxInput, "max_provider_audio_to_bridge_write_ms": maxOutput, "core_events": stats.Events, "telephony": a.liveAgentAudio(row.ID)}
				if !expectedText.IsZero() {
					result["caller_end_to_expected_answer_transcript_ms"] = float64(expectedText.Sub(ended)) / float64(time.Millisecond)
				}
				raw, _ := json.MarshalIndent(result, "", "  ")
				if err := os.WriteFile(filepath.Join(output, "latency.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
				t.Logf("LATENCY path=%s/%s live=%t caller-end-to-first-reply=%v max-Core-input-wait=%.1fms", path.provider, path.direction, live, first.Sub(ended), maxInput)
				if !live {
					// Fake provider needs 500ms of silence; allow bounded carrier/SIP
					// reserve and local scheduling, but fail on multi-second queueing.
					if delay := first.Sub(ended); delay < 400*time.Millisecond || delay > 1200*time.Millisecond {
						t.Errorf("transport latency outside acceptance budget: %v", delay)
					}
					if maxInput > 150 {
						t.Errorf("Core adds excessive input delay: %.1fms", maxInput)
					}
				}
				if live && (first.Sub(ended) > 2*time.Second || expectedText.Sub(ended) > 2*time.Second) {
					t.Errorf("live conversational response exceeded 2s: first audio=%v, expected-answer transcript=%v", first.Sub(ended), expectedText.Sub(ended))
				}
			})
		}
	}
}
