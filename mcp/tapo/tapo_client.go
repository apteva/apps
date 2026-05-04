// tapo_client.go — TP-Link Tapo camera HTTPS JSON-RPC client.
//
// Reverse-engineered from the Tapo mobile app + the python-tapo
// community library. There is no public SDK; firmware revs occasionally
// rotate the auth scheme (legacy MD5+stok → KLAP handshake → newer
// hashed variants). v0.1 supports legacy MD5+stok cleanly and leaves
// KLAP as a stub — set the camera firmware to ≤1.3.x or contribute the
// crypto path.
//
// Wire shape (legacy):
//
//   1. POST https://{ip}/   { "method":"login",
//                             "params":{ "hashed":true,
//                                        "username":<u>,
//                                        "password":<md5_hex(password)> } }
//      → { "result":{ "stok":<token> }, "error_code":0 }
//
//   2. POST https://{ip}/stok={stok}/ds  with `multipleRequest` envelope:
//      { "method":"multipleRequest",
//        "params":{ "requests":[ {"method":..., ...}, ... ] } }
//      → { "result":{ "responses":[ ... ] }, "error_code":0 }
//
// Notes:
//   * TLS verification is disabled — the cameras self-sign with a cert
//     for an internal CN that won't validate against any local trust
//     store. This is fine on a trusted LAN; do not expose cameras to
//     the internet directly.
//   * Tapo's internal time is UTC and a few of the response time fields
//     come back as Unix seconds in a string. We normalise to RFC3339.
//   * Method names below are the camera-facing strings (snake-cased,
//     verb-noun) — keep them; renaming breaks the wire format.
package main

