package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAndroidTakeTrimsDrainAndPreservesIdleTail(t *testing.T) {
	for _, audio := range []bool{false, true} {
		src := testVideo(t, 2, audio)
		for _, duration := range []time.Duration{1250 * time.Millisecond, 3250 * time.Millisecond} {
			t.Run(fmt.Sprintf("audio=%v/duration=%s", audio, duration), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "capture.mp4")
				b, _ := os.ReadFile(src)
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
				if err := normaliseAndroidTake(context.Background(), path, duration, audio); err != nil {
					t.Fatal(err)
				}
				p, err := probeMedia(context.Background(), path)
				if err != nil || math.Abs(float64(p.DurationMS-duration.Milliseconds())) > 40 || p.HasAudio != audio {
					t.Fatalf("take duration/audio: %+v, %v", p, err)
				}
				if b, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-f", "null", "-").CombinedOutput(); err != nil || len(b) != 0 {
					t.Fatalf("decode: %v %s", err, b)
				}
			})
		}
	}
	if err := normaliseAndroidTake(context.Background(), testVideo(t, 1, false), time.Second, true); err == nil || !strings.Contains(err.Error(), "audio") {
		t.Fatalf("silently invented requested device audio: %v", err)
	}
}

func TestRecordedRunFailsWhenCaptureFails(t *testing.T) {
	valid := testVideo(t, 1, false)
	for _, tc := range []struct {
		name, src, expectedKind string
		stopErr                 error
		audio, assertionFails   bool
	}{
		{name: "unplayable", expectedKind: "infrastructure_error"},
		{name: "playable-but-stop-failed", src: valid, stopErr: errors.New("encoder shutdown failed"), expectedKind: "infrastructure_error"},
		{name: "audio-disappeared", src: valid, audio: true, expectedKind: "infrastructure_error"},
		{name: "retain-assertion-failure", expectedKind: "assertion_failed", assertionFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, backend, rb := recordFixture(t, tc.src)
			rb.stopErr, rb.audio = tc.stopErr, tc.audio
			step := map[string]any{"action": "screenshot"}
			if tc.assertionFails {
				step = map[string]any{"action": "assert_text", "params": map[string]any{"text": "not present"}}
			}
			id := execute(t, e, "p", "mobile_test_run", map[string]any{"device_id": "android:phone", "record": map[string]any{"audio": tc.audio}, "steps": []any{step}}).(map[string]any)["run_id"].(string)
			r := waitRun(t, e, "p", id)
			if r.Status != "failed" || r.FailureKind != tc.expectedKind || !strings.Contains(r.Error, "test recording failed") {
				t.Fatalf("false passing run: %+v", r)
			}
			if len(r.Results) != 1 || r.Results[0].ArtifactID == "" {
				t.Fatalf("lost step evidence: %+v", r)
			}
			backend.mu.Lock()
			closes := backend.closes
			backend.mu.Unlock()
			if closes != 1 {
				t.Fatalf("session was not closed: %d", closes)
			}
		})
	}
}

func TestScrcpyTerminalReadyAndGracefulStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix capture terminal")
	}
	t.Setenv("MOBILE_CAPTURE_TEST_HELPER", "ready")
	p, ready, err := startScrcpyProcess(os.Args[0], []string{"-test.run=^TestScrcpyTerminalHelper$"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer p.interrupt(ctx, time.Second)
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("capture did not signal real readiness")
	}
	if err := p.interrupt(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOBILE_CAPTURE_TEST_HELPER", "fail")
	p, ready, err = startScrcpyProcess(os.Args[0], []string{"-test.run=^TestScrcpyTerminalHelper$"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("failed capture process leaked")
	}
	select {
	case <-ready:
		t.Fatal("failed encoder was declared ready")
	default:
	}
	if p.exitError() == nil {
		t.Fatal("lost encoder failure")
	}
}

func TestRecordedRunFailsWhenRecorderExitsEarly(t *testing.T) {
	e, _, rb := recordFixture(t, testVideo(t, 1, false))
	id := execute(t, e, "p", "mobile_test_run", map[string]any{"device_id": "android:phone", "record": true,
		"steps": []any{map[string]any{"action": "wait", "params": map[string]any{"ms": 150}}}}).(map[string]any)["run_id"].(string)
	deadline := time.Now().Add(2 * time.Second)
	var recorder *fakeRecorder
	for time.Now().Before(deadline) {
		rb.mu.Lock()
		recorder = rb.last
		rb.mu.Unlock()
		if recorder != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if recorder == nil {
		t.Fatal("capture did not start")
	}
	close(recorder.done)
	r := waitRun(t, e, "p", id)
	if r.Status != "failed" || !strings.Contains(r.Error, "capture stopped unexpectedly") {
		t.Fatalf("early stop passed: %+v", r)
	}
}

func TestScrcpyTerminalHelper(t *testing.T) {
	mode := os.Getenv("MOBILE_CAPTURE_TEST_HELPER")
	if mode == "" {
		return
	}
	if mode == "fail" {
		fmt.Println("ERROR: encoder unavailable")
		os.Exit(1)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	fmt.Println("INFO: Recording started to mp4 file: test.mp4")
	<-stop
	fmt.Println("INFO: Recording complete to mp4 file: test.mp4")
	os.Exit(0)
}
