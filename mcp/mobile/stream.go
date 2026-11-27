package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Live video: scrcpy-server on the phone encodes H.264; Mobile reads its
// video socket and fans the packets out to WebSocket viewers. Each binary
// message is scrcpy's 12-byte header plus payload, unchanged:
//   - bit 63 of the first 8 bytes set: session meta (flags, width, height)
//   - otherwise: pts with bit 62 = config packet, bit 61 = key frame; then size
// Viewers send JSON input (touch, key, text) on the same socket.

const (
	streamTokenTTL    = 5 * time.Minute
	defaultMaxSize    = 1280
	defaultBitRate    = 4000
	viewerQueue       = 96
	ctrlInjectKeycode = 0
	ctrlInjectText    = 1
	ctrlInjectTouch   = 2
	ctrlResetVideo    = 17
	flagSession       = uint64(1) << 63
	flagConfig        = uint64(1) << 62
	flagKeyFrame      = uint64(1) << 61
)

type streamOptions struct {
	MaxSize     int  `json:"max_size"`
	BitRateKbps int  `json:"bit_rate_kbps"`
	ShowTouches bool `json:"show_touches"`
}

type streamToken struct {
	sid, pid string
	expires  time.Time
}

type streamHub struct {
	e       *engine
	mu      sync.Mutex
	streams map[string]*liveStream // by session
	tokens  map[string]*streamToken
}

func newStreamHub(e *engine) *streamHub {
	h := &streamHub{e: e, streams: map[string]*liveStream{}, tokens: map[string]*streamToken{}}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-e.ctx.Done():
				h.mu.Lock()
				all := []*liveStream{}
				for _, st := range h.streams {
					all = append(all, st)
				}
				h.mu.Unlock()
				for _, st := range all {
					st.end("runner shutting down")
				}
				return
			case <-t.C:
				h.expireTokens()
			}
		}
	}()
	return h
}

func (h *streamHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.streams)
}

// issue returns a short-lived token for a session's live video, renewing an
// existing token when one is passed.
func (h *streamHub) issue(s Session, args map[string]any) (map[string]any, error) {
	if s.Device.Platform != "android" || s.Device.Driver != "adb" {
		return nil, problem("unsupported", "live video needs an Android phone on the ADB driver; use mode=poll for other devices")
	}
	opts := streamOptions{MaxSize: integer(args, "max_size", defaultMaxSize), BitRateKbps: integer(args, "bit_rate_kbps", defaultBitRate), ShowTouches: boolean(args, "show_touches")}
	if opts.MaxSize < 320 || opts.MaxSize > 4096 || opts.BitRateKbps < 250 || opts.BitRateKbps > 50000 {
		return nil, problem("invalid_argument", "max_size must be 320..4096 and bit_rate_kbps 250..50000")
	}
	if _, _, err := scrcpyServer(context.Background()); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if raw := str(args, "token"); raw != "" {
		t := h.tokens[raw]
		if t == nil || t.sid != s.ID || t.pid != s.ProjectID {
			return nil, problem("not_found", "stream token not found or expired")
		}
		t.expires = time.Now().Add(streamTokenTTL)
		return streamTicket(s.ID, raw, t.expires), nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(b)
	exp := time.Now().Add(streamTokenTTL)
	h.tokens[token] = &streamToken{sid: s.ID, pid: s.ProjectID, expires: exp}
	if st := h.streams[s.ID]; st != nil {
		st.setOptions(opts)
	} else {
		h.streams[s.ID] = newLiveStream(h, s, opts)
	}
	return streamTicket(s.ID, token, exp), nil
}

func streamTicket(sid, token string, exp time.Time) map[string]any {
	return map[string]any{"session_id": sid, "mode": "video", "token": token, "expires_at": exp.UTC().Format(time.RFC3339), "valid_seconds": int(time.Until(exp).Seconds()), "codec": "h264", "framing": "scrcpy-12-byte-header"}
}

func (h *streamHub) expireTokens() {
	h.mu.Lock()
	now := time.Now()
	expired := map[string]bool{}
	for k, t := range h.tokens {
		if now.After(t.expires) {
			expired[k] = true
			delete(h.tokens, k)
		}
	}
	streams := []*liveStream{}
	for _, st := range h.streams {
		streams = append(streams, st)
	}
	h.mu.Unlock()
	for _, st := range streams {
		st.dropViewers(func(v *viewer) bool { return expired[v.token] }, "stream link expired")
	}
}

// end stops a session's stream for good (session stopped, closed, expired).
func (h *streamHub) end(sid, reason string) {
	h.mu.Lock()
	st := h.streams[sid]
	delete(h.streams, sid)
	for k, t := range h.tokens {
		if t.sid == sid {
			delete(h.tokens, k)
		}
	}
	h.mu.Unlock()
	if st != nil {
		st.end(reason)
	}
}

// suspend stops the phone side but keeps viewers connected (phone offline).
func (h *streamHub) suspend(sid string) {
	h.mu.Lock()
	st := h.streams[sid]
	h.mu.Unlock()
	if st != nil {
		st.stopDevice()
	}
}

// deviceBack restarts a suspended stream; viewers get fresh config and a key frame.
func (h *streamHub) deviceBack(sid string) {
	h.mu.Lock()
	st := h.streams[sid]
	h.mu.Unlock()
	if st != nil && st.viewerCount() > 0 {
		go st.ensureRunning()
	}
}

func (h *streamHub) stream(token, sid, pid string) (*liveStream, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.tokens[token]
	if t == nil || t.sid != sid || t.pid != pid || time.Now().After(t.expires) {
		return nil, problem("forbidden", "stream token is invalid or expired")
	}
	st := h.streams[sid]
	if st == nil {
		return nil, problem("session_inactive", "the stream has ended")
	}
	return st, nil
}

var upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 64 << 10, CheckOrigin: sameOrigin}

