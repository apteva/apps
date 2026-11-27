package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

// WebSocket pass-through (0.16.0).
//
// A route whose target is an app HTTP path may opt in with websocket settings.
// The upgrade request is checked like any other request (key, scope, rate
// limit, CORS origin) and then relayed to the app through the platform's app
// proxy. Frames are copied both ways unchanged; the gateway answers pings on
// each side itself. One request-log row is written per connection when it
// closes, with bytes in/out and the close code and reason. No frame content
// is recorded.
//
// Browsers cannot set headers on a WebSocket, so a browser first calls a
// ws_ticket route with its key (an ordinary REST call) and connects with
// ?ticket=. A ticket is single use, bound to the key and API, and expires
// after wsTicketTTL. API keys in WebSocket URLs (?api_key=) are refused.

const (
	wsTicketTargetRef      = "gateway"
	wsTicketTTL            = 60 * time.Second
	wsTicketsPerKey        = 32
	wsTicketsPerProject    = 10000
	wsDefaultMaxConns      = 4
	wsDefaultMaxDuration   = 3600
	wsDefaultIdleTimeout   = 300
	wsDefaultMaxMessage    = 4 << 20
	wsMaxMessageCeiling    = 16 << 20
	wsPingInterval         = 25 * time.Second
	wsPeerSilenceLimit     = 75 * time.Second
	wsCloseWriteWait       = 2 * time.Second
	wsUpstreamErrBodyLimit = 4096

	// Close codes the gateway sends on its own behalf. Upstream and client
	// close codes are relayed as they are.
	wsCloseCredentialRevoked = 4001
	wsCloseMaxDuration       = 4002
	wsCloseIdleTimeout       = 4003
	wsCloseCredentialExpired = 4004
)

// webSocketConfig is a route's stored WebSocket setting.
type webSocketConfig struct {
	Enabled              bool  `json:"enabled"`
	MaxConnectionsPerKey int   `json:"max_connections_per_key"`
	MaxDurationSeconds   int   `json:"max_duration_seconds"`
	IdleTimeoutSeconds   int   `json:"idle_timeout_seconds"`
	MaxMessageBytes      int64 `json:"max_message_bytes"`
	BytesPerSecond       int64 `json:"bytes_per_second"`
}

func parseWebSocketConfig(raw string) (webSocketConfig, error) {
	var cfg webSocketConfig
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return cfg, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, errors.New("websocket must be true, false, or an object of enabled, max_connections_per_key, max_duration_seconds, idle_timeout_seconds, max_message_bytes, bytes_per_second")
	}
	if !cfg.Enabled {
		return webSocketConfig{}, nil
	}
	if cfg.MaxConnectionsPerKey == 0 {
		cfg.MaxConnectionsPerKey = wsDefaultMaxConns
	}
	if cfg.MaxDurationSeconds == 0 {
		cfg.MaxDurationSeconds = wsDefaultMaxDuration
	}
	if cfg.IdleTimeoutSeconds == 0 {
		cfg.IdleTimeoutSeconds = wsDefaultIdleTimeout
	}
	if cfg.MaxMessageBytes == 0 {
		cfg.MaxMessageBytes = wsDefaultMaxMessage
	}
	switch {
	case cfg.MaxConnectionsPerKey < 1 || cfg.MaxConnectionsPerKey > 100:
		return cfg, errors.New("websocket.max_connections_per_key must be between 1 and 100")
	case cfg.MaxDurationSeconds < 1 || cfg.MaxDurationSeconds > 86400:
		return cfg, errors.New("websocket.max_duration_seconds must be between 1 and 86400")
	case cfg.IdleTimeoutSeconds < 1 || cfg.IdleTimeoutSeconds > 3600:
		return cfg, errors.New("websocket.idle_timeout_seconds must be between 1 and 3600")
	case cfg.MaxMessageBytes < 1 || cfg.MaxMessageBytes > wsMaxMessageCeiling:
		return cfg, fmt.Errorf("websocket.max_message_bytes must be between 1 and %d", wsMaxMessageCeiling)
	case cfg.BytesPerSecond != 0 && (cfg.BytesPerSecond < 1024 || cfg.BytesPerSecond > 1<<30):
		return cfg, errors.New("websocket.bytes_per_second must be 0 (unlimited) or between 1024 and 1073741824")
	}
	return cfg, nil
}

