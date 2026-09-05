package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) handleAPIs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		pid, err := a.projectFromRequest(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		rows, err := dbListAPIs(a.ctx.AppDB(), pid)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"apis": rows, "count": len(rows)})
	case http.MethodPost:
		var body map[string]any
		if err := decodeManagementBody(w, r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		ctx, err := a.managementContext(r, body)
		if err != nil {
			httpErr(w, http.StatusForbidden, err.Error())
			return
		}
		res, err := a.toolAPICreate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, res)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleAPIItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/apis/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		httpErr(w, http.StatusBadRequest, "api id or slug required")
		return
	}
	args := map[string]any{"project_id": r.URL.Query().Get("project_id")}
	if id, err := strconv.Atoi(parts[0]); err == nil {
		args["id"] = id
	} else {
		args["slug"] = parts[0]
	}
	ctx, err := a.managementContext(r, args)
	if err != nil {
		httpErr(w, http.StatusForbidden, err.Error())
		return
	}
	api, err := a.resolveAPI(ctx, args)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	if len(parts) > 1 {
		switch parts[1] {
		case "routes":
			a.handleAPIRoutes(w, r, api)
		case "keys":
			a.handleAPIKeys(w, r, api)
		case "logs":
			if r.Method != http.MethodGet {
				httpErr(w, http.StatusMethodNotAllowed, "GET")
				return
			}
			logs, err := dbListLogsBefore(a.ctx.AppDB(), api.ProjectID, api.ID, atoiDefault(r.URL.Query().Get("limit"), 100), int64(atoiDefault(r.URL.Query().Get("before_id"), 0)))
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			httpJSON(w, map[string]any{"logs": logs, "count": len(logs)})
		default:
			httpErr(w, http.StatusNotFound, "not found")
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		httpJSON(w, map[string]any{"api": api})
	case http.MethodPatch, http.MethodPut:
		var patch map[string]any
		if err := decodeManagementBody(w, r, &patch); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		patch["project_id"] = api.ProjectID
		patch["id"] = api.ID
		res, err := a.toolAPIUpdate(a.ctx.WithProject(api.ProjectID), patch)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, res)
	case http.MethodDelete:
		res, err := a.toolAPIDelete(a.ctx.WithProject(api.ProjectID), map[string]any{"project_id": api.ProjectID, "id": api.ID})
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, res)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET, PATCH, PUT, or DELETE")
	}
}

