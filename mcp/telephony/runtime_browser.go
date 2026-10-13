package main

import (
	"bytes"
	"context"
	"crypto/hmac"
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
	"time"

	sdk "github.com/apteva/app-sdk"
)

const runtimeBrowserPrefix = "/runtime-browser"

type runtimeBrowserGrant struct {
	Token      string                `json:"-"`
	Origin     string                `json:"origin"`
	Issuer     string                `json:"issuer"`
	InstallID  int64                 `json:"install_id"`
	Project    string                `json:"project_id"`
	UserID     int64                 `json:"user_id"`
	Deployment string                `json:"deployment_id"`
	Generation int64                 `json:"generation"`
	Expires    int64                 `json:"expires_at"`
	Principal  *sdk.TrustedPrincipal `json:"principal"`
}
type runtimeBrowserSessions struct {
	mu       sync.Mutex
	grants   map[string]runtimeBrowserGrant
	reserved int
}

func (a *App) handleRuntimeEndpoints(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET only", 405)
		return
	}
	if a.runtime == nil {
		writeJSON(w, map[string]any{"managed": false})
		return
	}
	rt := a.runtime
	rt.mu.Lock()
	state := rt.state
	rt.mu.Unlock()
	writeJSON(w, map[string]any{"managed": true, "deployment_id": rt.deployment, "generation": rt.generation, "browser_api_base": a.runtimeBrowserBase("browser"), "browser_media_base": a.runtimeBrowserBase("media"), "browser_signaling_base": a.runtimeBrowserBase("signaling"), "state": state})
}

// Bootstrap is authenticated by the existing proxy. Exchange the original
// user's credentials with Server; process credentials never become browser grants.
func (a *App) handleRuntimeBrowserBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || a.runtime == nil || a.runtimeEndpoint("browser", "") == "" {
		http.Error(w, "direct browser unavailable", 409)
		return
	}
	principal, err := sdk.PrincipalFromRequest(r)
	operatorID, _ := strconv.ParseInt(r.Header.Get("X-Apteva-Operator-ID"), 10, 64)
	if principal != nil && principal.SubjectID == "" {
		operatorID = principal.UserID
	}
	// Current Server forwards platform operators without a TrustedPrincipal;
	// delegated subjects use that structure. Neither header alone mints access:
	// Server reauthenticates the original user credential below, and the returned
	// verified user ID must match this protected proxy request.
	if err != nil || operatorID <= 0 || r.Header.Get("X-Apteva-Subject-ID") != "" || principal != nil && principal.SubjectID != "" {
		http.Error(w, "managed platform user required", 403)
		return
	}
	var request struct {
		Origin string `json:"origin"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&request) != nil {
		http.Error(w, "invalid origin request", 400)
		return
	}
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if !validRuntimeURL(base, false) {
		http.Error(w, "runtime gateway unavailable", 503)
		return
	}
	body, _ := json.Marshal(map[string]any{"origin": request.Origin, "operations": []string{"read", "write", "stream"}})
	req, err := http.NewRequestWithContext(r.Context(), "POST", fmt.Sprintf("%s/api/apps/installs/%d/deployment/browser-credentials", base, a.installID), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "runtime gateway unavailable", 503)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if original := r.Header.Get("X-Apteva-Original-Authorization"); original != "" {
		req.Header.Set("Authorization", original)
	}
	if cookie := r.Header.Get("Cookie"); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if req.Header.Get("Authorization") == "" && req.Header.Get("Cookie") == "" {
		http.Error(w, "user delegation unavailable", 403)
		return
	}
	resp, err := a.runtime.http.Do(req)
	if err != nil {
		http.Error(w, "browser authorization temporarily unavailable", 503)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		http.Error(w, "browser authorization denied", resp.StatusCode)
		return
	}
	var result struct {
		Token    string `json:"token"`
		Expires  int64  `json:"expires_at"`
		Endpoint string `json:"endpoint"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&result) != nil || result.Token == "" {
		http.Error(w, "invalid browser authorization", 502)
		return
	}
	grant, err := a.authorizeRuntimeBrowser(r.Context(), result.Token, request.Origin, "read")
	if err != nil {
		writeRuntimeAuthorizationError(w, err)
		return
	}
	if grant.UserID != operatorID {
		http.Error(w, "browser identity mismatch", 403)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"token": result.Token, "expires_at": result.Expires, "endpoint": runtimeHTTPURL(a.runtimeEndpoint("browser", runtimeBrowserPrefix))})
}
func (a *App) authorizeRuntimeBrowser(ctx context.Context, token, origin, operation string) (runtimeBrowserGrant, error) {
	var out runtimeBrowserGrant
	if len(token) == 0 || len(token) > 4096 || origin == "" || a.runtime == nil {
		return out, runtimeCallbackError{Status: http.StatusForbidden}
	}
	err := a.runtime.callback(ctx, "POST", "browser-authorize", map[string]any{"token": token, "origin": origin, "operation": operation}, &out)
	if err != nil {
		return out, err
	}
	if out.InstallID != a.installID || out.Deployment != a.runtime.deployment || out.Generation != a.runtime.generation || out.Origin != origin || out.UserID <= 0 || out.Expires <= time.Now().Unix() || out.Principal != nil && (out.Principal.UserID != out.UserID || out.Principal.ProjectID != out.Project || out.Principal.SubjectID != "") {
		return out, runtimeCallbackError{Status: http.StatusForbidden}
	}
	// Fresh callback authorization is the authority, including when Server omits
	// the optional principal for a platform operator. Give existing SDK handlers
	// a short-lived verified principal without extending the browser credential.
	if out.Principal == nil {
		out.Principal = &sdk.TrustedPrincipal{Version: sdk.TrustedPrincipalVersion, UserID: out.UserID, ProjectID: out.Project}
	}
	out.Principal.ExpiresAt = min(out.Expires, time.Now().Add(time.Minute).Unix())
	out.Token = token
	return out, nil
}
func (a *App) rememberRuntimeMedia(raw []byte, grant runtimeBrowserGrant) {
	var session struct {
		Call  string `json:"call_id"`
		Token string `json:"session_token"`
		URL   string `json:"media_url"`
	}
	if json.Unmarshal(raw, &session) != nil || session.Call == "" {
		return
	}
	if session.Token == "" && session.URL != "" {
		u, err := url.Parse(session.URL)
		if err == nil {
			session.Token = u.Path[strings.LastIndex(u.Path, "/")+1:]
		}
	}
	if session.Token == "" {
		return
	}
	key := phoneHash(session.Call + ":" + session.Token)
	a.rememberRuntimeGrant(key, grant)
}
func (a *App) rememberRuntimeGrant(key string, grant runtimeBrowserGrant) {
	s := &a.runtimeBrowsers
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.grants == nil {
		s.grants = map[string]runtimeBrowserGrant{}
	}
	for k, v := range s.grants {
		if v.Expires <= time.Now().Unix() {
			delete(s.grants, k)
		}
	}
	if _, exists := s.grants[key]; !exists && len(s.grants) >= 1024 {
		return
	}
	s.grants[key] = grant
}

