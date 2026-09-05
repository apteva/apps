package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const browserOriginRegistrationPrefix = "api-"

type gatewayCORSPolicy struct {
	Enabled       bool
	Origins       []string
	AllowMethods  []string
	AllowHeaders  []string
	ExposeHeaders []string
	Credentials   bool
	MaxAge        int
}

func browserOriginRegistrationKey(apiID int64) string {
	return browserOriginRegistrationPrefix + strconv.FormatInt(apiID, 10)
}

func parseEffectiveCORSPolicy(apiJSON, routeJSON string) (gatewayCORSPolicy, error) {
	if _, err := policyObject(apiJSON); err != nil {
		return gatewayCORSPolicy{}, err
	}
	if _, err := policyObject(routeJSON); err != nil {
		return gatewayCORSPolicy{}, err
	}
	return parseGatewayCORSPolicy(effectiveJSON(apiJSON, routeJSON))
}

func parseGatewayCORSPolicy(raw map[string]any) (gatewayCORSPolicy, error) {
	policy := gatewayCORSPolicy{MaxAge: 600}
	if len(raw) == 0 {
		return policy, nil
	}

	origins, err := stringListValue(raw["origins"])
	if err != nil {
		return policy, fmt.Errorf("cors.origins: %w", err)
	}
	if legacy, _ := raw["allow_origin"].(string); strings.TrimSpace(legacy) != "" {
		origins = append(origins, strings.TrimSpace(legacy))
	}

	enabled, hasEnabled, err := boolValue(raw["enabled"])
	if err != nil {
		return policy, fmt.Errorf("cors.enabled: %w", err)
	}
	if !hasEnabled {
		enabled = len(origins) > 0
	}
	policy.Enabled = enabled
	if !enabled {
		return policy, nil
	}
	if len(origins) == 0 {
		return policy, errors.New("enabled CORS requires at least one explicit HTTP(S) origin")
	}

	seen := map[string]bool{}
	for _, origin := range origins {
		normalized, err := normalizeBrowserOrigin(origin)
		if err != nil {
			return policy, err
		}
		if !seen[normalized] {
			seen[normalized] = true
			policy.Origins = append(policy.Origins, normalized)
		}
	}
	if len(policy.Origins) > 100 {
		return policy, errors.New("CORS supports at most 100 origins per API")
	}
	sort.Strings(policy.Origins)

	policy.AllowMethods, err = normalizedMethods(raw["allow_methods"])
	if err != nil {
		return policy, fmt.Errorf("cors.allow_methods: %w", err)
	}
	policy.AllowHeaders, err = normalizedHeaders(raw["allow_headers"])
	if err != nil {
		return policy, fmt.Errorf("cors.allow_headers: %w", err)
	}
	if len(policy.AllowHeaders) == 0 {
		policy.AllowHeaders = []string{"authorization", "content-type", "x-api-key"}
	}
	policy.ExposeHeaders, err = normalizedHeaders(raw["expose_headers"])
	if err != nil {
		return policy, fmt.Errorf("cors.expose_headers: %w", err)
	}
	if credentials, ok, err := boolValue(firstValue(raw, "credentials", "allow_credentials")); err != nil {
		return policy, fmt.Errorf("cors.credentials: %w", err)
	} else if ok {
		policy.Credentials = credentials
	}
	if rawMaxAge, exists := raw["max_age"]; exists {
		maxAge, ok := intValue(rawMaxAge)
		if !ok {
			return policy, errors.New("cors.max_age must be an integer")
		}
		if maxAge < 0 || maxAge > 86400 {
			return policy, errors.New("cors.max_age must be between 0 and 86400 seconds")
		}
		policy.MaxAge = maxAge
	}
	return policy, nil
}

func normalizeBrowserOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "*" {
		return "", errors.New("wildcard CORS origins are not supported; use explicit HTTP(S) origins")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("invalid CORS origin %q", raw)
	}
	host := strings.ToLower(u.Host)
	if u.Scheme == "http" && u.Port() == "80" || u.Scheme == "https" && u.Port() == "443" {
		host = u.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(host), nil
}

func firstValue(raw map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			return value
		}
	}
	return nil
}

func boolValue(value any) (bool, bool, error) {
	if value == nil {
		return false, false, nil
	}
	switch v := value.(type) {
	case bool:
		return v, true, nil
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		return parsed, true, err
	default:
		return false, true, errors.New("must be a boolean")
	}
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), v == float64(int(v))
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil
	default:
		return 0, false
	}
}

func stringListValue(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case string:
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out, nil
	case []string:
		return append([]string{}, v...), nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, errors.New("must contain only strings")
			}
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	default:
		return nil, errors.New("must be a string or array of strings")
	}
}

