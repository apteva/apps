package main

import (
	"bufio"
	"context"
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

	doneOnce sync.Once
	done     chan runnerExit
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

	indexPath := filepath.Join(opts.dataDir, "index.m3u8")
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
	args = append(args,
		"-c", "copy",
		"-f", "hls",
		"-hls_time", strconv.Itoa(opts.hlsTime),
		"-hls_list_size", strconv.Itoa(opts.hlsWindow),
		"-hls_flags", "independent_segments+program_date_time+append_list+delete_segments",
		"-hls_segment_filename", segPath,
		indexPath,
	)

	// Branch 2 — recording mp4. Same codec-copy, second output.
	if opts.record {
		args = append(args,
			"-c", "copy",
			"-movflags", "+faststart",
			"-f", "mp4",
			filepath.Join(opts.dataDir, "record.mp4"),
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
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	go r.scrape(stderr)
	go r.wait()

	return r, nil
}

// ffmpegProgressLine matches lines like:
//   frame= 1234 fps= 30.0 q=-1.0 size=  12345kB time=00:00:41.16 bitrate=2456.7kbits/s drop=2 speed=1.00x
// We tolerate variable whitespace and missing fields.
var (
	rxFps      = regexp.MustCompile(`fps=\s*([0-9.]+)`)
	rxBitrate  = regexp.MustCompile(`bitrate=\s*([0-9.]+)kbits/s`)
	rxDrop     = regexp.MustCompile(`drop=\s*([0-9]+)`)
	rxFrame    = regexp.MustCompile(`frame=\s*([0-9]+)`)
	rxRes      = regexp.MustCompile(`Stream.*Video:.* ([0-9]{2,5})x([0-9]{2,5})`)
)

// scrape parses ffmpeg's stderr for periodic progress lines and the
// initial Stream metadata. Updates the atomics; the watchdog and
// metric tools read them. Closes when stderr EOFs.
func (r *streamRunner) scrape(stderr io.ReadCloser) {
	defer stderr.Close()
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()

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
		_ = rxFrame // reserved for v0.2 frame-count metric
	}
}

// wait blocks on the cmd, then signals done exactly once.
func (r *streamRunner) wait() {
	err := r.cmd.Wait()
	r.doneOnce.Do(func() {
		// Distinguish "ffmpeg exited because we asked" from real errors.
		// On signal-induced exit (SIGINT from stop()), Wait returns
		// *exec.ExitError with ExitCode() == -1 on Unix. The runner's
		// stop() path treats that as graceful, so map ExitError-with-
		// signal to nil here.
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				if exitErr.ExitCode() == -1 || exitErr.ExitCode() == 255 {
					// Killed by signal we sent — graceful.
					err = nil
				}
			}
		}
		r.done <- runnerExit{err: err}
		close(r.done)
	})
}

// stop sends SIGINT to give ffmpeg a chance to flush the recording
// (writing the moov atom for fast-start mp4 requires graceful exit),
// waits up to grace, then SIGTERMs and finally SIGKILLs.
// Returns whatever the runner's exit reported.
func (r *streamRunner) stop(grace time.Duration) error {
	if r.cmd == nil || r.cmd.Process == nil {
		return nil
	}
	// SIGINT via process group — ffmpeg interprets as "finish current
	// segment + close output files cleanly".
	_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGINT)

	select {
	case ex := <-r.done:
		return ex.err
	case <-time.After(grace):
	}
	_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case ex := <-r.done:
		return ex.err
	case <-time.After(2 * time.Second):
	}
	_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
	ex := <-r.done
	return ex.err
}

// metrics returns a snapshot of the current scraped values.
type runnerMetrics struct {
	BitrateKbps    int
	FPS            float64
	Resolution     string
	DroppedFrames  int
	UptimeSeconds  int
	HasPublisher   bool
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

// isAlive returns true if the runner's done channel hasn't fired yet.
// Non-blocking check; safe for the watchdog's periodic poll.
func (r *streamRunner) isAlive() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

// tryReadExit returns the exit info if the runner has finished, else
// (nil, false). Non-blocking.
func (r *streamRunner) tryReadExit() (runnerExit, bool) {
	select {
	case ex, ok := <-r.done:
		if !ok {
			return runnerExit{}, false
		}
		return ex, true
	default:
		return runnerExit{}, false
	}
}

// recordingAvailable returns true if the runner was configured to
// record AND the file is non-empty on disk.
func (r *streamRunner) recordingAvailable() bool {
	if !r.record {
		return false
	}
	path := filepath.Join(r.dataDir, "record.mp4")
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return st.Size() > 0
}

// run-time guard: we use exec.Command + SysProcAttr.Setpgid which is
// Unix-only. macOS + Linux are the supported deployment targets per
// the workspace's release.sh. If someone tries to build on Windows
// the build will fail — that's the right outcome.
var _ = context.TODO
var _ = strings.Builder{}
