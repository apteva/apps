package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: fetch
display_name: Fetch
version: 0.1.2
description: Controlled HTTP client for agents and operators.
author: Apteva
icon: /ui/icon.svg
icon_style: monochrome
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: fetch_request
      description: "Send one controlled HTTP request. Args: method, url, headers?, query?, body?, environment_id?, variables?, timeout_ms?, follow_redirects?, max_response_bytes?, save_history?."
  ui_panels:
    - slot: project.page
      label: Fetch
      icon: send
      entry: /ui/FetchPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/fetch
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/fetch.db
  migrations: migrations/
config_schema:
  - name: allowed_hosts
    type: text
    label: Allowed hosts
  - name: blocked_hosts
    type: text
    label: Blocked hosts
  - name: allow_private_networks
    type: bool
    default: false
    label: Allow private networks
  - name: allow_loopback
    type: bool
    default: false
    label: Allow loopback
  - name: default_timeout_ms
    type: number
    default: 15000
    label: Default timeout (ms)
  - name: max_response_bytes
    type: number
    default: 1048576
    label: Max response bytes
  - name: history_max_entries
    type: number
    default: 1000
    label: History retention
upgrade_policy: auto-patch
`

type App struct {
	runtimeMu sync.Mutex
	transport *http.Transport
	secrets   *secretCodec
	migrated  bool
}

const (
	maxInboundJSONBytes  = 2 << 20
	maxRequestBodyBytes  = 5 << 20
	maxResponseBodyBytes = 5 << 20
	maxURLBytes          = 16 << 10
	maxHeaderBytes       = 64 << 10
	maxHeaderCount       = 200
	maxQueryBytes        = 64 << 10
)

var privateLikePrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
}

var alwaysBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("fetch app requires a db block")
	}
	globalCtx = ctx
	if err := a.ensureRuntime(ctx); err != nil {
		return err
	}
	ctx.Logger().Info("fetch mounted", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	if a.transport != nil {
		a.transport.CloseIdleConnections()
	}
	return nil
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) ensureRuntime(ctx *sdk.AppCtx) error {
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	if a.secrets == nil {
		codec, err := loadSecretCodec(ctx)
		if err != nil {
			return err
		}
		a.secrets = codec
	}
	if a.transport == nil {
		a.transport = newSafeTransport(ctx)
	}
	if !a.migrated {
		if err := migrateLegacySecrets(ctx.AppDB(), a.secrets, ctx.Logger()); err != nil {
			return err
		}
		a.migrated = true
	}
	return nil
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/execute", Handler: a.handleExecute},
		{Pattern: "/requests", Handler: a.handleRequests},
		{Pattern: "/requests/", Handler: a.handleRequestItem},
		{Pattern: "/environments", Handler: a.handleEnvironments},
		{Pattern: "/environments/", Handler: a.handleEnvironmentItem},
		{Pattern: "/history", Handler: a.handleHistory},
		{Pattern: "/history/", Handler: a.handleHistoryItem},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{{
		Name:        "fetch_request",
		Description: "Send one controlled HTTP request. Args: method, url, headers?, query?, body? ({json|text|base64|form}), environment_id?, variables?, timeout_ms?, follow_redirects?, max_response_bytes?, save_history?. Private/link-local/loopback IPs are blocked unless the install config explicitly allows private networks.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"method":             map[string]any{"type": "string"},
				"url":                map[string]any{"type": "string"},
				"headers":            map[string]any{"type": "object"},
				"query":              map[string]any{"type": "object"},
				"body":               map[string]any{"type": "object"},
				"environment_id":     map[string]any{"type": "integer"},
				"variables":          map[string]any{"type": "object"},
				"timeout_ms":         map[string]any{"type": "integer"},
				"follow_redirects":   map[string]any{"type": "boolean"},
				"max_response_bytes": map[string]any{"type": "integer"},
				"save_history":       map[string]any{"type": "boolean"},
			},
			"required": []string{"method", "url"},
		},
		Handler: a.toolFetchRequest,
	}}
}

type SavedRequest struct {
	ID            int64           `json:"id"`
	ProjectID     string          `json:"project_id,omitempty"`
	Slug          string          `json:"slug"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Method        string          `json:"method"`
	URLTemplate   string          `json:"url_template"`
	Headers       json.RawMessage `json:"headers,omitempty"`
	Query         json.RawMessage `json:"query,omitempty"`
	Body          json.RawMessage `json:"body,omitempty"`
	EnvironmentID *int64          `json:"environment_id,omitempty"`
	CreatedAt     string          `json:"created_at,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
}

type Environment struct {
	ID        int64            `json:"id"`
	ProjectID string           `json:"project_id,omitempty"`
	Slug      string           `json:"slug"`
	Name      string           `json:"name"`
	Vars      []EnvironmentVar `json:"vars,omitempty"`
	CreatedAt string           `json:"created_at,omitempty"`
	UpdatedAt string           `json:"updated_at,omitempty"`
}

type EnvironmentVar struct {
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
	IsSecret  bool   `json:"is_secret"`
	HasValue  bool   `json:"has_value,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type FetchResult struct {
	Status        int               `json:"status"`
	StatusText    string            `json:"status_text"`
	Headers       map[string]string `json:"headers"`
	ContentType   string            `json:"content_type,omitempty"`
	BodyBytes     int               `json:"body_bytes,omitempty"`
	BodyText      string            `json:"body_text,omitempty"`
	BodyJSON      any               `json:"body_json,omitempty"`
	BodyBase64    string            `json:"body_base64,omitempty"`
	BodyTruncated bool              `json:"body_truncated"`
	DurationMS    int64             `json:"duration_ms"`
	FinalURL      string            `json:"final_url"`
	HistoryID     int64             `json:"history_id,omitempty"`
}