// normalizeWebSocketSetting validates a route's websocket argument and
// returns the stored JSON, with defaults filled in. Only GET routes to an app
// target may accept WebSocket upgrades.
func normalizeWebSocketSetting(kind, method, raw string) (string, error) {
	cfg, err := parseWebSocketConfig(raw)
	if err != nil {
		return "", err
	}
	if !cfg.Enabled {
		return "{}", nil
	}
	if kind != "app" {
		return "", errors.New("websocket is only supported on app routes")
	}
	if method != "GET" {
		return "", errors.New("websocket routes must use GET")
	}
	b, _ := json.Marshal(cfg)
	return string(b), nil
}

// webSocketArg reads the route tool's websocket argument: a boolean or an
// object. Absent means disabled.
func webSocketArg(args map[string]any) (string, error) {
	v, ok := args["websocket"]
	if !ok || v == nil {
		return "{}", nil
	}
	switch t := v.(type) {
	case bool:
		if !t {
			return "{}", nil
		}
		return `{"enabled":true}`, nil
	case map[string]any:
		obj := make(map[string]any, len(t)+1)
		for k, val := range t {
			obj[k] = val
		}
		if _, set := obj["enabled"]; !set {
			obj["enabled"] = true
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return "", err
		}
		return string(b), nil
	default:
		return "", errors.New("websocket must be a boolean or an object")
	}
}

// --- tickets -------------------------------------------------------------

type wsTicket struct {
	project string
	apiID   int64
	keyID   int64
	expires time.Time
}

type wsTicketStore struct {
	mu      sync.Mutex
	tickets map[string]wsTicket // sha256(ticket) -> ticket
}

func ticketDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *wsTicketStore) mint(project string, apiID, keyID int64, now time.Time) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tickets == nil {
		s.tickets = map[string]wsTicket{}
	}
	perKey, perProject := 0, 0
	for k, t := range s.tickets {
		if !t.expires.After(now) {
			delete(s.tickets, k)
			continue
		}
		if t.project != project {
			continue
		}
		perProject++
		if t.keyID == keyID {
			perKey++
		}
	}
	if perKey >= wsTicketsPerKey || perProject >= wsTicketsPerProject {
		return "", time.Time{}, errors.New("too many outstanding websocket tickets")
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", time.Time{}, err
	}
	raw := "wst_" + base64.RawURLEncoding.EncodeToString(b[:])
	exp := now.Add(wsTicketTTL)
	s.tickets[ticketDigest(raw)] = wsTicket{project: project, apiID: apiID, keyID: keyID, expires: exp}
	return raw, exp, nil
}

// consume returns the ticket once; a second use, an expired ticket or a
// ticket for another API fails.
func (s *wsTicketStore) consume(raw, project string, apiID int64, now time.Time) (wsTicket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := ticketDigest(raw)
	t, ok := s.tickets[d]
	if !ok {
		return wsTicket{}, false
	}
	delete(s.tickets, d)
	if !t.expires.After(now) || t.project != project || t.apiID != apiID {
		return wsTicket{}, false
	}
	return t, true
}

func (s *wsTicketStore) revokeKey(project string, keyID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.tickets {
		if t.project == project && t.keyID == keyID {
			delete(s.tickets, k)
		}
	}
}

// dispatchWSTicket mints a ticket for the API key that authenticated this
// request.
func (a *App) dispatchWSTicket(w http.ResponseWriter, api *API, auth authContext) (int, error) {
	if auth.Kind != "api_key" || auth.KeyID == 0 {
		httpErr(w, http.StatusBadRequest, "websocket tickets require API key authentication")
		return http.StatusBadRequest, errors.New("ws_ticket without api key")
	}
	ticket, exp, err := a.wsTickets.mint(api.ProjectID, api.ID, auth.KeyID, time.Now())
	if err != nil {
		httpErr(w, http.StatusTooManyRequests, err.Error())
		return http.StatusTooManyRequests, err
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ticket": ticket, "expires_at": exp.UTC().Format(time.RFC3339), "expires_in": int(wsTicketTTL.Seconds()),
	})
	return http.StatusCreated, nil
}

