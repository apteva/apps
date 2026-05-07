// Apteva Tapo app — local-LAN control of TP-Link Tapo cameras.
//
// One sidecar manages many cameras. Each camera is a row in
// `cameras` plus a lazily-created tapo.Client kept in clientCache for
// reuse across calls — the legacy stok session is good for ~25min, so
// constantly re-logging-in would 401-loop us.
//
// The motion poller (one Worker) walks every online camera every
// pollInterval, fetches recent on-device events, deduplicates against
// motion_events.raw_event_id, and (when configured) auto-snapshots to
// the storage app and emits a `tapo.motion` event so todo / messaging
// apps can react.
package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: tapo
display_name: Tapo Cameras
version: 0.2.1
description: Local-LAN control of TP-Link Tapo cameras.
author: Apteva
scopes: [project, global]
requires:
  permissions: [db.write.app, net.egress]
  integrations: []
  apps:
    - name: ffmpeg
      version: ">=0.1.0"
      optional: true
      reason: "snapshot_capture delegates to ffmpeg_grab_frame"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: cameras_add,           description: "Register a Tapo camera." }
    - { name: cameras_list,          description: "List registered cameras." }
    - { name: cameras_get,           description: "Read one camera." }
    - { name: cameras_test,          description: "Re-probe a camera." }
    - { name: cameras_rename,        description: "Rename / re-room a camera." }
    - { name: cameras_remove,        description: "Delete a camera." }
    - { name: snapshot_capture,      description: "Grab a still frame." }
    - { name: stream_get_url,        description: "Get an RTSP/HLS URL." }
    - { name: ptz_move,              description: "Pan/tilt the camera." }
    - { name: ptz_calibrate,         description: "Recenter to factory zero." }
    - { name: ptz_preset_save,       description: "Save current pose as a preset." }
    - { name: ptz_preset_recall,     description: "Move to a saved preset." }
    - { name: ptz_preset_list,       description: "List on-camera presets." }
    - { name: ptz_preset_delete,     description: "Delete a preset." }
    - { name: privacy_set,           description: "Toggle privacy lens cover." }
    - { name: led_set,               description: "Toggle status LED." }
    - { name: night_mode_set,        description: "Set night vision mode." }
    - { name: motion_detection_set,  description: "Toggle motion detection." }
    - { name: motion_events_recent,  description: "List cached motion events." }
    - { name: siren_trigger,         description: "Trigger the camera siren." }
  ui_panels:
    - slot: project.page
      label: Cameras
      icon: video
      entry: /ui/CamerasPanel.mjs
  ui_components:
    - name: camera-tile
      entry: /ui/CameraTile.mjs
      slots: [chat.message_attachment]
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/tapo
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/tapo.db
  migrations: migrations/
