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
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

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

	upgrader := websocket.Upgrader{
		ReadBufferSize: 4096, WriteBufferSize: 1 << 16,
		CheckOrigin: a.streamOriginAllowed,
	}
	conn, err := upgrader.Upgrade(w, r, nil)
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
	conn.SetReadLimit(64 << 10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endSession := a.beginStream(sim.ID, cancel)
	defer endSession()

	var writeMu sync.Mutex
	writeJSON := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
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

	a.runH264StreamLoop(ctx, sim, source, writeBinary, cancel)
}

// runH264StreamLoop reaps every adb screenrecord child and immediately starts
// a replacement after Android's hard 180-second limit. The WebSocket and token
// remain stable, so both the Simulator and Code panels keep working without a
// coordinated client reconnect.
func (a *App) runH264StreamLoop(ctx context.Context, sim *Sim, source *streamSource, write func([]byte) error, cancel context.CancelFunc) {
	for source != nil {
		framer := newAnnexBFramer(func(au []byte, _ bool) {
			if err := write(au); err != nil {
				cancel()
			}
		})
		_ = framer.feed(bufio.NewReaderSize(source.Stdout, 1<<20))
		if source.Cmd != nil {
			// Wait is mandatory after Start even when CommandContext killed
			// the child; otherwise each reconnect leaves process resources.
			if source.Cmd.Process != nil {
				_ = source.Cmd.Process.Kill()
			}
			_ = source.Cmd.Wait()
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
		next, err := startStreamSource(ctx, sim)
		if err != nil {
			cancel()
			return
		}
		source = next
	}
}

// handleInputMessage parses an inbound control message and routes it
// to the platform input backend. Malformed messages are ignored.
func (a *App) handleInputMessage(sim *Sim, data []byte) {
	if len(data) > 64<<10 {
		return
	}
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
	if validateInputEvent(ev) != nil {
		return
	}
	_ = a.sendInput(sim, ev)
}

func (a *App) sendInput(sim *Sim, ev inputEvent) error {
	if sim == nil {
		return errNotFound
	}
	if err := validateInputEvent(ev); err != nil {
		return err
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

func (a *App) beginStream(simID string, cancel context.CancelFunc) func() {
	id := randHex(12)
	a.streamMu.Lock()
	previous := a.streams[simID]
	if a.streams == nil {
		a.streams = make(map[string]activeStream)
	}
	a.streams[simID] = activeStream{id: id, cancel: cancel}
	a.streamMu.Unlock()
	if previous.cancel != nil {
		previous.cancel()
	}
	return func() {
		a.streamMu.Lock()
		if current, ok := a.streams[simID]; ok && current.id == id {
			delete(a.streams, simID)
		}
		a.streamMu.Unlock()
	}
}

func (a *App) stopStream(simID string) {
	a.streamMu.Lock()
	stream := a.streams[simID]
	delete(a.streams, simID)
	a.streamMu.Unlock()
	if stream.cancel != nil {
		stream.cancel()
	}
}

func (a *App) stopAllStreams() {
	a.streamMu.Lock()
	streams := a.streams
	a.streams = make(map[string]activeStream)
	a.streamMu.Unlock()
	for _, stream := range streams {
		if stream.cancel != nil {
			stream.cancel()
		}
	}
}

func (a *App) streamOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true // non-browser MCP/client
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	originHost := strings.ToLower(u.Host)
	allowed := []string{r.Host, r.Header.Get("X-Forwarded-Host")}
	if a != nil && a.appCtx != nil {
		if info, err := a.appCtx.PlatformInfo(); err == nil && info != nil && info.PublicURL != "" {
			if public, err := url.Parse(info.PublicURL); err == nil {
				allowed = append(allowed, public.Host)
			}
		}
	}
	for _, host := range allowed {
		if strings.EqualFold(originHost, strings.TrimSpace(host)) {
			return true
		}
	}
	return false
}

type simError struct{ msg string }

func (e *simError) Error() string { return e.msg }
