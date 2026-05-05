// FFmpeg — sync ffmpeg-as-a-service for sibling Apteva apps. See
// apteva.yaml for the public surface. Stateless: no DB, no panel,
// just three MCP tools wrapping the local ffmpeg/ffprobe binaries.

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const manifestYAML = `schema: apteva-app/v1
name: ffmpeg
display_name: FFmpeg
version: 0.1.0
description: Stateless ffmpeg-as-a-service. Three sync MCP tools — grab_frame / probe / version — for sibling apps that need a single frame out of a stream without bundling their own ffmpeg.
author: Apteva
scopes: [project, global]
requires:
  permissions: [net.egress]
provides:
  mcp_tools:
    - { name: ffmpeg_grab_frame, description: "Grab one frame from any ffmpeg-readable URL." }
    - { name: ffmpeg_probe,      description: "Run ffprobe and return parsed JSON." }
    - { name: ffmpeg_version,    description: "Cached ffmpeg -version output." }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/ffmpeg
  port: 8080
  health_check: /health
upgrade_policy: auto-patch
`

type App struct {
	ctx         *sdk.AppCtx
	ffmpegPath  string
	ffprobePath string
	versionStr  string
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("ffmpeg: invalid manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	a.ctx = ctx

	a.ffmpegPath = configString(ctx, "ffmpeg_path", "")
	if a.ffmpegPath == "" {
		p, err := exec.LookPath("ffmpeg")
		if err != nil {
			return errors.New("ffmpeg not found on PATH — install ffmpeg or set ffmpeg_path in this app's config")
		}
		a.ffmpegPath = p
	}
	a.ffprobePath = configString(ctx, "ffprobe_path", "")
	if a.ffprobePath == "" {
		p, err := exec.LookPath("ffprobe")
		if err != nil {
			return errors.New("ffprobe not found on PATH — install ffmpeg (ffprobe ships in the same package) or set ffprobe_path")
		}
		a.ffprobePath = p
	}

	// Cache the version string so ffmpeg_version is free at call
	// time. ffmpeg -version writes the build line to stdout; we strip
	// to the first line for a clean banner.
	if out, err := exec.Command(a.ffmpegPath, "-version").Output(); err == nil {
		first := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
		a.versionStr = first
	} else {
		a.versionStr = "ffmpeg (version unknown — `ffmpeg -version` failed)"
	}
	ctx.Logger().Info("ffmpeg mounted",
		"ffmpeg_path", a.ffmpegPath,
		"ffprobe_path", a.ffprobePath,
		"version", a.versionStr)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) HTTPRoutes() []sdk.Route           { return nil } // SDK provides /health

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "ffmpeg_grab_frame",
			Description: "Grab a single frame from any ffmpeg-readable URL (RTSP, HTTP, file://). " +
				"Returns {bytes_base64, size_bytes, content_type, took_ms}. Sync; capped at 4 MiB. " +
				"Args: url (required), format? (jpeg|png, default jpeg), timeout_seconds? (1..30, default 8).",
			InputSchema: schemaObj(map[string]any{
				"url":             map[string]any{"type": "string"},
				"format":          map[string]any{"type": "string"},
				"timeout_seconds": map[string]any{"type": "integer"},
			}, []string{"url"}),
			Handler: a.toolGrabFrame,
		},
		{
			Name: "ffmpeg_probe",
			Description: "Run ffprobe and return its parsed JSON: streams, duration, codecs, dimensions, bitrate. " +
				"Sync. Args: url (required), timeout_seconds? (1..30, default 8).",
			InputSchema: schemaObj(map[string]any{
				"url":             map[string]any{"type": "string"},
				"timeout_seconds": map[string]any{"type": "integer"},
			}, []string{"url"}),
			Handler: a.toolProbe,
		},
		{
			Name:        "ffmpeg_version",
			Description: "Cached `ffmpeg -version` output. Returns {version, ffmpeg_path, ffprobe_path}. No args.",
			InputSchema: schemaObj(nil, nil),
			Handler:     a.toolVersion,
		},
	}
}

func (a *App) toolVersion(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return map[string]any{
		"version":      a.versionStr,
		"ffmpeg_path":  a.ffmpegPath,
		"ffprobe_path": a.ffprobePath,
	}, nil
}

// ─── helpers ───────────────────────────────────────────────────────

func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v, ok := ctx.Config()[key]; ok && v != "" {
		return v
	}
	return def
}

func schemaObj(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object"}
	if props != nil {
		out["properties"] = props
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func intArg(args map[string]any, key string, def int) int {
	if args == nil {
		return def
	}
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return def
}

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// boundedTimeout returns the caller's timeout_seconds clamped to
// [1,30]. Zero or negative inputs (including the absent-key case)
// fall back to defSec — passing 0 explicitly is treated the same as
// not passing the field at all.
func boundedTimeout(args map[string]any, defSec int) int {
	t := intArg(args, "timeout_seconds", defSec)
	if t <= 0 {
		t = defSec
	}
	if t > 30 {
		t = 30
	}
	return t
}

// snippet caps long byte slices for inclusion in error messages —
// stderr captures from ffmpeg can be large.
func snippet(b []byte) string {
	const max = 1024
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}

// _ = os keeps the import live until OnMount uses it directly.
var _ = os.Getenv

func main() { sdk.Run(&App{}) }
