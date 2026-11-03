package main

import (
	"context"
	"errors"

	sdk "github.com/apteva/app-sdk"
)

type RenderProgress struct {
	Fraction       float64
	OutTimeSeconds float64
	Frame          int64
	Speed          string
}

type renderProgressKey struct{}

func withRenderProgress(ctx context.Context, fn func(RenderProgress)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, renderProgressKey{}, fn)
}

func reportRenderProgress(ctx context.Context, progress RenderProgress) {
	if ctx == nil {
		return
	}
	if fn, ok := ctx.Value(renderProgressKey{}).(func(RenderProgress)); ok && fn != nil {
		fn(progress)
	}
}

// Executor is the swap-point between rendering modes.
//
// Render is expected to block until the work completes (local, remote
// via SSH) OR until the work is queued at an async provider (Shotstack
// et al). Sync executors return Result.Sync=true with bytes ready
// (LocalPath populated); the caller handles storage save + row
// completion. Async executors return Sync=false with ProviderRenderID
// populated; a worker poll loop handles completion.
//
// Composer ships local + component (renderer worker) executors.
type Executor interface {
	Name() string
	Render(ctx context.Context, app *sdk.AppCtx, edit *Edit, output Output, projectID string) (Result, error)
}

type Result struct {
	Sync             bool
	LocalPath        string // sync executors: path to the rendered file on the sidecar's disk
	Cleanup          func() // sync executors: remove temporary render artifacts after LocalPath is persisted
	ProviderRenderID string // async executors: handle for the worker to poll
	DurationMS       int64  // wall-clock render time (when known)
	CostUSD          float64
	FFmpegCommand    string // captured for debugging; empty for SaaS executors
}

// selectExecutor walks the precedence ladder:
//
//  1. render_executor integration bound → SaaS path (stub in v0.1)
//  2. execution_mode=component (or a legacy render_host_id) → the private
//     renderer component, placed by Server
//  3. otherwise → local ffmpeg
//
// Callers can override via composition_render(executor: "local"|"component")
// — see chooseExecutor.
func selectExecutor(ctx *sdk.AppCtx) Executor {
	if bound := ctx.IntegrationFor("render_executor"); bound != nil {
		// SaaS executors land in v0.2. Fall through to the ffmpeg ladder
		// so installs with a half-wired SaaS binding still render.
		ctx.Logger().Warn("render_executor bound but SaaS executors not wired in v0.1; falling back",
			"slug", bound.AppSlug)
	}
	if componentRenderingEnabled(ctx) {
		return &componentFFmpegExecutor{}
	}
	return &localFFmpegExecutor{}
}

// chooseExecutor honours an explicit override from the tool/HTTP
// caller. "" → use the ladder; "local"|"component" → force. "remote" is
// the pre-0.11 name for offloaded rendering and maps to the component.
func chooseExecutor(ctx *sdk.AppCtx, override string) (Executor, error) {
	switch override {
	case "", "auto":
		return selectExecutor(ctx), nil
	case "local":
		return &localFFmpegExecutor{}, nil
	case "component", "remote":
		return &componentFFmpegExecutor{}, nil
	}
	return nil, errors.New("unknown executor: " + override)
}
