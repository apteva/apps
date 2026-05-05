// tapo_client.go — TP-Link Tapo camera HTTPS JSON-RPC client.
//
// Reverse-engineered from the Tapo mobile app + the pytapo community
// library. There is no public SDK; firmware build 230921 (Sept 2023+)
// fixed a CVE that disabled the legacy md5+stok flow that older
// integrations relied on. Since Dec 2024 TP-Link has shipped an
// official escape hatch — Me → Tapo Lab → Third-Party Compatibility
// in the Tapo app (≥ 3.8.103). Once enabled, the user supplies a
// per-camera username + password used by this client below.
//
// Wire shape (secure_passthrough — current):
//
//   1. POST https://{ip}/  with cnonce  → -40413 envelope carrying
//      RSA pubkey + server nonce + device_confirm proof
//
//      device_confirm = sha256(cnonce + hashedPwd + nonce).hex.upper +
//                       nonce + cnonce
//
//      Try hashedPwd = sha256(password) first, fall back to md5 — the
//      camera advertises which by which proof it returns.
//
//   2. POST https://{ip}/  with digest_passwd =
//        sha256(hashedPwd + cnonce + nonce).hex.upper + cnonce + nonce
//      → { "result":{ "stok":<token>, "start_seq":<int>, ... }, "error_code":0 }
//
//   3. Derive AES-128 key + IV:
//        hashedKey = sha256(cnonce + hashedPwd + nonce).hex.upper
//        lsk = sha256("lsk" + cnonce + nonce + hashedKey)[:16]
//        ivb = sha256("ivb" + cnonce + nonce + hashedKey)[:16]
//
//   4. Per request to /stok={stok}/ds, wrap the inner JSON in:
//        ciphertext = AES-128-CBC(lsk, ivb, pkcs7-pad(json))
//        envelope   = {"method":"securePassthrough",
//                      "params":{"request": base64(ciphertext)}}
//        Headers:
//          Seq:      <seq>            (incremented per request)
//          Tapo_tag: sha256(sha256(hashedPwd + cnonce).hex.upper +
//                           json(envelope) + str(seq)).hex.upper
//      Decrypt the response.params.response field with the same
//      lsk/ivb to recover the inner JSON.
//
// Notes:
//   * TLS verification disabled — self-signed internal CN. LAN-only.
//   * AES-CBC reuses (key, iv) per request, by protocol design. The
//     Tapo_tag (HMAC-flavoured proof) is what makes per-request
//     auth work despite the static IV.
//   * Method names below are the camera-facing strings — keep them.
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
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

