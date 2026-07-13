package main

// Public ingest for the static-site tag. The manifest and SDK route both
// explicitly expose GET /collect without platform authentication; the write
// key in ?k= is the credential. Always responds with a 1x1 GIF so a bad key
// never breaks the page; events are recorded only for a valid, non-revoked,
// rate-limited, origin-allowed key. project_id is taken from the key, never
// the client.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
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
	mu sync.Mutex
	m  map[string]*tokenBucket
}

var collectLimiter = &rateLimiter{m: map[string]*tokenBucket{}}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	b := rl.m[key]
	if b == nil {
		rl.m[key] = &tokenBucket{tokens: rateBurst - 1, last: now}
		return true
	}
	b.tokens += now.Sub(b.last).Seconds() * ratePerSec
	if b.tokens > rateBurst {
		b.tokens = rateBurst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// originAllowed enforces a key's allowed-origins list, but only when the
// request actually carries an Origin/Referer host — pixel GETs often
// don't, so this is a best-effort guard, not a hard wall.
func originAllowed(wk *WriteKey, r *http.Request) bool {
	if len(wk.AllowedOrigins) == 0 {
		return true
	}
	host := hostOf(r.Header.Get("Origin"))
	if host == "" {
		host = hostOf(r.Header.Get("Referer"))
	}
	if host == "" {
		return true // nothing to check against
	}
	for _, o := range wk.AllowedOrigins {
		if strings.EqualFold(hostOf(o), host) || strings.EqualFold(o, host) {
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
	return props
}

// GET /collect — public tag ingest. See file header.
func (a *App) handleCollect(w http.ResponseWriter, r *http.Request) {
	setCollectCORS(w, r)
	q := r.URL.Query()
	wk := lookupActiveWriteKey(globalCtx.AppDB(), q.Get("k"))
	if wk != nil && originAllowed(wk, r) && collectLimiter.allow(wk.Key) {
		event := q.Get("e")
		if event == "" {
			event = "page_view"
		}
		propsJSON := "{}"
		if b, err := json.Marshal(collectProps(q)); err == nil {
			propsJSON = string(b)
		}
		id, err := insertEvent(globalCtx.AppDB(), EventInsert{
			TS:        time.Now().UnixMilli(),
			App:       wk.Site,
			Topic:     event,
			ProjectID: wk.ProjectID,
			UserID:    q.Get("uid"),
			SessionID: q.Get("sid"),
			Source:    "web",
			Props:     propsJSON,
		})
		if err == nil {
			touchWriteKey(globalCtx.AppDB(), wk.ID)
			globalCtx.EmitWithProject("event.recorded", wk.ProjectID, map[string]any{
				"id": id, "app": wk.Site, "topic": event, "ts": time.Now().UnixMilli(),
			})
			w.Header().Set("X-Apa", "ok")
		}
	} else if wk == nil {
		w.Header().Set("X-Apa", "invalid-key")
	} else {
		w.Header().Set("X-Apa", "dropped")
	}
	writePixel(w)
}