// serveViewer upgrades a viewer connection for session sid.
func (h *streamHub) serveViewer(w http.ResponseWriter, r *http.Request, pid, sid, token string) {
	st, err := h.stream(token, sid, pid)
	if err != nil {
		writeFailure(w, err)
		return
	}
	s, err := h.e.session(pid, sid)
	if err != nil || (s.Status != "active" && s.Status != "paused") {
		writeFailure(w, problem("session_inactive", "session is not active"))
		return
	}
	if st.viewerCount() >= h.e.drivers.maxViewers() {
		writeFailure(w, problem("device_unavailable", "this session already has the maximum number of stream viewers"))
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	v := &viewer{conn: conn, token: token, out: make(chan wsMessage, viewerQueue), closed: make(chan struct{}), needKey: true}
	st.addViewer(v)
	go v.writeLoop()
	go st.ensureRunning()
	v.readLoop(st)
	st.removeViewer(v, "")
}

func (d *drivers) maxViewers() int {
	if d.cfg.StreamViewers > 0 {
		return d.cfg.StreamViewers
	}
	return 4
}

type wsMessage struct {
	kind int
	data []byte
}

type viewer struct {
	conn      *websocket.Conn
	token     string
	out       chan wsMessage
	closed    chan struct{}
	closeOnce sync.Once
	needKey   bool // skipping until the next key frame
}

func (v *viewer) send(m wsMessage) bool {
	select {
	case <-v.closed:
		return false
	case v.out <- m:
		return true
	default:
		return false
	}
}

func (v *viewer) close(reason string) {
	v.closeOnce.Do(func() {
		_ = v.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason), time.Now().Add(time.Second))
		close(v.closed)
		_ = v.conn.Close()
	})
}

func (v *viewer) writeLoop() {
	for {
		select {
		case <-v.closed:
			return
		case m := <-v.out:
			_ = v.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := v.conn.WriteMessage(m.kind, m.data); err != nil {
				v.close("")
				return
			}
		}
	}
}