func normalizedMethods(value any) ([]string, error) {
	items, err := stringListValue(value)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		method := strings.ToUpper(strings.TrimSpace(item))
		switch method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
			http.MethodDelete, http.MethodHead, http.MethodOptions:
		default:
			return nil, fmt.Errorf("unsupported method %q", item)
		}
		if !seen[method] {
			seen[method] = true
			out = append(out, method)
		}
	}
	sort.Strings(out)
	return out, nil
}

func normalizedHeaders(value any) ([]string, error) {
	items, err := stringListValue(value)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		header := strings.ToLower(strings.TrimSpace(item))
		if !validHTTPToken(header) {
			return nil, fmt.Errorf("invalid header name %q", item)
		}
		if !seen[header] {
			seen[header] = true
			out = append(out, header)
		}
	}
	sort.Strings(out)
	return out, nil
}

func containsFold(items []string, value string) bool {
	for _, item := range items {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}

func requestedCORSHeaders(r *http.Request) ([]string, error) {
	raw := strings.TrimSpace(r.Header.Get("Access-Control-Request-Headers"))
	if raw == "" {
		return nil, nil
	}
	return normalizedHeaders(raw)
}

func prepareGatewayCORS(w http.ResponseWriter, r *http.Request, api *API, route *APIRoute, requestMethod string, preflight bool) (http.ResponseWriter, bool, error) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return w, false, nil
	}
	normalizedOrigin, err := normalizeBrowserOrigin(origin)
	if err != nil {
		return w, false, errors.New("origin not allowed")
	}
	policy, err := parseEffectiveCORSPolicy(api.CORSJSON, route.CORSJSON)
	if err != nil {
		return w, false, err
	}
	if !policy.Enabled || !containsFold(policy.Origins, normalizedOrigin) {
		return w, false, errors.New("origin not allowed")
	}
	requestMethod = strings.ToUpper(strings.TrimSpace(requestMethod))
	if len(policy.AllowMethods) > 0 && !containsFold(policy.AllowMethods, requestMethod) {
		return w, false, errors.New("method not allowed by CORS policy")
	}
	requestedHeaders, err := requestedCORSHeaders(r)
	if err != nil {
		return w, false, err
	}
	for _, header := range requestedHeaders {
		if !containsFold(policy.AllowHeaders, header) {
			return w, false, fmt.Errorf("header %q not allowed by CORS policy", header)
		}
	}

	if preflight {
		methods := policy.AllowMethods
		if len(methods) == 0 {
			methods = []string{requestMethod}
		}
		writePreflightCORSHeaders(w.Header(), normalizedOrigin, policy, methods)
		w.WriteHeader(http.StatusNoContent)
		return w, true, nil
	}
	return &gatewayCORSResponseWriter{ResponseWriter: w, origin: normalizedOrigin, policy: policy}, false, nil
}

func writePreflightCORSHeaders(header http.Header, origin string, policy gatewayCORSPolicy, methods []string) {
	clearCORSHeaders(header)
	header.Set("Access-Control-Allow-Origin", origin)
	header.Set("Access-Control-Allow-Methods", strings.Join(methods, ", "))
	header.Set("Access-Control-Allow-Headers", strings.Join(policy.AllowHeaders, ", "))
	header.Set("Access-Control-Max-Age", strconv.Itoa(policy.MaxAge))
	if policy.Credentials {
		header.Set("Access-Control-Allow-Credentials", "true")
	}
	addVary(header, "Origin")
	addVary(header, "Access-Control-Request-Method")
	addVary(header, "Access-Control-Request-Headers")
}

type gatewayCORSResponseWriter struct {
	http.ResponseWriter
	origin string
	policy gatewayCORSPolicy
}

func (w *gatewayCORSResponseWriter) apply() {
	header := w.Header()
	clearCORSHeaders(header)
	header.Set("Access-Control-Allow-Origin", w.origin)
	if w.policy.Credentials {
		header.Set("Access-Control-Allow-Credentials", "true")
	}
	if len(w.policy.ExposeHeaders) > 0 {
		header.Set("Access-Control-Expose-Headers", strings.Join(w.policy.ExposeHeaders, ", "))
	}
	addVary(header, "Origin")
}

func (w *gatewayCORSResponseWriter) WriteHeader(status int) {
	w.apply()
	w.ResponseWriter.WriteHeader(status)
}

func (w *gatewayCORSResponseWriter) Write(body []byte) (int, error) {
	w.apply()
	return w.ResponseWriter.Write(body)
}