// Reserve capacity before a call/session action can have side effects. Existing
// leases can still renew when the map is full; a new call cannot lose its grant.
func (a *App) reserveRuntimeBrowserSession(path string) (func(), bool) {
	if !runtimeCreatesBrowserSession(path) {
		return func() {}, true
	}
	s := &a.runtimeBrowsers
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, grant := range s.grants {
		if grant.Expires <= time.Now().Unix() {
			delete(s.grants, key)
		}
	}
	if len(s.grants)+s.reserved >= 1024 {
		return nil, false
	}
	s.reserved++
	return func() { s.mu.Lock(); s.reserved--; s.mu.Unlock() }, true
}
func (a *App) runtimeMediaGrant(path string) (string, runtimeBrowserGrant, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 4 {
		return "", runtimeBrowserGrant{}, false
	}
	key := phoneHash(parts[2] + ":" + parts[3])
	s := &a.runtimeBrowsers
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.grants[key]
	return key, grant, ok
}
func (a *App) handleRuntimeBrowser(w http.ResponseWriter, r *http.Request) {
	if a.runtime == nil || a.runtimeEndpoint("browser", "") == "" {
		http.NotFound(w, r)
		return
	}
	origin := r.Header.Get("Origin")
	expected, _ := url.Parse(os.Getenv("APTEVA_PUBLIC_URL"))
	allowed := ""
	if expected != nil && expected.Host != "" {
		allowed = expected.Scheme + "://" + expected.Host
	}
	if origin == "" || origin != allowed {
		http.Error(w, "browser origin not allowed", 403)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "OPTIONS" {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.WriteHeader(204)
		return
	}
	request := r.Clone(r.Context())
	request.URL.Path = strings.TrimPrefix(r.URL.Path, runtimeBrowserPrefix)
	// Direct ingress never trusts client-supplied platform identity headers.
	for key := range request.Header {
		if strings.HasPrefix(strings.ToLower(key), "x-apteva-") {
			request.Header.Del(key)
		}
	}
	var grant runtimeBrowserGrant
	var err error
	socket := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	if socket {
		_, grant, _ = a.runtimeMediaGrant(request.URL.Path)
		if grant.Token == "" {
			http.Error(w, "runtime media authorization missing", 403)
			return
		}
		grant, err = a.authorizeRuntimeBrowser(r.Context(), grant.Token, origin, "stream")
	} else {
		op := "read"
		if r.Method != "GET" && r.Method != "HEAD" {
			op = "write"
		}
		grant, err = a.authorizeRuntimeBrowser(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), origin, op)
	}
	if err != nil {
		writeRuntimeAuthorizationError(w, err)
		return
	}
	request.Header.Set("X-Apteva-Project-ID", grant.Project)
	request.Header.Set("X-Apteva-Operator-ID", strconv.FormatInt(grant.UserID, 10))
	raw, _ := json.Marshal(grant.Principal)
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(os.Getenv("APTEVA_APP_TOKEN")))
	mac.Write([]byte(encoded))
	request.Header.Set(sdk.HeaderTrustedPrincipal, encoded)
	request.Header.Set(sdk.HeaderTrustedPrincipalSignature, hex.EncodeToString(mac.Sum(nil)))
	query := request.URL.Query()
	if grant.Project != "" {
		if p := query.Get("project_id"); p != "" && p != grant.Project {
			http.Error(w, "project mismatch", 403)
			return
		}
		query.Set("project_id", grant.Project)
	}
	request.URL.RawQuery = query.Encode()
	// Permit only the existing human-call API. Lifecycle, MCP, provider setup,
	// internal peers and arbitrary SDK endpoints are never exposed here.
	handler := a.runtimeBrowserHandler(request.URL.Path)
	if handler == nil {
		http.NotFound(w, r)
		return
	}
	if socket {
		key, _, _ := a.runtimeMediaGrant(request.URL.Path)
		ctx, cancel := context.WithCancel(request.Context())
		defer cancel()
		request = request.WithContext(ctx)
		go a.watchRuntimeBrowser(ctx, cancel, key, origin)
		handler(runtimeSocketResponse{ResponseWriter: w, ctx: ctx}, request)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, request.Body, 64<<10))
	if err != nil {
		http.Error(w, "request too large", 413)
		return
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	if request.URL.Path == "/calls/events" {
		handler(w, request)
		return
	}
	release, available := a.reserveRuntimeBrowserSession(request.URL.Path)
	if !available {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{"code": "runtime_session_capacity"})
		return
	}
	defer release()
	// Session actions return small JSON documents. Buffer just those responses
	// so operator place/answer results receive an ordinary renewable media lease
	// before the browser sees them. Lists/recordings retain native streaming.
	recorder := &runtimeBrowserResponse{ResponseWriter: w, buffered: runtimeCreatesBrowserSession(request.URL.Path)}
	handler(recorder, request)
	if recorder.buffered {
		defer recorder.finish()
		if recorder.status >= 200 && recorder.status < 300 {
			session, err := a.runtimeLeasedSession(recorder.body.Bytes(), grant)
			if err != nil {
				recorder.status = http.StatusServiceUnavailable
				recorder.body.Reset()
				recorder.body.WriteString(`{"code":"runtime_media_session_unavailable"}`)
				return
			}
			recorder.body.Reset()
			recorder.body.Write(session)
		}
	}
	if recorder.status >= 200 && recorder.status < 300 {
		a.rememberRuntimeMedia(recorder.body.Bytes(), grant)
		if strings.Contains(request.URL.Path, "/renew/") || strings.Contains(request.URL.Path, "-renew/") {
			var renewal struct {
				Token string `json:"session_token"`
			}
			if json.Unmarshal(body, &renewal) == nil && renewal.Token != "" {
				id := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
				a.rememberRuntimeGrant(phoneHash(id+":"+renewal.Token), grant)
			}
		}
	}
}