// apiKeyPolicyOf returns the api_key method of an effective route policy.
func apiKeyPolicyOf(policy authorizerPolicy) (authorizerPolicy, bool) {
	if policy.Kind == "api_key" {
		return policy, true
	}
	if policy.Kind == "any_of" {
		for _, raw := range policy.Methods {
			m, err := parseAuthPolicy(string(raw))
			if err == nil && m.Kind == "api_key" {
				return m, true
			}
		}
	}
	return policy, false
}

// authorizeWebSocket authenticates an upgrade request. A ticket stands in
// for the API key; without one the ordinary carriers apply, except that an
// API key in the URL is refused.
func (a *App) authorizeWebSocket(r *http.Request, api *API, route *APIRoute, ticket *wsTicket) (authContext, error) {
	policy, err := effectiveAuthPolicy(api.AuthJSON, route.AuthJSON)
	if err != nil {
		return authContext{}, authFailure(500, "invalid authentication configuration", err)
	}
	if policy.Kind == "" || policy.Kind == "public" {
		return authContext{Kind: "public"}, authFailure(401, "websocket routes require authentication", nil)
	}
	if ticket == nil {
		return a.authorizeRequest(r, api, route)
	}
	keyPolicy, ok := apiKeyPolicyOf(policy)
	if !ok {
		return authContext{Kind: policy.Kind}, authFailure(401, "this route does not accept API key tickets", nil)
	}
	credential, ok, err := dbGetActiveAPIKeyByID(a.ctx.AppDB(), api.ProjectID, api.ID, ticket.keyID)
	if err != nil {
		return authContext{Kind: "api_key"}, authFailure(503, "authentication service unavailable", err)
	}
	if !ok {
		return authContext{Kind: "api_key"}, authFailure(401, "invalid api key", nil)
	}
	if !keyHasRequiredScopes(credential.Scopes, keyPolicy.RequiredScopes) {
		return authContext{Kind: "api_key", Subject: "api_key", KeyID: credential.ID}, authFailure(403, "api key lacks a required scope", nil)
	}
	principal, expiry := principalForAPIKey(credential)
	return authContext{Kind: "api_key", Subject: principal.Subject, KeyID: credential.ID, Principal: principal, ExpiresAt: expiry}, nil
}

// --- connection relay ----------------------------------------------------

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	// The Origin was checked against the API's CORS policy before upgrade.
	CheckOrigin: func(*http.Request) bool { return true },
}

// wsHandshakeHeaders are set by the dialer itself and must not be copied.
var wsHandshakeHeaders = []string{"Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Extensions", "Sec-Websocket-Protocol", "Sec-Websocket-Accept", "Origin"}

func wsUpstreamURL(route *APIRoute, publicPath string, query url.Values) (string, string, error) {
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	token := outboundAppToken()
	if base == "" || token == "" {
		return "", "", errors.New("gateway configuration missing")
	}
	targetPath := route.TargetPath
	if targetPath == "" {
		targetPath = publicPath
	}
	q := sanitizedQuery(query)
	q.Del("ticket")
	q.Set("project_id", route.ProjectID)
	u, err := joinTarget(base+"/api/apps/callback/apps/"+url.PathEscape(route.TargetRef)+"/proxy", targetPath, publicPath, q.Encode())
	if err != nil {
		return "", "", err
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return "", "", err
	}
	values := parsed.Query()
	values.Set("project_id", route.ProjectID)
	parsed.RawQuery = values.Encode()
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", "", errors.New("invalid gateway URL scheme")
	}
	return parsed.String(), token, nil
}

