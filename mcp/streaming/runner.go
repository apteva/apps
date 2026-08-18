package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// streamRunner owns one ffmpeg child process for the lifetime of a
// stream. The model is: we ask ffmpeg to listen on an RTMP port; it
// blocks until a publisher connects; once frames flow, it segments
// to HLS and (optionally) records to mp4. When the publisher
// disconnects gracefully ffmpeg exits 0; on crash/SIGTERM it exits
// nonzero — both paths flow through done(), which the watchdog reads.
type streamRunner struct {
	streamID  int64
	port      int
	ffmpegBin string
	dataDir   string // absolute, where segments + recording live
	streamKey string
	hlsTime   int
	hlsWindow int
	record    bool

	cmd *exec.Cmd

	// Latest scraped values — written by the stderr goroutine, read by
	// metric tools. Atomic so reads don't need the runner mutex.
	bitrateKbps   atomic.Int64
	fps           atomic.Uint64 // *1000, fixed-point
	droppedFrames atomic.Int64
	resolution    atomic.Pointer[string]

	// startedAt is set when the first bitrate line is scraped — that's
	// the first moment we know a publisher is actually pushing frames.
	startedAt atomic.Int64 // unix nanos; zero before publisher push

	// stopRequested records that WE asked ffmpeg to die. Without it
	// there is no way to tell a signal we sent from an OOM-kill or a
	// SIGSEGV, and v0.1 called every signal death graceful.
	stopRequested atomic.Bool

	doneOnce sync.Once
	done     chan runnerExit

	// scraped closes when the stderr reader has drained the pipe.
	// os/exec requires that all pipe reads finish before Wait() is
	// called; v0.1 raced them and lost the final stderr lines, which
	// is exactly where the disconnect/error detail lives.
	scraped chan struct{}

	// exit memoizes the value read off done so repeat polls (the
	// watchdog after stop(), say) don't see "still running" once the
	// channel has been drained and closed.
	exit atomic.Pointer[runnerExit]
}

type runnerOpts struct {
	streamID  int64
	port      int
	ffmpegBin string
	dataDir   string
	streamKey string
	hlsTime   int
	hlsWindow int
	record    bool
}

type runnerExit struct {
	err error // nil on graceful exit; non-nil on crash / nonzero exit
}

// newFFmpegRunner spawns the actual ffmpeg child. Tests inject a
// fake via App.runnerFactory.
func newFFmpegRunner(opts runnerOpts) (*streamRunner, error) {
	if err := os.MkdirAll(opts.dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", opts.dataDir, err)
	}

	indexPath := filepath.Join(opts.dataDir, indexPlaylistFile)
	segPath := filepath.Join(opts.dataDir, "seg-%05d.ts")

	args := []string{
		"-hide_banner",
		"-loglevel", "info",
		// RTMP ingest, listening for one publisher.
		"-listen", "1",
		"-i", fmt.Sprintf("rtmp://0.0.0.0:%d/live/%s", opts.port, opts.streamKey),
	}

	// Branch 1 — HLS. -c copy: no transcode, whatever OBS pushes
	// (typically H.264/AAC) goes straight to HLS.
	//
	// hls_list_size bounds the LIVE playlist to a rolling window so a
	// long stream doesn't end up serving a manifest with thousands of
	// entries to every viewer every couple of seconds. delete_segments
	// is deliberately NOT in hls_flags: it never fired in v0.1 (which
	// ran at list_size 0), and now that the window is finite it would
	// delete exactly the segments replay needs. Segments stay on disk
	// until the retention sweeper or streams_delete reclaims them, and
	// finalize builds the full VOD manifest from them.
	args = append(args,
		"-c", "copy",
		"-f", "hls",
		"-hls_time", strconv.Itoa(opts.hlsTime),
		"-hls_list_size", strconv.Itoa(opts.hlsWindow),
		"-hls_flags", "independent_segments+program_date_time+append_list",
		"-hls_segment_filename", segPath,
		indexPath,
	)

	// Branch 2 — recording mp4. Same codec-copy, second output.
	if opts.record {
		args = append(args,
			"-c", "copy",
			"-movflags", "+faststart",
			"-f", "mp4",
			filepath.Join(opts.dataDir, recordingFile),
		)
	}

	cmd := exec.Command(opts.ffmpegBin, args...)
	// Group children so a SIGTERM hits the whole tree, not just the
	// ffmpeg PID — defensive against any future ffmpeg subprocesses.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	r := &streamRunner{
		streamID:  opts.streamID,
		port:      opts.port,
		ffmpegBin: opts.ffmpegBin,
		dataDir:   opts.dataDir,
		streamKey: opts.streamKey,
		hlsTime:   opts.hlsTime,
		hlsWindow: opts.hlsWindow,
		record:    opts.record,
		cmd:       cmd,
		done:      make(chan runnerExit, 1),
		scraped:   make(chan struct{}),
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	go func() {
		r.scrape(stderr)
		close(r.scraped)
	}()
	go r.wait()

	return r, nil
}

