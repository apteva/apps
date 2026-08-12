package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) handleAPIs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		pid, err := projectFromRequest(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		rows, err := dbListAPIs(globalCtx.AppDB(), pid)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"apis": rows, "count": len(rows)})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		res, err := a.toolAPICreate(globalCtx, body)
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
	api, err := a.resolveAPI(globalCtx, args)
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
			logs, err := dbListLogs(globalCtx.AppDB(), api.ProjectID, api.ID, atoiDefault(r.URL.Query().Get("limit"), 100))
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
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		patch["project_id"] = api.ProjectID
		patch["id"] = api.ID
		res, err := a.toolAPIUpdate(globalCtx, patch)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, res)
	case http.MethodDelete:
		ok, err := dbDeleteAPI(globalCtx.AppDB(), api.ProjectID, api.ID)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"deleted": ok})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET, PATCH, PUT, or DELETE")
	}
}

func (a *App) handleAPIRoutes(w http.ResponseWriter, r *http.Request, api *API) {
	switch r.Method {
	case http.MethodGet:
		rows, err := dbListRoutes(globalCtx.AppDB(), api.ProjectID, api.ID)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"routes": rows, "count": len(rows)})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["project_id"] = api.ProjectID
		body["api_id"] = api.ID
		res, err := a.toolRouteAdd(globalCtx, body)
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
		keys, err := dbListAPIKeys(globalCtx.AppDB(), api.ProjectID, api.ID)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"keys": keys, "count": len(keys)})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		key, secret, err := dbCreateAPIKey(globalCtx.AppDB(), api.ProjectID, api.ID, stringArg(body, "name", "default"))
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"key": key, "secret": secret})
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
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
	if _, ok := body.Args["project_id"]; !ok {
		if pid, err := projectFromRequest(r); err == nil {
			body.Args["project_id"] = pid
		}
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
	out, err := handler(globalCtx, body.Args)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleGateway(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	pid, err := projectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	host := hostOnly(r.Host)
	gwPath := strings.TrimPrefix(r.URL.Path, "/gw")
	if gwPath == "" {
		gwPath = "/"
	}
	api, publicPath, err := a.resolvePublicAPI(r, pid, host, gwPath)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	logRow := RequestLog{ProjectID: pid, APIID: api.ID, Hostname: host, Method: r.Method, Path: publicPath, StatusCode: 500}
	defer func() {
		logRow.DurationMS = time.Since(start).Milliseconds()
		dbInsertLog(globalCtx.AppDB(), logRow)
	}()
	if api.Status != "active" {
		logRow.StatusCode = http.StatusServiceUnavailable
		httpErr(w, http.StatusServiceUnavailable, "api disabled")
		return
	}
	route, params, err := dbMatchRoute(globalCtx.AppDB(), pid, api.ID, r.Method, publicPath)
	if err != nil {
		logRow.Error = err.Error()
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
	authCtx, err := a.authorizeRequest(r, api, route)
	logRow.AuthKind = authCtx.Kind
	logRow.Subject = authCtx.Subject
	if err != nil {
		logRow.StatusCode = http.StatusUnauthorized
		logRow.Error = err.Error()
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if applyCORS(w, r, api, route) {
		logRow.StatusCode = http.StatusNoContent
		return
	}
	status, err := a.dispatchRoute(w, r, api, route, publicPath, params, authCtx)
	logRow.StatusCode = status
	if err != nil {
		logRow.Error = err.Error()
	}
}

func (a *App) resolvePublicAPI(r *http.Request, pid, host, path string) (*API, string, error) {
	if host != "" {
		if api, err := dbGetAPIByHostname(globalCtx.AppDB(), pid, host); err != nil {
			return nil, "", err
		} else if api != nil {
			return api, path, nil
		}
	}
	parts := splitPath(path)
	if len(parts) == 0 {
		return nil, "", errors.New("api slug required")
	}
	api, err := dbGetAPIBySlug(globalCtx.AppDB(), pid, parts[0])
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
	Kind    string
	Subject string
}

func (a *App) authorizeRequest(r *http.Request, api *API, route *APIRoute) (authContext, error) {
	policy := effectiveJSON(api.AuthJSON, route.AuthJSON)
	kind := stringFromMap(policy, "kind", "public")
	if route.TargetKind == "app_events" && (kind == "" || kind == "public") {
		return authContext{Kind: "public"}, errors.New("app_events routes require api_key or auth_jwt authentication")
	}
	switch kind {
	case "", "public":
		return authContext{Kind: "public"}, nil
	case "api_key":
		key := bearerToken(r.Header.Get("Authorization"))
		if key == "" {
			key = r.Header.Get("X-API-Key")
		}
		if key == "" {
			key = r.URL.Query().Get("api_key")
		}
		ok, err := dbValidateAPIKey(globalCtx.AppDB(), api.ProjectID, api.ID, key)
		if err != nil {
			return authContext{Kind: "api_key"}, err
		}
		if !ok {
			return authContext{Kind: "api_key"}, errors.New("invalid api key")
		}
		return authContext{Kind: "api_key", Subject: "api_key"}, nil
	case "auth_jwt":
		subject, err := a.verifyAuthJWT(r, api.ProjectID)
		return authContext{Kind: "auth_jwt", Subject: subject}, err
	default:
		return authContext{Kind: kind}, errors.New("unknown auth policy")
	}
}

func (a *App) verifyAuthJWT(r *http.Request, projectID string) (string, error) {
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
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.New("auth jwt rejected")
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if user, _ := out["user"].(map[string]any); user != nil {
		if s, _ := user["id"].(string); s != "" {
			return s, nil
		}
		if f, _ := user["id"].(float64); f != 0 {
			return strconv.FormatInt(int64(f), 10), nil
		}
		if email, _ := user["email"].(string); email != "" {
			return email, nil
		}
	}
	return "auth_user", nil
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
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	event := map[string]any{
		"method":      r.Method,
		"path":        publicPath,
		"headers":     publicHeaders(r.Header),
		"query":       queryMap(r.URL.Query()),
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

	resp, err := a.httpClient.Do(req)
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
			return resp.StatusCode, err
		}
		return resp.StatusCode, nil
	}

	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if readErr != nil {
		httpErr(w, http.StatusBadGateway, readErr.Error())
		return http.StatusBadGateway, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := functionUpstreamError(responseBody)
		httpErr(w, http.StatusBadGateway, msg)
		return http.StatusBadGateway, errors.New(msg)
	}
	if len(responseBody) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return http.StatusNoContent, nil
	}
	rawResp := json.RawMessage(responseBody)
	if json.Valid(rawResp) && writeStructuredResponse(w, rawResp) {
		return statusFromStructured(rawResp, http.StatusOK), nil
	}
	copyUpstreamResponseHeaders(w.Header(), resp.Header)
	if json.Valid(rawResp) {
		w.Header().Set("Content-Type", "application/json")
	} else if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(responseBody)
	return resp.StatusCode, nil
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

func copyResponseStream(w http.ResponseWriter, src io.Reader) error {
	buf := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
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
	token := os.Getenv("APTEVA_APP_TOKEN")
	if base == "" || token == "" {
		httpErr(w, http.StatusBadGateway, "APTEVA_GATEWAY_URL/APTEVA_APP_TOKEN required for app targets")
		return http.StatusBadGateway, errors.New("gateway env missing")
	}
	targetPath := route.TargetPath
	if targetPath == "" {
		targetPath = publicPath
	}
	u := base + "/api/apps/" + url.PathEscape(route.TargetRef) + targetPath
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	req, err := cloneProxyRequest(r, u)
	if err != nil {
		return http.StatusBadGateway, err
	}
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

func (a *App) doProxy(w http.ResponseWriter, req *http.Request) (int, error) {
	resp, err := a.httpClient.Do(req)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return http.StatusBadGateway, err
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return resp.StatusCode, nil
}

func cloneProxyRequest(r *http.Request, target string) (*http.Request, error) {
	var body io.Reader
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		return nil, err
	}
	copyHeaders(req.Header, r.Header)
	req.Header.Del("Host")
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
	p := targetPath
	if p == "" {
		p = publicPath
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(p, "/")
	u.RawQuery = rawQuery
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
	return base
}

func parseJSONObj(s string) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal([]byte(defaultJSON(s)), &out)
	return out
}

func stringFromMap(m map[string]any, key, def string) string {
	if s, _ := m[key].(string); s != "" {
		return s
	}
	return def
}

func applyCORS(w http.ResponseWriter, r *http.Request, api *API, route *APIRoute) bool {
	policy := effectiveJSON(api.CORSJSON, route.CORSJSON)
	if len(policy) == 0 || stringFromMap(policy, "enabled", "") == "false" {
		return false
	}
	origin := r.Header.Get("Origin")
	allow := stringFromMap(policy, "allow_origin", "*")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", allow)
	}
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Headers", stringFromMap(policy, "allow_headers", "authorization, content-type, x-api-key"))
	w.Header().Set("Access-Control-Allow-Methods", stringFromMap(policy, "allow_methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS"))
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

func writeStructuredResponse(w http.ResponseWriter, raw json.RawMessage) bool {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	if _, ok := obj["statusCode"]; !ok {
		if _, ok := obj["body"]; !ok {
			return false
		}
	}
	code := statusFromStructured(raw, http.StatusOK)
	if headers, _ := obj["headers"].(map[string]any); headers != nil {
		for k, v := range headers {
			if s, ok := v.(string); ok {
				w.Header().Set(k, s)
			}
		}
	}
	body := obj["body"]
	if code != 0 {
		w.WriteHeader(code)
	}
	switch b := body.(type) {
	case string:
		_, _ = w.Write([]byte(b))
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(b)
	}
	return true
}

func statusFromStructured(raw json.RawMessage, def int) int {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return def
	}
	if f, _ := obj["statusCode"].(float64); f != 0 {
		return int(f)
	}
	return def
}

func publicHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, vals := range h {
		l := strings.ToLower(k)
		if l == "authorization" || l == "cookie" || l == "set-cookie" {
			continue
		}
		out[k] = strings.Join(vals, ", ")
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
	if i := strings.IndexByte(h, ':'); i >= 0 {
		return h[:i]
	}
	return h
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