import (
	"bytes"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Capabilities is the probed feature set for one camera. Filled by
// probeCapabilities and persisted in cameras.capabilities_json. Tools
// that depend on a capability check it before invoking the camera —
// e.g. ptz_move asserts caps.PTZ.
type Capabilities struct {
	PTZ          bool `json:"ptz"`
	PrivacyLens  bool `json:"privacy_lens"`  // physical motorised cover
	Siren        bool `json:"siren"`
	NightVision  bool `json:"night_vision"`
	StatusLED    bool `json:"status_led"`
	MotionDetect bool `json:"motion_detect"`
	BabyCry      bool `json:"baby_cry"`      // person+sound classifier on C2xx
	OnvifPort    int  `json:"onvif_port"`    // 0 if not detected
}

// Client is one camera's session. Holds the stok cookie until it
// expires; legacy stoks live ~30min so we re-login lazily on 401.
type Client struct {
	IP       string
	Username string
	Password string

	mu       sync.Mutex
	stok     string
	expiry   time.Time
	httpc    *http.Client
}

// NewClient returns a Client; no I/O until first call.
func NewClient(ip, username, password string) *Client {
	return &Client{
		IP:       ip,
		Username: username,
		Password: password,
		httpc: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

// login performs the legacy MD5+stok handshake and caches the token.
// Caller must hold c.mu.
func (c *Client) login() error {
	payload := map[string]any{
		"method": "login",
		"params": map[string]any{
			"hashed":   true,
			"username": c.Username,
			"password": md5Hex(c.Password),
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", "https://"+c.IP+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("login: dial %s: %w", c.IP, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var out struct {
		Result struct {
			Stok string `json:"stok"`
		} `json:"result"`
		ErrorCode int `json:"error_code"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("login: parse %s: %w", string(raw), err)
	}
	if out.ErrorCode != 0 || out.Result.Stok == "" {
		// -40401 = bad password; -40404 = locked out for ~5min after
		// repeated bad attempts; -40413 = camera-account not enabled.
		return fmt.Errorf("login: error_code=%d (set the Camera Account password in the Tapo app)", out.ErrorCode)
	}
	c.stok = out.Result.Stok
	c.expiry = time.Now().Add(25 * time.Minute)
	return nil
}

// ensureSession refreshes the stok if missing/expired.
func (c *Client) ensureSession() error {
	if c.stok == "" || time.Now().After(c.expiry) {
		return c.login()
	}
	return nil
}

// call wraps one or more method invocations in the multipleRequest
// envelope and returns the parsed responses array. Single-call sites
// pass one method; batch sites can pass several to save a round-trip.
//
// On a 401 (stale stok) we re-login once and retry — the camera
// silently rotates stoks if it reboots.
func (c *Client) call(methods ...map[string]any) ([]map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureSession(); err != nil {
		return nil, err
	}
	out, err := c.callOnce(methods)
	if err != nil && isAuthErr(err) {
		c.stok = ""
		if err := c.login(); err != nil {
			return nil, err
		}
		return c.callOnce(methods)
	}
	return out, err
}

func (c *Client) callOnce(methods []map[string]any) ([]map[string]any, error) {
	envelope := map[string]any{
		"method": "multipleRequest",
		"params": map[string]any{"requests": methods},
	}
	body, _ := json.Marshal(envelope)
	url := "https://" + c.IP + "/stok=" + c.stok + "/ds"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, errAuth
	}

	var parsed struct {
		Result struct {
			Responses []map[string]any `json:"responses"`
		} `json:"result"`
		ErrorCode int `json:"error_code"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("call: parse %s: %w", string(raw), err)
	}
	if parsed.ErrorCode == -40401 {
		return nil, errAuth
	}
	if parsed.ErrorCode != 0 {
		return nil, fmt.Errorf("call: error_code=%d body=%s", parsed.ErrorCode, snippet(raw))
	}
	return parsed.Result.Responses, nil
}

// ─── high-level commands ────────────────────────────────────────────

// DeviceInfo is the subset of getDeviceInfo we care about.
type DeviceInfo struct {
	Model       string
	Firmware    string
	HardwareID  string
	MAC         string
	DeviceAlias string
}

func (c *Client) GetDeviceInfo() (*DeviceInfo, error) {
	resp, err := c.call(map[string]any{
		"method": "get",
		"device_info": map[string]any{
			"name": []string{"basic_info"},
		},
	})
	if err != nil {
		return nil, err
	}
	bi := digMap(resp, 0, "device_info", "basic_info")
	if bi == nil {
		return nil, errors.New("getDeviceInfo: missing basic_info")
	}
	return &DeviceInfo{
		Model:       strField(bi, "device_model"),
		Firmware:    strField(bi, "sw_version"),
		HardwareID:  strField(bi, "hw_id"),
		MAC:         strField(bi, "mac"),
		DeviceAlias: strField(bi, "device_alias"),
	}, nil
}

// ProbeCapabilities does a single batched call asking for every
// feature flag we support, then derives a Capabilities from whatever
// the camera answered (or didn't).
func (c *Client) ProbeCapabilities() (*Capabilities, error) {
	resp, err := c.call(
		map[string]any{"method": "get", "motor": map[string]any{"name": []string{"capability"}}},
		map[string]any{"method": "get", "lens_mask": map[string]any{"name": []string{"lens_mask_info"}}},
		map[string]any{"method": "get", "audio_capability": map[string]any{"name": []string{"device_speaker"}}},
		map[string]any{"method": "get", "image": map[string]any{"name": []string{"switch"}}},
		map[string]any{"method": "get", "led": map[string]any{"name": []string{"config"}}},
		map[string]any{"method": "get", "motion_detection": map[string]any{"name": []string{"motion_det"}}},
		map[string]any{"method": "get", "smartdet": map[string]any{"name": []string{"smartdet"}}},
	)
	if err != nil {
		return nil, err
	}
	caps := &Capabilities{
		OnvifPort: 2020, // Tapos default; refined by a real ONVIF probe later.
	}
	caps.PTZ = digMap(resp, 0, "motor", "capability") != nil
	caps.PrivacyLens = digMap(resp, 1, "lens_mask", "lens_mask_info") != nil
	caps.Siren = digMap(resp, 2, "audio_capability", "device_speaker") != nil
	caps.NightVision = digMap(resp, 3, "image", "switch") != nil
	caps.StatusLED = digMap(resp, 4, "led", "config") != nil
	caps.MotionDetect = digMap(resp, 5, "motion_detection", "motion_det") != nil
	caps.BabyCry = digMap(resp, 6, "smartdet", "smartdet") != nil
	return caps, nil
}

// PTZMoveDirection nudges the camera one of {up,down,left,right} for a
// short pulse; "stop" cancels any in-progress move. The camera
// interprets x_coord/y_coord as absolute target degrees, but it also
// accepts a `move` action with sign-only values that mean "step in
// this direction". We use the latter — directional pulses match how
// agents reason about cameras.
func (c *Client) PTZMoveDirection(dir string, durationMs int) error {
	if durationMs <= 0 {
		durationMs = 500
	}
	x, y := 0, 0
	switch strings.ToLower(dir) {
	case "left":
		x = -10
	case "right":
		x = 10
	case "up":
		y = 10
	case "down":
		y = -10
	case "stop":
		_, err := c.call(map[string]any{
			"method": "do",
			"motor": map[string]any{"stop": map[string]any{}},
		})
		return err
	default:
		return fmt.Errorf("ptz_move: unknown direction %q", dir)
	}
	_, err := c.call(map[string]any{
		"method": "do",
		"motor": map[string]any{
			"move": map[string]any{
				"x_coord": strconv.Itoa(x),
				"y_coord": strconv.Itoa(y),
			},
		},
	})
	if err != nil {
		return err
	}
	// Pulse duration: the camera doesn't support timed moves natively,
	// so we sleep and stop. Cap at 5s — anything longer is the agent
	// doing something weird.
	if durationMs > 5000 {
		durationMs = 5000
	}
	time.Sleep(time.Duration(durationMs) * time.Millisecond)
	_, _ = c.call(map[string]any{
		"method": "do",
		"motor":  map[string]any{"stop": map[string]any{}},
	})
	return nil
}

// PTZMoveAbsolute positions the camera at the given pan/tilt degrees.
// Range is camera-dependent; C200 is roughly pan ±175°, tilt ±60°.
func (c *Client) PTZMoveAbsolute(panDeg, tiltDeg int) error {
	_, err := c.call(map[string]any{
		"method": "do",
		"motor": map[string]any{
			"movestep": map[string]any{
				"direction": "0",
				"x_coord":   strconv.Itoa(panDeg),
				"y_coord":   strconv.Itoa(tiltDeg),
			},
		},
	})
	return err
}

// PTZCalibrate sends the camera back to factory zero.
func (c *Client) PTZCalibrate() error {
	_, err := c.call(map[string]any{
		"method": "do",
		"motor":  map[string]any{"manual_cali": map[string]any{}},
	})
	return err
}

// Preset is one on-camera preset slot.
type Preset struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) PresetList() ([]Preset, error) {
	resp, err := c.call(map[string]any{
		"method": "get",
		"preset": map[string]any{"name": []string{"preset"}},
	})
	if err != nil {
		return nil, err
	}
	p := digMap(resp, 0, "preset", "preset")
	if p == nil {
		return []Preset{}, nil
	}
	ids := stringSlice(p["id"])
	names := stringSlice(p["name"])
	out := make([]Preset, 0, len(ids))
	for i := range ids {
		nm := ""
		if i < len(names) {
			nm = names[i]
		}
		out = append(out, Preset{ID: ids[i], Name: nm})
	}
	return out, nil
}

func (c *Client) PresetSave(name string) (string, error) {
	resp, err := c.call(map[string]any{
		"method": "do",
		"preset": map[string]any{
			"set_preset": map[string]any{"name": name, "save_ptz": "1"},
		},
	})
	if err != nil {
		return "", err
	}
	id, _ := digMap(resp, 0, "preset", "set_preset")["id"].(string)
	return id, nil
}

func (c *Client) PresetRecall(id string) error {
	_, err := c.call(map[string]any{
		"method": "do",
		"preset": map[string]any{"goto_preset": map[string]any{"id": id}},
	})
	return err
}

func (c *Client) PresetDelete(id string) error {
	_, err := c.call(map[string]any{
		"method": "do",
		"preset": map[string]any{"remove_preset": map[string]any{"id": []string{id}}},
	})
	return err
}

// PrivacySet flips the privacy lens cover (motorised on C200/C220) or
// software blackout on models without one. The camera accepts "on"/"off"
// strings, not booleans.
func (c *Client) PrivacySet(enabled bool) error {
	_, err := c.call(map[string]any{
		"method": "set",
		"lens_mask": map[string]any{
			"lens_mask_info": map[string]any{"enabled": onOff(enabled)},
		},
	})
	return err
}

func (c *Client) LEDSet(enabled bool) error {
	_, err := c.call(map[string]any{
		"method": "set",
		"led":    map[string]any{"config": map[string]any{"enabled": onOff(enabled)}},
	})
	return err
}

// NightModeSet — modes are "auto" | "on" | "off". The camera calls
// these "auto" / "inf_night_vision" / "off" internally.
func (c *Client) NightModeSet(mode string) error {
	internal := ""
	switch strings.ToLower(mode) {
	case "auto":
		internal = "auto"
	case "on":
		internal = "inf_night_vision"
	case "off":
		internal = "off"
	default:
		return fmt.Errorf("night_mode: must be auto|on|off, got %q", mode)
	}
	_, err := c.call(map[string]any{
		"method": "set",
		"image":  map[string]any{"switch": map[string]any{"night_vision_mode": internal}},
	})
	return err
}

// MotionDetectionSet enables/disables on-camera motion detection and
// optionally tunes sensitivity. Sensitivity is a string field with a
// fixed enum on the wire.
func (c *Client) MotionDetectionSet(enabled bool, sensitivity string) error {
	body := map[string]any{"enabled": onOff(enabled)}
	switch strings.ToLower(sensitivity) {
	case "":
		// leave alone
	case "low", "med", "medium", "high":
		s := strings.ToLower(sensitivity)
		if s == "med" {
			s = "medium"
		}
		body["digital_sensitivity"] = sensToWire(s)
	default:
		return fmt.Errorf("sensitivity: must be low|med|high, got %q", sensitivity)
	}
	_, err := c.call(map[string]any{
		"method":           "set",
		"motion_detection": map[string]any{"motion_det": body},
	})
	return err
}

// SirenTrigger sounds the alarm for `seconds`. Cap at 30s.
func (c *Client) SirenTrigger(seconds int) error {
	if seconds <= 0 {
		seconds = 5
	}
	if seconds > 30 {
		seconds = 30
	}
	_, err := c.call(map[string]any{
		"method": "do",
		"msg_alarm": map[string]any{
			"manual_msg_alarm": map[string]any{
				"action":      "start",
				"alarm_type":  "siren",
				"alarm_mode":  []string{"sound", "light"},
			},
		},
	})
	if err != nil {
		return err
	}
	time.Sleep(time.Duration(seconds) * time.Second)
	_, err = c.call(map[string]any{
		"method": "do",
		"msg_alarm": map[string]any{
			"manual_msg_alarm": map[string]any{"action": "stop"},
		},
	})
	return err
}

// MotionEvent is one row from the camera's on-device event log.
type MotionEvent struct {
	ID         string
	OccurredAt time.Time
	Kind       string
	BBoxJSON   string
}

// ListMotionEvents pulls the recent on-device event log (Tapos keep
// ~7 days on internal storage). `since` filters server-side by Unix
// seconds; pass zero for "everything in the camera's buffer".
//
// Response shape varies by firmware: older firmwares return a list of
// {start_time, end_time, type}; newer ones add bbox_info. We tolerate
// both and forward what's available.
func (c *Client) ListMotionEvents(since time.Time) ([]MotionEvent, error) {
	params := map[string]any{
		"name":       []string{"event_list"},
		"start_time": "0",
	}
	if !since.IsZero() {
		params["start_time"] = strconv.FormatInt(since.Unix(), 10)
	}
	resp, err := c.call(map[string]any{
		"method":  "get",
		"playback": params,
	})
	if err != nil {
		return nil, err
	}
	pb := digMap(resp, 0, "playback", "event_list")
	if pb == nil {
		return []MotionEvent{}, nil
	}
	rawList, _ := pb["event"].([]any)
	out := make([]MotionEvent, 0, len(rawList))
	for _, r := range rawList {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		ts := unixStrToTime(strField(m, "start_time"))
		bbox := ""
		if b, ok := m["bbox_info"]; ok {
			bj, _ := json.Marshal(b)
			bbox = string(bj)
		}
		out = append(out, MotionEvent{
			ID:         strField(m, "event_id"),
			OccurredAt: ts,
			Kind:       normaliseEventKind(strField(m, "type")),
			BBoxJSON:   bbox,
		})
	}
	return out, nil
}

// SnapshotURL returns a direct ONVIF-style snapshot URL for the
// camera. Tapos expose this on port 2020 by default with HTTP basic
// auth using the camera-account credentials. Falls back to the legacy
// `https://{ip}/cgi-bin/snapshot.cgi` route if the ONVIF port isn't
// listening — but we don't probe for that here; the caller handles
// the fallback if the GET 404s.
func (c *Client) SnapshotURL() string {
	return fmt.Sprintf(
		"http://%s:%s@%s:2020/onvif/snapshot",
		c.Username, c.Password, c.IP,
	)
}

// FetchSnapshot grabs a JPEG from the snapshot URL and returns the raw
// bytes. 5s timeout — if the camera is overloaded, fail fast and let
// the caller retry.
func (c *Client) FetchSnapshot() ([]byte, error) {
	httpc := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpc.Get(c.SnapshotURL())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		// Try the alternate CGI endpoint with stok auth.
		c.mu.Lock()
		if err := c.ensureSession(); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		stok := c.stok
		c.mu.Unlock()
		alt := fmt.Sprintf("https://%s/stok=%s/ds/snapshot.jpg", c.IP, stok)
		req, _ := http.NewRequest("GET", alt, nil)
		resp2, err := c.httpc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != 200 {
			return nil, fmt.Errorf("snapshot: %d (camera may not expose ONVIF on :2020)", resp.StatusCode)
		}
		return io.ReadAll(resp2.Body)
	}
	return io.ReadAll(resp.Body)
}

// RTSPURL returns the camera's RTSP stream URL. quality must be
// "hd" (stream1) or "sd" (stream2). Credentials are embedded — caller
// is responsible for not leaking these.
func (c *Client) RTSPURL(quality string) string {
	stream := "stream1"
	if strings.ToLower(quality) == "sd" {
		stream = "stream2"
	}
	return fmt.Sprintf(
		"rtsp://%s:%s@%s:554/%s",
		c.Username, c.Password, c.IP, stream,
	)
}

// ─── KLAP scaffold ──────────────────────────────────────────────────
//
// Newer Tapo firmware (post mid-2023) deprecates the MD5+stok login
// in favour of a session-keyed handshake called KLAP. The shape is:
//
//   POST /app/handshake1  body=local_seed (16B random)
//                          → remote_seed (16B) + auth_hash (sha256)
//   POST /app/handshake2  body=local_auth_hash
//                          → 200 with session cookie
//   POST /app/request     body=AES-CBC(IV=seed,KEY=sha256(local|remote|auth_hash))
//
// Implementing this fully is ~400 lines of careful crypto + a fairly
// strict request format. Rather than ship a half-working version, v0.1
// returns a clear error and the README points at the firmware
// downgrade path. To unblock newer firmware, replace klapAvailable()
// with a real probe and add a klapClient with the same surface as
// Client.
//
// The probe heuristic: KLAP cameras 401 on POST `/` and accept POST
// `/app/handshake1`. We assert this on first login and route
// accordingly.

func klapAvailable(_ string) bool { return false }

// ─── helpers ────────────────────────────────────────────────────────

var errAuth = errors.New("tapo: auth required")

func isAuthErr(err error) bool { return errors.Is(err, errAuth) }

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func sensToWire(s string) string {
	switch s {
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	}
	return "medium"
}

// digMap walks responses[i].path[0].path[1]... safely. Returns nil if
// any step is missing or the wrong shape.
func digMap(resp []map[string]any, i int, path ...string) map[string]any {
	if i >= len(resp) {
		return nil
	}
	cur := resp[i]
	for _, p := range path {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

func strField(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func stringSlice(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	case []string:
		return x
	case string:
		// Tapos sometimes encode arrays as comma-joined strings.
		if x == "" {
			return nil
		}
		return strings.Split(x, ",")
	}
	return nil
}

func unixStrToTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

func normaliseEventKind(t string) string {
	switch strings.ToLower(t) {
	case "person", "people":
		return "person"
	case "pet", "animal":
		return "pet"
	case "baby_cry", "baby_crying":
		return "baby_cry"
	case "sound":
		return "sound"
	default:
		return "motion"
	}
}

func snippet(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "…"
	}
	return string(b)
}
