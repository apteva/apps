package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed capture/ioscapture.swift
var iosCaptureSource []byte

const (
	methodScreenrecord = "adb_screenrecord"
	methodScrcpy       = "scrcpy"
	methodAppium       = "appium"
	methodUSB          = "ios_usb"
	screenrecordChunk  = 170
)

func (d *drivers) recordMethods(v Device) (standard, high string, highAudio bool) {
	if v.Platform == "android" {
		if _, err := lookTool("adb"); err == nil {
			standard = methodScreenrecord
		}
		if _, err := lookTool("scrcpy"); err == nil {
			high, highAudio = methodScrcpy, true
		}
		return
	}
	if runtime.GOOS == "darwin" && !v.Physical && v.Driver == driverXCTest {
		return methodSimctl, methodSimctl, false
	}
	if d.cfg.AppiumURL != "" {
		standard = methodAppium
	}
	if runtime.GOOS == "darwin" && v.Physical {
		if _, err := lookTool("xcrun"); err == nil {
			high, highAudio = methodUSB, true
		}
	}
	return
}

// recordCapabilities extends a device's capability list for mobile_devices.
func (d *drivers) recordCapabilities(v Device) []string {
	standard, high, audio := d.recordMethods(v)
	var out []string
	if standard != "" || high != "" {
		out = append(out, "record")
	}
	if high != "" {
		out = append(out, "record_high")
	}
	if audio {
		out = append(out, "record_audio")
	}
	return out
}

func (d *drivers) RecordPlan(v Device, o recordOptions) (recordPlan, error) {
	standard, high, highAudio := d.recordMethods(v)
	method := ""
	switch o.Quality {
	case "standard":
		method = standard
	case "high":
		method = high
	default:
		method = high
		if method == "" {
			method = standard
		}
		// Prefer the method that can honour an explicit audio request.
		if !o.Audio && high == "" {
			method = standard
		}
	}
	if method == "" {
		hint := "install adb"
		switch {
		case v.Platform == "android" && o.Quality == "high":
			hint = "install scrcpy 2.1 or newer on the runner"
		case v.Platform == "ios" && o.Quality == "high":
			hint = "run Mobile on a Mac with Xcode and connect the device by USB"
		case v.Platform == "ios":
			hint = "set appium_url, or use quality=high with a USB-connected device on a Mac"
		}
		return recordPlan{}, problem("setup_required", "screen recording is unavailable for this device: "+hint)
	}
	plan := recordPlan{Method: method, Ext: ".mp4"}
	if method == high {
		plan.Audio = highAudio && o.Audio
	}
	if method == methodUSB {
		plan.Ext = ".mov"
	}
	return plan, nil
}

func (d *drivers) StartRecording(ctx context.Context, s Session, p recordPlan, o recordOptions, path string) (recorder, error) {
	switch p.Method {
	case methodScreenrecord:
		return d.startScreenrecord(s, path)
	case methodScrcpy:
		return d.startScrcpy(ctx, s, p, o, path)
	case methodAppium:
		return d.startAppiumRecording(ctx, s, o, path)
	case methodUSB:
		return d.startUSBCapture(ctx, s, p, path)
	case methodSimctl:
		return d.startSimRecording(ctx, s, path)
	}
	return nil, problem("unsupported", "unknown recording method")
}

// process wraps a capture child that is stopped with SIGINT so it can finish
// its container (moov atom) instead of being killed mid-write.
type process struct {
	cmd    *exec.Cmd
	done   chan struct{}
	err    error
	stderr *limitedBuffer
}

