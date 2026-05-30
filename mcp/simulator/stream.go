package main

// Live screen streaming over WebSocket. The panel's DeviceFrame
// component opens stream_url; this handler validates the short-lived
// token, upgrades to WebSocket, spawns the platform stream source
// (android screenrecord / ios idb video-stream / ios simctl
// screenshot loop), and pushes one binary message per frame. Inbound
// text messages are input events routed to the platform input backend.
//
// Protocol (must match ui/components/DeviceFrame.tsx):
//   server→client text:   {"type":"meta","platform","width","height","codec":"h264|png"}
//   server→client binary:  one H.264 access unit or one PNG frame per message
//   client→server text:    {"type":"input","kind":"tap|swipe|key|text", ...}
//
// On-demand: the stream source is only spawned while a client is
// connected, and torn down on disconnect, so an idle booted sim
// doesn't burn CPU encoding frames nobody watches.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 1 << 16,
	// The platform proxies same-origin dashboard traffic; the ws_token
	// in the URL is the actual auth. Accept any Origin so the proxy
	// hop doesn't get rejected on header rewriting.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// handleStream is registered at /stream/<sim_id> (NoAuth — the
// ws_token query param is the bearer). Validates the token, resolves
// the sim, upgrades, and runs the duplex loop.
func (a *App) handleStream(w http.ResponseWriter, r *http.Request) {
	simID := strings.TrimPrefix(r.URL.Path, "/stream/")
	simID = strings.Trim(simID, "/")
	if simID == "" {
		http.Error(w, "sim_id required", http.StatusBadRequest)
		return
	}
	token := r.URL.Query().Get("t")
	if token == "" {
		http.Error(w, "stream token required", http.StatusUnauthorized)
		return
	}
	if a.appCtx == nil || a.appCtx.AppDB() == nil {
		http.Error(w, "app not ready", http.StatusServiceUnavailable)
		return
	}
	resolved, err := dbResolveStreamToken(a.appCtx.AppDB(), token)
	if err != nil || resolved != simID {
		http.Error(w, "invalid or expired stream token", http.StatusUnauthorized)
		return
	}
	sim, err := dbGetSim(a.appCtx.AppDB(), simID)
	if err != nil || sim == nil {
		http.Error(w, "sim not found", http.StatusNotFound)
		return
	}
	if sim.Status != "booted" {
		http.Error(w, "sim not booted", http.StatusConflict)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error
	}
	a.runStreamSession(sim, conn)
}

// runStreamSession owns one WebSocket's lifetime: spawn the stream
// source, fan its access units to the socket, and pump inbound input
// events. Returns when either side closes.
func (a *App) runStreamSession(sim *Sim, conn *websocket.Conn) {
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var writeMu sync.Mutex
	writeJSON := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(v)
	}
	writeBinary := func(b []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteMessage(websocket.BinaryMessage, b)
	}

	// Spawn the platform stream source. H.264 sources expose stdout;
	// screenshot sources push complete image frames directly.
	source, err := startStreamSource(ctx, sim)
	if err != nil {
		_ = writeJSON(map[string]any{"type": "error", "message": err.Error()})
		return
	}
	defer func() {
		if source.Cmd != nil && source.Cmd.Process != nil {
			_ = source.Cmd.Process.Kill()
		}
	}()

	// Meta frame first so the client can size the canvas + choose the
	// decode path. Dimensions are best-effort; for H.264 the decoder
	// also reads real dimensions from the in-band SPS.
	w, h := deviceDimensions(sim)
	_ = writeJSON(map[string]any{
		"type": "meta", "platform": sim.Platform,
		"width": w, "height": h, "codec": source.Codec,
	})

	// Reader goroutine: inbound input events. Also detects client
	// close, which cancels the context and tears down the source.
	go func() {
		defer cancel()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt != websocket.TextMessage {
				continue
			}
			a.handleInputMessage(sim, data)
		}
	}()

	if source.FrameLoop != nil {
		source.FrameLoop(ctx, func(frame []byte) error {
			if err := writeBinary(frame); err != nil {
				cancel()
				return err
			}
			return nil
		})
		return
	}

	// Writer: frame raw H.264 stdout into access units and ship each as
	// one binary message.
	framer := newAnnexBFramer(func(au []byte, _ bool) {
		if err := writeBinary(au); err != nil {
			cancel()
		}
	})
	_ = framer.feed(bufio.NewReaderSize(source.Stdout, 1<<20))
	// feed returns on EOF (source exited) or write failure. For android
	// screenrecord's time-limit exits we could loop+respawn here; v0.1
	// ends the session and the client reconnects.
}

// handleInputMessage parses an inbound control message and routes it
// to the platform input backend. Malformed messages are ignored.
func (a *App) handleInputMessage(sim *Sim, data []byte) {
	var msg struct {
		Type string  `json:"type"`
		Kind string  `json:"kind"`
		X    float64 `json:"x"`
		Y    float64 `json:"y"`
		X2   float64 `json:"x2"`
		Y2   float64 `json:"y2"`
		MS   int     `json:"ms"`
		Key  string  `json:"key"`
		Text string  `json:"text"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || msg.Type != "input" {
		return
	}
	ev := inputEvent{
		Kind: msg.Kind, X: msg.X, Y: msg.Y, X2: msg.X2, Y2: msg.Y2,
		DurationMS: msg.MS, Key: msg.Key, Text: msg.Text,
	}
	_ = a.sendInput(sim, ev)
}

func (a *App) sendInput(sim *Sim, ev inputEvent) error {
	if sim == nil {
		return errNotFound
	}
	switch sim.Platform {
	case "android":
		return androidSendInput(sim.Serial, ev)
	case "ios":
		return a.iosSendInput(sim.ID, ev)
	}
	return errUnknownPlatform(sim.Platform)
}

type streamSource struct {
	Cmd       *exec.Cmd
	Stdout    io.Reader
	Codec     string
	FrameLoop func(context.Context, func([]byte) error)
}

// startStreamSource spawns or constructs the platform-specific source.
// H.264 sources return Cmd+Stdout; screenshot sources return FrameLoop.
func startStreamSource(ctx context.Context, sim *Sim) (*streamSource, error) {
	switch sim.Platform {
	case "android":
		cmd, stdout, err := startAndroidScreenStream(ctx, sim.Serial)
		if err != nil {
			return nil, err
		}
		return &streamSource{Cmd: cmd, Stdout: stdout, Codec: "h264"}, nil
	case "ios":
		return startIOSVideoStream(ctx, sim.ID)
	}
	return nil, errUnknownPlatform(sim.Platform)
}

// deviceDimensions returns best-effort pixel dimensions for the canvas
// hint. v0.1 returns 0,0 — the client reads real dimensions from the
// decoded frame. Kept as a seam so a later version can query
// `adb shell wm size` / simctl without touching the protocol.
func deviceDimensions(sim *Sim) (int, int) {
	_ = sim
	return 0, 0
}

func errUnknownPlatform(p string) error {
	return &simError{"unknown platform: " + p}
}

type simError struct{ msg string }

func (e *simError) Error() string { return e.msg }
