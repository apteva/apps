package main

// Live screen streaming over WebSocket. The panel's DeviceFrame
// component opens stream_url; this handler validates the short-lived
// token, upgrades to WebSocket, spawns the platform stream source
// (android screenrecord / ios idb video-stream), reassembles H.264
// access units, and pushes one binary message per AU. Inbound text
// messages are input events routed to the platform input backend.
//
// Protocol (must match ui/components/DeviceFrame.tsx):
//   server→client text:   {"type":"meta","platform","width","height","codec":"h264"}
//   server→client binary:  one H.264 access unit (Annex-B) per message
//   client→server text:    {"type":"input","kind":"tap|swipe|key|text", ...}
//
// On-demand: the stream source is only spawned while a client is
// connected, and torn down on disconnect, so an idle booted sim
// doesn't burn CPU encoding frames nobody watches.

import (
	"bufio"
	"context"
	"encoding/json"
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

	// meta frame first so the client can size the canvas + configure
	// its decoder. Dimensions are best-effort; the decoder also reads
	// them from the in-band SPS, so 0s here are non-fatal.
	w, h := deviceDimensions(sim)
	_ = writeJSON(map[string]any{
		"type": "meta", "platform": sim.Platform,
		"width": w, "height": h, "codec": "h264",
	})

	// Spawn the platform stream source. Its stdout is a raw H.264
	// Annex-B stream we frame into access units.
	cmd, stdout, err := startStreamSource(ctx, sim)
	if err != nil {
		_ = writeJSON(map[string]any{"type": "error", "message": err.Error()})
		return
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

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

	// Writer: frame the source's stdout into access units and ship
	// each as one binary message.
	framer := newAnnexBFramer(func(au []byte, _ bool) {
		if err := writeBinary(au); err != nil {
			cancel()
		}
	})
	_ = framer.feed(bufio.NewReaderSize(stdout, 1<<20))
	// feed returns on EOF (source exited) or write failure. For
	// android screenrecord's time-limit exits we could loop+respawn
	// here; v0.1 ends the session and the client reconnects.
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
	switch sim.Platform {
	case "android":
		_ = androidSendInput(sim.Serial, ev)
	case "ios":
		_ = a.iosSendInput(sim.ID, ev)
	}
}

// startStreamSource spawns the platform-specific H.264 source and
// returns the running command + its stdout pipe.
func startStreamSource(ctx context.Context, sim *Sim) (*exec.Cmd, interface{ Read([]byte) (int, error) }, error) {
	switch sim.Platform {
	case "android":
		return startAndroidScreenStream(ctx, sim.Serial)
	case "ios":
		return startIOSVideoStream(ctx, sim.ID)
	}
	return nil, nil, errUnknownPlatform(sim.Platform)
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
