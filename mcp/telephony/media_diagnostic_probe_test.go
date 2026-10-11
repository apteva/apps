//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"
)

// Runs the real Bun probe against the actual Telephony HTTP route, beside an
// independent bare echo. No external server, platform or carrier is contacted.
func TestMediaDiagnosticBunProbe(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Fatal("Bun is required for this explicitly selected integration test")
	}
	seconds := 6
	codecs := []string{"l16-16", "pcmu-8"}
	if raw := os.Getenv("TELEPHONY_DIAGNOSTIC_SOAK_SECONDS"); raw != "" {
		var err error
		seconds, err = strconv.Atoi(raw)
		if err != nil || seconds <= 300 {
			t.Fatal("soak must exceed the old five-minute cap")
		}
		codecs = []string{"l16-16"}
	}
	for _, codec := range codecs {
		t.Run(codec, func(t *testing.T) {
			a := &App{}
			s, token, _ := a.mediaDiagnostics.create("local", 0, time.Now())
			server := httptest.NewServer(http.HandlerFunc(a.handleDiagnosticEcho))
			defer server.Close()
			control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, reader, err := upgradeBuffered(w, r)
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					b, op, err := wsutil.ReadClientData(reader)
					if err != nil {
						return
					}
					if wsutil.WriteServerMessage(conn, op, b) != nil {
						return
					}
				}
			}))
			defer control.Close()
			output := filepath.Join(t.TempDir(), "probe.json")
			if dir := os.Getenv("TELEPHONY_DIAGNOSTIC_TEST_OUTPUT"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				output = filepath.Join(dir, codec+".json")
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Duration(seconds+30)*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bun", "benchmarks/media-diagnostic/probe.ts", "--url", strings.Replace(server.URL, "http://", "ws://", 1)+"/media/diagnostic/echo", "--control-url", strings.Replace(control.URL, "http://", "ws://", 1), "--seconds", strconv.Itoa(seconds), "--codec", codec, "--output", output)
			cmd.Env = append(os.Environ(), "TELEPHONY_DIAGNOSTIC_TOKEN="+token)
			b, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("probe: %v %s", err, b)
			}
			t.Log(string(b))
			raw, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Results []struct {
					Label    string `json:"label"`
					Sent     int    `json:"sent_frames"`
					Received int    `json:"echoed_frames"`
					Missing  int    `json:"missing_frames"`
					Failure  string `json:"failure"`
					Code     int    `json:"close_code"`
				}
			}
			if json.Unmarshal(raw, &result) != nil || len(result.Results) != 2 {
				t.Fatal(string(raw))
			}
			for _, r := range result.Results {
				if r.Sent < seconds*45 || r.Received != r.Sent || r.Missing != 0 || r.Failure != "" || r.Code != 1000 {
					t.Fatalf("bad paired probe: %+v", r)
				}
			}
			report := diagnosticFinished(t, s)
			if report.MediaFrames < int64(result.Results[0].Sent) || report.Sent != report.Received || report.CloseCode != 1000 {
				t.Fatal(report)
			}
			encoded, _ := json.Marshal(report)
			if dir := os.Getenv("TELEPHONY_DIAGNOSTIC_TEST_OUTPUT"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, codec+"-server.json"), encoded, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if report.Settings.DurationSeconds != 0 || report.CloseTrigger != "client_stop" || report.EventsEvicted == 0 && seconds > 300 {
				t.Fatal(report)
			}
			if strings.Contains(string(encoded), token) || strings.Contains(string(raw), token) {
				t.Fatal("token retained")
			}
		})
	}
}

// No --seconds argument: exercise checkpointing and operator stop of the
// default indefinite client, rather than relying on a finite runner timeout.
func TestMediaDiagnosticBunIndefiniteStop(t *testing.T) {
	a := &App{}
	s, token, _ := a.mediaDiagnostics.create("local", 0, time.Now())
	server := httptest.NewServer(http.HandlerFunc(a.handleDiagnosticEcho))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "indefinite.json")
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bun", "benchmarks/media-diagnostic/probe.ts", "--url", strings.Replace(server.URL, "http://", "ws://", 1)+"/media/diagnostic/echo", "--output", output)
	cmd.Env = append(os.Environ(), "TELEPHONY_DIAGNOSTIC_TOKEN="+token)
	var captured bytes.Buffer
	cmd.Stdout, cmd.Stderr = &captured, &captured
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	deadline := time.Now().Add(15 * time.Second)
	checkpoint := false
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(output)
		var report struct {
			State   string `json:"state"`
			Results []struct {
				Sent int `json:"sent_frames"`
			} `json:"results"`
		}
		if err == nil && json.Unmarshal(raw, &report) == nil && report.State == "running" && len(report.Results) == 1 && report.Results[0].Sent > 400 {
			checkpoint = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !checkpoint {
		t.Fatal("no live checkpoint")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("%v %s", err, captured.String())
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Results []struct {
			Sent     int    `json:"sent_frames"`
			Echoed   int    `json:"echoed_frames"`
			Code     int    `json:"close_code"`
			Expected bool   `json:"expected_finish"`
			Failure  string `json:"failure"`
		} `json:"results"`
	}
	if json.Unmarshal(raw, &report) != nil || len(report.Results) != 1 {
		t.Fatal(string(raw))
	}
	r := report.Results[0]
	if r.Sent != r.Echoed || r.Code != 1000 || !r.Expected || r.Failure != "" {
		t.Fatal(string(raw))
	}
	if diagnosticFinished(t, s).CloseTrigger != "client_stop" {
		t.Fatal(s.snapshot())
	}
}