func writeRuntimeAuthorizationError(w http.ResponseWriter, err error) {
	if permanentRuntimeFailure(err) {
		http.Error(w, "runtime browser authorization denied", http.StatusForbidden)
		return
	}
	// A callback outage must remain a retryable renewal failure, not revoke an
	// established media lease. Socket monitors still enforce its verified expiry.
	w.Header().Set("Retry-After", "1")
	http.Error(w, "runtime browser authorization temporarily unavailable", http.StatusServiceUnavailable)
}

func runtimeCreatesBrowserSession(path string) bool {
	if path == "/softphone/place" {
		return true
	}
	for _, action := range []string{"answer", "attach", "takeover", "listen", "coach"} {
		if strings.HasPrefix(path, "/softphone/"+action+"/") {
			return true
		}
	}
	return false
}

func (a *App) runtimeLeasedSession(raw []byte, grant runtimeBrowserGrant) ([]byte, error) {
	var session softphoneSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, err
	}
	if session.CallID == "" || session.MediaURL == "" || session.LeaseSeconds != 0 {
		return raw, nil
	}
	unlock := a.softphones.lockClaim(session.CallID)
	defer unlock()
	row, err := a.db().findCall(session.CallID)
	if err != nil || row == nil || grant.Project != "" && row.ProjectID != grant.Project || isTerminalStatus(row.Status) || row.PeerKind != peerKindHuman || row.PeerToken == "" {
		return nil, errors.New("direct media session is no longer current")
	}
	// Legacy outbound place replies carry the token only in media_url.
	validToken := secureEqual(row.PeerToken, session.SessionToken) || session.SessionToken == "" && session.MediaURL == a.softphoneMediaURL(row.ID, row.PeerToken)
	if !validToken {
		return nil, errors.New("direct media session is no longer current")
	}
	leased, err := a.issuePhoneSession(row, nil, audioSessionCorrelation{InitiatingAction: "attach"})
	if err != nil {
		return nil, err
	}
	return json.Marshal(leased)
}
func (a *App) runtimeBrowserHandler(path string) http.HandlerFunc {
	// These routes already enforce project/operator and business permissions.
	// Reuse their wrappers instead of defining another authorization policy.
	for _, prefix := range []string{"/audio-health", "/recordings/", "/recording-settings", "/call-control-settings", "/numbers/", "/routing/", "/access/"} {
		if path == prefix || strings.HasSuffix(prefix, "/") && strings.HasPrefix(path, prefix) {
			for _, route := range a.HTTPRoutes() {
				if route.Pattern == prefix && !route.NoAuth {
					return route.Handler
				}
			}
		}
	}
	if path == "/calls/events" {
		return a.runtimeHTTP(a.handleCallNotifications, false)
	}
	if path == "/calls" {
		return a.runtimeHTTP(a.handleListCalls, false)
	}
	if strings.HasPrefix(path, "/calls/") && strings.Count(strings.Trim(path, "/"), "/") == 2 {
		return a.runtimeHTTP(a.handleCallAction, false)
	}
	if strings.HasPrefix(path, "/softphone/media/") {
		return a.runtimeHTTP(a.handleSoftphoneMedia, false)
	}
	if strings.HasPrefix(path, "/softphone/listen-media/") {
		return a.runtimeHTTP(a.handleListenMedia, false)
	}
	if strings.HasPrefix(path, "/softphone/") {
		return a.runtimeHTTP(a.handleSoftphoneAction, false)
	}
	return nil
}
func (a *App) watchRuntimeBrowser(ctx context.Context, cancel context.CancelFunc, key, origin string) {
	verifiedUntil := time.Now().Add(15 * time.Second)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s := &a.runtimeBrowsers
			s.mu.Lock()
			grant, ok := s.grants[key]
			s.mu.Unlock()
			if !ok || grant.Expires <= time.Now().Unix() || !a.runtimeOwned() {
				cancel()
				return
			}
			if _, err := a.authorizeRuntimeBrowser(ctx, grant.Token, origin, "stream"); err != nil {
				if permanentRuntimeFailure(err) || !time.Now().Before(verifiedUntil) {
					cancel()
					return
				}
			} else {
				verifiedUntil = time.Now().Add(15 * time.Second)
			}
		}
	}
}

type runtimeBrowserResponse struct {
	http.ResponseWriter
	body     bytes.Buffer
	status   int
	buffered bool
}

func (w *runtimeBrowserResponse) WriteHeader(status int) {
	w.status = status
	if !w.buffered {
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *runtimeBrowserResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.body.Len() < 64<<10 {
		w.body.Write(data[:min(len(data), (64<<10)-w.body.Len())])
	}
	if w.buffered {
		return len(data), nil
	}
	return w.ResponseWriter.Write(data)
}

func (w *runtimeBrowserResponse) finish() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(w.body.Bytes())
}