// ffmpegProgressLine matches lines like:
//
//	frame= 1234 fps= 30.0 q=-1.0 size=  12345kB time=00:00:41.16 bitrate=2456.7kbits/s drop=2 speed=1.00x
//
// We tolerate variable whitespace and missing fields.
var (
	rxFps     = regexp.MustCompile(`fps=\s*([0-9.]+)`)
	rxBitrate = regexp.MustCompile(`bitrate=\s*([0-9.]+)kbits/s`)
	rxDrop    = regexp.MustCompile(`drop=\s*([0-9]+)`)
	rxFrame   = regexp.MustCompile(`frame=\s*([0-9]+)`)
	rxRes     = regexp.MustCompile(`Stream.*Video:.* ([0-9]{2,5})x([0-9]{2,5})`)
)

// scrape parses ffmpeg's stderr for periodic progress lines and the
// initial Stream metadata. Updates the atomics; the watchdog and
// metric tools read them. Returns when stderr EOFs.
//
// Two things it must NOT do, both of which v0.1 got wrong:
//
//   - Close the pipe. os/exec closes it itself after Wait; closing it
//     from here while ffmpeg is still writing kills the encode with
//     SIGPIPE. (v0.1's `defer stderr.Close()` did exactly that
//     whenever the scanner stopped early.)
//   - Stop reading. ffmpeg blocks on a full stderr pipe, so if the
//     scanner gives up (a token past the 1MB limit, say) we keep
//     draining to io.Discard rather than leaving the encoder wedged.
func (r *streamRunner) scrape(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	// ffmpeg terminates its periodic stats lines with \r (it rewrites
	// one status line in place) and only the non-progress messages with
	// \n. A plain line scanner therefore holds every stats update
	// hostage until some unrelated \n-terminated message flushes it,
	// making the scraped bitrate/fps stale by up to a segment.
	scanner.Split(splitLinesCR)
	defer func() {
		// Whatever ended the scan, drain the rest so ffmpeg never
		// blocks writing to a full pipe.
		_, _ = io.Copy(io.Discard, stderr)
	}()
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// One-shot: pluck resolution from the first "Stream … Video:" line.
		if r.resolution.Load() == nil {
			if m := rxRes.FindStringSubmatch(line); len(m) == 3 {
				res := m[1] + "x" + m[2]
				r.resolution.Store(&res)
			}
		}

		// Periodic: bitrate + fps + drop counter.
		hadProgress := false
		if m := rxBitrate.FindStringSubmatch(line); len(m) == 2 {
			if f, err := strconv.ParseFloat(m[1], 64); err == nil {
				r.bitrateKbps.Store(int64(f))
				hadProgress = true
			}
		}
		if m := rxFps.FindStringSubmatch(line); len(m) == 2 {
			if f, err := strconv.ParseFloat(m[1], 64); err == nil {
				r.fps.Store(uint64(f * 1000))
				hadProgress = true
			}
		}
		if m := rxDrop.FindStringSubmatch(line); len(m) == 2 {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				r.droppedFrames.Store(n)
			}
		}
		if hadProgress && r.startedAt.Load() == 0 {
			// First progress line = publisher is live.
			r.startedAt.Store(time.Now().UnixNano())
		}
		_ = rxFrame // reserved for v0.3 frame-count metric
	}
}

// splitLinesCR is a bufio.SplitFunc that breaks on \n OR \r, so
// ffmpeg's in-place progress updates surface immediately. \r\n yields
// an empty token for the \n, which the scrape loop skips.
func splitLinesCR(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil // ask for more
}

// wait blocks on the cmd, then signals done exactly once.
func (r *streamRunner) wait() {
	// os/exec: "Wait will not return until all reads from the pipe have
	// completed". Reading the pipe concurrently with Wait races the
	// close and drops the tail of stderr.
	if r.scraped != nil {
		<-r.scraped
	}
	err := classifyExit(r.cmd.Wait(), r.stopRequested.Load())
	r.doneOnce.Do(func() {
		r.done <- runnerExit{err: err}
		close(r.done)
	})
}

// classifyExit maps cmd.Wait()'s error to the runner's exit error.
//
// v0.1 mapped ANY signal death to "graceful" (`ExitCode() == -1 || ==
// 255 → err = nil`) because the runner kept no record of whether we
// were the ones who asked ffmpeg to stop. An OOM-kill or a SIGSEGV
// mid-webinar was therefore persisted as a clean `ended` stream with
// an empty error column. Only a stop WE requested is graceful;
// everything else keeps — and names — the signal.
func classifyExit(err error, stopRequested bool) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	sig := syscall.Signal(0)
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		sig = ws.Signal()
	}
	code := exitErr.ExitCode()
	if stopRequested {
		// SIGINT/SIGTERM/SIGKILL from stop(), or ffmpeg's own "exit 255
		// when interrupted" convention.
		if sig != 0 || code == -1 || code == 255 {
			return nil
		}
		return err
	}
	if sig != 0 {
		return fmt.Errorf("ffmpeg killed by signal %d (%s)", int(sig), sig.String())
	}
	return err
}