func startProcess(name string, args []string, stdout io.Writer) (*process, error) {
	cmd := exec.Command(name, args...)
	stderr := &limitedBuffer{limit: 16 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &process{cmd: cmd, done: make(chan struct{}), stderr: stderr}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *process) interrupt(ctx context.Context, grace time.Duration) error {
	select {
	case <-p.done:
		return p.exitError()
	default:
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		_ = p.cmd.Process.Kill()
		<-p.done
		return errors.New("capture did not stop in time and was killed")
	case <-ctx.Done():
		_ = p.cmd.Process.Kill()
		<-p.done
		return ctx.Err()
	}
	return p.exitError()
}

// exitError treats an interrupt-triggered exit as success.
func (p *process) exitError() error {
	if p.err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(p.err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGINT {
			return nil
		}
		if msg := lastLine(p.stderr.String()); msg != "no error output" {
			return errors.New(msg)
		}
	}
	return p.err
}

// --- Android: adb screenrecord in chunks ------------------------------------

type screenrecordRecorder struct {
	d       *drivers
	serial  string
	base    string
	path    string
	stop    chan struct{}
	stopped sync.Once
	done    chan struct{}
	mu      sync.Mutex
	chunks  []string
	err     error
}

func (d *drivers) startScreenrecord(s Session, path string) (recorder, error) {
	adb, err := lookTool("adb")
	if err != nil {
		return nil, problem("setup_required", "adb is required for Android recording")
	}
	r := &screenrecordRecorder{d: d, serial: s.Device.UDID, base: "/sdcard/apteva-" + filepath.Base(strings.TrimSuffix(path, filepath.Ext(path))), path: path, stop: make(chan struct{}), done: make(chan struct{})}
	first := make(chan error, 1)
	go func() {
		defer close(r.done)
		for i := 0; ; i++ {
			remote := fmt.Sprintf("%s-%03d.mp4", r.base, i)
			// screenrecord stops by itself at its time limit; loop to record longer.
			p, err := startProcess(adb, []string{"-s", r.serial, "shell", fmt.Sprintf("screenrecord --bit-rate 12000000 --time-limit %d %s", screenrecordChunk, shellQuote(remote))}, io.Discard)
			if i == 0 {
				first <- err
			}
			if err != nil {
				r.mu.Lock()
				r.err = err
				r.mu.Unlock()
				return
			}
			r.mu.Lock()
			r.chunks = append(r.chunks, remote)
			r.mu.Unlock()
			select {
			case <-p.done:
				if p.err != nil && i == 0 {
					r.mu.Lock()
					r.err = fmt.Errorf("screenrecord failed: %s", lastLine(p.stderr.String()))
					r.mu.Unlock()
					return
				}
			case <-r.stop:
				<-p.done
				return
			}
			select {
			case <-r.stop:
				return
			default:
			}
		}
	}()
	if err := <-first; err != nil {
		return nil, err
	}
	return r, nil
}

func (r *screenrecordRecorder) Done() <-chan struct{} { return r.done }

func (r *screenrecordRecorder) Stop(ctx context.Context) error {
	r.stopped.Do(func() { close(r.stop) })
	// SIGINT makes screenrecord finish the current MP4.
	_, _ = r.d.adb(ctx, r.serial, "shell", "pkill -INT screenrecord || kill -INT $(pidof screenrecord)")
	select {
	case <-r.done:
	case <-time.After(20 * time.Second):
		return errors.New("screenrecord did not stop")
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	chunks, loopErr := append([]string(nil), r.chunks...), r.err
	r.mu.Unlock()
	if len(chunks) == 0 {
		if loopErr != nil {
			return loopErr
		}
		return errors.New("screenrecord produced no output")
	}
	time.Sleep(500 * time.Millisecond) // let the device close the final file
	locals := []string{}
	defer func() {
		for _, l := range locals {
			if l != r.path {
				_ = os.Remove(l)
			}
		}
	}()
	adb, _ := lookTool("adb")
	for i, remote := range chunks {
		local := fmt.Sprintf("%s.part%03d.mp4", r.path, i)
		if len(chunks) == 1 {
			local = r.path
		}
		if err := runLong(ctx, 10*time.Minute, adb, "-s", r.serial, "pull", remote, local); err != nil {
			return fmt.Errorf("could not copy the recording from the phone: %w", err)
		}
		_, _ = r.d.adb(ctx, r.serial, "shell", "rm -f "+shellQuote(remote))
		locals = append(locals, local)
	}
	if len(locals) == 1 {
		return loopErr
	}
	return concatVideos(ctx, locals, r.path)
}

func concatVideos(ctx context.Context, parts []string, out string) error {
	ffmpeg, err := lookTool("ffmpeg")
	if err != nil {
		return problem("setup_required", "ffmpeg is required to join Android recordings longer than 170 seconds")
	}
	list := out + ".concat.txt"
	var b strings.Builder
	for _, p := range parts {
		b.WriteString("file " + shellQuote(p) + "\n")
	}
	if err := os.WriteFile(list, []byte(b.String()), 0600); err != nil {
		return err
	}
	defer os.Remove(list)
	return runLong(ctx, 10*time.Minute, ffmpeg, "-hide_banner", "-nostdin", "-y", "-v", "error", "-f", "concat", "-safe", "0", "-i", list, "-c", "copy", "-movflags", "+faststart", out)
}

func runLong(ctx context.Context, limit time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var stderr limitedBuffer
	stderr.limit = 8192
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s: %s", filepath.Base(name), lastLine(stderr.String()))
	}
	return nil
}

// --- Android: scrcpy ---------------------------------------------------------

type procRecorder struct{ p *process }

func (r *procRecorder) Done() <-chan struct{} { return r.p.done }
func (r *procRecorder) Stop(ctx context.Context) error {
	return r.p.interrupt(ctx, 30*time.Second)
}

var scrcpyVersion = regexp.MustCompile(`scrcpy (\d+)\.(\d+)`)

func scrcpyArgs(serial, path string, audio bool) []string {
	args := []string{"--serial", serial, "--no-playback", "--record", path, "--record-format", "mp4", "--video-bit-rate", "16M"}
	if audio {
		return append(args, "--audio-codec", "aac", "--audio-bit-rate", "256K")
	}
	return append(args, "--no-audio")
}

func (d *drivers) startScrcpy(ctx context.Context, s Session, p recordPlan, _ recordOptions, path string) (recorder, error) {
	bin, err := lookTool("scrcpy")
	if err != nil {
		return nil, problem("setup_required", "scrcpy is not installed")
	}
	out, err := runCommand(ctx, bin, "--version")
	m := scrcpyVersion.FindStringSubmatch(string(out))
	if err != nil || m == nil {
		return nil, problem("setup_required", "cannot read the scrcpy version")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 2 || major == 2 && minor < 1 {
		return nil, problem("setup_required", "scrcpy 2.1 or newer is required for recording")
	}
	proc, err := startProcess(bin, scrcpyArgs(s.Device.UDID, path, p.Audio), io.Discard)
	if err != nil {
		return nil, err
	}
	// scrcpy exits quickly on a bad device or encoder; report that directly.
	select {
	case <-proc.done:
		return nil, fmt.Errorf("scrcpy stopped: %s", lastLine(proc.stderr.String()))
	case <-time.After(2 * time.Second):
	}
	return &procRecorder{proc}, nil
}

// --- iOS: Appium start/stop_recording_screen -------------------------------

type appiumRecorder struct {
	d    *drivers
	s    Session
	path string
	done chan struct{}
}

func (d *drivers) startAppiumRecording(ctx context.Context, s Session, o recordOptions, path string) (recorder, error) {
	options := map[string]any{"videoType": "libx264", "videoFps": 30, "videoQuality": "high", "timeLimit": strconv.Itoa(o.MaxSeconds), "forceRestart": true}
	if _, err := d.sessionWire(ctx, s, "POST", "/appium/start_recording_screen", map[string]any{"options": options}); err != nil {
		return nil, err
	}
	return &appiumRecorder{d: d, s: s, path: path, done: make(chan struct{})}, nil
}

func (r *appiumRecorder) Done() <-chan struct{} { return r.done }

func (r *appiumRecorder) Stop(ctx context.Context) error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return r.d.wireBase64(ctx, "POST", "/session/"+url.PathEscape(r.s.ProviderID)+"/appium/stop_recording_screen", map[string]any{"options": map[string]any{}}, f, 2<<30)
}

// wireBase64 streams a WebDriver {"value":"<base64>"} response to w without
// holding the whole video in memory.
func (d *drivers) wireBase64(ctx context.Context, method, path string, body any, w io.Writer, limit int64) error {
	u, err := url.Parse(d.cfg.AppiumURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return problem("invalid_configuration", "appium_url must be an HTTP(S) base URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := *d.http
	client.Timeout = 0 // bounded by ctx; large videos take a while
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return problem("infrastructure_error", "cannot reach Appium to collect the recording")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return problem("infrastructure_error", fmt.Sprintf("Appium could not stop the recording (HTTP %d); check that ffmpeg is installed on the Appium host", res.StatusCode))
	}
	br := bufio.NewReaderSize(res.Body, 64<<10)
	if err := seekJSONString(br, "value"); err != nil {
		return err
	}
	n, err := io.Copy(w, io.LimitReader(base64.NewDecoder(base64.StdEncoding, &jsonStringReader{r: br}), limit+1))
	if err != nil {
		return fmt.Errorf("invalid recording payload from Appium: %w", err)
	}
	if n > limit {
		return errors.New("recording exceeds the size limit")
	}
	if n == 0 {
		return errors.New("Appium returned an empty recording")
	}
	return nil
}

// seekJSONString advances r to just after the opening quote of key's string value.
func seekJSONString(r *bufio.Reader, key string) error {
	pattern := []byte(`"` + key + `"`)
	matched := 0
	for {
		c, err := r.ReadByte()
		if err != nil {
			return errors.New("Appium response has no recording value")
		}
		if c == pattern[matched] {
			matched++
			if matched == len(pattern) {
				break
			}
			continue
		}
		matched = 0
		if c == pattern[0] {
			matched = 1
		}
	}
	for {
		c, err := r.ReadByte()
		if err != nil {
			return errors.New("Appium response is truncated")
		}
		switch c {
		case ' ', '\n', '\r', '\t', ':':
			continue
		case '"':
			return nil
		default:
			return errors.New("Appium returned no recording data")
		}
	}
}

// jsonStringReader yields a JSON string body until its closing quote,
// dropping escape backslashes (base64 only ever needs "\/").
type jsonStringReader struct {
	r    *bufio.Reader
	done bool
}

func (j *jsonStringReader) Read(p []byte) (int, error) {
	if j.done {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) {
		c, err := j.r.ReadByte()
		if err != nil {
			if n > 0 {
				return n, nil
			}
			return 0, io.ErrUnexpectedEOF
		}
		if c == '"' {
			j.done = true
			break
		}
		if c == '\\' {
			if c, err = j.r.ReadByte(); err != nil {
				return n, io.ErrUnexpectedEOF
			}
		}
		p[n] = c
		n++
	}
	if n == 0 && j.done {
		return 0, io.EOF
	}
	return n, nil
}

// --- iOS: USB capture through AVFoundation ----------------------------------

func (d *drivers) captureHelper(ctx context.Context) (string, error) {
	sum := sha256.Sum256(iosCaptureSource)
	dir := d.cfg.ToolDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "apteva-mobile")
	}
	bin := filepath.Join(dir, "ioscapture-"+hex.EncodeToString(sum[:6]))
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	d.helperMu.Lock()
	defer d.helperMu.Unlock()
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	src := bin + ".swift"
	if err := os.WriteFile(src, iosCaptureSource, 0600); err != nil {
		return "", err
	}
	defer os.Remove(src)
	tmp := bin + ".tmp"
	if err := runLong(ctx, 5*time.Minute, "xcrun", "swiftc", "-O", "-o", tmp, src); err != nil {
		return "", problem("setup_required", "could not build the iOS capture helper (Xcode command line tools required): "+err.Error())
	}
	return bin, os.Rename(tmp, bin)
}

