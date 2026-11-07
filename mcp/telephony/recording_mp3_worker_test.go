package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// The test binary re-executes itself as the worker, as Telephony does.
func TestMain(m *testing.M) {
	if isRecordingMP3Worker(os.Args) {
		os.Exit(recordingMP3WorkerMain(os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

func TestRecordingMP3WorkerArgumentOnlyMatchesAlone(t *testing.T) {
	for _, args := range [][]string{{"telephony"}, {"telephony", "serve"}, {"telephony", recordingMP3WorkerArg, "extra"}, {recordingMP3WorkerArg}} {
		if isRecordingMP3Worker(args) {
			t.Fatal("normal start treated as a worker", args)
		}
	}
	if !isRecordingMP3Worker([]string{"telephony", recordingMP3WorkerArg}) {
		t.Fatal("worker not recognized")
	}
}

func TestRecordingMP3BatchIsOneFrame(t *testing.T) {
	for _, c := range []struct{ rate, channels, bytes int }{
		{8000, 1, 576}, {8000, 2, 1152}, {16000, 1, 1152}, {24000, 2, 2304}, {32000, 1, 2304}, {44100, 2, 4608}, {48000, 2, 4608},
	} {
		if got := recordingMP3BatchSamples(c.rate) * c.channels * 2; got != c.bytes {
			t.Fatal(c, got)
		}
	}
}

func recordingWorkerDest(t *testing.T) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "out.mp3")
	if err := os.WriteFile(dest, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return dest
}

// Same bytes as the same conversion inside the process, at low priority.
func TestRecordingMP3WorkerMatchesInProcessAtLowPriority(t *testing.T) {
	source := writeMP3TestWAV(t, 2, 24000, 3)
	dest := recordingWorkerDest(t)
	result, err := runRecordingMP3Worker(t.Context(), recordingMP3WorkerRequest{Source: source, Dest: dest, Channels: 2, Limit: maxProviderRecordingBytes})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.PriorityError != "" {
		t.Fatalf("%+v", result)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if !result.NiceKnown || result.Nice != recordingMP3WorkerNice {
			t.Fatalf("worker threads at nice %d (known=%v), want %d", result.Nice, result.NiceKnown, recordingMP3WorkerNice)
		}
	}
	inline := recordingWorkerDest(t)
	if _, err := convertRecordingMP3(t.Context(), source, inline, 2, maxProviderRecordingBytes); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(dest)
	b, _ := os.ReadFile(inline)
	if len(a) == 0 || int64(len(a)) != result.Size || !bytes.Equal(a, b) {
		t.Fatal("worker output differs", len(a), len(b), result.Size)
	}
}

func TestRecordingMP3WorkerGetsNoTelephonyEnvironment(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "secret")
	cmd := newRecordingMP3WorkerCommand(t.Context(), "/bin/true")
	if !slices.Equal(cmd.Env, []string{"GOMAXPROCS=1"}) {
		t.Fatal(cmd.Env)
	}
}

func TestRecordingMP3WorkerErrorsKeepTheirMeaning(t *testing.T) {
	source := writeMP3TestWAV(t, 2, 24000, 2)
	dest := recordingWorkerDest(t)
	err := convertRecordingMP3InWorker(t.Context(), source, dest, 2, 100)
	var limit *recordingSizeLimitError
	if !errors.As(err, &limit) || limit.Limit != 100 || recordingImportBlockReason(err) != "recording_file_too_large" {
		t.Fatal("size limit lost its type", err)
	}
	err = convertRecordingMP3InWorker(t.Context(), source, dest, 1, maxProviderRecordingBytes)
	if err == nil || !strings.Contains(err.Error(), "channel metadata") {
		t.Fatal("channel mismatch", err)
	}
	err = convertRecordingMP3InWorker(t.Context(), filepath.Join(t.TempDir(), "missing.wav"), dest, 2, maxProviderRecordingBytes)
	if err == nil || errors.Is(err, errRecordingMP3WorkerUnavailable) {
		t.Fatal("missing source", err)
	}
}

func useRecordingMP3WorkerExecutable(t *testing.T, path string) {
	t.Helper()
	previous := recordingMP3WorkerExecutable
	recordingMP3WorkerExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { recordingMP3WorkerExecutable = previous })
}

// A host that cannot start processes still gets its recordings converted.
func TestRecordingMP3FallsBackInProcessWhenWorkerCannotStart(t *testing.T) {
	useRecordingMP3WorkerExecutable(t, filepath.Join(t.TempDir(), "missing"))
	source := writeMP3TestWAV(t, 2, 24000, 2)
	out, err := encodeRecordingMP3(t.Context(), source, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(out)
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Fatal("no output", err)
	}
}

// A worker that started and failed is a failed conversion for the job to retry.
func TestRecordingMP3WorkerCrashIsNotRepeatedInProcess(t *testing.T) {
	for _, program := range []string{"false", "true"} {
		path, err := exec.LookPath(program)
		if err != nil {
			t.Skip(err)
		}
		useRecordingMP3WorkerExecutable(t, path)
		root, _ := recordingTemporaryRoot()
		before, _ := os.ReadDir(root)
		out, err := encodeRecordingMP3(t.Context(), writeMP3TestWAV(t, 2, 24000, 2), 2)
		if err == nil || errors.Is(err, errRecordingMP3WorkerUnavailable) {
			os.Remove(out)
			t.Fatal("worker failure", program, err)
		}
		if after, _ := os.ReadDir(root); len(after) != len(before) {
			t.Fatal("failed conversion leaked temporary files")
		}
	}
}

func TestRecordingMP3WorkerExitsWhenParentGoes(t *testing.T) {
	executable, err := recordingMP3WorkerExecutable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := newRecordingMP3WorkerCommand(t.Context(), executable)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(recordingMP3WorkerRequest{Source: writeMP3TestWAV(t, 2, 24000, 600), Dest: recordingWorkerDest(t), Limit: maxProviderRecordingBytes})
	stdin.Write(payload)
	time.Sleep(200 * time.Millisecond)
	stdin.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		var status *exec.ExitError
		if !errors.As(err, &status) || status.ExitCode() != recordingMP3WorkerOrphaned {
			t.Fatal("worker exit", err)
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("worker outlived its parent")
	}
}
