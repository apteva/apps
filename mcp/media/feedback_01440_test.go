package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The original stays private. Supply the checksum-verified native source to run
// this full-programme regression: MEDIA_FREEZE_REGRESSION_SOURCE=/path/source.mp4.
func TestFreezePlay99846Normalization(t *testing.T) {
	source := os.Getenv("MEDIA_FREEZE_REGRESSION_SOURCE")
	if source == "" {
		t.Skip("set MEDIA_FREEZE_REGRESSION_SOURCE to the original file 99846")
	}
	f, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != "9f96857a97bfddd572f079a866ae59ca8c70c7d5eecdb2487a1dfffcc739d3c8" {
		t.Fatalf("wrong source checksum: %s", got)
	}
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	output := filepath.Join(dir, "freeze.m4a")
	row := &RenderRow{Operation: "audio_filter", SourceFileIDs: []string{"99846"}, Params: json.RawMessage(`{"target_lufs":-16,"target_peak_dbtp":-1.5}`)}
	plan, err := buildPlan(row.Operation, row.SourceFileIDs, row.Params, "freeze.m4a", ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	args, err := materialiseArgs(plan.Args, []string{source}, dir)
	if err != nil {
		t.Fatal(err)
	}
	args = append(args, output)
	binary, _ := exec.LookPath("ffmpeg")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd, err := localRuntimeCommand(ctx, row, dir, binary, source, output, args)
	if err != nil {
		t.Fatal(err)
	}
	log, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, log)
	}
	var result map[string]any
	if err := json.Unmarshal(runtimeResultFromLog(string(log)), &result); err != nil {
		t.Fatal(err)
	}
	d := result["audio_normalization"].(map[string]any)
	if d["validated"] != true || d["timeline_validated"] != true || d["decode_ok"] != true || d["sample_rate"] != float64(48000) {
		t.Fatal(d)
	}
	loudness, peak := measureLoudness(t, output)
	if math.Abs(loudness+16) > .5 || peak > -1.4 {
		t.Fatalf("outside delivery limits: %.2f LUFS / %.2f dBTP", loudness, peak)
	}
	t.Logf("Freeze Play: %.2f LUFS / %.2f dBTP; attempts=%v", loudness, peak, d["attempt"])
}