// stop sends SIGINT to give ffmpeg a chance to flush the recording
// (writing the moov atom for fast-start mp4 requires graceful exit),
// waits up to grace, then SIGTERMs and finally SIGKILLs.
// Returns whatever the runner's exit reported.
//
// grace comes from App.finalizeGrace: generous for recording streams
// (+faststart rewrites the entire file on close), short otherwise.
func (r *streamRunner) stop(grace time.Duration) error {
	// Mark first: whatever signal lands from here on is one we asked
	// for, and classifyExit reads this flag.
	r.stopRequested.Store(true)
	if r.cmd == nil || r.cmd.Process == nil {
		return nil
	}
	// SIGINT via process group — ffmpeg interprets as "finish current
	// segment + close output files cleanly".
	_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGINT)

	select {
	case ex := <-r.done:
		return r.consumeExit(ex).err
	case <-time.After(grace):
	}
	_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case ex := <-r.done:
		return r.consumeExit(ex).err
	case <-time.After(2 * time.Second):
	}
	_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
	ex := <-r.done
	return r.consumeExit(ex).err
}

// metrics returns a snapshot of the current scraped values.
type runnerMetrics struct {
	BitrateKbps   int
	FPS           float64
	Resolution    string
	DroppedFrames int
	UptimeSeconds int
	HasPublisher  bool
}

func (r *streamRunner) metrics() runnerMetrics {
	m := runnerMetrics{
		BitrateKbps:   int(r.bitrateKbps.Load()),
		FPS:           float64(r.fps.Load()) / 1000.0,
		DroppedFrames: int(r.droppedFrames.Load()),
	}
	if res := r.resolution.Load(); res != nil {
		m.Resolution = *res
	}
	if started := r.startedAt.Load(); started != 0 {
		m.HasPublisher = true
		m.UptimeSeconds = int(time.Since(time.Unix(0, started)).Seconds())
	}
	return m
}

// consumeExit memoizes the first exit value read off done and returns
// the memoized one thereafter. Without it, whoever reads the channel
// first (usually stop()) consumes the only copy and every later
// observer — tryReadExit, another stop() — sees a closed, empty
// channel.
//
// (v0.1's isAlive() lived here too. It read the done channel and threw
// the exit error away, so calling it once turned a crashed stream into
// a silently-ended one. Nothing referenced it; it's gone.)
func (r *streamRunner) consumeExit(ex runnerExit) runnerExit {
	r.exit.CompareAndSwap(nil, &ex)
	if p := r.exit.Load(); p != nil {
		return *p
	}
	return ex
}

// tryReadExit returns the exit info if the runner has finished, else
// (zero, false). Non-blocking.
//
// v0.1 inverted the closed-channel case: `ok == false` (channel closed
// AND already drained) was returned as "not finished", so a runner
// whose exit had been consumed elsewhere looked alive forever and the
// watchdog never cleaned it up.
func (r *streamRunner) tryReadExit() (runnerExit, bool) {
	if p := r.exit.Load(); p != nil {
		return *p, true
	}
	select {
	case ex, ok := <-r.done:
		if !ok {
			// Closed and drained with nothing memoized — the process is
			// gone either way.
			return runnerExit{}, true
		}
		return r.consumeExit(ex), true
	default:
		return runnerExit{}, false
	}
}

// recordingAvailable returns true if the runner was configured to
// record AND a *complete* mp4 is on disk.
func (r *streamRunner) recordingAvailable() bool {
	if !r.record {
		return false
	}
	return mp4HasMoov(filepath.Join(r.dataDir, recordingFile))
}

// mp4HasMoov reports whether path is a complete mp4 — i.e. it carries
// a top-level `moov` atom.
//
// ffmpeg writes moov only when the output is closed cleanly (and with
// -movflags +faststart it rewrites the whole file to move moov to the
// front), so a SIGKILLed encode leaves ftyp + a partial mdat and
// nothing else. v0.1 accepted any non-empty file, which meant a
// truncated, unplayable recording was persisted as "finalized" and
// handed to viewers as the replay.
func mp4HasMoov(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() < 8 {
		return false
	}

	var off int64
	var hdr [16]byte
	// A sane mp4 has a handful of top-level boxes; the bound keeps a
	// corrupt file from turning this into a long walk.
	for i := 0; i < 64; i++ {
		if n, err := f.ReadAt(hdr[:8], off); err != nil || n < 8 {
			return false
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		if string(hdr[4:8]) == "moov" {
			return true
		}
		switch size {
		case 1: // 64-bit largesize follows the type
			if n, err := f.ReadAt(hdr[8:16], off+8); err != nil || n < 8 {
				return false
			}
			size = int64(binary.BigEndian.Uint64(hdr[8:16]))
		case 0: // box runs to EOF — nothing can follow it
			return false
		}
		if size < 8 {
			return false
		}
		off += size
		if off >= st.Size() {
			return false
		}
	}
	return false
}

// run-time guard: we use exec.Command + SysProcAttr.Setpgid which is
// Unix-only. macOS + Linux are the supported deployment targets per
// the workspace's release.sh. If someone tries to build on Windows
// the build will fail — that's the right outcome.
var _ = context.TODO
var _ = strings.Builder{}
