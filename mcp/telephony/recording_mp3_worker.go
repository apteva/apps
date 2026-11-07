package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"time"
)

// Recording conversion is batch CPU work that used to share the process, and
// its raised scheduling priority, with live call audio. It now runs as the same
// executable in a separate process, below normal priority and with no access to
// Telephony's environment. The recording job around it does not change: claims,
// retained originals, retries and upload verification all stay in the parent.
const (
	recordingMP3WorkerArg = "--telephony-recording-mp3-worker"
	// Absolute, because the worker inherits whatever priority Telephony runs at.
	recordingMP3WorkerNice = 10
	// Exit status when the parent went away during a conversion.
	recordingMP3WorkerOrphaned = 3
	// Holds the result and the tail of a crash report.
	recordingMP3WorkerOutputLimit = 64 << 10
)

// Only for a process that cannot start. A worker that started and failed is an
// ordinary failed conversion, retried by the recording job; repeating the work
// in-process could let the same fault reach live calls.
var errRecordingMP3WorkerUnavailable = errors.New("recording conversion process could not start")

type recordingMP3WorkerRequest struct {
	Source   string `json:"source"`
	Dest     string `json:"dest"`
	Channels int    `json:"channels,omitempty"`
	Limit    int64  `json:"limit"`
}

type recordingMP3WorkerResult struct {
	OK            bool   `json:"ok,omitempty"`
	Size          int64  `json:"size,omitempty"`
	Error         string `json:"error,omitempty"`
	LimitExceeded int64  `json:"limit_exceeded,omitempty"`
	// The highest priority (lowest nice value) among the worker's threads.
	Nice          int    `json:"nice"`
	NiceKnown     bool   `json:"nice_known,omitempty"`
	PriorityError string `json:"priority_error,omitempty"`
}

func isRecordingMP3Worker(args []string) bool {
	return len(args) == 2 && args[1] == recordingMP3WorkerArg
}

func recordingMP3WorkerMain(stdin io.Reader, stdout io.Writer) int {
	var result recordingMP3WorkerResult
	if err := lowerRecordingMP3WorkerPriority(); err != nil {
		result.PriorityError = err.Error()
	}
	var request recordingMP3WorkerRequest
	decoder := json.NewDecoder(stdin)
	if err := decoder.Decode(&request); err != nil {
		return recordingMP3WorkerOrphaned
	}
	// The parent holds stdin open for as long as it wants the result.
	go func() {
		io.Copy(io.Discard, io.MultiReader(decoder.Buffered(), stdin))
		os.Exit(recordingMP3WorkerOrphaned)
	}()
	size, err := convertRecordingMP3(context.Background(), request.Source, request.Dest, request.Channels, request.Limit)
	var limitErr *recordingSizeLimitError
	switch {
	case errors.As(err, &limitErr):
		result.LimitExceeded = limitErr.Limit
	case err != nil:
		result.Error = err.Error()
	default:
		result.OK, result.Size = true, size
	}
	result.Nice, result.NiceKnown = recordingMP3WorkerThreadNice()
	if json.NewEncoder(stdout).Encode(result) != nil {
		return 1
	}
	return 0
}

// /proc/self/exe keeps working while an upgrade replaces the binary on disk.
var recordingMP3WorkerExecutable = func() (string, error) {
	if _, err := os.Stat("/proc/self/exe"); err == nil {
		return "/proc/self/exe", nil
	}
	return os.Executable()
}

func newRecordingMP3WorkerCommand(ctx context.Context, executable string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable, recordingMP3WorkerArg)
	// No inherited tokens or settings: the worker only needs the paths it is given.
	cmd.Env = []string{"GOMAXPROCS=1"}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

type recordingMP3WorkerOutput struct{ data []byte }

func (o *recordingMP3WorkerOutput) Write(p []byte) (int, error) {
	o.data = append(o.data, p[:min(len(p), recordingMP3WorkerOutputLimit-len(o.data))]...)
	return len(p), nil
}

func runRecordingMP3Worker(ctx context.Context, request recordingMP3WorkerRequest) (recordingMP3WorkerResult, error) {
	var result recordingMP3WorkerResult
	executable, err := recordingMP3WorkerExecutable()
	if err != nil {
		return result, fmt.Errorf("%w: %v", errRecordingMP3WorkerUnavailable, err)
	}
	cmd := newRecordingMP3WorkerCommand(ctx, executable)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return result, fmt.Errorf("%w: %v", errRecordingMP3WorkerUnavailable, err)
	}
	var stdout, stderr recordingMP3WorkerOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		stdin.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, fmt.Errorf("%w: %v", errRecordingMP3WorkerUnavailable, err)
	}
	// A short write means the worker already died, which Wait reports.
	if payload, err := json.Marshal(request); err == nil {
		stdin.Write(payload)
	}
	err = cmd.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if err == nil {
		err = json.Unmarshal(stdout.data, &result)
	}
	if err != nil {
		return recordingMP3WorkerResult{}, fmt.Errorf("recording conversion process failed: %v: %s", err, stderr.data)
	}
	return result, nil
}

func convertRecordingMP3InWorker(ctx context.Context, source, dest string, channels int, limit int64) error {
	result, err := runRecordingMP3Worker(ctx, recordingMP3WorkerRequest{Source: source, Dest: dest, Channels: channels, Limit: limit})
	if err != nil {
		return err
	}
	if result.PriorityError != "" || result.NiceKnown && result.Nice < recordingMP3WorkerNice {
		log.Printf("recording conversion process kept priority: nice=%d err=%s", result.Nice, result.PriorityError)
	}
	if result.LimitExceeded > 0 {
		return &recordingSizeLimitError{result.LimitExceeded}
	}
	if !result.OK {
		return errors.New(result.Error)
	}
	info, err := os.Stat(dest)
	if err != nil {
		return err
	}
	if info.Size() != result.Size {
		return fmt.Errorf("recording conversion wrote %d bytes but reported %d", info.Size(), result.Size)
	}
	return nil
}
