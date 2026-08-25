package main

// URL minting — single source of truth for how storage hands shareable
// links back to callers (agents, dashboards, downstream apps).
//
// Shareable URLs use a dedicated, read-only public path. Authenticated
// dashboard reads keep using /files so no mutation or metadata route
// needs to bypass the platform gateway.
//
//   <PublicURL>/api/apps/storage/public/files/<id>/content
//
// Whether a shareable URL works without auth depends on the file's
// visibility, decided server-side in httpServeContent:
//
//   public  → anyone can fetch (no session, no signature)
//   signed  → requires ?sig=…&exp=… (added by files_get_url)
//   private → requires an authenticated request — dashboard cookie,
//             API key, or app-install bearer
//
// Resolution chain for the absolute base:
//
//   1. cdn zone (when cdn_zone_id != 0 in install config) — public
//      URLs only; signed and private always go to publicBase. cdn
//      mints "https://<zone-hostname>/files/<id>/content".
//   2. ctx.PlatformInfo().PublicURL — live-fresh from the platform's
//      server_settings.public_url (admin-editable from Settings →
//      Server). Short cache via the SDK so setting changes propagate
//      without sidecar restart.
//   3. APTEVA_PUBLIC_URL / STORAGE_PUBLIC_URL env — fallback for
//      older platforms / test harnesses. Frozen at spawn.
//   4. "" — neither available; fall back to relative paths so the
//      same-origin dashboard still works in dev / no-network installs.

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// publicBase resolves the platform's externally-reachable base URL.
// Trailing slashes are stripped so callers can append paths directly.
func publicBase(ctx *sdk.AppCtx) string {
	if ctx != nil {
		if info, err := ctx.PlatformInfo(); err == nil && info != nil && info.PublicURL != "" {
			return strings.TrimRight(info.PublicURL, "/")
		}
	}
	if v := envPublicURL(); v != "" {
		return strings.TrimRight(v, "/")
	}
	return ""
}