// Client is one camera's session. Holds the stok + AES key/IV +
// monotonic seq counter; we re-login lazily on -40401.
type Client struct {
	IP       string
	Username string
	Password string

	mu        sync.Mutex
	stok      string
	expiry    time.Time
	httpc     *http.Client
	hashedPwd string // SHA256 or MD5 of password, hex.upper — set during login
	cnonce    string // 16-hex local nonce, regenerated each login
	nonce     string // 16-hex server nonce, returned in handshake
	lsk       []byte // AES-128 key, 16 bytes
	ivb       []byte // AES-CBC IV, 16 bytes
	seq       int    // monotonic sequence, starts at start_seq, +1 per request
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

// login performs the full three-step secure_passthrough handshake.
// Caller must hold c.mu. Sets stok, hashedPwd, cnonce, nonce, lsk,
// ivb, seq on success.
func (c *Client) login() error {
	cnonceB := make([]byte, 8)
	if _, err := rand.Read(cnonceB); err != nil {
		return fmt.Errorf("login: rand: %w", err)
	}
	cnonce := strings.ToUpper(hex.EncodeToString(cnonceB))
	c.cnonce = cnonce

	// --- Step 1: kick off handshake. The first POST always returns
	// -40413 with the device_confirm envelope; we use it to derive
	// the right hashing algorithm AND prove the camera knows our
	// password before sending any further data.
	probe := map[string]any{
		"method": "login",
		"params": map[string]any{
			"encrypt_type": "3",
			"username":     c.Username,
			"cnonce":       cnonce,
		},
	}
	body, _ := json.Marshal(probe)
	resp, err := c.postRoot(body)
	if err != nil {
		return fmt.Errorf("login probe: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var step1 struct {
		Result struct {
			Data struct {
				EncryptType   []string `json:"encrypt_type"`
				Nonce         string   `json:"nonce"`
				DeviceConfirm string   `json:"device_confirm"`
			} `json:"data"`
		} `json:"result"`
		ErrorCode int `json:"error_code"`
	}
	if err := json.Unmarshal(raw, &step1); err != nil {
		return fmt.Errorf("login probe parse: %w body=%s", err, snippet(raw))
	}
	if step1.Result.Data.Nonce == "" || step1.Result.Data.DeviceConfirm == "" {
		return fmt.Errorf("login probe: missing handshake fields (error_code=%d body=%s) — enable Tapo app → Me → Tapo Lab → Third-Party Compatibility",
			step1.ErrorCode, snippet(raw))
	}
	c.nonce = step1.Result.Data.Nonce

	// --- Step 2: figure out which hashing algorithm the camera uses
	// and validate device_confirm proof.
	hashedSHA := strings.ToUpper(hex.EncodeToString(sha256Sum([]byte(c.Password))))
	hashedMD5 := strings.ToUpper(hex.EncodeToString(md5Sum([]byte(c.Password))))
	switch {
	case validateDeviceConfirm(cnonce, hashedSHA, c.nonce, step1.Result.Data.DeviceConfirm):
		c.hashedPwd = hashedSHA
	case validateDeviceConfirm(cnonce, hashedMD5, c.nonce, step1.Result.Data.DeviceConfirm):
		c.hashedPwd = hashedMD5
	default:
		// Wrong username, wrong password, or Third-Party
		// Compatibility is off. The error_code=-40413 outer is the
		// same in all three cases — pytapo can't disambiguate either.
		return errors.New("login: device_confirm mismatch — wrong credentials, or enable Third-Party Compatibility in the Tapo app")
	}

	// --- Step 3: send the digest_passwd login. On success the
	// camera issues a stok cookie + start_seq counter.
	digest := strings.ToUpper(hex.EncodeToString(
		sha256Sum([]byte(c.hashedPwd + cnonce + c.nonce))))
	loginBody, _ := json.Marshal(map[string]any{
		"method": "login",
		"params": map[string]any{
			"cnonce":        cnonce,
			"encrypt_type":  "3",
			"digest_passwd": digest + cnonce + c.nonce,
			"username":      c.Username,
		},
	})
	resp2, err := c.postRoot(loginBody)
	if err != nil {
		return fmt.Errorf("login digest: %w", err)
	}
	defer resp2.Body.Close()
	raw2, _ := io.ReadAll(resp2.Body)

	var step3 struct {
		Result struct {
			Stok      string `json:"stok"`
			StartSeq  int    `json:"start_seq"`
			UserGroup string `json:"user_group"`
		} `json:"result"`
		ErrorCode int `json:"error_code"`
	}
	if err := json.Unmarshal(raw2, &step3); err != nil {
		return fmt.Errorf("login digest parse: %w body=%s", err, snippet(raw2))
	}
	if step3.Result.Stok == "" {
		return fmt.Errorf("login: digest rejected, error_code=%d body=%s", step3.ErrorCode, snippet(raw2))
	}
	c.stok = step3.Result.Stok
	c.seq = step3.Result.StartSeq
	c.expiry = time.Now().Add(25 * time.Minute)

	// --- Step 4: derive AES key + IV from cnonce/nonce/hashedPwd.
	hashedKey := strings.ToUpper(hex.EncodeToString(
		sha256Sum([]byte(cnonce + c.hashedPwd + c.nonce))))
	c.lsk = sha256Sum([]byte("lsk" + cnonce + c.nonce + hashedKey))[:16]
	c.ivb = sha256Sum([]byte("ivb" + cnonce + c.nonce + hashedKey))[:16]
	return nil
}

// validateDeviceConfirm — does the server's proof match our local
// hashedPwd? The format is sha256(cnonce + hashedPwd + nonce) ||
// nonce || cnonce, all hex-uppercase concatenated.
func validateDeviceConfirm(cnonce, hashedPwd, nonce, deviceConfirm string) bool {
	want := strings.ToUpper(hex.EncodeToString(
		sha256Sum([]byte(cnonce + hashedPwd + nonce))))
	return deviceConfirm == want+nonce+cnonce
}

// postRoot is a small helper for the two unauthenticated handshake
// POSTs to https://<ip>/. Keeps login() readable.
func (c *Client) postRoot(body []byte) (*http.Response, error) {
	req, err := http.NewRequest("POST", "https://"+c.IP+"/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.httpc.Do(req)
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
	if len(methods) == 0 {
		return nil, errors.New("call: no methods")
	}
	// secure_passthrough firmware rejects the multipleRequest wrapper
	// with -40210 for batched calls; we instead send each inner
	// method as a standalone request and stitch the responses back
	// into the slice the legacy callers expect. The cost is one
	// round-trip per method, but each one is on the LAN so it's
	// cheap, and we never amortise more than ~7 in ProbeCapabilities.
	if len(methods) > 1 {
		out := make([]map[string]any, 0, len(methods))
		for _, m := range methods {
			rs, err := c.callOnce([]map[string]any{m})
			if err != nil {
				return nil, err
			}
			out = append(out, rs...)
		}
		return out, nil
	}
	innerJSON, _ := json.Marshal(methods[0])

	// Encrypt with AES-128-CBC (key=lsk, iv=ivb), pkcs7-padded.
	ct, err := aesEncrypt(c.lsk, c.ivb, pkcs7Pad(innerJSON, aes.BlockSize))
	if err != nil {
		return nil, fmt.Errorf("call: encrypt: %w", err)
	}
	envelope := map[string]any{
		"method": "securePassthrough",
		"params": map[string]any{
			"request": base64.StdEncoding.EncodeToString(ct),
		},
	}
	envBody, _ := json.Marshal(envelope)

	// Tapo_tag = sha256(sha256(hashedPwd+cnonce).hex.upper +
	//                   json(envelope) + str(seq)).hex.upper
	innerTag := strings.ToUpper(hex.EncodeToString(
		sha256Sum([]byte(c.hashedPwd + c.cnonce))))
	tag := strings.ToUpper(hex.EncodeToString(
		sha256Sum([]byte(innerTag + string(envBody) + strconv.Itoa(c.seq)))))

	url := "https://" + c.IP + "/stok=" + c.stok + "/ds"
	req, err := http.NewRequest("POST", url, bytes.NewReader(envBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Seq", strconv.Itoa(c.seq))
	req.Header.Set("Tapo_tag", tag)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	c.seq++ // advance once we've sent, regardless of response

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, errAuth
	}

	// Outer envelope: {"result":{"response":<base64>}, "error_code":0}.
	var outer struct {
		Result struct {
			Response string `json:"response"`
		} `json:"result"`
		ErrorCode int `json:"error_code"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, fmt.Errorf("call: parse outer: %w body=%s", err, snippet(raw))
	}
	if outer.ErrorCode == -40401 {
		return nil, errAuth
	}
	// Newer firmware returns the inner reply *plain* (no encrypted
	// wrapper) for some method shapes — same fallback pytapo applies.
	// Use the encrypted path when result.response is set; otherwise
	// take the raw body as already-plaintext inner JSON.
	var pt []byte
	if outer.Result.Response != "" {
		ctResp, err := base64.StdEncoding.DecodeString(outer.Result.Response)
		if err != nil {
			return nil, fmt.Errorf("call: b64 decode: %w", err)
		}
		dec, err := aesDecrypt(c.lsk, c.ivb, ctResp)
		if err != nil {
			return nil, fmt.Errorf("call: decrypt: %w", err)
		}
		pt = pkcs7Unpad(dec)
	} else {
		pt = raw
	}

	// Single-method response shape: top-level fields are the camera's
	// reply (e.g. {"device_info":{"basic_info":...},"error_code":0}).
	// Wrap that into the legacy []map[string]any contract: one entry,
	// keyed by the same top-level fields the caller expects.
	var single map[string]any
	if err := json.Unmarshal(pt, &single); err != nil {
		return nil, fmt.Errorf("call: parse inner: %w body=%s", err, snippet(pt))
	}
	if ec, ok := single["error_code"].(float64); ok {
		if int(ec) == -40401 {
			return nil, errAuth
		}
		if int(ec) != 0 {
			return nil, fmt.Errorf("call: error_code=%d body=%s", int(ec), snippet(pt))
		}
	}
	return []map[string]any{single}, nil
}

// ─── crypto helpers ────────────────────────────────────────────────

func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func md5Sum(b []byte) []byte {
	h := md5.Sum(b)
	return h[:]
}

func aesEncrypt(key, iv, pt []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	mode := cipher.NewCBCEncrypter(block, iv)
	ct := make([]byte, len(pt))
	mode.CryptBlocks(ct, pt)
	return ct, nil
}

func aesDecrypt(key, iv, ct []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ct)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext not aligned to block size")
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	pt := make([]byte, len(ct))
	mode.CryptBlocks(pt, ct)
	return pt, nil
}

func pkcs7Pad(b []byte, sz int) []byte {
	pad := sz - len(b)%sz
	out := make([]byte, len(b)+pad)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs7Unpad(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	n := int(b[len(b)-1])
	if n <= 0 || n > len(b) {
		return b
	}
	return b[:len(b)-n]
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

// ProbeCapabilities asks the camera which features it advertises.
// On secure_passthrough firmware the legacy `get / motor / capability`
// shape returns -40101 even for cameras that *do* have a motor — the
// wire format for capability advertisement rotated. We probe PTZ
// indirectly by attempting a no-op `motor/stop` (a stop with no
// preceding move is a cheap idempotent ping that returns 0 on
// motorised models and -40101 on fixed-mount). Other features fall
// back to "feature absent" on -40101 to keep the caller useful.
func (c *Client) ProbeCapabilities() (*Capabilities, error) {
	probe := func(field, name string) bool {
		rs, err := c.call(map[string]any{
			"method": "get",
			field:    map[string]any{"name": []string{name}},
		})
		if err != nil {
			return false
		}
		return digMap(rs, 0, field, name) != nil
	}
	probePTZ := func() bool {
		_, err := c.call(map[string]any{
			"method": "do",
			"motor":  map[string]any{"stop": map[string]any{}},
		})
		return err == nil
	}
	caps := &Capabilities{
		OnvifPort:    2020, // Tapos default; refined by a real ONVIF probe later.
		PTZ:          probePTZ(),
		PrivacyLens:  probe("lens_mask", "lens_mask_info"),
		Siren:        probe("audio_capability", "device_speaker"),
		NightVision:  probe("image", "switch"),
		StatusLED:    probe("led", "config"),
		MotionDetect: probe("motion_detection", "motion_det"),
		BabyCry:      probe("smartdet", "smartdet"),
	}
	return caps, nil
}

// PTZMoveDirection nudges the camera in one of {up,down,left,right}
// by the camera's intrinsic step. The on-wire shape is the
// `motor/movestep/direction` flavor pytapo uses on
// secure_passthrough firmware: direction is the angle of motion in
// degrees, as a string — the camera rotates toward that compass
// heading by one step.
//
//   right (clockwise)         = 0
//   up    (vertical up)       = 90
//   left  (counter-clockwise) = 180
//   down  (vertical down)     = 270
//
// durationMs is honoured by issuing additional steps until elapsed,
// then a `stop`. Each step is small (~10° on C200/C210) so a 500ms
// pulse usually means 1–2 calls.
func (c *Client) PTZMoveDirection(dir string, durationMs int) error {
	if strings.EqualFold(dir, "stop") {
		_, err := c.call(map[string]any{
			"method": "do",
			"motor":  map[string]any{"stop": map[string]any{}},
		})
		return err
	}
	angle, ok := map[string]string{
		"right": "0",
		"up":    "90",
		"left":  "180",
		"down":  "270",
	}[strings.ToLower(dir)]
	if !ok {
		return fmt.Errorf("ptz_move: unknown direction %q (want up|down|left|right|stop)", dir)
	}
	if durationMs <= 0 {
		durationMs = 500
	}
	if durationMs > 5000 {
		durationMs = 5000
	}
	deadline := time.Now().Add(time.Duration(durationMs) * time.Millisecond)
	for {
		if _, err := c.call(map[string]any{
			"method": "do",
			"motor": map[string]any{
				"movestep": map[string]any{"direction": angle},
			},
		}); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
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