type lineWatcher struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	lines chan string
}

func (l *lineWatcher) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			l.buf.WriteString(line)
			break
		}
		select {
		case l.lines <- strings.TrimSpace(line):
		default:
		}
	}
	return len(p), nil
}

func (d *drivers) startUSBCapture(ctx context.Context, s Session, p recordPlan, path string) (recorder, error) {
	bin, err := d.captureHelper(ctx)
	if err != nil {
		return nil, err
	}
	args := []string{"record", "--udid", s.Device.UDID, "--out", path}
	if s.Device.CaptureID != "" {
		args = append(args, "--capture-id", s.Device.CaptureID)
	}
	if !p.Audio {
		args = append(args, "--no-audio")
	}
	watch := &lineWatcher{lines: make(chan string, 16)}
	proc, err := startProcess(bin, args, watch)
	if err != nil {
		return nil, err
	}
	timeout := time.NewTimer(25 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case line := <-watch.lines:
			if line == "READY" {
				return &procRecorder{proc}, nil
			}
			if strings.HasPrefix(line, "ERROR: ") {
				<-proc.done
				return nil, problem("setup_required", strings.TrimPrefix(line, "ERROR: "))
			}
		case <-proc.done:
			// The helper may exit before its last line is consumed.
			select {
			case line := <-watch.lines:
				if strings.HasPrefix(line, "ERROR: ") {
					return nil, problem("setup_required", strings.TrimPrefix(line, "ERROR: "))
				}
			default:
			}
			return nil, errors.New("iOS capture helper exited before recording started")
		case <-timeout.C:
			_ = proc.interrupt(context.Background(), 5*time.Second)
			return nil, problem("infrastructure_error", "USB capture did not start within 25 seconds; unlock the device and confirm it is trusted")
		case <-ctx.Done():
			_ = proc.interrupt(context.Background(), 5*time.Second)
			return nil, ctx.Err()
		}
	}
}