// readLoop reads viewer input until the connection closes.
func (v *viewer) readLoop(st *liveStream) {
	v.conn.SetReadLimit(64 << 10)
	for {
		kind, data, err := v.conn.ReadMessage()
		if err != nil {
			v.close("")
			return
		}
		if kind != websocket.TextMessage {
			continue
		}
		var in streamInput
		if json.Unmarshal(data, &in) != nil {
			continue
		}
		if err := st.input(in); err != nil {
			msg, _ := json.Marshal(map[string]any{"type": "error", "code": errorCode(err), "error": err.Error()})
			v.send(wsMessage{websocket.TextMessage, msg})
		}
	}
}

type streamInput struct {
	Type    string  `json:"type"`   // touch | key | text | keyframe
	Action  string  `json:"action"` // down | move | up (touch)
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Pointer int     `json:"pointer"`
	Key     string  `json:"key"`
	Keycode *int    `json:"keycode"`
	Text    string  `json:"text"`
}

type liveStream struct {
	hub *streamHub
	s   Session

	mu       sync.Mutex
	opts     streamOptions
	viewers  map[*viewer]bool
	running  bool
	starting bool
	width    int
	height   int
	session  []byte // last session-meta header
	config   []byte // last config packet with header
	ctrl     net.Conn
	ctrlMu   sync.Mutex
	proc     *exec.Cmd
	port     int
	cancel   context.CancelFunc
	gen      int
	ended    bool
	gesture  *gestureState
}

type gestureState struct {
	started time.Time
	points  int
	x, y    int
	pointer int
}

func newLiveStream(h *streamHub, s Session, opts streamOptions) *liveStream {
	return &liveStream{hub: h, s: s, opts: opts, viewers: map[*viewer]bool{}}
}

func (st *liveStream) setOptions(opts streamOptions) {
	st.mu.Lock()
	changed := st.opts != opts
	st.opts = opts
	running := st.running
	st.mu.Unlock()
	if changed && running {
		st.stopDevice()
		go st.ensureRunning()
	}
}

func (st *liveStream) viewerCount() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.viewers)
}

func (st *liveStream) addViewer(v *viewer) {
	st.mu.Lock()
	st.viewers[v] = true
	hello, _ := json.Marshal(map[string]any{"type": "hello", "codec": "h264", "width": st.width, "height": st.height, "session_id": st.s.ID})
	v.send(wsMessage{websocket.TextMessage, hello})
	if st.session != nil {
		v.send(wsMessage{websocket.BinaryMessage, st.session})
	}
	if st.config != nil {
		v.send(wsMessage{websocket.BinaryMessage, st.config})
	}
	running := st.running
	st.mu.Unlock()
	if running {
		st.requestKeyFrame()
	}
}

func (st *liveStream) removeViewer(v *viewer, reason string) {
	v.close(reason)
	st.mu.Lock()
	delete(st.viewers, v)
	left := len(st.viewers)
	st.mu.Unlock()
	if left == 0 {
		st.releaseTouch()
	}
}

func (st *liveStream) dropViewers(match func(*viewer) bool, reason string) {
	st.mu.Lock()
	drop := []*viewer{}
	for v := range st.viewers {
		if match(v) {
			drop = append(drop, v)
		}
	}
	st.mu.Unlock()
	for _, v := range drop {
		st.removeViewer(v, reason)
	}
}

func (st *liveStream) broadcast(msg []byte, config, key bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for v := range st.viewers {
		if v.needKey && !config && !key {
			continue
		}
		if !v.send(wsMessage{websocket.BinaryMessage, msg}) {
			// A slow viewer skips frames until the next key frame.
			v.needKey = true
			continue
		}
		if key {
			v.needKey = false
		}
	}
}

// ensureRunning starts scrcpy-server on the phone if it is not running.
func (st *liveStream) ensureRunning() {
	st.mu.Lock()
	if st.running || st.starting || st.ended || len(st.viewers) == 0 {
		st.mu.Unlock()
		return
	}
	st.starting = true
	st.gen++
	gen := st.gen
	opts := st.opts
	st.mu.Unlock()
	err := st.start(gen, opts)
	st.mu.Lock()
	st.starting = false
	st.mu.Unlock()
	if err != nil {
		msg, _ := json.Marshal(map[string]any{"type": "error", "code": errorCode(err), "error": err.Error()})
		st.mu.Lock()
		for v := range st.viewers {
			v.send(wsMessage{websocket.TextMessage, msg})
		}
		st.mu.Unlock()
	}
}