// wsCallerHeaders carry the verified caller to the app. The platform proxy
// stamps X-Apteva-Bound-Caller-App-Name with this app's name, so an app can
// trust these headers when that header names the API Gateway. Inbound
// X-Apteva-* headers from clients are always stripped first.
func wsCallerHeaders(h http.Header, auth authContext) {
	h.Set("X-Apteva-Gateway-Auth-Kind", auth.Kind)
	if auth.Subject != "" {
		h.Set("X-Apteva-Gateway-Subject", auth.Subject)
	}
	if auth.KeyID != 0 {
		h.Set("X-Apteva-Gateway-Key-ID", strconv.FormatInt(auth.KeyID, 10))
	}
	if auth.Principal != nil {
		if b, err := json.Marshal(auth.Principal); err == nil {
			h.Set("X-Apteva-Gateway-Principal", base64.RawURLEncoding.EncodeToString(b))
		}
	}
}

type wsCloseState struct {
	once   sync.Once
	code   int
	reason string
}

func (s *wsCloseState) set(code int, reason string) {
	s.once.Do(func() { s.code, s.reason = code, reason })
}

// byteBucket throttles a connection to a byte rate shared by both
// directions. A nil bucket is unlimited.
type byteBucket struct {
	mu      sync.Mutex
	rate    float64
	tokens  float64
	updated time.Time
}