upgrade_policy: auto-patch
`

const (
	pollInterval     = 30 * time.Second
	snapshotMaxBytes = 4 << 20 // 4 MiB cap on inline base64 returns
)

var globalCtx *sdk.AppCtx

// clientCache keeps one *Client per camera id, alive until the row is
// deleted. The Client manages its own session locking internally, so
// the cache only needs a coarse lock for insert/delete.
var (
	clientCache   = map[int64]*Client{}
	clientCacheMu sync.Mutex
)

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("tapo: invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("tapo: requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("tapo mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// One worker, simple loop. The framework supervises restarts so we
// don't have to worry about crash-loops here.
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name: "motion-poller",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				t := time.NewTicker(pollInterval)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return nil
					case <-t.C:
						if err := pollAllCameras(app); err != nil {
							app.Logger().Warn("motion poll", "err", err.Error())
						}
						pruneMotionEvents(app)
					}
				}
			},
		},
	}
}

// ─── HTTP routes (panel reads) ──────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/cameras", Handler: a.handleCameras},
		{Pattern: "/cameras/", Handler: a.handleCameraItem},
		{Pattern: "/snapshots/", Handler: a.handleSnapshot}, // GET /snapshots/{id}.jpg
		{Pattern: "/events", Handler: a.handleEvents},
	}
}

func (a *App) handleCameras(w http.ResponseWriter, r *http.Request) {
	pid := projectScope()
	switch r.Method {
	case http.MethodGet:
		out, err := listCameras(globalCtx.AppDB(), pid)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body struct {
			Name, Room, IP, Username, Password string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		out, err := addCamera(globalCtx.AppDB(), pid, body.Name, body.Room, body.IP, body.Username, body.Password)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handleCameraItem(w http.ResponseWriter, r *http.Request) {
	pid := projectScope()
	rest := strings.TrimPrefix(r.URL.Path, "/cameras/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "" && r.Method == http.MethodGet:
		c, err := getCamera(globalCtx.AppDB(), pid, id)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		writeJSON(w, c)
	case action == "" && r.Method == http.MethodPut:
		var body struct{ Name, Room *string }
		json.NewDecoder(r.Body).Decode(&body)
		if err := renameCamera(globalCtx.AppDB(), pid, id, body.Name, body.Room); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		c, _ := getCamera(globalCtx.AppDB(), pid, id)
		writeJSON(w, c)
	case action == "" && r.Method == http.MethodDelete:
		if err := removeCamera(globalCtx.AppDB(), pid, id); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	case action == "test" && r.Method == http.MethodPost:
		out, err := testCamera(globalCtx.AppDB(), pid, id)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		writeJSON(w, out)
	case action == "ptz" && r.Method == http.MethodPost:
		var body struct {
			Direction               string `json:"direction"`
			DurationMs              int    `json:"duration_ms"`
			PanDegrees, TiltDegrees *int
		}
		json.NewDecoder(r.Body).Decode(&body)
		if err := doPTZ(pid, id, body.Direction, body.DurationMs, body.PanDegrees, body.TiltDegrees); err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		w.WriteHeader(204)
	default:
		http.Error(w, "method/path not supported", 405)
	}
}

// handleSnapshot serves a fresh JPEG bypassing base64 — the panel uses
// this in <img src> tags so the browser can stream it. /snapshots/{id}.jpg
func (a *App) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	id := pathInt(r.URL.Path, "/snapshots/")
	cam, err := getCamera(globalCtx.AppDB(), projectScope(), id)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	cli, err := clientFor(cam)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	jpg, err := snapshotViaFfmpegApp(globalCtx, cli)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(jpg)
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	pid := projectScope()
	camID, _ := strconv.ParseInt(r.URL.Query().Get("camera_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	since := r.URL.Query().Get("since")
	out, err := listEvents(globalCtx.AppDB(), pid, camID, since, limit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

// ─── MCP tools ──────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	obj := func(props map[string]any, req []string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(req) > 0 {
			s["required"] = req
		}
		return s
	}
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "integer"}
	boo := map[string]any{"type": "boolean"}

	return []sdk.Tool{
		{Name: "cameras_add",
			Description: "Register a Tapo camera. Probes capabilities and verifies the camera-account credentials. The username/password are the Camera Account set in the Tapo mobile app, NOT your TP-Link cloud login. Args: name, ip, username, password, room?.",
			InputSchema: obj(map[string]any{
				"name": str, "ip": str, "username": str, "password": str, "room": str,
			}, []string{"name", "ip", "username", "password"}),
			Handler: a.toolCamerasAdd},
		{Name: "cameras_list",
			Description: "List registered cameras with online status, model, room and probed capabilities.",
			InputSchema: obj(nil, nil),
			Handler:     a.toolCamerasList},
		{Name: "cameras_get",
			Description: "Read one camera by id.",
			InputSchema: obj(map[string]any{"id": num}, []string{"id"}),
			Handler:     a.toolCamerasGet},
		{Name: "cameras_test",
			Description: "Re-probe a camera. Refreshes online status, firmware and capability flags.",
			InputSchema: obj(map[string]any{"id": num}, []string{"id"}),
			Handler:     a.toolCamerasTest},
		{Name: "cameras_rename",
			Description: "Rename a camera or change its room. Pass either name or room (or both).",
			InputSchema: obj(map[string]any{"id": num, "name": str, "room": str}, []string{"id"}),
			Handler:     a.toolCamerasRename},
		{Name: "cameras_remove",
			Description: "Delete a camera registration.",
			InputSchema: obj(map[string]any{"id": num}, []string{"id"}),
			Handler:     a.toolCamerasRemove},
		{Name: "snapshot_capture",
			Description: "Grab a still frame. By default returns base64 JPEG inline (capped at 4MB). When save_to_storage=true, pushes to the storage app and returns the file_id instead.",
			InputSchema: obj(map[string]any{
				"id": num, "save_to_storage": boo, "folder": str,
			}, []string{"id"}),
			Handler: a.toolSnapshotCapture},
		{Name: "stream_get_url",
			Description: "Mint a streaming URL. quality: hd (default) | sd. protocol: rtsp (default) | hls. The URL embeds camera-account credentials — treat as a secret.",
			InputSchema: obj(map[string]any{
				"id": num, "quality": str, "protocol": str, "ttl_seconds": num,
			}, []string{"id"}),
			Handler: a.toolStreamGetURL},
		{Name: "ptz_move",
			Description: "Pan/tilt the camera. Pass direction (up|down|left|right|stop) for a directional pulse, or pan_degrees + tilt_degrees for an absolute move. duration_ms optional (default 500, capped at 5000) for directional pulses.",
			InputSchema: obj(map[string]any{
				"id": num, "direction": str, "duration_ms": num,
				"pan_degrees": num, "tilt_degrees": num,
			}, []string{"id"}),
			Handler: a.toolPTZMove},
		{Name: "ptz_calibrate",
			Description: "Send the camera back to factory zero.",
			InputSchema: obj(map[string]any{"id": num}, []string{"id"}),
			Handler:     a.toolPTZCalibrate},
		{Name: "ptz_preset_save",
			Description: "Save the current pose as a named on-camera preset. Returns the preset_id.",
			InputSchema: obj(map[string]any{"id": num, "name": str}, []string{"id", "name"}),
			Handler:     a.toolPresetSave},
		{Name: "ptz_preset_recall",
			Description: "Move to a saved preset. Pass preset_id (numeric on-camera id) or preset_name.",
			InputSchema: obj(map[string]any{
				"id": num, "preset_id": str, "preset_name": str,
			}, []string{"id"}),
			Handler: a.toolPresetRecall},
		{Name: "ptz_preset_list",
			Description: "List on-camera presets.",
			InputSchema: obj(map[string]any{"id": num}, []string{"id"}),
			Handler:     a.toolPresetList},
		{Name: "ptz_preset_delete",
			Description: "Delete an on-camera preset.",
			InputSchema: obj(map[string]any{"id": num, "preset_id": str}, []string{"id", "preset_id"}),
			Handler:     a.toolPresetDelete},
		{Name: "privacy_set",
			Description: "Toggle the privacy lens cover (or software blackout on models without one).",
			InputSchema: obj(map[string]any{"id": num, "enabled": boo}, []string{"id", "enabled"}),
			Handler:     a.toolPrivacySet},
		{Name: "led_set",
			Description: "Toggle the camera status LED.",
			InputSchema: obj(map[string]any{"id": num, "enabled": boo}, []string{"id", "enabled"}),
			Handler:     a.toolLEDSet},
		{Name: "night_mode_set",
			Description: "Set night vision mode. mode: auto | on | off.",
			InputSchema: obj(map[string]any{"id": num, "mode": str}, []string{"id", "mode"}),
			Handler:     a.toolNightModeSet},
		{Name: "motion_detection_set",
			Description: "Toggle motion detection and tune sensitivity. sensitivity: low | med | high (optional).",
			InputSchema: obj(map[string]any{
				"id": num, "enabled": boo, "sensitivity": str,
			}, []string{"id", "enabled"}),
			Handler: a.toolMotionDetectionSet},
		{Name: "motion_events_recent",
			Description: "Return cached motion events. camera_id optional (omit for all cameras in scope), since RFC3339, limit default 100.",
			InputSchema: obj(map[string]any{
				"camera_id": num, "since": str, "limit": num,
			}, nil),
			Handler: a.toolMotionEventsRecent},
		{Name: "siren_trigger",
			Description: "Sound the camera siren. duration_seconds default 5, capped at 30.",
			InputSchema: obj(map[string]any{
				"id": num, "duration_seconds": num,
			}, []string{"id"}),
			Handler: a.toolSirenTrigger},
	}
}

// ─── tool handlers ──────────────────────────────────────────────────

func (a *App) toolCamerasAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name := strArg(args, "name")
	ip := strArg(args, "ip")
	user := strArg(args, "username")
	pass := strArg(args, "password")
	room := strArg(args, "room")
	if name == "" || ip == "" || user == "" || pass == "" {
		return nil, errors.New("name, ip, username and password are required")
	}
	return addCamera(ctx.AppDB(), projectScope(), name, room, ip, user, pass)
}

func (a *App) toolCamerasList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return listCameras(ctx.AppDB(), projectScope())
}

func (a *App) toolCamerasGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	return getCamera(ctx.AppDB(), projectScope(), id)
}

func (a *App) toolCamerasTest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	return testCamera(ctx.AppDB(), projectScope(), id)
}

func (a *App) toolCamerasRename(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	var name, room *string
	if v, ok := args["name"].(string); ok {
		name = &v
	}
	if v, ok := args["room"].(string); ok {
		room = &v
	}
	if err := renameCamera(ctx.AppDB(), projectScope(), id, name, room); err != nil {
		return nil, err
	}
	return getCamera(ctx.AppDB(), projectScope(), id)
}

func (a *App) toolCamerasRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if err := removeCamera(ctx.AppDB(), projectScope(), id); err != nil {
		return nil, err
	}
	return map[string]any{"status": "deleted", "id": id}, nil
}

func (a *App) toolSnapshotCapture(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	cam, err := getCameraFull(ctx.AppDB(), projectScope(), id)
	if err != nil {
		return nil, err
	}
	cli, err := clientFor(cam)
	if err != nil {
		return nil, err
	}
	jpg, err := snapshotViaFfmpegApp(ctx, cli)
	if err != nil {
		return nil, err
	}
	if save, _ := args["save_to_storage"].(bool); save {
		folder := strArg(args, "folder")
		if folder == "" {
			folder = "/cameras/" + cam.Name
		}
		fileID, err := pushToStorage(ctx, folder, cam.Name, jpg)
		if err != nil {
			return nil, fmt.Errorf("save_to_storage: %w", err)
		}
		return map[string]any{
			"camera_id": id,
			"file_id":   fileID,
			"bytes":     len(jpg),
		}, nil
	}
	if len(jpg) > snapshotMaxBytes {
		return nil, fmt.Errorf("snapshot too large for inline return (%d bytes); pass save_to_storage=true", len(jpg))
	}
	return map[string]any{
		"camera_id":    id,
		"jpeg_base64":  base64.StdEncoding.EncodeToString(jpg),
		"bytes":        len(jpg),
		"content_type": "image/jpeg",
	}, nil
}

func (a *App) toolStreamGetURL(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	cam, err := getCameraFull(ctx.AppDB(), projectScope(), id)
	if err != nil {
		return nil, err
	}
	cli, err := clientFor(cam)
	if err != nil {
		return nil, err
	}
	quality := strArg(args, "quality")
	if quality == "" {
		quality = "hd"
	}
	proto := strArg(args, "protocol")
	if proto == "" {
		proto = "rtsp"
	}
	if proto != "rtsp" {
		// HLS would require a transcoder sidecar — call out clearly
		// rather than ship a half-working URL.
		return nil, errors.New("protocol=hls requires a transcoder; v0.1 supports rtsp only")
	}
	url := cli.RTSPURL(quality)
	ttl := intArg(args, "ttl_seconds")
	if ttl == 0 {
		ttl = 3600
	}
	return map[string]any{
		"camera_id": id,
		"url":       url,
		"protocol":  "rtsp",
		"quality":   quality,
		"expires_at": time.Now().UTC().Add(time.Duration(ttl) * time.Second).Format(time.RFC3339),
	}, nil
}

func (a *App) toolPTZMove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	dir := strArg(args, "direction")
	dur := int(intArg(args, "duration_ms"))
	var pan, tilt *int
	if v, ok := args["pan_degrees"]; ok {
		n := int(toInt64(v))
		pan = &n
	}
	if v, ok := args["tilt_degrees"]; ok {
		n := int(toInt64(v))
		tilt = &n
	}
	return nil, doPTZ(projectScope(), id, dir, dur, pan, tilt)
}

func (a *App) toolPTZCalibrate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	cli, err := clientForID(ctx.AppDB(), projectScope(), id)
	if err != nil {
		return nil, err
	}
	return nil, cli.PTZCalibrate()
}

func (a *App) toolPresetSave(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	name := strArg(args, "name")
	if id == 0 || name == "" {
		return nil, errors.New("id and name required")
	}
	cli, err := clientForID(ctx.AppDB(), projectScope(), id)
	if err != nil {
		return nil, err
	}
	pid, err := cli.PresetSave(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"preset_id": pid, "name": name}, nil
}

func (a *App) toolPresetRecall(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	cli, err := clientForID(ctx.AppDB(), projectScope(), id)
	if err != nil {
		return nil, err
	}
	pid := strArg(args, "preset_id")
	if pid == "" {
		name := strArg(args, "preset_name")
		if name == "" {
			return nil, errors.New("preset_id or preset_name required")
		}
		// Resolve name → id by listing.
		presets, err := cli.PresetList()
		if err != nil {
			return nil, err
		}
		for _, p := range presets {
			if strings.EqualFold(p.Name, name) {
				pid = p.ID
				break
			}
		}
		if pid == "" {
			return nil, fmt.Errorf("preset %q not found", name)
		}
	}
	return nil, cli.PresetRecall(pid)
}

func (a *App) toolPresetList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	cli, err := clientForID(ctx.AppDB(), projectScope(), id)
	if err != nil {
		return nil, err
	}
	return cli.PresetList()
}

func (a *App) toolPresetDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	pid := strArg(args, "preset_id")
	if id == 0 || pid == "" {
		return nil, errors.New("id and preset_id required")
	}
	cli, err := clientForID(ctx.AppDB(), projectScope(), id)
	if err != nil {
		return nil, err
	}
	return nil, cli.PresetDelete(pid)
}

func (a *App) toolPrivacySet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	cli, err := clientForID(ctx.AppDB(), projectScope(), id)
	if err != nil {
		return nil, err
	}
	on, _ := args["enabled"].(bool)
	return nil, cli.PrivacySet(on)
}

func (a *App) toolLEDSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	cli, err := clientForID(ctx.AppDB(), projectScope(), id)
	if err != nil {
		return nil, err
	}
	on, _ := args["enabled"].(bool)
	return nil, cli.LEDSet(on)
}

func (a *App) toolNightModeSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	cli, err := clientForID(ctx.AppDB(), projectScope(), id)
	if err != nil {
		return nil, err
	}
	return nil, cli.NightModeSet(strArg(args, "mode"))
}

func (a *App) toolMotionDetectionSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	cli, err := clientForID(ctx.AppDB(), projectScope(), id)
	if err != nil {
		return nil, err
	}
	on, _ := args["enabled"].(bool)
	return nil, cli.MotionDetectionSet(on, strArg(args, "sensitivity"))
}

func (a *App) toolMotionEventsRecent(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	camID := intArg(args, "camera_id")
	since := strArg(args, "since")
	limit := int(intArg(args, "limit"))
	return listEvents(ctx.AppDB(), projectScope(), camID, since, limit)
}

func (a *App) toolSirenTrigger(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := intArg(args, "id")
	cli, err := clientForID(ctx.AppDB(), projectScope(), id)
	if err != nil {
		return nil, err
	}
	return nil, cli.SirenTrigger(int(intArg(args, "duration_seconds")))
}

// ─── DB layer ───────────────────────────────────────────────────────

type Camera struct {
	ID           int64           `json:"id"`
	ProjectID    string          `json:"project_id"`
	Name         string          `json:"name"`
	Room         string          `json:"room"`
	IP           string          `json:"ip"`
	Username     string          `json:"username"`
	Model        string          `json:"model"`
	Firmware     string          `json:"firmware"`
	Capabilities Capabilities    `json:"capabilities"`
	Online       bool            `json:"online"`
	LastSeenAt   string          `json:"last_seen_at,omitempty"`
	LastError    string          `json:"last_error,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	password     string          `json:"-"` // decrypted on demand, not exposed
}