var scrcpyVersionRe = regexp.MustCompile(`scrcpy (\d+\.\d+(?:\.\d+)?)`)

// scrcpyServer finds the server jar shipped with the runner's scrcpy and its
// version (the server refuses a mismatched version argument).
// scrcpyServer finds scrcpy-server and scrcpy's version; the answer is
// cached for a minute because device listings ask on every call.
var scrcpyCache struct {
	sync.Mutex
	at        time.Time
	path, ver string
	err       error
}

func scrcpyServer(ctx context.Context) (string, string, error) {
	scrcpyCache.Lock()
	defer scrcpyCache.Unlock()
	if time.Since(scrcpyCache.at) < time.Minute {
		return scrcpyCache.path, scrcpyCache.ver, scrcpyCache.err
	}
	path, ver, err := findScrcpyServer(ctx)
	scrcpyCache.at, scrcpyCache.path, scrcpyCache.ver, scrcpyCache.err = time.Now(), path, ver, err
	return path, ver, err
}

func findScrcpyServer(ctx context.Context) (string, string, error) {
	bin, err := lookTool("scrcpy")
	if err != nil {
		return "", "", problem("setup_required", "live video needs scrcpy 2.1+ installed on the runner")
	}
	out, err := runCommand(ctx, bin, "--version")
	m := scrcpyVersionRe.FindStringSubmatch(string(out))
	if err != nil || m == nil {
		return "", "", problem("setup_required", "cannot read the scrcpy version")
	}
	candidates := []string{os.Getenv("SCRCPY_SERVER_PATH")}
	for _, b := range []string{bin, resolved(bin)} {
		dir := filepath.Dir(b)
		candidates = append(candidates, filepath.Join(dir, "..", "share", "scrcpy", "scrcpy-server"), filepath.Join(dir, "scrcpy-server"))
	}
	candidates = append(candidates, "/opt/homebrew/share/scrcpy/scrcpy-server", "/usr/local/share/scrcpy/scrcpy-server", "/usr/share/scrcpy/scrcpy-server")
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && info.Mode().IsRegular() {
			return c, m[1], nil
		}
	}
	return "", "", problem("setup_required", "scrcpy-server was not found next to scrcpy; set SCRCPY_SERVER_PATH on the runner")
}

func resolved(p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}