func (b *byteBucket) wait(ctx context.Context, n int) error {
	if b == nil || b.rate <= 0 {
		return nil
	}
	b.mu.Lock()
	delay := b.reserve(n)
	b.mu.Unlock()
	if delay <= 0 {
		return nil
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// reserve takes n bytes from the budget and returns how long the caller must
// wait for them. Callers hold b.mu.
func (b *byteBucket) reserve(n int) time.Duration {
	now := time.Now()
	b.tokens += now.Sub(b.updated).Seconds() * b.rate
	if b.tokens > b.rate {
		b.tokens = b.rate
	}
	b.updated = now
	b.tokens -= float64(n)
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(-b.tokens / b.rate * float64(time.Second))
}

func newByteBucket(rate int64) *byteBucket {
	if rate <= 0 {
		return nil
	}
	return &byteBucket{rate: float64(rate), tokens: float64(rate), updated: time.Now()}
}

// serveWebSocket relays one authorized upgrade. It always writes the
// response itself; the returned status is 101 once the client was upgraded.
func (a *App) serveWebSocket(w http.ResponseWriter, r *http.Request, route *APIRoute, publicPath string, auth authContext, cfg webSocketConfig, logRow *RequestLog, releaseSlot func()) (int, error) {
	target, token, err := wsUpstreamURL(route, publicPath, r.URL.Query())
	if err != nil {
		httpErr(w, http.StatusBadGateway, "gateway configuration missing")
		return http.StatusBadGateway, err
	}
	header := sanitizedHeaders(r.Header)
	for _, k := range wsHandshakeHeaders {
		header.Del(k)
	}
	header.Set("Authorization", "Bearer "+token)
	header.Set("X-Request-ID", gatewayRequestID(r.Context()))
	wsCallerHeaders(header, auth)

	dialCtx, cancelDial := context.WithTimeout(r.Context(), time.Duration(route.TimeoutMS)*time.Millisecond)
	defer cancelDial()
	dialer := websocket.Dialer{
		HandshakeTimeout: time.Duration(route.TimeoutMS) * time.Millisecond,
		ReadBufferSize:   32 << 10,
		WriteBufferSize:  32 << 10,
		Subprotocols:     websocket.Subprotocols(r),
	}
	upstream, resp, err := dialer.DialContext(dialCtx, target, header)
	if err != nil {
		// Refused handshakes pass the app's 4xx answer through, so a client
		// sees why (for example an expired stream token).
		if resp != nil && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, wsUpstreamErrBodyLimit))
			resp.Body.Close()
			if ct := resp.Header.Get("Content-Type"); ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(body)
			return resp.StatusCode, fmt.Errorf("upstream refused websocket: %d", resp.StatusCode)
		}
		if resp != nil {
			resp.Body.Close()
		}
		if r.Context().Err() != nil && errors.Is(context.Cause(r.Context()), errStreamCredentialRevoked) {
			httpErr(w, http.StatusUnauthorized, "credential revoked")
			return http.StatusUnauthorized, err
		}
		httpErr(w, http.StatusBadGateway, "upstream websocket unavailable")
		return http.StatusBadGateway, err
	}
	var respHeader http.Header
	if p := upstream.Subprotocol(); p != "" {
		respHeader = http.Header{"Sec-Websocket-Protocol": {p}}
	}
	client, err := wsUpgrader.Upgrade(w, r, respHeader)
	if err != nil {
		upstream.Close()
		// Upgrade already answered the client with an HTTP error.
		return http.StatusBadRequest, err
	}
	// The connection no longer needs an HTTP request slot; WebSocket
	// connections have their own caps.
	releaseSlot()
	opened := time.Now()
	logRow.OpenedAt = opened.UTC().Format(time.RFC3339Nano)
	logRow.StatusCode = http.StatusSwitchingProtocols

	var bytesIn, bytesOut atomic.Int64
	var lastActivity atomic.Int64
	lastActivity.Store(opened.UnixNano())
	closeState := &wsCloseState{}
	done := make(chan struct{})
	var doneOnce sync.Once
	finish := func(code int, reason string) {
		closeState.set(code, reason)
		doneOnce.Do(func() { close(done) })
	}

	limit := cfg.MaxMessageBytes
	client.SetReadLimit(limit)
	upstream.SetReadLimit(limit)
	for _, c := range []*websocket.Conn{client, upstream} {
		conn := c
		_ = conn.SetReadDeadline(time.Now().Add(wsPeerSilenceLimit))
		// The client's pings and pongs count as activity, so a live viewer
		// of a still screen (no frames) is not cut by idle_timeout. The
		// app's pongs only prove the app is up, so they do not.
		fromClient := conn == client
		conn.SetPongHandler(func(string) error {
			if fromClient {
				lastActivity.Store(time.Now().UnixNano())
			}
			return conn.SetReadDeadline(time.Now().Add(wsPeerSilenceLimit))
		})
		conn.SetPingHandler(func(data string) error {
			if fromClient {
				lastActivity.Store(time.Now().UnixNano())
			}
			_ = conn.SetReadDeadline(time.Now().Add(wsPeerSilenceLimit))
			err := conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(wsCloseWriteWait))
			if errors.Is(err, websocket.ErrCloseSent) {
				return nil
			}
			return err
		})
		// Close frames are relayed by the pumps; do not auto-echo them.
		conn.SetCloseHandler(func(int, string) error { return nil })
	}

	relayCtx, cancelRelay := context.WithCancel(r.Context())
	defer cancelRelay()

	// One byte budget for the connection: client-to-app and app-to-client
	// bytes both draw from it.
	bucket := newByteBucket(cfg.BytesPerSecond)
	pump := func(src, dst *websocket.Conn, counter *atomic.Int64, fromClient bool) {
		for {
			mt, data, err := src.ReadMessage()
			if err != nil {
				var ce *websocket.CloseError
				switch {
				case errors.As(err, &ce):
					// Relay the peer's close code and reason unchanged.
					finish(ce.Code, ce.Text)
				case errors.Is(err, websocket.ErrReadLimit):
					if fromClient {
						finish(websocket.CloseMessageTooBig, "message exceeds the route's max_message_bytes")
					} else {
						finish(websocket.CloseMessageTooBig, "upstream message exceeds the route's max_message_bytes")
					}
				default:
					if fromClient {
						finish(websocket.CloseGoingAway, "client connection lost")
					} else {
						finish(1014, "upstream connection lost")
					}
				}
				return
			}
			_ = src.SetReadDeadline(time.Now().Add(wsPeerSilenceLimit))
			lastActivity.Store(time.Now().UnixNano())
			if err := bucket.wait(relayCtx, len(data)); err != nil {
				return
			}
			if err := dst.WriteMessage(mt, data); err != nil {
				if fromClient {
					finish(1014, "upstream connection lost")
				} else {
					finish(websocket.CloseGoingAway, "client connection lost")
				}
				return
			}
			counter.Add(int64(len(data)))
		}
	}
	go pump(client, upstream, &bytesIn, true)
	go pump(upstream, client, &bytesOut, false)

	maxDuration := time.NewTimer(time.Duration(cfg.MaxDurationSeconds) * time.Second)
	defer maxDuration.Stop()
	idle := time.Duration(cfg.IdleTimeoutSeconds) * time.Second
	// Ping often enough that a live peer's pong lands well inside the idle
	// window, and check at least four times per window.
	pingEvery := min(wsPingInterval, idle/3)
	checkEvery := min(time.Second, idle/4)
	tick := time.NewTicker(checkEvery)
	defer tick.Stop()
	lastPing := time.Now()
	ctx := r.Context()