type HistoryRow struct {
	ID                   int64           `json:"id"`
	SavedRequestID       *int64          `json:"saved_request_id,omitempty"`
	Source               string          `json:"source,omitempty"`
	Method               string          `json:"method"`
	URL                  string          `json:"url"`
	RedactedRequestJSON  json.RawMessage `json:"redacted_request,omitempty"`
	Status               int             `json:"status,omitempty"`
	RedactedResponseJSON json.RawMessage `json:"redacted_response,omitempty"`
	DurationMS           int64           `json:"duration_ms,omitempty"`
	Error                string          `json:"error,omitempty"`
	CreatedAt            string          `json:"created_at"`
}

type executeOptions struct {
	source         string
	savedRequestID int64
	defaultSave    bool
	requestCtx     context.Context
	historyMax     int
}

func (a *App) toolFetchRequest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	return a.execute(ctx, pid, args, executeOptions{source: "agent", defaultSave: true})
}

func (a *App) execute(ctx *sdk.AppCtx, pid string, args map[string]any, opts executeOptions) (*FetchResult, error) {
	if err := a.ensureRuntime(ctx); err != nil {
		return nil, err
	}
	if opts.historyMax <= 0 {
		opts.historyMax = configInt(ctx, "history_max_entries", 1000)
	}
	if opts.historyMax <= 0 {
		opts.historyMax = 1000
	}
	if opts.historyMax > 10000 {
		opts.historyMax = 10000
	}
	reqSpec, secretValues, err := normalizeRequestSpec(ctx, pid, args, a.secrets)
	if err != nil {
		historyID := maybeRecordHistory(ctx.AppDB(), pid, opts, args, nil, 0, err, secretValues)
		return &FetchResult{HistoryID: historyID}, err
	}
	start := time.Now()
	requestCtx := opts.requestCtx
	if requestCtx == nil {
		requestCtx = context.Background()
	}
	result, err := a.doFetch(requestCtx, ctx, reqSpec)
	duration := time.Since(start).Milliseconds()
	err = sanitizeRunError(err, reqSpec.URL, reqSpec.SafeURL, secretValues)
	if result != nil {
		result.DurationMS = duration
		result.FinalURL = redactURL(result.FinalURL, secretValues)
	}
	save := opts.defaultSave
	if v, ok := args["save_history"].(bool); ok {
		save = v
	}
	if save {
		historyID := recordHistory(ctx.AppDB(), pid, opts, reqSpec.historyRequest(), result, duration, err, secretValues)
		if result != nil {
			result.HistoryID = historyID
		}
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

type requestSpec struct {
	Method          string
	URL             string
	SafeURL         string
	Headers         map[string]string
	BodyBytes       []byte
	Timeout         time.Duration
	FollowRedirects bool
	MaxBytes        int64
}

func (r requestSpec) historyRequest() map[string]any {
	return map[string]any{
		"method":     r.Method,
		"url":        r.SafeURL,
		"headers":    r.Headers,
		"body_bytes": len(r.BodyBytes),
	}
}

func normalizeRequestSpec(ctx *sdk.AppCtx, pid string, args map[string]any, codec *secretCodec) (requestSpec, []string, error) {
	var secrets []string
	vars := map[string]string{}
	secretKeys := map[string]bool{}
	if envID := int64Arg(args, "environment_id"); envID != 0 {
		envVars, secretVals, err := dbEnvironmentVars(ctx.AppDB(), pid, envID, true, codec)
		if err != nil {
			return requestSpec{}, nil, err
		}
		for _, v := range envVars {
			vars[v.Key] = v.Value
			if v.IsSecret {
				secretKeys[v.Key] = true
			}
		}
		secrets = append(secrets, secretVals...)
	}
	if raw, ok := args["variables"].(map[string]any); ok {
		for k, v := range raw {
			value := fmt.Sprint(v)
			vars[k] = value
			if secretKeys[k] || isSensitiveName(k) {
				secrets = append(secrets, value)
			}
		}
	}

	method := strings.ToUpper(strings.TrimSpace(strArg(args, "method")))
	if method == "" {
		method = http.MethodGet
	}
	if !validMethod(method) {
		return requestSpec{}, secrets, fmt.Errorf("unsupported method %q", method)
	}
	rawURL := interpolate(strArg(args, "url"), vars)
	if len(rawURL) > maxURLBytes {
		return requestSpec{}, secrets, fmt.Errorf("url exceeds %d bytes", maxURLBytes)
	}
	u, err := url.Parse(rawURL)
	if err != nil || u == nil || u.Scheme == "" || u.Host == "" {
		return requestSpec{}, secrets, errors.New("valid absolute http(s) url required")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return requestSpec{}, secrets, errors.New("url scheme must be http or https")
	}
	if err := validateHostPolicy(ctx, u.Hostname()); err != nil {
		return requestSpec{}, secrets, err
	}
	query := u.Query()
	if q, ok := args["query"].(map[string]any); ok {
		for k, v := range q {
			query.Set(k, interpolate(fmt.Sprint(v), vars))
		}
		u.RawQuery = query.Encode()
	}
	if len(u.RawQuery) > maxQueryBytes {
		return requestSpec{}, secrets, fmt.Errorf("query exceeds %d bytes", maxQueryBytes)
	}

	headers := map[string]string{}
	if h, ok := args["headers"].(map[string]any); ok {
		if len(h) > maxHeaderCount {
			return requestSpec{}, secrets, fmt.Errorf("header count exceeds %d", maxHeaderCount)
		}
		headerBytes := 0
		for k, v := range h {
			key := http.CanonicalHeaderKey(strings.TrimSpace(k))
			if key == "" {
				continue
			}
			value := interpolate(fmt.Sprint(v), vars)
			headerBytes += len(key) + len(value)
			if headerBytes > maxHeaderBytes {
				return requestSpec{}, secrets, fmt.Errorf("headers exceed %d bytes", maxHeaderBytes)
			}
			headers[key] = value
		}
	}

	body, contentType, err := buildBody(args["body"], vars)
	if err != nil {
		return requestSpec{}, secrets, err
	}
	if contentType != "" && headers["Content-Type"] == "" {
		headers["Content-Type"] = contentType
	}
	timeout := time.Duration(intArg(args, "timeout_ms", configInt(ctx, "default_timeout_ms", 15000))) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	maxBytes := int64(intArg(args, "max_response_bytes", configInt(ctx, "max_response_bytes", 1<<20)))
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	if maxBytes > maxResponseBodyBytes {
		maxBytes = maxResponseBodyBytes
	}
	return requestSpec{
		Method:          method,
		URL:             u.String(),
		SafeURL:         redactURL(u.String(), secrets),
		Headers:         headers,
		BodyBytes:       body,
		Timeout:         timeout,
		FollowRedirects: boolArgDefault(args, "follow_redirects", true),
		MaxBytes:        maxBytes,
	}, secrets, nil
}

func (a *App) doFetch(requestCtx context.Context, ctx *sdk.AppCtx, spec requestSpec) (*FetchResult, error) {
	client := &http.Client{
		Timeout:   spec.Timeout,
		Transport: a.transport,
	}
	if !spec.FollowRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	} else {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			if len(via) > 0 {
				previous := via[len(via)-1].URL
				if previous.Scheme == "https" && req.URL.Scheme == "http" {
					return errors.New("https to http redirect blocked")
				}
				if !sameOrigin(previous, req.URL) {
					for key := range req.Header {
						if !safeCrossOriginHeader(key) {
							req.Header.Del(key)
						}
					}
				}
			}
			return validateHostPolicy(ctx, req.URL.Hostname())
		}
	}
	var body io.Reader
	if len(spec.BodyBytes) > 0 {
		body = bytes.NewReader(spec.BodyBytes)
	}
	req, err := http.NewRequestWithContext(requestCtx, spec.Method, spec.URL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range spec.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, spec.MaxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	truncated := int64(len(data)) > spec.MaxBytes
	if truncated {
		data = data[:spec.MaxBytes]
	}
	headers := map[string]string{}
	for k, vals := range resp.Header {
		headers[http.CanonicalHeaderKey(k)] = redactHeaderValue(k, strings.Join(vals, ", "))
	}
	out := &FetchResult{
		Status:        resp.StatusCode,
		StatusText:    resp.Status,
		Headers:       headers,
		ContentType:   resp.Header.Get("Content-Type"),
		BodyBytes:     len(data),
		BodyTruncated: truncated,
		FinalURL:      resp.Request.URL.String(),
	}
	ctype := resp.Header.Get("Content-Type")
	if isTextLike(ctype, data) {
		text := string(data)
		out.BodyText = text
		if strings.Contains(strings.ToLower(ctype), "json") || json.Valid(data) {
			var j any
			if json.Unmarshal(data, &j) == nil {
				out.BodyJSON = j
			}
		}
	} else if len(data) > 0 {
		out.BodyBase64 = base64.StdEncoding.EncodeToString(data)
	}
	return out, nil
}

func newSafeTransport(ctx *sdk.AppCtx) *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		DialContext: func(c context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if err := validateHostPolicy(ctx, host); err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(c, host)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, ipAddr := range ips {
				ip, ok := netip.AddrFromSlice(ipAddr.IP)
				if !ok {
					continue
				}
				ip = ip.Unmap()
				if err := validateIPPolicy(ctx, ip); err != nil {
					lastErr = err
					continue
				}
				conn, err := dialer.DialContext(c, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, errors.New("host resolved no usable addresses")
		},
	}
}

func sameOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func safeCrossOriginHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Accept", "Accept-Language", "Content-Type", "User-Agent":
		return true
	default:
		return false
	}
}

