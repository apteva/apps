package main

import (
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─── Exit classification (finding 2) ───────────────────────────────

// realSignalExit runs a process that kills itself with sig and returns
// the *exec.ExitError.
func realSignalExit(t *testing.T, sig string) error {
	t.Helper()
	err := exec.Command("sh", "-c", "kill -"+sig+" $$").Run()
	if err == nil {
		t.Fatalf("expected the child to die of SIG%s", sig)
	}
	return err
}

func TestClassifyExit_SignalWeDidntRequestIsAnError(t *testing.T) {
	err := classifyExit(realSignalExit(t, "KILL"), false)
	if err == nil {
		t.Fatal("SIGKILL with no stop request should NOT be reported as a clean exit")
	}
	if !strings.Contains(err.Error(), "signal") {
		t.Errorf("error should name the signal, got %q", err)
	}
}

func TestClassifyExit_SignalWeRequestedIsGraceful(t *testing.T) {
	if err := classifyExit(realSignalExit(t, "INT"), true); err != nil {
		t.Errorf("SIGINT after stop() should be graceful, got %v", err)
	}
}

func TestClassifyExit_NonZeroExitStaysAnError(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 3").Run()
	if got := classifyExit(err, true); got == nil {
		t.Error("exit 3 is a real failure even when a stop was requested")
	}
	if got := classifyExit(nil, false); got != nil {
		t.Errorf("clean exit classified as %v", got)
	}
}

func TestStop_MarksStopRequested(t *testing.T) {
	r := &streamRunner{done: make(chan runnerExit, 1)}
	if r.stopRequested.Load() {
		t.Fatal("stopRequested should start false")
	}
	_ = r.stop(time.Second)
	if !r.stopRequested.Load() {
		t.Error("stop() must record that the signal was ours")
	}
}

// ─── Exit memoization (finding 19) ─────────────────────────────────

func TestTryReadExit_StaysExitedAfterTheValueIsConsumed(t *testing.T) {
	r := &streamRunner{done: make(chan runnerExit, 1)}
	fakeStop(r, errFake("boom"))

	ex, ok := r.tryReadExit()
	if !ok {
		t.Fatal("first poll should report the exit")
	}
	if ex.err == nil || !strings.Contains(ex.err.Error(), "boom") {
		t.Errorf("first poll lost the error: %v", ex.err)
	}
	// v0.1 returned (zero,false) here — "still alive" — because the
	// channel was closed and drained.
	ex2, ok2 := r.tryReadExit()
	if !ok2 {
		t.Fatal("second poll reported the runner as still alive")
	}
	if ex2.err == nil || ex2.err.Error() != ex.err.Error() {
		t.Errorf("second poll returned %v, want %v", ex2.err, ex.err)
	}
}

func TestTryReadExit_RunningRunnerReportsNotFinished(t *testing.T) {
	r := &streamRunner{done: make(chan runnerExit, 1)}
	if _, ok := r.tryReadExit(); ok {
		t.Error("a live runner must not report an exit")
	}
}

// ─── mp4 completeness (finding 7b) ─────────────────────────────────

func mp4Box(typ string, payload []byte) []byte {
	out := make([]byte, 4, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
	out = append(out, typ...)
	return append(out, payload...)
}

// writeTestMP4 writes a minimal mp4 skeleton. withMoov=false mimics a
// recording that was SIGKILLed before ffmpeg wrote its index.
func writeTestMP4(t *testing.T, path string, withMoov bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := mp4Box("ftyp", []byte("isom0000"))
	body = append(body, mp4Box("mdat", make([]byte, 64))...)
	if withMoov {
		body = append(body, mp4Box("moov", make([]byte, 32))...)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMp4HasMoov(t *testing.T) {
	dir := t.TempDir()

	complete := filepath.Join(dir, "complete.mp4")
	writeTestMP4(t, complete, true)
	if !mp4HasMoov(complete) {
		t.Error("a file with a moov atom should be recognized as complete")
	}

	truncated := filepath.Join(dir, "truncated.mp4")
	writeTestMP4(t, truncated, false)
	if mp4HasMoov(truncated) {
		t.Error("a moov-less file is a truncated recording, not a finalized one")
	}

	empty := filepath.Join(dir, "empty.mp4")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if mp4HasMoov(empty) {
		t.Error("empty file accepted")
	}
	if mp4HasMoov(filepath.Join(dir, "missing.mp4")) {
		t.Error("missing file accepted")
	}
}

func TestRecordingAvailable_RequiresRecordAndMoov(t *testing.T) {
	dir := t.TempDir()
	writeTestMP4(t, filepath.Join(dir, recordingFile), true)

	off := &streamRunner{dataDir: dir, record: false}
	if off.recordingAvailable() {
		t.Error("record=false must never report a recording")
	}
	on := &streamRunner{dataDir: dir, record: true}
	if !on.recordingAvailable() {
		t.Error("complete mp4 with record=true should be available")
	}

	partialDir := t.TempDir()
	writeTestMP4(t, filepath.Join(partialDir, recordingFile), false)
	partial := &streamRunner{dataDir: partialDir, record: true}
	if partial.recordingAvailable() {
		t.Error("truncated mp4 reported as an available recording")
	}
}

// ─── stderr scraping (finding 24) ──────────────────────────────────

func TestSplitLinesCR(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a\nb\n", []string{"a", "b"}},
		{"a\rb\r", []string{"a", "b"}},
		{"a\r\nb", []string{"a", "", "b"}},
		{"no-terminator", []string{"no-terminator"}},
	}
	for _, c := range cases {
		got := []string{}
		rest := []byte(c.in)
		for len(rest) > 0 {
			adv, tok, _ := splitLinesCR(rest, true)
			if adv == 0 {
				break
			}
			got = append(got, string(tok))
			rest = rest[adv:]
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("split(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ffmpeg rewrites one status line in place, terminating each update
// with \r. v0.1's line scanner held those hostage until an unrelated
// \n-terminated message flushed them.
func TestScrape_ParsesCarriageReturnProgressLines(t *testing.T) {
	pr, pw := io.Pipe()
	r := &streamRunner{}
	done := make(chan struct{})
	go func() {
		r.scrape(pr)
		close(done)
	}()

	if _, err := pw.Write([]byte("frame=  120 fps= 30.0 bitrate=2456.7kbits/s drop=2\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.bitrateKbps.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.bitrateKbps.Load(); got != 2456 {
		t.Errorf("bitrate=%d, want 2456 (progress line never flushed?)", got)
	}
	if got := r.fps.Load(); got != 30000 {
		t.Errorf("fps=%d (fixed point), want 30000", got)
	}
	if got := r.droppedFrames.Load(); got != 2 {
		t.Errorf("dropped=%d, want 2", got)
	}
	if r.startedAt.Load() == 0 {
		t.Error("first progress line should mark the publisher live")
	}

	pw.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scrape didn't return after EOF")
	}
}

// A token past the scanner's cap must not end the read: v0.1 exited
// scrape, which ran `defer stderr.Close()` and killed ffmpeg with
// SIGPIPE mid-stream.
func TestScrape_DrainsPastAnOversizedToken(t *testing.T) {
	huge := strings.Repeat("x", 2*1024*1024) // > the 1MB scanner cap
	src := strings.NewReader(huge + "\nbitrate= 999.0kbits/s\n")
	r := &streamRunner{}
	done := make(chan struct{})
	go func() {
		r.scrape(src)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scrape wedged on an oversized token instead of draining")
	}
	if src.Len() != 0 {
		t.Errorf("%d bytes left unread — ffmpeg would block on a full pipe", src.Len())
	}
}
