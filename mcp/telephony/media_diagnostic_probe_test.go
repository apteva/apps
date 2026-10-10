//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	for _, codec := range []string{"l16-16", "pcmu-8"} {
		t.Run(codec, func(t *testing.T) {
			a := &App{}
			s, token, _ := a.mediaDiagnostics.create("local", 10*time.Second, time.Now())
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
			ctx, cancel := context.WithTimeout(t.Context(), 110*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bun", "benchmarks/media-diagnostic/probe.ts", "--url", strings.Replace(server.URL, "http://", "ws://", 1)+"/media/diagnostic/echo", "--control-url", strings.Replace(control.URL, "http://", "ws://", 1), "--seconds", "6", "--codec", codec, "--output", output)
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
				if r.Sent < 250 || r.Received != r.Sent || r.Missing != 0 || r.Failure != "" || r.Code != 1000 {
					t.Fatalf("bad paired probe: %+v", r)
				}
			}
			report := diagnosticFinished(t, s)
			if report.MediaFrames < int64(result.Results[0].Sent) || report.Sent != report.Received || report.CloseCode != 1000 {
				t.Fatal(report)
			}
			encoded, _ := json.Marshal(report)
			if strings.Contains(string(encoded), token) || strings.Contains(string(raw), token) {
				t.Fatal("token retained")
			}
		})
	}
}