func (st *liveStream) start(gen int, opts streamOptions) error {
	d := st.hub.e.drivers
	serial := st.s.Device.UDID
	ctx, cancel := context.WithCancel(st.hub.e.ctx)
	setup, setupCancel := context.WithTimeout(ctx, 20*time.Second)
	defer setupCancel()
	fail := func(err error) error {
		cancel()
		return err
	}
	server, version, err := scrcpyServer(setup)
	if err != nil {
		return fail(err)
	}
	remote := "/data/local/tmp/apteva-scrcpy-server-" + version + ".jar"
	if _, err = d.adb(setup, serial, "push", server, remote); err != nil {
		return fail(problem("device_unavailable", "could not copy scrcpy-server to the phone"))
	}
	var scid [4]byte
	_, _ = rand.Read(scid[:])
	id := binary.BigEndian.Uint32(scid[:]) & 0x7fffffff
	out, err := d.adb(setup, serial, "forward", "tcp:0", fmt.Sprintf("localabstract:scrcpy_%08x", id))
	if err != nil {
		return fail(problem("device_unavailable", "adb forward failed"))
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return fail(problem("device_unavailable", "adb forward returned no port"))
	}
	showTouches := "false"
	if opts.ShowTouches {
		showTouches = "true"
	}
	// cleanup=true makes the server restore stay-awake and show-touches to
	// their previous values when it exits, even if the phone is unplugged.
	cmd := exec.CommandContext(ctx, "adb", "-s", serial, "shell", "CLASSPATH="+remote, "app_process", "/", "com.genymobile.scrcpy.Server", version,
		fmt.Sprintf("scid=%08x", id), "log_level=warn", "audio=false", "control=true", "tunnel_forward=true", "cleanup=true",
		"stay_awake=true", "show_touches="+showTouches, "send_device_meta=false",
		"max_size="+strconv.Itoa(opts.MaxSize), "video_bit_rate="+strconv.Itoa(opts.BitRateKbps*1000))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err = cmd.Start(); err != nil {
		_, _ = d.adb(context.Background(), serial, "forward", "--remove", "tcp:"+strconv.Itoa(port))
		return fail(err)
	}
	cleanup := func() {
		cancel()
		_ = cmd.Wait()
		rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = d.adb(rctx, serial, "forward", "--remove", "tcp:"+strconv.Itoa(port))
		rcancel()
	}
	video, err := dialScrcpy(setup, port, true)
	if err != nil {
		cleanup()
		return problem("device_unavailable", "the phone's video server did not start; unlock the phone and retry")
	}
	ctrl, err := dialScrcpy(setup, port, false)
	if err != nil {
		video.Close()
		cleanup()
		return problem("device_unavailable", "the phone's control channel did not open")
	}
	var codec [4]byte
	_ = video.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err = io.ReadFull(video, codec[:]); err != nil || binary.BigEndian.Uint32(codec[:]) <= 1 {
		video.Close()
		ctrl.Close()
		cleanup()
		return problem("device_unavailable", "the phone could not start its video encoder")
	}
	_ = video.SetReadDeadline(time.Time{})
	st.mu.Lock()
	if st.ended || st.gen != gen {
		st.mu.Unlock()
		video.Close()
		ctrl.Close()
		cleanup()
		return nil
	}
	st.running, st.ctrl, st.proc, st.port = true, ctrl, cmd, port
	st.cancel = func() {
		video.Close()
		ctrl.Close()
		cleanup()
	}
	st.config, st.session = nil, nil
	for v := range st.viewers {
		v.needKey = true
	}
	st.mu.Unlock()
	go st.readVideo(gen, video)
	return nil
}

