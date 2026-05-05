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
//   1. ctx.PlatformAPI().WhoAmI().PublicURL — live-fresh from the
//      platform's server_settings.public_url (admin-editable from
//      Settings → Server). Sub-second cache via the SDK so setting
//      changes propagate without sidecar restart.
//   2. APTEVA_PUBLIC_URL / STORAGE_PUBLIC_URL env — fallback for
//      older platforms / test harnesses. Frozen at spawn.
//   3. "" — neither available; fall back to relative paths so the
//      same-origin dashboard still works in dev / no-network installs.

import (
	"fmt"
	"os"
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
func absoluteContentURL(ctx *sdk.AppCtx, f *File) string {
	rel := buildContentURL(f) // "/files/<id>/content"
	base := publicBase(ctx)
	if base == "" {
		return "/api/apps/storage" + rel
	}
	return base + "/api/apps/storage" + rel
}

// signedAbsoluteURL returns the absolute form of a signed URL.
// Same path as absoluteContentURL, with ?sig=&exp= appended; the
// platform's authMiddleware carves out signed URLs for app paths.
func signedAbsoluteURL(ctx *sdk.AppCtx, fileID int64, sig string, exp int64) string {
	rel := fmt.Sprintf("/files/%d/content?sig=%s&exp=%d", fileID, sig, exp)
	base := publicBase(ctx)
	if base == "" {
		return "/api/apps/storage" + rel
	}
	return base + "/api/apps/storage" + rel
}
