package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const maxRequestBytes int64 = 8 << 20
const maxFunctionBytes int64 = 1 << 20
const maxFunctionResponseBytes int64 = 2 << 20
const maxPolicyBytes = 64 << 10
const authTimeout = 5 * time.Second
const bodyTimeout = 30 * time.Second

func boundedIntArg(args map[string]any, key string, def, min, max int) (int, error) {
	v, ok := args[key]
	if !ok {
		return def, nil
	}
	var raw string
	switch n := v.(type) {
	case int:
		raw = strconv.Itoa(n)
	case int64:
		raw = strconv.FormatInt(n, 10)
	case json.Number:
		raw = string(n)
	case float64:
		raw = strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < int64(min) || n > int64(max) {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, min, max)
	}
	return int(n), nil
}

func gatewayHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 10 * time.Second
	tr.MaxIdleConns = 100
	tr.MaxIdleConnsPerHost = 32
	tr.IdleConnTimeout = 90 * time.Second
	tr.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func decodeManagementBody(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(bodyTimeout))
	defer controller.SetReadDeadline(time.Time{})
	d := json.NewDecoder(r.Body)
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		return err
	}
	if object, ok := out.(*map[string]any); ok && *object == nil {
		return errors.New("expected JSON object")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

func policyObject(raw string) (map[string]any, error) {
	if len(raw) > maxPolicyBytes {
		return nil, errors.New("policy exceeds 64 KiB")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(defaultJSON(raw)), &obj); err != nil || obj == nil {
		return nil, errors.New("policy must be a JSON object")
	}
	return obj, nil
}
func validateAuthPolicy(raw string) error {
	obj, err := policyObject(raw)
	if err != nil {
		return err
	}
	for key, value := range obj {
		if key != "kind" {
			return errors.New("unsupported auth field: " + key)
		}
		kind, ok := value.(string)
		if !ok {
			return errors.New("auth.kind must be a string")
		}
		switch kind {
		case "public", "api_key", "auth_jwt":
		default:
			return errors.New("auth.kind must be public, api_key, or auth_jwt")
		}
	}
	return nil
}
func effectiveAuthKind(base, override string) (string, error) {
	if err := validateAuthPolicy(base); err != nil {
		return "", err
	}
	if err := validateAuthPolicy(override); err != nil {
		return "", err
	}
	return stringFromMap(effectiveJSON(base, override), "kind", "public"), nil
}
func normalizedAuthArg(args map[string]any, key, def string) (string, error) {
	if v, ok := args[key]; ok && v == nil {
		return "", errors.New("auth must be an object")
	}
	s, err := jsonTextArg(args, key, def)
	if err != nil {
		return "", err
	}
	if err := validateAuthPolicy(s); err != nil {
		return "", err
	}
	return s, nil
}

func sanitizedQuery(q url.Values) url.Values {
	out := make(url.Values, len(q))
	for key, vals := range q {
		switch strings.ToLower(key) {
		case "api_key", "project_id", "install_id", "_project_id":
			continue
		}
		out[key] = append([]string(nil), vals...)
	}
	return out
}
func sanitizedHeaders(src http.Header) http.Header {
	out := src.Clone()
	stripHopHeaders(out)
	for key := range out {
		low := strings.ToLower(key)
		if low == "authorization" || low == "cookie" || low == "x-api-key" || low == "host" || low == "forwarded" || strings.HasPrefix(low, "x-apteva-") || strings.HasPrefix(low, "x-forwarded-") || low == "x-user-id" {
			out.Del(key)
		}
	}
	return out
}
func stripHopHeaders(h http.Header) {
	for _, line := range h.Values("Connection") {
		for _, key := range strings.Split(line, ",") {
			h.Del(strings.TrimSpace(key))
		}
	}
	for _, key := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(key)
	}
}
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, &http.MaxBytesError{Limit: limit}
	}
	return b, nil
}
func bearerExpiry(r *http.Request) time.Time {
	// Used only after Auth has verified this exact token. Never authenticates it.
	parts := strings.Split(bearerToken(r.Header.Get("Authorization")), ".")
	if len(parts) != 3 {
		return time.Now().Add(time.Minute)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Now()
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return time.Now()
	}
	seconds, err := claims.Exp.Int64()
	if err != nil {
		return time.Now()
	}
	return time.Unix(seconds, 0)
}
func withAuthDeadline(r *http.Request) (*http.Request, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(r.Context(), authTimeout)
	return r.WithContext(ctx), cancel
}
func jsonObjectBytes(raw []byte) (map[string]any, error) {
	var obj map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&obj); err != nil || obj == nil {
		return nil, errors.New("invalid JSON object")
	}
	return obj, nil
}

func (a *App) validateAppTarget(project, ref string) error {
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	token := outboundAppToken()
	if base == "" || token == "" {
		return errors.New("app targets require a configured platform gateway")
	}
	ctx, cancel := context.WithTimeout(context.Background(), authTimeout)
	defer cancel()
	target := base + "/api/apps/callback/apps/" + url.PathEscape(ref) + "/proxy/health?project_id=" + url.QueryEscape(project)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.performRequest(req)
	if err != nil {
		return errors.New("cannot verify app target binding")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("app target is unavailable or not bound; configure the upstream app binding")
	}
	return nil
}

func validHTTPToken(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}
func validHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 127 || s[i] < 32 && s[i] != '\t' {
			return false
		}
	}
	return true
}

type responseTransferError struct{ err error }

func (e *responseTransferError) Error() string { return e.err.Error() }
func (e *responseTransferError) Unwrap() error { return e.err }