func validateHostPolicy(ctx *sdk.AppCtx, host string) error {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "" {
		return errors.New("host required")
	}
	if blocked := hostList(ctx, "blocked_hosts"); len(blocked) > 0 && hostMatchesAny(host, blocked) {
		return fmt.Errorf("host %q is blocked by install policy", host)
	}
	if allowed := hostList(ctx, "allowed_hosts"); len(allowed) > 0 && !hostMatchesAny(host, allowed) {
		return fmt.Errorf("host %q is not in allowed_hosts", host)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return validateIPPolicy(ctx, ip.Unmap())
	}
	return nil
}

func validateIPPolicy(ctx *sdk.AppCtx, ip netip.Addr) error {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || prefixContains(alwaysBlockedPrefixes, ip) {
		return fmt.Errorf("blocked non-routable or special-use address %s", ip)
	}
	if ip.IsLoopback() {
		if configBool(ctx, "allow_loopback", false) {
			return nil
		}
		return fmt.Errorf("blocked loopback address %s", ip)
	}
	if ip == netip.MustParseAddr("fd00:ec2::254") {
		return errors.New("blocked metadata address")
	}
	if ip.IsPrivate() || prefixContains(privateLikePrefixes, ip) {
		if configBool(ctx, "allow_private_networks", false) {
			return nil
		}
		return fmt.Errorf("blocked private address %s", ip)
	}
	return nil
}

