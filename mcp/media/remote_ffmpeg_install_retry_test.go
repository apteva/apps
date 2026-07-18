package main

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestRemoteFFmpegInstaller_RetriesTransientStartupBinding(t *testing.T) {
	inst := newRemoteFFmpegInstaller()
	var calls int
	var delays []time.Duration
	inst.installFn = func(context.Context, *sdk.AppCtx, int64) (installedPaths, error) {
		calls++
		if calls < 3 {
			return installedPaths{}, errors.New("instances call: HTTP 503: target app not ready: instances")
		}
		return installedPaths{FFmpeg: "/remote/ffmpeg", FFprobe: "/remote/ffprobe"}, nil
	}
	inst.sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}

	paths, err := inst.Ensure(t.Context(), nil, 1)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if calls != 3 || len(delays) != 2 || delays[0] != 250*time.Millisecond || delays[1] != 500*time.Millisecond {
		t.Fatalf("calls=%d delays=%v, want 3 calls with 250ms/500ms retry", calls, delays)
	}
	if paths.FFmpeg != "/remote/ffmpeg" || paths.FFprobe != "/remote/ffprobe" {
		t.Fatalf("paths = %+v", paths)
	}

	// A success remains process-cached and does not touch Instances again.
	if _, err := inst.Ensure(t.Context(), nil, 1); err != nil {
		t.Fatalf("cached Ensure: %v", err)
	}
	if calls != 3 {
		t.Fatalf("successful result was not cached; calls=%d", calls)
	}
}

func TestRemoteFFmpegInstaller_FailureCacheExpiresWithoutRestart(t *testing.T) {
	inst := newRemoteFFmpegInstaller()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	inst.now = func() time.Time { return now }
	var calls int
	inst.installFn = func(context.Context, *sdk.AppCtx, int64) (installedPaths, error) {
		calls++
		if calls == 1 {
			return installedPaths{}, errors.New("remote disk temporarily unavailable")
		}
		return installedPaths{FFmpeg: "/remote/ffmpeg", FFprobe: "/remote/ffprobe"}, nil
	}

	if _, err := inst.Ensure(t.Context(), nil, 9); err == nil {
		t.Fatal("first Ensure unexpectedly succeeded")
	}
	if _, err := inst.Ensure(t.Context(), nil, 9); err == nil || calls != 1 {
		t.Fatalf("failure cooldown did not suppress immediate retry: calls=%d err=%v", calls, err)
	}

	now = now.Add(remoteInstallFailureBase)
	paths, err := inst.Ensure(t.Context(), nil, 9)
	if err != nil {
		t.Fatalf("Ensure after cooldown: %v", err)
	}
	if calls != 2 || paths.FFmpeg != "/remote/ffmpeg" {
		t.Fatalf("failure remained poisoned after cooldown: calls=%d paths=%+v", calls, paths)
	}
}

func TestRetryableRemoteInstallError(t *testing.T) {
	for _, msg := range []string{
		"instances call: HTTP 403: app not bound: instances",
		"instances call: HTTP 503: target app not ready: instances",
		"target unreachable: dial tcp: connection refused",
	} {
		if !isRetryableRemoteInstallError(errors.New(msg)) {
			t.Errorf("expected retryable: %q", msg)
		}
	}
	if isRetryableRemoteInstallError(errors.New("unsupported remote arch ppc64")) {
		t.Fatal("permanent architecture error classified retryable")
	}
}