// envPublicURL is split out for testkit override — tests set
// STORAGE_PUBLIC_URL to a controlled value rather than depending on
// APTEVA_PUBLIC_URL which is reserved for the real platform.
func envPublicURL() string {
	for _, key := range []string{"STORAGE_PUBLIC_URL", "APTEVA_PUBLIC_URL"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

// absoluteContentURL returns the file's canonical URL. Public files use
// the read-only public route; signed and private files use the authenticated
// route until a signed share URL is explicitly minted.
//
// When the install is linked to a cdn zone (cdn_zone_id != 0) AND
// the file's visibility is public, the URL is minted on the zone's
// hostname via cdn_url_for. Signed and private files always go to
// publicBase because the cdn edge doesn't carry HMAC or auth state
// in v0.1.
func absoluteContentURL(ctx *sdk.AppCtx, f *File) string {
	rel := buildContentURL(f) // "/files/<id>/content"
	if f != nil && f.Visibility == "public" {
		rel = withContentVersion(rel, f)
		if u := cdnURLFor(ctx, rel, f.ProjectID); u != "" {
			return u
		}
		rel = withContentVersion(buildPublicContentURL(f), f)
	}
	base := publicBase(ctx)
	if base == "" {
		return "/api/apps/storage" + rel
	}
	return base + "/api/apps/storage" + rel
}

func withContentVersion(rel string, f *File) string {
	version := contentVersion(f)
	if version == "" {
		return rel
	}
	separator := "?"
	if strings.Contains(rel, "?") {
		separator = "&"
	}
	return rel + separator + "v=" + url.QueryEscape(version)
}

func contentVersion(f *File) string {
	if f == nil {
		return ""
	}
	hash := strings.ToLower(strings.TrimSpace(f.SHA256))
	if len(hash) > 16 {
		hash = hash[:16]
	}
	for _, r := range hash {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return hash
}

// cdnURLFor asks the cdn app to mint a URL on the install's linked
// zone. Returns "" when:
//   - the install isn't linked to a zone (cdn_zone_id == 0)
//   - the cdn app isn't installed / unreachable (CallAppResult errors)
//   - cdn returns an empty URL (shouldn't happen, treat as fallback)
//
// All failure modes fall through to publicBase silently — a cdn
// outage must never produce broken file URLs.
type cdnBaseEntry struct {
	base      string
	expiresAt time.Time
}

var cdnBases = struct {
	sync.Mutex
	entries map[string]cdnBaseEntry
}{entries: map[string]cdnBaseEntry{}}

// cdnURLFor resolves the zone once, then assembles file URLs locally. The CDN
// tool itself performs a DB lookup per call even though every file in a zone
// shares the same scheme + hostname. Caching the root URL removes a cross-app
// HTTP call per public row while keeping zone changes fresh within one minute.
func cdnURLFor(ctx *sdk.AppCtx, rel, projectID string) string {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return ""
	}
	zoneID := cdnZoneForInstall(ctx)
	if zoneID == 0 {
		return ""
	}
	pid := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
	if pid == "" {
		pid = strings.TrimSpace(projectID)
	}
	cacheKey := fmt.Sprintf("%d\x00%s", zoneID, pid)
	now := time.Now()
	cdnBases.Lock()
	defer cdnBases.Unlock()
	if cached, ok := cdnBases.entries[cacheKey]; ok && now.Before(cached.expiresAt) {
		if cached.base == "" {
			return ""
		}
		return cached.base + rel
	}

	args := map[string]any{
		"zone_id":     zoneID,
		"origin_path": "/",
	}
	if pid != "" {
		args["_project_id"] = pid
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := ctx.PlatformAPI().CallAppResult("cdn", "cdn_url_for", args, &out); err != nil {
		cdnBases.entries[cacheKey] = cdnBaseEntry{expiresAt: now.Add(5 * time.Second)}
		return ""
	}
	base := strings.TrimRight(out.URL, "/")
	if base == "" {
		cdnBases.entries[cacheKey] = cdnBaseEntry{expiresAt: now.Add(5 * time.Second)}
		return ""
	}
	cdnBases.entries[cacheKey] = cdnBaseEntry{base: base, expiresAt: now.Add(time.Minute)}
	return base + rel
}

func resetCDNBaseCache() {
	cdnBases.Lock()
	cdnBases.entries = map[string]cdnBaseEntry{}
	cdnBases.Unlock()
}

// cdnZoneForInstall reads the cdn_zone_id install config; "0" or
// missing means no link. Parses defensively — the config field is a
// text input, so a typo lands as 0 rather than crashing.
func cdnZoneForInstall(ctx *sdk.AppCtx) int64 {
	if ctx == nil {
		return 0
	}
	v := strings.TrimSpace(ctx.Config().Get("cdn_zone_id"))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// signedAbsoluteURL returns the absolute form of a signed URL.
// Uses the read-only public route and includes the filename suffix so
// the URL ends in the proper extension for downstream sniffers.
// `?sig=&exp=` are appended; the manifest lets this constrained route
// through the gateway and httpServeContent validates the signature.
//
// project_id rides as a query param so apteva-server's
// /api/apps/<name>/... proxy (handleAppProxy) can route to the
// install of `storage` that owns the file. Without it the proxy
// falls back to byName.GetByName which is last-wins across all
// project-scoped installs and 404s for any file not in that
// arbitrarily-chosen install's DB. The DLNA app's gateway-proxied
// fetch is the canonical case that surfaces this — the TV's URL
// has no auth context to disambiguate by, only what the URL
// itself carries.
func signedAbsoluteURL(ctx *sdk.AppCtx, f *File, sig string, exp int64) string {
	return signedAbsoluteURLWithDisposition(ctx, f, sig, exp, DispositionInline)
}

func signedAbsoluteURLWithDisposition(ctx *sdk.AppCtx, f *File, sig string, exp int64, disposition ContentDisposition) string {
	rel := buildPublicContentURL(f)
	if disposition == DispositionAttachment {
		rel = buildPublicDownloadURL(f)
	}
	q := fmt.Sprintf("?sig=%s&exp=%d", url.QueryEscape(sig), exp)
	if version := contentVersion(f); version != "" {
		q += "&v=" + url.QueryEscape(version)
	}
	if f != nil && f.ProjectID != "" {
		q += "&project_id=" + url.QueryEscape(f.ProjectID)
	}
	base := publicBase(ctx)
	if base == "" {
		return "/api/apps/storage" + rel + q
	}
	return base + "/api/apps/storage" + rel + q
}

func signedAbsoluteProxyURL(ctx *sdk.AppCtx, f *File, sig string, exp int64, disposition ContentDisposition) string {
	rel := buildPublicProxyURL(f, disposition)
	q := fmt.Sprintf("?sig=%s&exp=%d", url.QueryEscape(sig), exp)
	if version := contentVersion(f); version != "" {
		q += "&v=" + url.QueryEscape(version)
	}
	if f != nil && f.ProjectID != "" {
		q += "&project_id=" + url.QueryEscape(f.ProjectID)
	}
	base := publicBase(ctx)
	if base == "" {
		return "/api/apps/storage" + rel + q
	}
	return base + "/api/apps/storage" + rel + q
}