// dialScrcpy connects to the forwarded socket. With tunnel_forward the first
// (video) socket receives one dummy byte once the server is listening.
func dialScrcpy(ctx context.Context, port int, first bool) (net.Conn, error) {
	addr := "127.0.0.1:" + strconv.Itoa(port)
	for {
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			if !first {
				return conn, nil
			}
			var b [1]byte
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, err = io.ReadFull(conn, b[:]); err == nil {
				_ = conn.SetReadDeadline(time.Time{})
				return conn, nil
			}
			conn.Close()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func (st *liveStream) readVideo(gen int, video net.Conn) {
	header := make([]byte, 12)
	for {
		if _, err := io.ReadFull(video, header); err != nil {
			break
		}
		word := binary.BigEndian.Uint64(header[:8])
		if word&flagSession != 0 {
			msg := append([]byte(nil), header...)
			st.mu.Lock()
			st.width = int(binary.BigEndian.Uint32(header[4:8]))
			st.height = int(binary.BigEndian.Uint32(header[8:12]))
			st.session = msg
			st.mu.Unlock()
			st.broadcast(msg, true, false)
			continue
		}
		size := binary.BigEndian.Uint32(header[8:12])
		if size > 16<<20 {
			break
		}
		msg := make([]byte, 12+int(size))
		copy(msg, header)
		if _, err := io.ReadFull(video, msg[12:]); err != nil {
			break
		}
		config := word&flagConfig != 0
		key := word&flagKeyFrame != 0
		if config {
			st.mu.Lock()
			st.config = msg
			st.mu.Unlock()
		}
		st.broadcast(msg, config, key)
	}
	// The phone side ended (unplugged, killed, or stopped on purpose).
	st.mu.Lock()
	current := st.gen == gen && st.running
	st.mu.Unlock()
	if current {
		st.stopDevice()
	}
}

// requestKeyFrame asks the encoder to restart so a joining viewer can decode.
func (st *liveStream) requestKeyFrame() {
	_ = st.sendControl([]byte{ctrlResetVideo})
}

func (st *liveStream) sendControl(msg []byte) error {
	st.mu.Lock()
	ctrl := st.ctrl
	st.mu.Unlock()
	if ctrl == nil {
		return problem("device_unavailable", "the stream is not connected to the phone")
	}
	st.ctrlMu.Lock()
	defer st.ctrlMu.Unlock()
	_ = ctrl.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := ctrl.Write(msg)
	return err
}

// stopDevice ends the phone side; viewers stay connected.
func (st *liveStream) stopDevice() {
	st.releaseTouch()
	st.mu.Lock()
	cancel := st.cancel
	st.cancel, st.ctrl, st.running = nil, nil, false
	st.gen++
	st.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (st *liveStream) end(reason string) {
	st.mu.Lock()
	st.ended = true
	st.mu.Unlock()
	st.stopDevice()
	st.dropViewers(func(*viewer) bool { return true }, "stream ended: "+reason)
}

// releaseTouch lifts a finger a viewer left down.
func (st *liveStream) releaseTouch() {
	st.mu.Lock()
	g := st.gesture
	st.gesture = nil
	st.mu.Unlock()
	if g != nil {
		_ = st.sendControl(st.touchMessage(1, g.pointer, g.x, g.y))
		st.recordGesture(g, "released")
	}
}

func (st *liveStream) touchMessage(action byte, pointer, x, y int) []byte {
	st.mu.Lock()
	w, h := st.width, st.height
	st.mu.Unlock()
	m := make([]byte, 32)
	m[0] = ctrlInjectTouch
	m[1] = action
	binary.BigEndian.PutUint64(m[2:10], uint64(int64(pointer)))
	binary.BigEndian.PutUint32(m[10:14], uint32(int32(x)))
	binary.BigEndian.PutUint32(m[14:18], uint32(int32(y)))
	binary.BigEndian.PutUint16(m[18:20], uint16(w))
	binary.BigEndian.PutUint16(m[20:22], uint16(h))
	if action != 1 {
		binary.BigEndian.PutUint16(m[22:24], 0xffff)
	}
	return m
}

// input applies viewer input with the same rules as mobile_action.
func (st *liveStream) input(in streamInput) error {
	e := st.hub.e
	if e.ops.isStopping(st.s.ID) {
		return problem("session_inactive", "session is stopping")
	}
	if s, err := e.session(st.s.ProjectID, st.s.ID); err != nil || s.Status != "active" {
		return problem("session_inactive", "session is not active")
	}
	switch in.Type {
	case "keyframe":
		st.requestKeyFrame()
		return nil
	case "touch":
		st.mu.Lock()
		w, h := st.width, st.height
		st.mu.Unlock()
		if w <= 0 || h <= 0 {
			return problem("invalid_state", "no video yet")
		}
		if in.X < 0 || in.X > 1 || in.Y < 0 || in.Y > 1 || in.Pointer < 0 || in.Pointer > 9 {
			return problem("invalid_argument", "touch needs x, y between 0 and 1 and pointer 0..9")
		}
		x, y := int(in.X*float64(w-1)), int(in.Y*float64(h-1))
		var action byte
		switch in.Action {
		case "down":
			action = 0
			st.mu.Lock()
			st.gesture = &gestureState{started: time.Now(), points: 1, x: x, y: y, pointer: in.Pointer}
			st.mu.Unlock()
		case "move":
			action = 2
			st.mu.Lock()
			if st.gesture == nil {
				st.mu.Unlock()
				return nil
			}
			st.gesture.points++
			st.gesture.x, st.gesture.y = x, y
			st.mu.Unlock()
		case "up":
			action = 1
			st.mu.Lock()
			g := st.gesture
			st.gesture = nil
			st.mu.Unlock()
			if g != nil {
				defer st.recordGesture(g, "ok")
			}
		default:
			return problem("invalid_argument", "touch action must be down, move or up")
		}
		return st.sendControl(st.touchMessage(action, in.Pointer, x, y))
	case "key":
		args := map[string]any{"key": in.Key}
		if in.Keycode != nil {
			args = map[string]any{"keycode": float64(*in.Keycode)}
		}
		code, err := e.drivers.androidKeycode(args)
		if err != nil {
			return err
		}
		for _, action := range []byte{0, 1} {
			m := make([]byte, 14)
			m[0], m[1] = ctrlInjectKeycode, action
			binary.BigEndian.PutUint32(m[2:6], uint32(code))
			if err := st.sendControl(m); err != nil {
				return err
			}
		}
		st.recordInput("key", map[string]any{"keycode": code})
		return nil
	case "text":
		if in.Text == "" || len(in.Text) > 300 {
			return problem("invalid_argument", "text must be 1..300 bytes")
		}
		m := make([]byte, 5+len(in.Text))
		m[0] = ctrlInjectText
		binary.BigEndian.PutUint32(m[1:5], uint32(len(in.Text)))
		copy(m[5:], in.Text)
		if err := st.sendControl(m); err != nil {
			return err
		}
		st.recordInput("text", map[string]any{"text_length": len([]rune(in.Text))})
		return nil
	}
	return problem("invalid_argument", "input type must be touch, key, text or keyframe")
}

// One event per gesture, not per movement.
func (st *liveStream) recordGesture(g *gestureState, result string) {
	st.hub.e.events.add(st.s.ProjectID, "mobile.action.performed", map[string]any{
		"session_id": st.s.ID, "device_id": st.s.DeviceID, "caller_ref": st.s.CallerRef,
		"tool": "mobile_stream", "action": "gesture", "args": map[string]any{"points": g.points},
		"result": result, "duration_ms": time.Since(g.started).Milliseconds(),
	})
}

func (st *liveStream) recordInput(action string, args map[string]any) {
	st.hub.e.events.add(st.s.ProjectID, "mobile.action.performed", map[string]any{
		"session_id": st.s.ID, "device_id": st.s.DeviceID, "caller_ref": st.s.CallerRef,
		"tool": "mobile_stream", "action": action, "args": args, "result": "ok", "duration_ms": 0,
	})
}

// --- sidecar side --------------------------------------------------------------

// handleStream serves a viewer from the local engine, or relays it to the
// remote runner, which owns the phone and the stream.
func (a *App) handleStream(w http.ResponseWriter, r *http.Request) {
	if err := a.requestProject(r); err != nil {
		writeFailure(w, err)
		return
	}
	sid := strings.TrimPrefix(r.URL.Path, "/api/stream/")
	token := r.URL.Query().Get("token")
	if a.local != nil {
		a.local.streams.serveViewer(w, r, a.project, sid, token)
		return
	}
	rem, ok := a.executor.(*remote)
	if !ok {
		writeFailure(w, problem("unsupported", "live video is unavailable"))
		return
	}
	if !sameOrigin(r) {
		writeFailure(w, problem("forbidden", "cross-origin requests are not allowed"))
		return
	}
	target := strings.Replace(strings.Replace(rem.base, "https://", "wss://", 1), "http://", "ws://", 1) +
		"/v1/stream/" + sid + "?token=" + url.QueryEscape(token) + "&project_id=" + url.QueryEscape(a.project)
	header := http.Header{"Authorization": {"Bearer " + rem.token}}
	upstream, res, err := (&websocket.Dialer{HandshakeTimeout: 30 * time.Second}).DialContext(r.Context(), target, header)
	if err != nil {
		if res != nil && res.StatusCode >= 400 {
			var failure apiError
			if json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&failure) == nil && failure.Code != "" {
				writeFailure(w, &failure)
				return
			}
		}
		writeFailure(w, problem("infrastructure_error", "Mobile runner stream is unreachable"))
		return
	}
	defer upstream.Close()
	client, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer client.Close()
	done := make(chan struct{}, 2)
	pump := func(dst, src *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			kind, data, err := src.ReadMessage()
			if err != nil {
				_ = dst.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
				return
			}
			if err = dst.WriteMessage(kind, data); err != nil {
				return
			}
		}
	}
	go pump(client, upstream)
	go pump(upstream, client)
	<-done
}