func prefixContains(prefixes []netip.Prefix, ip netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func buildBody(raw any, vars map[string]string) ([]byte, string, error) {
	if raw == nil {
		return nil, "", nil
	}
	body, ok := raw.(map[string]any)
	if !ok {
		return nil, "", errors.New("body must be an object with json, text, base64, or form")
	}
	switch {
	case body["json"] != nil:
		resolved := interpolateAny(body["json"], vars)
		b, err := json.Marshal(resolved)
		return boundedRequestBody(b, "application/json", err)
	case body["text"] != nil:
		return boundedRequestBody([]byte(interpolate(fmt.Sprint(body["text"]), vars)), "text/plain; charset=utf-8", nil)
	case body["base64"] != nil:
		encoded := fmt.Sprint(body["base64"])
		if len(encoded) > base64.StdEncoding.EncodedLen(maxRequestBodyBytes) {
			return nil, "", fmt.Errorf("request body exceeds %d bytes", maxRequestBodyBytes)
		}
		b, err := base64.StdEncoding.DecodeString(encoded)
		return boundedRequestBody(b, "application/octet-stream", err)
	case body["form"] != nil:
		form, ok := body["form"].(map[string]any)
		if !ok {
			return nil, "", errors.New("body.form must be an object")
		}
		vals := url.Values{}
		for k, v := range form {
			vals.Set(k, interpolate(fmt.Sprint(v), vars))
		}
		return boundedRequestBody([]byte(vals.Encode()), "application/x-www-form-urlencoded", nil)
	default:
		return nil, "", nil
	}
}

func boundedRequestBody(body []byte, contentType string, err error) ([]byte, string, error) {
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxRequestBodyBytes {
		return nil, "", fmt.Errorf("request body exceeds %d bytes", maxRequestBodyBytes)
	}
	return body, contentType, nil
}

func maybeRecordHistory(db *sql.DB, pid string, opts executeOptions, input map[string]any, result *FetchResult, duration int64, err error, secrets []string) int64 {
	save := opts.defaultSave
	if v, ok := input["save_history"].(bool); ok {
		save = v
	}
	if !save {
		return 0
	}
	req := map[string]any{
		"method": strArg(input, "method"),
		"url":    redactURL(strArg(input, "url"), secrets),
	}
	return recordHistory(db, pid, opts, req, result, duration, err, secrets)
}

func recordHistory(db *sql.DB, pid string, opts executeOptions, req map[string]any, result *FetchResult, duration int64, runErr error, secrets []string) int64 {
	method := strings.ToUpper(fmt.Sprint(req["method"]))
	rawURL := redactURL(fmt.Sprint(req["url"]), secrets)
	reqJSON, _ := json.Marshal(redactAny(req, secrets))
	var respJSON []byte
	var status int
	if result != nil {
		status = result.Status
		respJSON, _ = json.Marshal(redactAny(map[string]any{
			"status":         result.Status,
			"headers":        result.Headers,
			"body_text":      preview(result.BodyText, 4096),
			"body_truncated": result.BodyTruncated,
			"final_url":      result.FinalURL,
		}, secrets))
	}
	var errText string
	if runErr != nil {
		errText = runErr.Error()
	}
	var saved any
	if opts.savedRequestID != 0 {
		saved = opts.savedRequestID
	}
	res, err := db.Exec(
		`INSERT INTO fetch_history
			(project_id, saved_request_id, source, method, url, redacted_request_json,
			 status, redacted_response_json, duration_ms, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, saved, opts.source, method, rawURL, string(reqJSON), status, string(respJSON), duration, errText,
	)
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	if opts.historyMax > 0 {
		_, _ = db.Exec(
			`DELETE FROM fetch_history
			 WHERE project_id=? AND id NOT IN (
				SELECT id FROM fetch_history WHERE project_id=? ORDER BY id DESC LIMIT ?
			 )`,
			pid, pid, opts.historyMax,
		)
	}
	return id
}

func dbSavedCreate(db *sql.DB, pid string, s *SavedRequest, codec *secretCodec) (*SavedRequest, error) {
	if s.Slug == "" {
		s.Slug = slugify(s.Name)
	}
	if s.Slug == "" || s.Name == "" || s.Method == "" || s.URLTemplate == "" {
		return nil, errors.New("slug/name/method/url_template required")
	}
	if !validMethod(strings.ToUpper(s.Method)) {
		return nil, fmt.Errorf("unsupported method %q", s.Method)
	}
	if err := validateSavedURL(s.URLTemplate); err != nil {
		return nil, err
	}
	var err error
	if s.Headers, err = protectRawJSON(s.Headers, codec, savedAAD(pid, "headers")); err != nil {
		return nil, err
	}
	if s.Query, err = protectRawJSON(s.Query, codec, savedAAD(pid, "query")); err != nil {
		return nil, err
	}
	if s.Body, err = protectRawJSON(s.Body, codec, savedAAD(pid, "body")); err != nil {
		return nil, err
	}
	res, err := db.Exec(
		`INSERT INTO fetch_saved_requests
			(project_id, slug, name, description, method, url_template, headers_json, query_json, body_json, environment_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, s.Slug, s.Name, nullStr(s.Description), strings.ToUpper(s.Method), s.URLTemplate,
		rawOrNull(s.Headers), rawOrNull(s.Query), rawOrNull(s.Body), nullableInt(s.EnvironmentID),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbSavedGet(db, pid, id, codec, false)
}

func dbSavedList(db *sql.DB, pid string, codec *secretCodec) ([]*SavedRequest, error) {
	rows, err := db.Query(
		`SELECT id, slug, name, COALESCE(description,''), method, url_template,
				COALESCE(headers_json,''), COALESCE(query_json,''), COALESCE(body_json,''),
				environment_id, created_at, updated_at
		 FROM fetch_saved_requests
		 WHERE project_id=? AND archived_at IS NULL
		 ORDER BY updated_at DESC, id DESC`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SavedRequest{}
	for rows.Next() {
		s, err := scanSaved(rows)
		if err != nil {
			return nil, err
		}
		public, err := transformSavedJSON(s, codec, pid, false)
		if err != nil {
			return nil, err
		}
		out = append(out, public)
	}
	return out, rows.Err()
}

func dbSavedGet(db *sql.DB, pid string, id int64, codec *secretCodec, reveal bool) (*SavedRequest, error) {
	s, err := dbSavedGetRaw(db, pid, id)
	if err != nil || s == nil {
		return s, err
	}
	return transformSavedJSON(s, codec, pid, reveal)
}

func dbSavedGetRaw(db *sql.DB, pid string, id int64) (*SavedRequest, error) {
	row := db.QueryRow(
		`SELECT id, slug, name, COALESCE(description,''), method, url_template,
				COALESCE(headers_json,''), COALESCE(query_json,''), COALESCE(body_json,''),
				environment_id, created_at, updated_at
		 FROM fetch_saved_requests
		 WHERE project_id=? AND id=? AND archived_at IS NULL`,
		pid, id,
	)
	s, err := scanSaved(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func dbSavedUpdate(db *sql.DB, pid string, id int64, patch map[string]any, codec *secretCodec) (*SavedRequest, error) {
	current, err := dbSavedGetRaw(db, pid, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errors.New("request not found")
	}
	sets := []string{}
	args := []any{}
	allowed := map[string]bool{
		"name": true, "description": true, "method": true, "url_template": true,
		"headers": true, "query": true, "body": true, "environment_id": true,
	}
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		col := k
		if k == "headers" || k == "query" || k == "body" {
			col = k + "_json"
			b, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			var old json.RawMessage
			switch k {
			case "headers":
				old = current.Headers
			case "query":
				old = current.Query
			case "body":
				old = current.Body
			}
			protected, err := protectSavedPatch(b, old, codec, savedAAD(pid, k))
			if err != nil {
				return nil, err
			}
			v = rawOrNull(protected)
		}
		if k == "method" {
			v = strings.ToUpper(fmt.Sprint(v))
			if !validMethod(fmt.Sprint(v)) {
				return nil, fmt.Errorf("unsupported method %q", v)
			}
		}
		if k == "url_template" {
			if err := validateSavedURL(fmt.Sprint(v)); err != nil {
				return nil, err
			}
		}
		sets = append(sets, col+"=?")
		args = append(args, v)
	}
	if len(sets) == 0 {
		return dbSavedGet(db, pid, id, codec, false)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	args = append(args, pid, id)
	_, err = db.Exec(`UPDATE fetch_saved_requests SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, args...)
	if err != nil {
		return nil, err
	}
	return dbSavedGet(db, pid, id, codec, false)
}

func savedAAD(pid, field string) string {
	return "fetch/saved/" + pid + "/" + field
}

func protectSavedPatch(next, current json.RawMessage, codec *secretCodec, aad string) (json.RawMessage, error) {
	var nextValue, currentValue any
	if len(next) > 0 {
		if err := json.Unmarshal(next, &nextValue); err != nil {
			return nil, err
		}
	}
	if len(current) > 0 {
		_ = json.Unmarshal(current, &currentValue)
	}
	merged, err := json.Marshal(mergeSecretMasks(nextValue, currentValue))
	if err != nil {
		return nil, err
	}
	return protectRawJSON(merged, codec, aad)
}

func transformSavedJSON(s *SavedRequest, codec *secretCodec, pid string, reveal bool) (*SavedRequest, error) {
	copy := *s
	if reveal {
		var err error
		if copy.Headers, err = revealRawJSON(copy.Headers, codec, savedAAD(pid, "headers")); err != nil {
			return nil, err
		}
		if copy.Query, err = revealRawJSON(copy.Query, codec, savedAAD(pid, "query")); err != nil {
			return nil, err
		}
		if copy.Body, err = revealRawJSON(copy.Body, codec, savedAAD(pid, "body")); err != nil {
			return nil, err
		}
	} else {
		copy.Headers = publicRawJSON(copy.Headers)
		copy.Query = publicRawJSON(copy.Query)
		copy.Body = publicRawJSON(copy.Body)
		copy.URLTemplate = publicSavedURL(copy.URLTemplate)
	}
	return &copy, nil
}

func scanSaved(row interface{ Scan(...any) error }) (*SavedRequest, error) {
	s := &SavedRequest{}
	var headers, query, body string
	var env sql.NullInt64
	if err := row.Scan(&s.ID, &s.Slug, &s.Name, &s.Description, &s.Method, &s.URLTemplate,
		&headers, &query, &body, &env, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.Headers = rawJSON(headers)
	s.Query = rawJSON(query)
	s.Body = rawJSON(body)
	if env.Valid {
		v := env.Int64
		s.EnvironmentID = &v
	}
	return s, nil
}

func dbEnvironmentCreate(db *sql.DB, pid string, e *Environment, codec *secretCodec) (*Environment, error) {
	if e.Slug == "" {
		e.Slug = slugify(e.Name)
	}
	if e.Slug == "" || e.Name == "" {
		return nil, errors.New("slug/name required")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO fetch_environments(project_id, slug, name) VALUES (?, ?, ?)`, pid, e.Slug, e.Name)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := replaceEnvVarsTx(tx, pid, id, e.Vars, codec); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbEnvironmentGet(db, pid, id, false, codec)
}

func dbEnvironmentList(db *sql.DB, pid string, codec *secretCodec) ([]*Environment, error) {
	rows, err := db.Query(
		`SELECT id, slug, name, created_at, updated_at
		 FROM fetch_environments
		 WHERE project_id=? AND archived_at IS NULL
		 ORDER BY name COLLATE NOCASE`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Environment{}
	for rows.Next() {
		e := &Environment{}
		if err := rows.Scan(&e.ID, &e.Slug, &e.Name, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, e := range out {
		vars, _, err := dbEnvironmentVars(db, pid, e.ID, false, codec)
		if err != nil {
			return nil, err
		}
		e.Vars = vars
	}
	return out, nil
}

func dbEnvironmentGet(db *sql.DB, pid string, id int64, reveal bool, codec *secretCodec) (*Environment, error) {
	row := db.QueryRow(
		`SELECT id, slug, name, created_at, updated_at
		 FROM fetch_environments
		 WHERE project_id=? AND id=? AND archived_at IS NULL`,
		pid, id,
	)
	e := &Environment{}
	if err := row.Scan(&e.ID, &e.Slug, &e.Name, &e.CreatedAt, &e.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	vars, _, err := dbEnvironmentVars(db, pid, id, reveal, codec)
	if err != nil {
		return nil, err
	}
	e.Vars = vars
	return e, nil
}

func dbEnvironmentUpdate(db *sql.DB, pid string, id int64, patch map[string]any, codec *secretCodec) (*Environment, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if name, ok := patch["name"].(string); ok && strings.TrimSpace(name) != "" {
		if _, err := tx.Exec(`UPDATE fetch_environments SET name=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, name, pid, id); err != nil {
			return nil, err
		}
	}
	if rawVars, ok := patch["vars"].([]any); ok {
		vars := []EnvironmentVar{}
		for _, raw := range rawVars {
			m, _ := raw.(map[string]any)
			if m == nil {
				continue
			}
			vars = append(vars, EnvironmentVar{
				Key:      strArg(m, "key"),
				Value:    strArg(m, "value"),
				IsSecret: boolArg(m, "is_secret"),
				HasValue: boolArg(m, "has_value"),
			})
		}
		if err := replaceEnvVarsTx(tx, pid, id, vars, codec); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbEnvironmentGet(db, pid, id, false, codec)
}

func replaceEnvVarsTx(tx *sql.Tx, pid string, envID int64, vars []EnvironmentVar, codec *secretCodec) error {
	existing := map[string]string{}
	rows, err := tx.Query(
		`SELECT key, COALESCE(value,''), COALESCE(value_encrypted,''), is_secret FROM fetch_environment_vars WHERE project_id=? AND environment_id=?`,
		pid, envID,
	)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key, value, encrypted string
		var isSecret int
		if err := rows.Scan(&key, &value, &encrypted, &isSecret); err != nil {
			rows.Close()
			return err
		}
		if isSecret != 0 && encrypted != "" {
			plain, err := codec.openString(encrypted, environmentAAD(pid, envID, key))
			if err != nil {
				rows.Close()
				return err
			}
			value = plain
		}
		existing[key] = value
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM fetch_environment_vars WHERE project_id=? AND environment_id=?`, pid, envID); err != nil {
		return err
	}
	for _, v := range vars {
		key := strings.TrimSpace(v.Key)
		if key == "" {
			continue
		}
		if (v.Value == "" || v.Value == secretMask) && v.HasValue {
			previous, ok := existing[key]
			if !ok {
				return fmt.Errorf("secret %q was renamed without a replacement value", key)
			}
			v.Value = previous
		}
		var value, encrypted any = v.Value, nil
		if v.IsSecret {
			sealed, err := codec.sealString(v.Value, environmentAAD(pid, envID, key))
			if err != nil {
				return err
			}
			value, encrypted = nil, sealed
		}
		if _, err := tx.Exec(
			`INSERT INTO fetch_environment_vars(project_id, environment_id, key, value, value_encrypted, is_secret)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			pid, envID, key, value, encrypted, boolToInt(v.IsSecret),
		); err != nil {
			return err
		}
	}
	return nil
}

func dbEnvironmentVars(db *sql.DB, pid string, envID int64, reveal bool, codec *secretCodec) ([]EnvironmentVar, []string, error) {
	rows, err := db.Query(
		`SELECT key, COALESCE(value,''), COALESCE(value_encrypted,''), is_secret, updated_at
		 FROM fetch_environment_vars
		 WHERE project_id=? AND environment_id=?
		 ORDER BY key COLLATE NOCASE`,
		pid, envID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []EnvironmentVar
	var secrets []string
	for rows.Next() {
		var v EnvironmentVar
		var encrypted string
		var secretInt int
		if err := rows.Scan(&v.Key, &v.Value, &encrypted, &secretInt, &v.UpdatedAt); err != nil {
			return nil, nil, err
		}
		v.IsSecret = secretInt != 0
		if v.IsSecret && encrypted != "" {
			plain, err := codec.openString(encrypted, environmentAAD(pid, envID, v.Key))
			if err != nil {
				return nil, nil, err
			}
			v.Value = plain
		}
		v.HasValue = v.Value != ""
		if v.IsSecret {
			if v.Value != "" {
				secrets = append(secrets, v.Value)
			}
			if !reveal {
				v.Value = ""
			}
		}
		out = append(out, v)
	}
	return out, secrets, rows.Err()
}

func (a *App) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := getCtx()
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if !decodeJSONBody(w, r, &body) {
		return
	}
	out, err := a.execute(ctx, pid, body, executeOptions{source: "human", defaultSave: true, requestCtx: r.Context()})
	if err != nil {
		httpJSONStatus(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "result": out})
		return
	}
	httpJSON(w, out)
}

func (a *App) handleRequests(w http.ResponseWriter, r *http.Request) {
	ctx := getCtx()
	if err := a.ensureRuntime(ctx); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := dbSavedList(ctx.AppDB(), pid, a.secrets)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, map[string]any{"requests": out})
	case http.MethodPost:
		var s SavedRequest
		if !decodeJSONBody(w, r, &s) {
			return
		}
		out, err := dbSavedCreate(ctx.AppDB(), pid, &s, a.secrets)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, map[string]any{"request": out})
	default:
		httpErr(w, 405, "GET or POST")
	}
}

func (a *App) handleRequestItem(w http.ResponseWriter, r *http.Request) {
	ctx := getCtx()
	if err := a.ensureRuntime(ctx); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/requests/"), "/"), "/")
	id := parseID(firstPart(parts))
	if id == 0 {
		httpErr(w, 400, "id required")
		return
	}
	if len(parts) == 2 && parts[1] == "run" {
		if r.Method != http.MethodPost {
			httpErr(w, 405, "POST only")
			return
		}
		saved, err := dbSavedGet(ctx.AppDB(), pid, id, a.secrets, true)
		if err != nil || saved == nil {
			httpErr(w, 404, "request not found")
			return
		}
		args := savedToArgs(saved)
		var overrides map[string]any
		if r.Body != nil && r.ContentLength != 0 {
			if !decodeJSONBody(w, r, &overrides) {
				return
			}
		}
		for k, v := range overrides {
			args[k] = v
		}
		out, err := a.execute(ctx, pid, args, executeOptions{source: "saved_request", savedRequestID: id, defaultSave: true})
		if err != nil {
			httpJSONStatus(w, 502, map[string]any{"error": err.Error(), "result": out})
			return
		}
		httpJSON(w, out)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := dbSavedGet(ctx.AppDB(), pid, id, a.secrets, false)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		if out == nil {
			httpErr(w, 404, "request not found")
			return
		}
		httpJSON(w, map[string]any{"request": out})
	case http.MethodPatch:
		var patch map[string]any
		if !decodeJSONBody(w, r, &patch) {
			return
		}
		out, err := dbSavedUpdate(ctx.AppDB(), pid, id, patch, a.secrets)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, map[string]any{"request": out})
	case http.MethodDelete:
		_, err := ctx.AppDB().Exec(`UPDATE fetch_saved_requests SET archived_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, id)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpErr(w, 405, "GET, PATCH, DELETE, or /run")
	}
}

func (a *App) handleEnvironments(w http.ResponseWriter, r *http.Request) {
	ctx := getCtx()
	if err := a.ensureRuntime(ctx); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := dbEnvironmentList(ctx.AppDB(), pid, a.secrets)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		httpJSON(w, map[string]any{"environments": out})
	case http.MethodPost:
		var e Environment
		if !decodeJSONBody(w, r, &e) {
			return
		}
		out, err := dbEnvironmentCreate(ctx.AppDB(), pid, &e, a.secrets)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, map[string]any{"environment": out})
	default:
		httpErr(w, 405, "GET or POST")
	}
}

func (a *App) handleEnvironmentItem(w http.ResponseWriter, r *http.Request) {
	ctx := getCtx()
	if err := a.ensureRuntime(ctx); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	id := parseID(strings.Trim(strings.TrimPrefix(r.URL.Path, "/environments/"), "/"))
	if id == 0 {
		httpErr(w, 400, "id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := dbEnvironmentGet(ctx.AppDB(), pid, id, false, a.secrets)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		if out == nil {
			httpErr(w, 404, "environment not found")
			return
		}
		httpJSON(w, map[string]any{"environment": out})
	case http.MethodPatch:
		var patch map[string]any
		if !decodeJSONBody(w, r, &patch) {
			return
		}
		out, err := dbEnvironmentUpdate(ctx.AppDB(), pid, id, patch, a.secrets)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		httpJSON(w, map[string]any{"environment": out})
	case http.MethodDelete:
		_, err := ctx.AppDB().Exec(`UPDATE fetch_environments SET archived_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, pid, id)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpErr(w, 405, "GET, PATCH, or DELETE")
	}
}

func (a *App) handleHistory(w http.ResponseWriter, r *http.Request) {
	ctx := getCtx()
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if r.Method == http.MethodDelete {
		if _, err := ctx.AppDB().Exec(`DELETE FROM fetch_history WHERE project_id=?`, pid); err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET or DELETE")
		return
	}
	limit := intArgFromQuery(r, "limit", 100)
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset := intArgFromQuery(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, saved_request_id, COALESCE(source,''), method, url,
				status, duration_ms, COALESCE(error,''), created_at
		 FROM fetch_history
		 WHERE project_id=?
		 ORDER BY id DESC LIMIT ? OFFSET ?`,
		pid, limit+1, offset,
	)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []HistoryRow{}
	for rows.Next() {
		row, err := scanHistorySummary(rows)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		out = append(out, *row)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	httpJSON(w, map[string]any{"history": out, "has_more": hasMore, "next_offset": offset + len(out)})
}

func (a *App) handleHistoryItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, 405, "GET only")
		return
	}
	ctx := getCtx()
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	id := parseID(strings.Trim(strings.TrimPrefix(r.URL.Path, "/history/"), "/"))
	row := ctx.AppDB().QueryRow(
		`SELECT id, saved_request_id, COALESCE(source,''), method, url,
				COALESCE(redacted_request_json,''), status,
				COALESCE(redacted_response_json,''), duration_ms, COALESCE(error,''), created_at
		 FROM fetch_history
		 WHERE project_id=? AND id=?`,
		pid, id,
	)
	out, err := scanHistory(row)
	if errors.Is(err, sql.ErrNoRows) {
		httpErr(w, 404, "history not found")
		return
	}
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	httpJSON(w, map[string]any{"entry": out})
}

func scanHistorySummary(row interface{ Scan(...any) error }) (*HistoryRow, error) {
	h := &HistoryRow{}
	var saved sql.NullInt64
	if err := row.Scan(&h.ID, &saved, &h.Source, &h.Method, &h.URL, &h.Status, &h.DurationMS, &h.Error, &h.CreatedAt); err != nil {
		return nil, err
	}
	if saved.Valid {
		v := saved.Int64
		h.SavedRequestID = &v
	}
	return h, nil
}

func scanHistory(row interface{ Scan(...any) error }) (*HistoryRow, error) {
	h := &HistoryRow{}
	var saved sql.NullInt64
	var req, resp string
	if err := row.Scan(&h.ID, &saved, &h.Source, &h.Method, &h.URL, &req, &h.Status, &resp, &h.DurationMS, &h.Error, &h.CreatedAt); err != nil {
		return nil, err
	}
	if saved.Valid {
		v := saved.Int64
		h.SavedRequestID = &v
	}
	h.RedactedRequestJSON = rawJSON(req)
	h.RedactedResponseJSON = rawJSON(resp)
	return h, nil
}

func savedToArgs(s *SavedRequest) map[string]any {
	args := map[string]any{
		"method": s.Method,
		"url":    s.URLTemplate,
	}
	decodeRawInto(s.Headers, "headers", args)
	decodeRawInto(s.Query, "query", args)
	decodeRawInto(s.Body, "body", args)
	if s.EnvironmentID != nil {
		args["environment_id"] = *s.EnvironmentID
	}
	return args
}

func decodeRawInto(raw json.RawMessage, key string, out map[string]any) {
	if len(raw) == 0 {
		return
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		out[key] = v
	}
}

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if pid := os.Getenv("APTEVA_PROJECT_ID"); pid != "" {
		return pid, nil
	}
	for _, key := range []string{"_project_id", "project_id"} {
		if pid := strings.TrimSpace(strArg(args, key)); pid != "" {
			return pid, nil
		}
	}
	return "", errors.New("project_id required for global fetch installs")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if pid := os.Getenv("APTEVA_PROJECT_ID"); pid != "" {
		return pid, nil
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		return pid, nil
	}
	return "", errors.New("project_id required")
}

func validMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

var varPattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_.-]*)\s*\}\}`)

func interpolate(s string, vars map[string]string) string {
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		key := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}"))
		if v, ok := vars[key]; ok {
			return v
		}
		return match
	})
}

func interpolateAny(v any, vars map[string]string) any {
	switch x := v.(type) {
	case string:
		return interpolate(x, vars)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = interpolateAny(x[i], vars)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			out[k] = interpolateAny(v, vars)
		}
		return out
	default:
		return v
	}
}

func redactAny(v any, secrets []string) any {
	switch x := v.(type) {
	case string:
		return redactSecrets(x, secrets)
	case map[string]string:
		out := map[string]string{}
		for k, v := range x {
			out[k] = redactHeaderValue(k, redactSecrets(v, secrets))
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			if isSensitiveHeader(k) || strings.Contains(strings.ToLower(k), "token") || strings.Contains(strings.ToLower(k), "secret") {
				out[k] = "[redacted]"
			} else {
				out[k] = redactAny(v, secrets)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = redactAny(x[i], secrets)
		}
		return out
	default:
		return v
	}
}

func redactSecrets(s string, secrets []string) string {
	out := s
	for _, secret := range secrets {
		if secret != "" {
			out = strings.ReplaceAll(out, secret, "[redacted]")
		}
	}
	return out
}

func redactHeaderValue(key, value string) string {
	if isSensitiveHeader(key) {
		return "[redacted]"
	}
	return value
}

func isSensitiveHeader(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	switch k {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "x-auth-token":
		return true
	default:
		normalized := strings.NewReplacer("-", "", "_", "", ".", "", " ", "").Replace(k)
		return strings.Contains(normalized, "auth") ||
			strings.Contains(normalized, "apikey") ||
			strings.Contains(normalized, "credential") ||
			strings.Contains(normalized, "signature") ||
			strings.Contains(normalized, "token") ||
			strings.Contains(normalized, "secret") ||
			strings.Contains(normalized, "password")
	}
}

func isTextLike(contentType string, data []byte) bool {
	if len(data) == 0 {
		return true
	}
	mt, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(mt, "text/") || strings.Contains(mt, "json") || strings.Contains(mt, "xml") || strings.Contains(mt, "javascript") {
		return true
	}
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}

func hostList(ctx *sdk.AppCtx, key string) []string {
	if ctx == nil {
		return nil
	}
	raw := strings.TrimSpace(ctx.Config().Get(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := []string{}
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func hostMatchesAny(host string, patterns []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSuffix(p, "."))
		if p == "*" || p == host {
			return true
		}
		if strings.HasPrefix(p, ".") && strings.HasSuffix(host, p) {
			return true
		}
	}
	return false
}

func configInt(ctx *sdk.AppCtx, key string, def int) int {
	if ctx == nil {
		return def
	}
	v := strings.TrimSpace(ctx.Config().Get(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func configBool(ctx *sdk.AppCtx, key string, def bool) bool {
	if ctx == nil {
		return def
	}
	v := strings.ToLower(strings.TrimSpace(ctx.Config().Get(key)))
	if v == "" {
		return def
	}
	return v == "true" || v == "1" || v == "yes"
}

func httpJSON(w http.ResponseWriter, v any) {
	httpJSONStatus(w, http.StatusOK, v)
}

func httpJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, status int, msg string) {
	httpJSONStatus(w, status, map[string]any{"error": msg})
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxInboundJSONBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			httpErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("json body exceeds %d bytes", maxInboundJSONBytes))
		} else {
			httpErr(w, http.StatusBadRequest, "invalid json")
		}
		return false
	}
	return true
}

func strArg(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func intArg(m map[string]any, key string, def int) int {
	if n := int64Arg(m, key); n != 0 {
		return int(n)
	}
	return def
}

func int64Arg(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	default:
		return 0
	}
}

func boolArg(m map[string]any, key string) bool {
	return boolArgDefault(m, key, false)
}

func boolArgDefault(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

func intArgFromQuery(r *http.Request, key string, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	return n
}

func parseID(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func firstPart(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(n *int64) any {
	if n == nil || *n == 0 {
		return nil
	}
	return *n
}

func rawOrNull(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return string(raw)
}

func rawJSON(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func preview(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

var globalCtx *sdk.AppCtx

func getCtx() *sdk.AppCtx { return globalCtx }

func main() {
	sdk.Run(&App{})
}
