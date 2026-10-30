package main

// Public ingest for the static-site tag. The manifest and SDK route both
// explicitly expose GET /collect without platform authentication; the write
// key in ?k= is the credential. Always responds with a 1x1 GIF so a bad key
// never breaks the page; events are recorded only for a valid, non-revoked,
// rate-limited, origin-allowed key. project_id is taken from the key, never
// the client.

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) handleTrackingTag(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(trackingTagJS)
}

// 43-byte transparent 1x1 GIF.
var pixelGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00,
	0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0x21, 0xf9, 0x04, 0x01, 0x00, 0x00, 0x00,
	0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02,
	0x44, 0x01, 0x00, 0x3b,
}

func writePixel(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pixelGIF)
}

func setCollectCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "*"
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	w.Header().Set("Vary", "Origin")
}

// ─── per-key rate limiter (in-memory token bucket) ────────────────────

const (
	ratePerSec = 25.0
	rateBurst  = 50.0
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

type rateLimiter struct {
	mu    sync.Mutex
	m     map[string]*tokenBucket
	rate  float64
	burst float64
}

var collectAdmissionLimiter = &rateLimiter{m: map[string]*tokenBucket{}, rate: 500, burst: 1000}
var collectLimiter = &rateLimiter{m: map[string]*tokenBucket{}}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	rate, burst := rl.rate, rl.burst
	if rate <= 0 {
		rate = ratePerSec
	}
	if burst <= 0 {
		burst = rateBurst
	}
	if len(rl.m) >= 4096 {
		for k, b := range rl.m {
			if now.Sub(b.last) > 5*time.Minute {
				delete(rl.m, k)
			}
		}
		if len(rl.m) >= 4096 && rl.m[key] == nil {
			return false
		}
	}
	b := rl.m[key]
	if b == nil {
		rl.m[key] = &tokenBucket{tokens: burst - 1, last: now}
		return true
	}
	b.tokens += now.Sub(b.last).Seconds() * rate
	if b.tokens > burst {
		b.tokens = burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// originAllowed requires Origin/Referer evidence when an allowlist is set.
// Public clients can spoof these headers; this is an abuse control, not identity.
// The tag's no-cors GET never carries Origin, and pages with
// Referrer-Policy: no-referrer send no Referer either, so the page URL the tag
// reports is the last resort. It is no weaker than the headers it replaces.
func originAllowed(wk *WriteKey, r *http.Request) bool {
	if len(wk.AllowedOrigins) == 0 {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		origin = r.URL.Query().Get("url")
	}
	host := hostOf(origin)
	if host == "" {
		host = hostOf(r.Header.Get("Referer"))
	}
	if host == "" {
		return false // Configured origin policies require origin evidence.
	}
	for _, o := range wk.AllowedOrigins {
		if (strings.Contains(o, "://") && canonicalOrigin(o) == canonicalOrigin(origin)) || (!strings.Contains(o, "://") && strings.EqualFold(o, host)) {
			return true
		}
	}
	return false
}

func hostOf(s string) string {
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	if u, err := url.Parse(s); err == nil {
		return u.Hostname()
	}
	return ""
}

// collectProps assembles the props object from the tag's query params.
// Known dimensions are promoted to stable keys; an optional `p` param
// carries extra JSON props the site set explicitly.
func collectProps(q url.Values) map[string]any {
	props := map[string]any{}
	for param, key := range map[string]string{
		"path": "path", "ref": "referrer", "title": "title",
		"lang": "lang", "device": "device", "platform": "platform",
		"country": "country", "screen": "screen",
		"url": "url", "host": "host",
	} {
		if v := q.Get(param); v != "" {
			props[key] = v
		}
	}
	if raw := q.Get("p"); raw != "" {
		var extra map[string]any
		if json.Unmarshal([]byte(raw), &extra) == nil {
			for k, v := range extra {
				props[k] = v
			}
		}
	}
	// Standard location fields cannot retain secrets in URLs, including extra props overrides.
	for _, key := range []string{"url", "path", "referrer", "link_url", "touch_landing", "first_landing"} {
		if value, ok := props[key].(string); ok {
			if i := strings.IndexAny(value, "?#"); i >= 0 {
				props[key] = value[:i]
			}
		}
	}
	// Acquisition touches come from page URLs anyone can craft; keep them short.
	for key, value := range props {
		if text, ok := value.(string); ok && isAttributionProp(key) && len(text) > maxAttributionValue {
			props[key] = text[:maxAttributionValue]
		}
	}
	return props
}

const maxAttributionValue = 200

// maxRetryAge bounds how far back a queued tag delivery may be dated.
const maxRetryAge = 24 * time.Hour

func isAttributionProp(key string) bool {
	key = strings.TrimPrefix(key, "first_")
	return strings.HasPrefix(key, "utm_") || strings.HasPrefix(key, "touch_") || key == "referrer" || key == "landing"
}

// collectClientIP keys the per-IP admission limiter. The platform gateway signs
// the client IP it resolved; that is the only source a visitor cannot forge.
// Platforms that predate signing send no metadata, so the old heuristic (the
// rightmost public X-Forwarded-For hop behind a loopback proxy) still applies
// there. Metadata that fails verification falls back to the peer address and
// never to forwarded headers.
func collectClientIP(r *http.Request) string {
	signed, err := sdk.ClientIPFromRequest(r)
	if err == nil && signed != "" {
		return signed
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	if err != nil {
		return ip
	}
	if peer := net.ParseIP(ip); peer != nil && peer.IsLoopback() {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := net.ParseIP(strings.TrimSpace(parts[i]))
			if candidate != nil && !candidate.IsLoopback() && !candidate.IsPrivate() {
				return candidate.String()
			}
		}
	}
	return ip
}

// GET /collect — public tag ingest. See file header.
func (a *App) handleCollect(w http.ResponseWriter, r *http.Request) {
	setCollectCORS(w, r)
	if len(r.URL.RawQuery) > 16384 {
		w.Header().Set("X-Apa", "payload-too-large")
		writePixel(w)
		return
	}
	if !collectAdmissionLimiter.allow("ip:" + collectClientIP(r)) {
		w.Header().Set("X-Apa", "rate-limited")
		writePixel(w)
		return
	}
	q := r.URL.Query()
	if len(q.Get("k")) > 256 || len(q.Get("eid")) > 128 || len(q.Get("e")) > 256 {
		writePixel(w)
		return
	}
	wk := lookupActiveWriteKey(requestReadDB(r), q.Get("k"))
	if wk != nil && originAllowed(wk, r) && collectLimiter.allow(wk.Key) {
		event := q.Get("e")
		if event == "" {
			event = "page_view"
		}
		props := collectProps(q)
		propsJSON := "{}"
		if b, err := json.Marshal(props); err == nil {
			propsJSON = string(b)
		}
		// A retried delivery reports how long it waited in the browser queue.
		var ts int64
		if age, err := strconv.ParseInt(q.Get("age"), 10, 64); err == nil && age > 0 && age <= maxRetryAge.Milliseconds() {
			ts = time.Now().UnixMilli() - age
		}
		// A visitor linked server-side by analytics_identify carries its user
		// from then on. uid stays accepted for pages that set it themselves.
		userID := q.Get("uid")
		if visitorID, _ := props["visitor_id"].(string); visitorID != "" {
			if linked := linkedUserID(requestReadDB(r), wk.ProjectID, visitorID); linked != "" {
				userID = linked
			}
		}
		id, err := insertEvent(requestWriteDB(r), EventInsert{
			TS:         ts,
			DeliveryID: q.Get("eid"),
			App:        wk.Site,
			Topic:      event,
			ProjectID:  wk.ProjectID,
			UserID:     userID,
			SessionID:  q.Get("sid"),
			Source:     "web",
			Props:      propsJSON,
		})
		if err == nil {
			if err := touchWriteKeyEvent(requestWriteDB(r), wk.ID, id); err != nil {
				globalCtx.Logger().Warn("tracking key counter", "err", err)
			}
			globalCtx.EmitWithProject("event.recorded", wk.ProjectID, map[string]any{
				"id": id, "app": wk.Site, "topic": event, "ts": time.Now().UnixMilli(),
			})
			w.Header().Set("X-Apa", "ok")
		} else if errors.Is(err, errDeliveryReused) {
			// A queued retry of an event that did arrive the first time.
			w.Header().Set("X-Apa", "duplicate")
		}
	} else if wk == nil {
		w.Header().Set("X-Apa", "invalid-key")
	} else {
		w.Header().Set("X-Apa", "dropped")
	}
	writePixel(w)
}

func canonicalOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && !((u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")) {
		host = net.JoinHostPort(host, port)
	}
	return strings.ToLower(u.Scheme) + "://" + host
}
