package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

const maxCarrierFrameBytes = 1 << 20

func maxCallsPerMinute() int {
	value := 10
	if raw := strings.TrimSpace(os.Getenv("TELEPHONY_MAX_CALLS_PER_MINUTE")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 && parsed <= 1000 {
			value = parsed
		}
	}
	return value
}

func validE164(value string) bool {
	if len(value) < 9 || len(value) > 16 || value[0] != '+' {
		return false
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func validVoice(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func (a *App) validatePublicEndpoint() error {
	base := a.publicBase()
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("server public URL must be configured as an externally reachable https:// URL before placing or routing calls")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("server public URL must not contain credentials, a query, or a fragment")
	}
	return nil
}

func secureEqual(left, right string) bool {
	if left == "" || right == "" || len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func (a *App) authorizeCallRequest(r *http.Request, row *callRow) error {
	token := r.URL.Query().Get("token")
	if strings.HasPrefix(r.URL.Path, "/media/") {
		token = mediaTokenFromPath(r.URL.Path)
	}
	if row == nil || !secureEqual(token, row.CallbackSecret) {
		return errors.New("invalid callback token")
	}
	if row.CarrierSlug == "twilio" {
		return a.verifyTwilioRequest(r, row.CarrierConnectionID)
	}
	return nil
}

func (a *App) verifyTwilioRequest(r *http.Request, connectionID int64) error {
	if globalCtx == nil || globalCtx.PlatformAPI() == nil {
		return errors.New("app context unavailable")
	}
	creds, err := globalCtx.PlatformAPI().GetConnectionCredentials(connectionID)
	if err != nil {
		return fmt.Errorf("load Twilio signing credential: %w", err)
	}
	authToken := strings.TrimSpace(creds.Fields["auth_token"])
	if authToken == "" {
		return errors.New("Twilio connection has no auth_token for request verification")
	}
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("parse Twilio request: %w", err)
	}
	fullURL := a.publicRequestURL(r)
	if !verifyTwilioSignature(fullURL, r.PostForm, authToken, r.Header.Get("X-Twilio-Signature")) {
		return errors.New("invalid Twilio request signature")
	}
	return nil
}

func (a *App) publicRequestURL(r *http.Request) string {
	baseURL := a.publicAppURL()
	if strings.HasPrefix(r.URL.Path, "/media/") {
		baseURL = a.publicInstalledAppURL()
		baseURL = strings.Replace(baseURL, "https://", "wss://", 1)
		baseURL = strings.Replace(baseURL, "http://", "ws://", 1)
	}
	return baseURL + r.URL.RequestURI()
}

func verifyTwilioSignature(fullURL string, form url.Values, authToken, expected string) bool {
	if fullURL == "" || authToken == "" || expected == "" {
		return false
	}
	keys := make([]string, 0, len(form))
	for key := range form {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var signed strings.Builder
	signed.WriteString(fullURL)
	for _, key := range keys {
		if values := form[key]; len(values) > 0 {
			signed.WriteString(key)
			signed.WriteString(values[0])
		}
	}
	mac := hmac.New(sha1.New, []byte(authToken))
	_, _ = mac.Write([]byte(signed.String()))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(expected))
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid-url>"
	}
	query := u.Query()
	for _, key := range []string{"token", "secret", "sig", "key", "auth"} {
		if query.Has(key) {
			query.Set(key, "REDACTED")
		}
	}
	u.RawQuery = query.Encode()
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && parts[len(parts)-4] == "media" {
		parts[len(parts)-1] = "REDACTED"
		u.Path = "/" + strings.Join(parts, "/")
	}
	return u.String()
}