func (w *gatewayCORSResponseWriter) Flush() {
	w.apply()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *gatewayCORSResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func clearCORSHeaders(header http.Header) {
	for key := range header {
		if strings.HasPrefix(strings.ToLower(key), "access-control-") {
			header.Del(key)
		}
	}
}

func addVary(header http.Header, value string) {
	for _, current := range header.Values("Vary") {
		for _, item := range strings.Split(current, ",") {
			if strings.EqualFold(strings.TrimSpace(item), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}

func effectiveAPIBrowserPolicy(db *sql.DB, api *API) (sdk.BrowserOriginPolicy, error) {
	policy := sdk.BrowserOriginPolicy{Preflight: sdk.BrowserPreflightApp}
	if api == nil || api.Status != "active" {
		return policy, nil
	}
	routes, err := dbListRoutes(db, api.ProjectID, api.ID)
	if err != nil {
		return policy, err
	}
	origins := map[string]bool{}
	addPolicy := func(cors gatewayCORSPolicy) {
		if !cors.Enabled {
			return
		}
		for _, origin := range cors.Origins {
			origins[origin] = true
		}
		policy.Credentials = policy.Credentials || cors.Credentials
	}
	if len(routes) == 0 {
		cors, err := parseEffectiveCORSPolicy(api.CORSJSON, "{}")
		if err != nil {
			return policy, err
		}
		addPolicy(cors)
	} else {
		for _, route := range routes {
			if !route.Enabled {
				continue
			}
			cors, err := parseEffectiveCORSPolicy(api.CORSJSON, route.CORSJSON)
			if err != nil {
				return policy, fmt.Errorf("route %d: %w", route.ID, err)
			}
			addPolicy(cors)
		}
	}
	for origin := range origins {
		policy.Origins = append(policy.Origins, origin)
	}
	sort.Strings(policy.Origins)
	if len(policy.Origins) > 100 {
		return policy, errors.New("effective API CORS policy exceeds 100 origins")
	}
	return policy, nil
}

func syncAPIBrowserOriginPolicy(ctx *sdk.AppCtx, api *API) (bool, error) {
	if ctx == nil || api == nil {
		return false, nil
	}
	_, err := ctx.AppDB().Exec(`INSERT INTO api_policy_sync(api_id,project_id,pending) VALUES(?,?,1) ON CONFLICT(api_id) DO UPDATE SET pending=1,delete_requested=0`, api.ID, api.ProjectID)
	if err != nil {
		return true, err
	}
	policy, err := effectiveAPIBrowserPolicy(ctx.AppDB(), api)
	if err == nil {
		if ctx.BrowserOriginsAPI() == nil || ctx.BrowserOriginPolicyAPI() == nil {
			err = errors.New("platform browser-origin API unavailable")
		} else if api.Status != "active" || len(policy.Origins) == 0 {
			err = ctx.DeleteBrowserOrigins(browserOriginRegistrationKey(api.ID))
		} else {
			_, err = ctx.ReplaceBrowserOriginPolicy(browserOriginRegistrationKey(api.ID), policy)
		}
	}
	message := ""
	if err != nil {
		message = safeUpstreamError(err)
	}
	_, dbErr := ctx.AppDB().Exec(`UPDATE api_policy_sync SET pending=?,error=? WHERE api_id=?`, err != nil, message, api.ID)
	return true, errors.Join(err, dbErr)
}

func reconcileBrowserOriginPolicies(ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.BrowserOriginsAPI() == nil || ctx.BrowserOriginPolicyAPI() == nil {
		return nil
	}
	apis, err := dbListAllAPIs(ctx.AppDB())
	if err != nil {
		return fmt.Errorf("list APIs: %w", err)
	}
	registrations, listErr := ctx.ListBrowserOriginRegistrations()
	desired := map[string]bool{}
	var errs []error
	if listErr != nil {
		errs = append(errs, fmt.Errorf("list browser-origin registrations: %w", listErr))
	}
	for _, api := range apis {
		policy, err := effectiveAPIBrowserPolicy(ctx.AppDB(), api)
		if err != nil {
			errs = append(errs, fmt.Errorf("API %s: %w", api.Slug, err))
			continue
		}
		if api.Status != "active" || len(policy.Origins) == 0 {
			continue
		}
		key := browserOriginRegistrationKey(api.ID)
		desired[key] = true
		if _, err := syncAPIBrowserOriginPolicy(ctx.WithProject(api.ProjectID), api); err != nil {
			errs = append(errs, fmt.Errorf("replace %s: %w", key, err))
		}
	}
	if listErr == nil {
		for _, registration := range registrations {
			if !strings.HasPrefix(registration.Key, browserOriginRegistrationPrefix) || desired[registration.Key] {
				continue
			}
			if err := ctx.DeleteBrowserOrigins(registration.Key); err != nil {
				errs = append(errs, fmt.Errorf("delete stale %s: %w", registration.Key, err))
			}
		}
	}
	return errors.Join(errs...)
}

func recordBrowserOriginSync(out map[string]any, attempted bool, err error) {
	if !attempted {
		return
	}
	out["browser_origins_synced"] = err == nil
	if err != nil {
		out["browser_origins_error"] = err.Error()
	}
}