loop:
	for {
		select {
		case <-done:
			break loop
		case <-ctx.Done():
			code, reason := wsCloseForCause(context.Cause(ctx))
			finish(code, reason)
			break loop
		case <-maxDuration.C:
			finish(wsCloseMaxDuration, "max connection duration reached")
			break loop
		case now := <-tick.C:
			if now.Sub(time.Unix(0, lastActivity.Load())) >= idle {
				finish(wsCloseIdleTimeout, "idle timeout")
				break loop
			}
			if now.Sub(lastPing) >= pingEvery {
				lastPing = now
				deadline := now.Add(wsCloseWriteWait)
				_ = client.WriteControl(websocket.PingMessage, nil, deadline)
				_ = upstream.WriteControl(websocket.PingMessage, nil, deadline)
			}
		}
	}
	cancelRelay()
	code, reason := closeState.code, closeState.reason
	deadline := time.Now().Add(wsCloseWriteWait)
	_ = client.WriteControl(websocket.CloseMessage, closePayload(code, reason), deadline)
	upCode, upReason := code, reason
	if upCode == 1014 || upCode == websocket.CloseAbnormalClosure {
		upCode, upReason = websocket.CloseGoingAway, "client connection lost"
	}
	_ = upstream.WriteControl(websocket.CloseMessage, closePayload(upCode, upReason), deadline)
	client.Close()
	upstream.Close()

	logRow.BytesIn = bytesIn.Load()
	logRow.BytesOut = bytesOut.Load()
	logRow.CloseCode = code
	logRow.CloseReason = reason
	return http.StatusSwitchingProtocols, nil
}

// closePayload builds a close frame body. 1005 and 1006 cannot be sent on
// the wire, so they become an empty close frame.
func closePayload(code int, reason string) []byte {
	if code == websocket.CloseNoStatusReceived || code == websocket.CloseAbnormalClosure || code == 0 {
		return []byte{}
	}
	return websocket.FormatCloseMessage(code, truncateCloseReason(reason))
}

// maxCloseReasonBytes is the protocol limit: a close frame body is at most
// 125 bytes, two of which are the code.
const maxCloseReasonBytes = 123

// truncateCloseReason cuts a reason to the protocol limit without splitting
// a UTF-8 character.
func truncateCloseReason(reason string) string {
	if len(reason) <= maxCloseReasonBytes {
		return reason
	}
	cut := maxCloseReasonBytes
	for cut > 0 && !utf8.RuneStart(reason[cut]) {
		cut--
	}
	return reason[:cut]
}

func wsCloseForCause(cause error) (int, string) {
	switch {
	case errors.Is(cause, errStreamCredentialRevoked):
		return wsCloseCredentialRevoked, "credential revoked"
	case errors.Is(cause, errStreamCredentialExpired):
		return wsCloseCredentialExpired, "credential expired"
	case errors.Is(cause, errStreamConfigChanged):
		return websocket.CloseServiceRestart, "route configuration changed"
	case errors.Is(cause, errStreamShutdown):
		return websocket.CloseGoingAway, "gateway shutting down"
	default:
		return websocket.CloseGoingAway, "client connection lost"
	}
}