func (a *App) handleAPIRoutes(w http.ResponseWriter, r *http.Request, api *API) {
	switch r.Method {
	case http.MethodGet:
		rows, err := dbListRoutes(a.ctx.AppDB(), api.ProjectID, api.ID)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"routes": rows, "count": len(rows)})
	case http.MethodPost:
		var body map[string]any
		if err := decodeManagementBody(w, r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["project_id"] = api.ProjectID
		body["api_id"] = api.ID
		res, err := a.toolRouteAdd(a.ctx.WithProject(api.ProjectID), body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, res)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleAPIKeys(w http.ResponseWriter, r *http.Request, api *API) {
	switch r.Method {
	case http.MethodGet:
		keys, err := dbListAPIKeys(a.ctx.AppDB(), api.ProjectID, api.ID)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"keys": keys, "count": len(keys)})
	case http.MethodPost:
		var body map[string]any
		if err := decodeManagementBody(w, r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["api_id"] = api.ID
		result, err := a.toolKeyCreate(a.ctx.WithProject(api.ProjectID), body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, result)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleToolsCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	if err := decodeManagementBody(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Tool == "" {
		httpErr(w, http.StatusBadRequest, "tool required")
		return
	}
	if body.Args == nil {
		body.Args = map[string]any{}
	}
	ctx, err := a.managementContext(r, body.Args)
	if err != nil {
		httpErr(w, http.StatusForbidden, err.Error())
		return
	}

	var handler sdk.ToolHandler
	for _, t := range a.MCPTools() {
		if t.Name == body.Tool {
			handler = t.Handler
			break
		}
	}
	if handler == nil {
		httpErr(w, http.StatusNotFound, "unknown tool: "+body.Tool)
		return
	}
	out, err := handler(ctx, body.Args)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleGateway(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if a.requestSlots != nil {
		select {
		case a.requestSlots <- struct{}{}:
			defer func() { <-a.requestSlots }()
		default:
			httpErr(w, 429, "gateway concurrency limit reached")
			return
		}
	}
	if r.ContentLength > maxRequestBytes {
		httpErr(w, 413, "request body too large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(bodyTimeout))
	defer controller.SetReadDeadline(time.Time{})
	pid, err := a.projectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	host := hostOnly(r.Host)
	gwPath := strings.TrimPrefix(r.URL.EscapedPath(), "/gw")
	if gwPath == "" {
		gwPath = "/"
	}
	api, publicPath, err := a.resolvePublicAPI(r, pid, host, gwPath)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	logRow := RequestLog{ProjectID: pid, APIID: api.ID, Hostname: host, Method: r.Method, Path: publicPath, StatusCode: 500, RequestID: newRequestID()}
	defer func() {
		logRow.DurationMS = time.Since(start).Milliseconds()
		logRow.Error = redactErrorText(logRow.Error)
		enqueueRequestLog(a.ctx.AppDB(), logRow)
	}()
	if api.Status != "active" {
		logRow.StatusCode = http.StatusServiceUnavailable
		httpErr(w, http.StatusServiceUnavailable, "api disabled")
		return
	}
	requestMethod := r.Method
	preflight := r.Method == http.MethodOptions && strings.TrimSpace(r.Header.Get("Origin")) != "" &&
		strings.TrimSpace(r.Header.Get("Access-Control-Request-Method")) != ""
	if preflight {
		requestMethod = strings.ToUpper(strings.TrimSpace(r.Header.Get("Access-Control-Request-Method")))
	}
	route, params, err := dbMatchRoute(a.ctx.AppDB(), pid, api.ID, requestMethod, publicPath)
	if err != nil {
		logRow.Error = safeUpstreamError(err)
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if route == nil {
		logRow.StatusCode = http.StatusNotFound
		httpErr(w, http.StatusNotFound, "route not found")
		return
	}
	logRow.RouteID = route.ID
	logRow.TargetKind = route.TargetKind
	logRow.TargetRef = route.TargetRef
	corsWriter, handled, err := prepareGatewayCORS(w, r, api, route, requestMethod, preflight)
	if err != nil {
		logRow.StatusCode = http.StatusForbidden
		logRow.Error = safeUpstreamError(err)
		httpErr(w, http.StatusForbidden, err.Error())
		return
	}
	if handled {
		logRow.StatusCode = http.StatusNoContent
		return
	}
	w = corsWriter
	authCtx, err := a.authorizeRequest(r, api, route)
	logRow.AuthKind = authCtx.Kind
	logRow.Subject = authCtx.Subject
	if err != nil {
		logRow.StatusCode = http.StatusUnauthorized
		logRow.Error = safeUpstreamError(err)
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	w.Header().Set("X-Request-ID", logRow.RequestID)
	if route.TargetKind == "app_events" {
		// Share the mutation lock with revocation so a stream cannot register
		// after the mutation canceled its older credentials/configuration.
		a.mutationMu.Lock()
		freshAPI, loadErr := dbGetPublicAPI(a.ctx.AppDB(), pid, "id", api.ID)
		freshRoute, routeErr := dbGetRouteByID(a.ctx.AppDB(), pid, route.ID)
		if loadErr != nil || routeErr != nil || freshAPI == nil || freshRoute == nil || freshAPI.Status != "active" || !freshRoute.Enabled || *freshAPI != *api || *freshRoute != *route {
			a.mutationMu.Unlock()
			httpErr(w, 503, "stream route unavailable")
			logRow.StatusCode = 503
			return
		}
		verified, authErr := a.authorizeRequest(r, freshAPI, freshRoute)
		if authErr != nil {
			a.mutationMu.Unlock()
			httpErr(w, 401, "stream authorization rejected")
			logRow.StatusCode = 401
			return
		}
		streamCtx, done, registerErr := a.streams.register(r.Context(), pid, api.ID, route.ID, verified.KeyID, verified.ExpiresAt)
		a.mutationMu.Unlock()
		if registerErr != nil {
			httpErr(w, 429, registerErr.Error())
			logRow.StatusCode = 429
			return
		}
		defer done()
		r = r.WithContext(streamCtx)
		api = freshAPI
		route = freshRoute
		authCtx = verified
	}
	status, err := a.dispatchRoute(w, r, api, route, publicPath, params, authCtx)
	logRow.StatusCode = status
	if err != nil {
		logRow.Error = safeUpstreamError(err)
		var transfer *responseTransferError
		if errors.As(err, &transfer) {
			panic(http.ErrAbortHandler)
		}
	}
}

func (a *App) resolvePublicAPI(r *http.Request, pid, host, path string) (*API, string, error) {
	if host != "" {
		if api, err := dbGetPublicAPI(a.ctx.AppDB(), pid, "hostname", host); err != nil {
			return nil, "", err
		} else if api != nil {
			return api, path, nil
		}
	}
	parts := splitPath(path)
	if len(parts) == 0 {
		return nil, "", errors.New("api slug required")
	}
	api, err := dbGetPublicAPI(a.ctx.AppDB(), pid, "slug", parts[0])
	if err != nil || api == nil {
		return nil, "", errors.New("api not found")
	}
	rest := "/" + strings.Join(parts[1:], "/")
	if len(parts) == 1 {
		rest = "/"
	}
	return api, rest, nil
}

type authContext struct {
	Kind      string    `json:"kind"`
	Subject   string    `json:"subject"`
	KeyID     int64     `json:"-"`
	ExpiresAt time.Time `json:"-"`
}

func (a *App) authorizeRequest(r *http.Request, api *API, route *APIRoute) (authContext, error) {
	kind, err := effectiveAuthKind(api.AuthJSON, route.AuthJSON)
	if err != nil {
		return authContext{}, err
	}
	if route.TargetKind == "app_events" && (kind == "" || kind == "public") {
		return authContext{Kind: "public"}, errors.New("app_events routes require api_key or auth_jwt authentication")
	}
	switch kind {
	case "", "public":
		return authContext{Kind: "public"}, nil
	case "api_key":
		// Prefer explicit public API-key carriers over Authorization. The
		// platform app proxy authenticates itself to this sidecar by replacing
		// Authorization with the app-install token. Native EventSource cannot
		// set a header, so its ?api_key= must survive that internal hop.
		key := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if queryKey := strings.TrimSpace(r.URL.Query().Get("api_key")); queryKey != "" {
			key = queryKey
		}
		if key == "" {
			key = bearerToken(r.Header.Get("Authorization"))
		}
		keyID, ok, err := validateAPIKey(a.ctx.AppDB(), api.ProjectID, api.ID, key)
		if err != nil {
			return authContext{Kind: "api_key"}, err
		}
		if !ok {
			return authContext{Kind: "api_key"}, errors.New("invalid api key")
		}
		return authContext{Kind: "api_key", Subject: "api_key", KeyID: keyID}, nil
	case "auth_jwt":
		subject, err := a.verifyAuthJWT(r, api.ProjectID)
		return authContext{Kind: "auth_jwt", Subject: subject, ExpiresAt: bearerExpiry(r)}, err
	default:
		return authContext{Kind: kind}, errors.New("unknown auth policy")
	}
}

func (a *App) verifyAuthJWT(r *http.Request, projectID string) (string, error) {
	r, cancel := withAuthDeadline(r)
	defer cancel()
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		return "", errors.New("missing bearer token")
	}
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if base == "" {
		return "", errors.New("APTEVA_GATEWAY_URL not set for auth_jwt verification")
	}
	u := base + "/api/apps/auth/me?project_id=" + url.QueryEscape(projectID)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.performRequest(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, readErr := readBounded(resp.Body, 1<<20)
	if readErr != nil {
		return "", errors.New("invalid Auth response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.New("auth jwt rejected")
	}
	var out map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&out) != nil {
		return "", errors.New("invalid Auth response")
	}
	if user, _ := out["user"].(map[string]any); user != nil {
		if s, _ := user["id"].(string); s != "" {
			return s, nil
		}
		if n, ok := user["id"].(json.Number); ok {
			if id, err := n.Int64(); err == nil && id > 0 {
				return strconv.FormatInt(id, 10), nil
			}
		}

	}
	return "", errors.New("Auth response is missing user identity")
}

func (a *App) dispatchRoute(w http.ResponseWriter, r *http.Request, api *API, route *APIRoute, publicPath string, params map[string]string, auth authContext) (int, error) {
	if route.TargetKind == "app_events" {
		return a.dispatchAppEvents(w, r, api, route)
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(route.TimeoutMS)*time.Millisecond)
	defer cancel()
	switch route.TargetKind {
	case "function":
		return a.dispatchFunction(w, r.WithContext(ctx), api, route, publicPath, params, auth)
	case "http":
		return a.dispatchHTTP(w, r.WithContext(ctx), route, publicPath)
	case "app":
		return a.dispatchApp(w, r.WithContext(ctx), route, publicPath)
	default:
		httpErr(w, http.StatusBadGateway, "unsupported target kind")
		return http.StatusBadGateway, errors.New("unsupported target kind")
	}
}

func (a *App) dispatchFunction(w http.ResponseWriter, r *http.Request, api *API, route *APIRoute, publicPath string, params map[string]string, auth authContext) (int, error) {
	raw, readErr := readBounded(r.Body, maxFunctionBytes)
	if readErr != nil {
		code := http.StatusBadRequest
		var sizeErr *http.MaxBytesError
		if errors.As(readErr, &sizeErr) {
			code = http.StatusRequestEntityTooLarge
		}
		httpErr(w, code, "invalid or oversized function request")
		return code, readErr
	}
	event := map[string]any{
		"method":      r.Method,
		"path":        publicPath,
		"headers":     publicHeaders(r.Header),
		"query":       queryMap(sanitizedQuery(r.URL.Query())),
		"params":      params,
		"raw_body":    string(raw),
		"auth":        auth,
		"received_at": time.Now().UTC().Format(time.RFC3339),
	}
	if len(raw) > 0 {
		var body any
		if err := json.Unmarshal(raw, &body); err == nil {
			event["body"] = body
		}
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return http.StatusInternalServerError, err
	}
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	token := outboundAppToken()
	if base == "" || token == "" {
		err := errors.New("APTEVA_GATEWAY_URL/APTEVA_OUTBOUND_TOKEN required for function targets")
		httpErr(w, http.StatusBadGateway, err.Error())
		return http.StatusBadGateway, err
	}
	query := url.Values{"project_id": []string{api.ProjectID}}
	target := base + "/api/apps/callback/apps/functions/proxy/fn/" + url.PathEscape(route.TargetRef) + "?" + query.Encode()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(eventJSON))
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return http.StatusBadGateway, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := a.performRequest(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			httpErr(w, http.StatusGatewayTimeout, err.Error())
			return http.StatusGatewayTimeout, err
		}
		httpErr(w, http.StatusBadGateway, err.Error())
		return http.StatusBadGateway, err
	}
	defer resp.Body.Close()

	if functionResponseIsStreaming(resp) {
		copyUpstreamResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		if err := flushResponse(w); err != nil {
			return resp.StatusCode, err
		}
		if err := copyResponseStream(w, resp.Body); err != nil {
			return resp.StatusCode, &responseTransferError{err}
		}
		return resp.StatusCode, nil
	}

	responseBody, readErr := readBounded(resp.Body, maxFunctionResponseBytes)
	if readErr != nil {
		httpErr(w, http.StatusBadGateway, readErr.Error())
		return http.StatusBadGateway, readErr
	}
	if len(responseBody) == 0 {
		copyUpstreamResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		return resp.StatusCode, nil
	}
	rawResp := json.RawMessage(responseBody)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && json.Valid(rawResp) {
		capture := &statusCapture{ResponseWriter: w}
		if writeStructuredResponse(capture, rawResp) {
			return capture.status, nil
		}
	}
	copyUpstreamResponseHeaders(w.Header(), resp.Header)
	if json.Valid(rawResp) {
		w.Header().Set("Content-Type", "application/json")
	} else if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(resp.StatusCode)
	_, writeErr := w.Write(responseBody)
	return resp.StatusCode, writeErr
}

func outboundAppToken() string {
	if token := strings.TrimSpace(os.Getenv("APTEVA_OUTBOUND_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN"))
}

func functionResponseIsStreaming(resp *http.Response) bool {
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("X-Apteva-Function-Stream")), "true") {
		return true
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return strings.EqualFold(mediaType, "text/event-stream")
}

func functionUpstreamError(body []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && strings.TrimSpace(payload.Error) != "" {
		return payload.Error
	}
	if msg := strings.TrimSpace(string(body)); msg != "" {
		return msg
	}
	return "function invocation failed"
}

func copyUpstreamResponseHeaders(dst, src http.Header) {
	src = src.Clone()
	stripHopHeaders(src)
	for key, values := range src {
		if !proxyResponseHeaderAllowed(key) {
			continue
		}
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func proxyResponseHeaderAllowed(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "", "connection", "proxy-connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade",
		"content-length":
		return false
	default:
		return true
	}
}

func flushResponse(w http.ResponseWriter) error {
	err := http.NewResponseController(w).Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

var responseBuffers = sync.Pool{New: func() any { return new([32 * 1024]byte) }}

func copyResponseStream(w http.ResponseWriter, src io.Reader) error {
	defer http.NewResponseController(w).SetWriteDeadline(time.Time{})
	buffer := responseBuffers.Get().(*[32 * 1024]byte)
	defer responseBuffers.Put(buffer)
	buf := buffer[:]
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(15 * time.Second))
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			if err := flushResponse(w); err != nil {
				return err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func (a *App) dispatchHTTP(w http.ResponseWriter, r *http.Request, route *APIRoute, publicPath string) (int, error) {
	target, err := joinTarget(route.TargetRef, route.TargetPath, publicPath, r.URL.RawQuery)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return http.StatusBadGateway, err
	}
	return a.proxyRequest(w, r, target)
}

func (a *App) dispatchApp(w http.ResponseWriter, r *http.Request, route *APIRoute, publicPath string) (int, error) {
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	token := outboundAppToken()
	if base == "" || token == "" {
		httpErr(w, 502, "gateway configuration missing")
		return 502, errors.New("gateway configuration missing")
	}
	targetPath := route.TargetPath
	if targetPath == "" {
		targetPath = publicPath
	}
	q := sanitizedQuery(r.URL.Query())
	q.Set("project_id", route.ProjectID)
	u, err := joinTarget(base+"/api/apps/callback/apps/"+url.PathEscape(route.TargetRef)+"/proxy", targetPath, publicPath, q.Encode())
	if err != nil {
		httpErr(w, 502, "invalid app target")
		return 502, err
	}
	req, err := cloneProxyRequest(r, u)
	if err != nil {
		httpErr(w, 502, "invalid app request")
		return 502, err
	}
	// joinTarget removes public routing metadata. Set the resolved project after sanitation.
	values := req.URL.Query()
	values.Set("project_id", route.ProjectID)
	req.URL.RawQuery = values.Encode()
	req.Header.Set("Authorization", "Bearer "+token)
	return a.doProxy(w, req)
}

func (a *App) proxyRequest(w http.ResponseWriter, r *http.Request, target string) (int, error) {
	req, err := cloneProxyRequest(r, target)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return http.StatusBadGateway, err
	}
	return a.doProxy(w, req)
}

func (a *App) performRequest(req *http.Request) (*http.Response, error) {
	client := *a.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(req)
}
func (a *App) doProxy(w http.ResponseWriter, req *http.Request) (int, error) {
	resp, err := a.performRequest(req)
	if err != nil {
		code := 502
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			code = 413
		} else if errors.Is(err, context.DeadlineExceeded) {
			code = 504
		}
		httpErr(w, code, "upstream request failed")
		return code, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 101 {
		httpErr(w, 502, "protocol upgrades are not supported")
		return 502, errors.New("unsupported upstream upgrade")
	}
	copyUpstreamResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	// Known-size ordinary responses send headers with the first body chunk.
	// Streams still expose headers immediately, before an event arrives.
	if resp.ContentLength < 0 || functionResponseIsStreaming(resp) {
		if err := flushResponse(w); err != nil {
			return resp.StatusCode, &responseTransferError{err}
		}
	}
	err = copyResponseStream(w, resp.Body)
	if err != nil {
		return resp.StatusCode, &responseTransferError{err}
	}
	return resp.StatusCode, nil
}
func cloneProxyRequest(r *http.Request, target string) (*http.Request, error) {
	if r.Header.Get("Upgrade") != "" {
		return nil, errors.New("protocol upgrades are not supported")
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = r.ContentLength
	req.Header = sanitizedHeaders(r.Header)
	return req, nil
}

func copyHeaders(dst, src http.Header) {
	for k, vals := range src {
		dst.Del(k)
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func joinTarget(base, targetPath, publicPath, rawQuery string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", errors.New("invalid upstream URL")
	}
	p := targetPath
	if p == "" {
		p = publicPath
	}
	escaped := strings.TrimRight(u.EscapedPath(), "/") + "/" + strings.TrimLeft(p, "/")
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		return "", err
	}
	u.Path = decoded
	u.RawPath = escaped
	supplied, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", err
	}
	q := sanitizedQuery(u.Query())
	for k, v := range sanitizedQuery(supplied) {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func effectiveJSON(apiJSON, routeJSON string) map[string]any {
	base := parseJSONObj(apiJSON)
	route := parseJSONObj(routeJSON)
	if len(route) == 0 {
		return base
	}
	for k, v := range route {
		base[k] = v
	}
	if _, ok := route["origins"]; ok {
		delete(base, "allow_origin")
	}
	if _, ok := route["allow_origin"]; ok {
		delete(base, "origins")
	}
	if _, ok := route["credentials"]; ok {
		delete(base, "allow_credentials")
	}
	return base
}

func parseJSONObj(s string) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal([]byte(defaultJSON(s)), &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func stringFromMap(m map[string]any, key, def string) string {
	if s, _ := m[key].(string); s != "" {
		return s
	}
	return def
}

func writeStructuredResponse(w http.ResponseWriter, raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	// A body field alone is ordinary application JSON, not an envelope.
	if _, ok := obj["statusCode"]; !ok {
		return false
	}
	code := statusFromStructured(raw, 0)
	if code < 200 || code > 599 {
		httpErr(w, 502, "invalid function response status")
		return true
	}
	var headers map[string]any
	if value, ok := obj["headers"]; ok && json.Unmarshal(value, &headers) != nil {
		httpErr(w, 502, "invalid function headers")
		return true
	}
	candidate := make(http.Header)
	for key, value := range headers {
		if !validHTTPToken(key) {
			httpErr(w, 502, "invalid function header name")
			return true
		}
		switch v := value.(type) {
		case string:
			if !validHeaderValue(v) {
				httpErr(w, 502, "invalid function header value")
				return true
			}
			candidate.Set(key, v)
		case []any:
			for _, item := range v {
				str, ok := item.(string)
				if !ok {
					httpErr(w, 502, "invalid function header value")
					return true
				}
				candidate.Add(key, str)
			}
		default:
			httpErr(w, 502, "invalid function header value")
			return true
		}
	}
	copyUpstreamResponseHeaders(w.Header(), candidate)
	body := obj["body"]
	var text string
	if json.Unmarshal(body, &text) == nil {
		w.WriteHeader(code)
		if code != 204 && code != 304 {
			_, _ = io.WriteString(w, text)
		}
	} else {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(code)
		if len(body) > 0 && code != 204 && code != 304 {
			_, _ = w.Write(body)
		}
	}
	return true
}
func statusFromStructured(raw json.RawMessage, def int) int {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return def
	}
	value, ok := obj["statusCode"]
	if !ok {
		return def
	}
	var code int
	if json.Unmarshal(value, &code) != nil {
		return 0
	}
	return code
}

func publicHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for key, values := range sanitizedHeaders(h) {
		out[key] = strings.Join(values, ", ")
	}
	return out
}

func queryMap(v url.Values) map[string]any {
	out := map[string]any{}
	for k, vals := range v {
		if len(vals) == 1 {
			out[k] = vals[0]
		} else {
			out[k] = vals
		}
	}
	return out
}

func bearerToken(authz string) string {
	if strings.HasPrefix(authz, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
	}
	return ""
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

type statusCapture struct {
	http.ResponseWriter
	status int
}

func (w *statusCapture) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
