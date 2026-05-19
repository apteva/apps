package main

import (
	"context"
	"errors"

	sdk "github.com/apteva/app-sdk"
)

// Executor is the swap-point between rendering modes.
//
// Render is expected to block until the work completes (local, remote
// via SSH) OR until the work is queued at an async provider (Shotstack
// et al). Sync executors return Result.Sync=true with bytes ready
// (LocalPath populated); the caller handles storage save + row
// completion. Async executors return Sync=false with ProviderRenderID
// populated; a worker poll loop handles completion.
//
// v0.1 ships local + remote only.
type Executor interface {
	Name() string
	Render(ctx context.Context, app *sdk.AppCtx, edit *Edit, output Output, projectID string) (Result, error)
}

type Result struct {
	Sync             bool
	LocalPath        string // sync executors: path to the rendered file on the sidecar's disk
	ProviderRenderID string // async executors: handle for the worker to poll
	DurationMS       int64  // wall-clock render time (when known)
	CostUSD          float64
	FFmpegCommand    string // captured for debugging; empty for SaaS executors
}

// selectExecutor walks the precedence ladder:
//
//  1. render_executor integration bound → SaaS path (stub in v0.1)
//  2. RENDER_HOST_ID env > 0 → remote ffmpeg via instances
//  3. otherwise → local ffmpeg
//
// Callers can override via composition_render(executor: "local"|"remote")
// — see chooseExecutor.
func selectExecutor(ctx *sdk.AppCtx) Executor {
	if bound := ctx.IntegrationFor("render_executor"); bound != nil {
		// SaaS executors land in v0.2. Fall through to the ffmpeg ladder
		// so installs with a half-wired SaaS binding still render.
		ctx.Logger().Warn("render_executor bound but SaaS executors not wired in v0.1; falling back",
			"slug", bound.AppSlug)
	}
	if id := renderHostID(); id > 0 {
		return &remoteFFmpegExecutor{hostID: id}
	}
	return &localFFmpegExecutor{}
}

// chooseExecutor honours an explicit override from the tool/HTTP
// caller. "" → use the ladder; "local"|"remote" → force.
func chooseExecutor(ctx *sdk.AppCtx, override string) (Executor, error) {
	switch override {
	case "":
		return selectExecutor(ctx), nil
	case "local":
		return &localFFmpegExecutor{}, nil
	case "remote":
		if renderHostID() == 0 {
			return nil, errors.New("executor=remote but RENDER_HOST_ID install-config is unset")
		}
		return &remoteFFmpegExecutor{hostID: renderHostID()}, nil
	}
	return nil, errors.New("unknown executor: " + override)
}
