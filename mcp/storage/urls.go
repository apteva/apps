package main

// URL minting — single source of truth for how storage hands shareable
// links back to callers (agents, dashboards, downstream apps).
//
// One URL per file. Same prefix regardless of visibility — like S3,
// where a public bucket's object URL and a presigned URL share the
// same path and only differ in auth carriers (query params, headers,
// or none).
//
//   <PublicURL>/api/apps/storage/files/<id>/content
//
// Whether that URL works without auth depends on the file's
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
//   2. ctx.PlatformAPI().WhoAmI().PublicURL — live-fresh from the
//      platform's server_settings.public_url (admin-editable from
//      Settings → Server). Sub-second cache via the SDK so setting
//      changes propagate without sidecar restart.
//   3. APTEVA_PUBLIC_URL / STORAGE_PUBLIC_URL env — fallback for
//      older platforms / test harnesses. Frozen at spawn.
//   4. "" — neither available; fall back to relative paths so the
//      same-origin dashboard still works in dev / no-network installs.

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// publicBase resolves the platform's externally-reachable base URL.
// Trailing slashes are stripped so callers can append paths directly.
func publicBase(ctx *sdk.AppCtx) string {
	if ctx != nil && ctx.PlatformAPI() != nil {
		if id, err := ctx.PlatformAPI().WhoAmI(); err == nil && id != nil && id.PublicURL != "" {
			return strings.TrimRight(id.PublicURL, "/")
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

// absoluteContentURL returns the file's canonical URL. Same shape
// for every visibility level — what differs is whether the request
// for the URL needs auth.
//
// When the install is linked to a cdn zone (cdn_zone_id != 0) AND
// the file's visibility is public, the URL is minted on the zone's
// hostname via cdn_url_for. Signed and private files always go to
// publicBase because the cdn edge doesn't carry HMAC or auth state
// in v0.1.
func absoluteContentURL(ctx *sdk.AppCtx, f *File) string {
	rel := buildContentURL(f) // "/files/<id>/content"
	if f != nil && f.Visibility == "public" {
		if u := cdnURLFor(ctx, rel); u != "" {
			return u
		}
	}
	base := publicBase(ctx)
	if base == "" {
		return "/api/apps/storage" + rel
	}
	return base + "/api/apps/storage" + rel
}

// cdnURLFor asks the cdn app to mint a URL on the install's linked
// zone. Returns "" when:
//   - the install isn't linked to a zone (cdn_zone_id == 0)
//   - the cdn app isn't installed / unreachable (CallAppResult errors)
//   - cdn returns an empty URL (shouldn't happen, treat as fallback)
//
// All failure modes fall through to publicBase silently — a cdn
// outage must never produce broken file URLs.
func cdnURLFor(ctx *sdk.AppCtx, rel string) string {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return ""
	}
	zoneID := cdnZoneForInstall(ctx)
	if zoneID == 0 {
		return ""
	}
	args := map[string]any{
		"zone_id":     zoneID,
		"origin_path": rel,
	}
	if pid := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); pid != "" {
		args["_project_id"] = pid
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := ctx.PlatformAPI().CallAppResult("cdn", "cdn_url_for", args, &out); err != nil {
		return ""
	}
	return out.URL
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
// Same path as absoluteContentURL — including the filename suffix
// so the URL ends in the proper extension for downstream sniffers.
// `?sig=&exp=` are appended; the platform's authMiddleware carves
// out signed URLs for app paths.
func signedAbsoluteURL(ctx *sdk.AppCtx, f *File, sig string, exp int64) string {
	rel := buildContentURL(f) // includes filename when present
	q := fmt.Sprintf("?sig=%s&exp=%d", sig, exp)
	base := publicBase(ctx)
	if base == "" {
		return "/api/apps/storage" + rel + q
	}
	return base + "/api/apps/storage" + rel + q
}