func projectScope() string {
	if pid := os.Getenv("APTEVA_PROJECT_ID"); pid != "" {
		return pid
	}
	return "default"
}

func addCamera(db *sql.DB, pid, name, room, ip, user, pass string) (*Camera, error) {
	// Probe before insert — half a row with no working creds is worse
	// than a clean rejection. Login + capability probe in one go.
	cli := NewClient(ip, user, pass)
	info, err := cli.GetDeviceInfo()
	if err != nil {
		return nil, fmt.Errorf("probe %s: %w", ip, err)
	}
	caps, err := cli.ProbeCapabilities()
	if err != nil {
		return nil, fmt.Errorf("capabilities %s: %w", ip, err)
	}
	capsJSON, _ := json.Marshal(caps)

	encPass, err := encryptPassword(pass)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := db.Exec(
		`INSERT INTO cameras
		   (project_id, name, room, ip, username, password_enc,
		    model, firmware, capabilities_json,
		    online, last_seen_at, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,1,?,?,?)`,
		pid, name, room, ip, user, encPass,
		info.Model, info.Firmware, string(capsJSON),
		now, now, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()

	// Cache the working client so the first follow-up call doesn't
	// re-login.
	clientCacheMu.Lock()
	clientCache[id] = cli
	clientCacheMu.Unlock()

	return getCamera(db, pid, id)
}

func listCameras(db *sql.DB, pid string) ([]Camera, error) {
	rows, err := db.Query(
		`SELECT id, project_id, name, room, ip, username,
		        model, firmware, capabilities_json,
		        online, last_seen_at, last_error, created_at, updated_at
		   FROM cameras WHERE project_id = ? ORDER BY name`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Camera{}
	for rows.Next() {
		c, err := scanCamera(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

func getCamera(db *sql.DB, pid string, id int64) (*Camera, error) {
	rows, err := db.Query(
		`SELECT id, project_id, name, room, ip, username,
		        model, firmware, capabilities_json,
		        online, last_seen_at, last_error, created_at, updated_at
		   FROM cameras WHERE project_id = ? AND id = ?`, pid, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("camera %d not found", id)
	}
	return scanCamera(rows)
}

// getCameraFull also loads the decrypted password, for tools that
// need to talk to the device. Internal-only; never serialised.
func getCameraFull(db *sql.DB, pid string, id int64) (*Camera, error) {
	c, err := getCamera(db, pid, id)
	if err != nil {
		return nil, err
	}
	var enc string
	if err := db.QueryRow(
		`SELECT password_enc FROM cameras WHERE project_id = ? AND id = ?`, pid, id,
	).Scan(&enc); err != nil {
		return nil, err
	}
	pass, err := decryptPassword(enc)
	if err != nil {
		return nil, err
	}
	c.password = pass
	return c, nil
}

func scanCamera(rows *sql.Rows) (*Camera, error) {
	var c Camera
	var capsJSON, lastSeen sql.NullString
	var online int
	if err := rows.Scan(
		&c.ID, &c.ProjectID, &c.Name, &c.Room, &c.IP, &c.Username,
		&c.Model, &c.Firmware, &capsJSON,
		&online, &lastSeen, &c.LastError, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	c.Online = online == 1
	c.LastSeenAt = lastSeen.String
	if capsJSON.Valid && capsJSON.String != "" {
		_ = json.Unmarshal([]byte(capsJSON.String), &c.Capabilities)
	}
	return &c, nil
}

func renameCamera(db *sql.DB, pid string, id int64, name, room *string) error {
	cols := []string{}
	args := []any{}
	if name != nil {
		cols = append(cols, "name = ?")
		args = append(args, *name)
	}
	if room != nil {
		cols = append(cols, "room = ?")
		args = append(args, *room)
	}
	if len(cols) == 0 {
		return nil
	}
	cols = append(cols, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(time.RFC3339))
	args = append(args, id, pid)
	_, err := db.Exec(
		`UPDATE cameras SET `+strings.Join(cols, ", ")+` WHERE id = ? AND project_id = ?`,
		args...,
	)
	return err
}

func removeCamera(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(`DELETE FROM cameras WHERE id = ? AND project_id = ?`, id, pid)
	if err != nil {
		return err
	}
	clientCacheMu.Lock()
	delete(clientCache, id)
	clientCacheMu.Unlock()
	return nil
}

func testCamera(db *sql.DB, pid string, id int64) (*Camera, error) {
	cam, err := getCameraFull(db, pid, id)
	if err != nil {
		return nil, err
	}
	cli, err := clientFor(cam)
	if err != nil {
		return nil, err
	}
	online := true
	lastErr := ""
	info, err := cli.GetDeviceInfo()
	if err != nil {
		online, lastErr = false, err.Error()
	}
	caps, _ := cli.ProbeCapabilities()
	capsJSON := "{}"
	if caps != nil {
		b, _ := json.Marshal(caps)
		capsJSON = string(b)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	model, fw := cam.Model, cam.Firmware
	if info != nil {
		model, fw = info.Model, info.Firmware
	}
	if _, err := db.Exec(
		`UPDATE cameras
		    SET model = ?, firmware = ?, capabilities_json = ?,
		        online = ?, last_seen_at = ?, last_error = ?, updated_at = ?
		  WHERE id = ? AND project_id = ?`,
		model, fw, capsJSON, boolToInt(online), now, lastErr, now, id, pid,
	); err != nil {
		return nil, err
	}
	return getCamera(db, pid, id)
}

// ─── motion events ──────────────────────────────────────────────────

type StoredEvent struct {
	ID             int64  `json:"id"`
	CameraID       int64  `json:"camera_id"`
	OccurredAt     string `json:"occurred_at"`
	Kind           string `json:"kind"`
	BBoxJSON       string `json:"bbox,omitempty"`
	SnapshotFileID *int64 `json:"snapshot_file_id,omitempty"`
}

func listEvents(db *sql.DB, pid string, camID int64, since string, limit int) ([]StoredEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `SELECT id, camera_id, occurred_at, kind, bbox_json, snapshot_file_id
	        FROM motion_events WHERE project_id = ?`
	args := []any{pid}
	if camID > 0 {
		q += ` AND camera_id = ?`
		args = append(args, camID)
	}
	if since != "" {
		q += ` AND occurred_at >= ?`
		args = append(args, since)
	}
	q += ` ORDER BY occurred_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredEvent{}
	for rows.Next() {
		var e StoredEvent
		var bbox sql.NullString
		var fid sql.NullInt64
		if err := rows.Scan(&e.ID, &e.CameraID, &e.OccurredAt, &e.Kind, &bbox, &fid); err != nil {
			return nil, err
		}
		e.BBoxJSON = bbox.String
		if fid.Valid {
			v := fid.Int64
			e.SnapshotFileID = &v
		}
		out = append(out, e)
	}
	return out, nil
}

// pollAllCameras fetches recent motion events from each online camera
// and inserts new ones (deduped by raw_event_id). When configured,
// auto-snapshots the first event of a burst and pushes it into the
// storage app.
func pollAllCameras(ctx *sdk.AppCtx) error {
	cams, err := listCamerasWithPasswords(ctx.AppDB(), projectScope())
	if err != nil {
		return err
	}
	autoSnap := configFlag(ctx, "default_snapshot_on_motion", true)
	for _, cam := range cams {
		if !cam.Online {
			continue
		}
		cli, err := clientFor(&cam)
		if err != nil {
			continue
		}
		// "Recent": last 10 minutes — generous enough to recover from
		// short network blips without re-loading the entire camera buffer.
		events, err := cli.ListMotionEvents(time.Now().Add(-10 * time.Minute))
		if err != nil {
			ctx.Logger().Warn("poll events", "camera", cam.Name, "err", err.Error())
			continue
		}
		for _, ev := range events {
			inserted, dbID, err := insertEventIfNew(ctx.AppDB(), cam, ev)
			if err != nil {
				ctx.Logger().Warn("insert event", "err", err.Error())
				continue
			}
			if !inserted {
				continue
			}
			if autoSnap {
				if jpg, err := snapshotViaFfmpegApp(ctx, cli); err == nil {
					if fid, err := pushToStorage(ctx, "/cameras/"+cam.Name, cam.Name, jpg); err == nil {
						_, _ = ctx.AppDB().Exec(
							`UPDATE motion_events SET snapshot_file_id = ? WHERE id = ?`,
							fid, dbID,
						)
					}
				}
			}
			ctx.Emit("tapo.motion", map[string]any{
				"camera_id":   cam.ID,
				"camera_name": cam.Name,
				"occurred_at": ev.OccurredAt.Format(time.RFC3339),
				"kind":        ev.Kind,
			})
		}
	}
	return nil
}

func insertEventIfNew(db *sql.DB, cam Camera, ev MotionEvent) (bool, int64, error) {
	if ev.ID == "" {
		// No id from camera — fall back to timestamp-based dedup so
		// we don't double-insert on poll overlap.
		ev.ID = "ts:" + ev.OccurredAt.Format(time.RFC3339)
	}
	res, err := db.Exec(
		`INSERT OR IGNORE INTO motion_events
		   (camera_id, project_id, occurred_at, kind, bbox_json, raw_event_id)
		 VALUES (?,?,?,?,?,?)`,
		cam.ID, cam.ProjectID, ev.OccurredAt.Format(time.RFC3339),
		ev.Kind, ev.BBoxJSON, ev.ID,
	)
	if err != nil {
		return false, 0, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, 0, nil
	}
	id, _ := res.LastInsertId()
	return true, id, nil
}

func pruneMotionEvents(ctx *sdk.AppCtx) {
	days := configInt(ctx, "motion_event_retention_days", 7)
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	_, _ = ctx.AppDB().Exec(`DELETE FROM motion_events WHERE occurred_at < ?`, cutoff)
}

// listCamerasWithPasswords is the poller-internal variant: returns
// every camera in scope including the decrypted password, ready for
// clientFor.
func listCamerasWithPasswords(db *sql.DB, pid string) ([]Camera, error) {
	rows, err := db.Query(
		`SELECT id, project_id, name, room, ip, username, password_enc,
		        model, firmware, capabilities_json,
		        online, last_seen_at, last_error, created_at, updated_at
		   FROM cameras WHERE project_id = ?`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Camera{}
	for rows.Next() {
		var c Camera
		var capsJSON, lastSeen sql.NullString
		var online int
		var enc string
		if err := rows.Scan(
			&c.ID, &c.ProjectID, &c.Name, &c.Room, &c.IP, &c.Username, &enc,
			&c.Model, &c.Firmware, &capsJSON,
			&online, &lastSeen, &c.LastError, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		c.Online = online == 1
		c.LastSeenAt = lastSeen.String
		if capsJSON.Valid && capsJSON.String != "" {
			_ = json.Unmarshal([]byte(capsJSON.String), &c.Capabilities)
		}
		if pass, err := decryptPassword(enc); err == nil {
			c.password = pass
		}
		out = append(out, c)
	}
	return out, nil
}

// ─── client cache ───────────────────────────────────────────────────

func clientFor(cam *Camera) (*Client, error) {
	if cam.password == "" {
		return nil, errors.New("camera password not loaded (use getCameraFull)")
	}
	clientCacheMu.Lock()
	defer clientCacheMu.Unlock()
	if cli, ok := clientCache[cam.ID]; ok {
		return cli, nil
	}
	cli := NewClient(cam.IP, cam.Username, cam.password)
	clientCache[cam.ID] = cli
	return cli, nil
}

func clientForID(db *sql.DB, pid string, id int64) (*Client, error) {
	cam, err := getCameraFull(db, pid, id)
	if err != nil {
		return nil, err
	}
	return clientFor(cam)
}

// doPTZ unifies the directional and absolute paths so the HTTP and
// MCP entry points stay identical.
func doPTZ(pid string, id int64, dir string, durationMs int, pan, tilt *int) error {
	cli, err := clientForID(globalCtx.AppDB(), pid, id)
	if err != nil {
		return err
	}
	if pan != nil && tilt != nil {
		return cli.PTZMoveAbsolute(*pan, *tilt)
	}
	if dir == "" {
		return errors.New("ptz_move: direction or (pan_degrees + tilt_degrees) required")
	}
	return cli.PTZMoveDirection(dir, durationMs)
}

// ─── cross-app: storage ─────────────────────────────────────────────

// pushToStorage uploads a JPEG into the storage app's `files_upload`
// tool. Returns the new file_id. Filenames are timestamped so a burst
// of motion snapshots produces distinct rows.
func pushToStorage(ctx *sdk.AppCtx, folder, camName string, jpg []byte) (int64, error) {
	fname := fmt.Sprintf("%s-%s.jpg", camName, time.Now().UTC().Format("20060102-150405"))
	var out struct {
		FileID int64 `json:"file_id"`
		ID     int64 `json:"id"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", map[string]any{
		"filename":     fname,
		"folder":       folder,
		"content_type": "image/jpeg",
		"bytes_base64": base64.StdEncoding.EncodeToString(jpg),
	}, &out); err != nil {
		return 0, fmt.Errorf("storage upload: %w", err)
	}
	if out.FileID != 0 {
		return out.FileID, nil
	}
	return out.ID, nil
}

// ─── credential encryption ──────────────────────────────────────────
//
// AES-GCM with a 32-byte key from APTEVA_SECRET (base64). Empty
// secret → plaintext, with a warning at first use. This is a
// deliberate v0.1 choice: the app DB is private to the install, so
// plaintext is acceptable on a trusted host but every shared-infra
// deployment should set the secret.

var (
	secretWarnedOnce sync.Once
	secretKeyCache   []byte
	secretKeyErr     error
	secretKeyMu      sync.Mutex
)

func secretKey() ([]byte, error) {
	secretKeyMu.Lock()
	defer secretKeyMu.Unlock()
	if secretKeyCache != nil || secretKeyErr != nil {
		return secretKeyCache, secretKeyErr
	}
	raw := os.Getenv("APTEVA_SECRET")
	if raw == "" && globalCtx != nil {
		raw = configString(globalCtx, "shared_secret", "")
	}
	if raw == "" {
		secretKeyErr = errors.New("no secret")
		return nil, secretKeyErr
	}
	k, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		secretKeyErr = fmt.Errorf("APTEVA_SECRET not base64: %w", err)
		return nil, secretKeyErr
	}
	if len(k) != 32 {
		secretKeyErr = fmt.Errorf("APTEVA_SECRET must decode to 32 bytes, got %d", len(k))
		return nil, secretKeyErr
	}
	secretKeyCache = k
	return k, nil
}

func encryptPassword(pass string) (string, error) {
	key, err := secretKey()
	if err != nil {
		secretWarnedOnce.Do(func() {
			if globalCtx != nil {
				globalCtx.Logger().Warn("tapo: storing camera passwords plaintext (set shared_secret to enable AES-GCM)")
			}
		})
		return pass, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := g.Seal(nonce, nonce, []byte(pass), nil)
	return "enc::" + base64.StdEncoding.EncodeToString(ct), nil
}

func decryptPassword(stored string) (string, error) {
	if !strings.HasPrefix(stored, "enc::") {
		return stored, nil
	}
	key, err := secretKey()
	if err != nil {
		return "", fmt.Errorf("password is encrypted but no secret available: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, "enc::"))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ct) < g.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, body := ct[:g.NonceSize()], ct[g.NonceSize():]
	pt, err := g.Open(nil, nonce, body, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// ─── helpers ────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func strArg(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	return ""
}

func intArg(args map[string]any, k string) int64 {
	return toInt64(args[k])
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func pathInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.IndexAny(rest, "./"); i >= 0 {
		rest = rest[:i]
	}
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}

func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v, ok := ctx.Config()[key]; ok && v != "" {
		return v
	}
	return def
}

func configInt(ctx *sdk.AppCtx, key string, def int) int {
	s := configString(ctx, key, "")
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func configFlag(ctx *sdk.AppCtx, key string, def bool) bool {
	s := strings.ToLower(configString(ctx, key, ""))
	switch s {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}

// ─── main ───────────────────────────────────────────────────────────

func main() { sdk.Run(&App{}) }
