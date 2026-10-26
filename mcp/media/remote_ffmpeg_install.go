package main

// Processing dependencies belong to the independently deployed component.
// Server resolves its placement; Media neither installs host binaries nor calls
// an infrastructure app.
import (
	"context"
	"errors"
	sdk "github.com/apteva/app-sdk"
)

type installedPaths struct{ FFmpeg, FFprobe string }
type remoteFFmpegInstaller struct{}

func newRemoteFFmpegInstaller() *remoteFFmpegInstaller { return &remoteFFmpegInstaller{} }
func sharedRemoteInstaller() *remoteFFmpegInstaller    { return newRemoteFFmpegInstaller() }
func (*remoteFFmpegInstaller) Ensure(ctx context.Context, app *sdk.AppCtx, _ int64) (installedPaths, error) {
	var caps struct {
		Protocol string `json:"protocol"`
		FFmpeg   string `json:"ffmpeg"`
		FFprobe  string `json:"ffprobe"`
	}
	if err := app.Components().Call(ctx, "executor", "capabilities", nil, &caps); err != nil {
		return installedPaths{}, err
	}
	if caps.Protocol != "apteva.media-executor/v1" || caps.FFmpeg == "" || caps.FFprobe == "" {
		return installedPaths{}, errors.New("executor dependencies unavailable: FFmpeg and FFprobe are required")
	}
	return installedPaths{caps.FFmpeg, caps.FFprobe}, nil
}
func runRemote(ctx context.Context, app *sdk.AppCtx, _ int64, command string, timeoutS int) (string, int, error) {
	out, exit, err := runComponentPlan(ctx, app, command, timeoutS)
	if failure := markMediaResourceFailure(ctx, err, out, exit); failure != nil {
		return out, exit, failure
	}
	return out, exit, err
}
