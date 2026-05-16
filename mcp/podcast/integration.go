package main

// integration.go — every cross-app call lives here. The podcast app
// owns shows/episodes/feeds; bytes, probing, analytics, ingress and
// DNS belong to sibling apps reached via ctx.PlatformAPI().
//
//   storage   (required) — files_get: byte length, mime, enclosure URL
//   media     (required) — media_get: exact duration
//   analytics (optional) — analytics_track: per-download events
//   routes    (optional) — routes_register: claim a feed hostname
//   domains   (optional) — domain_records_set: CNAME the hostname
//
// Cross-app calls use CallAppResult so the SDK strips the MCP envelope
// and unmarshals the inner JSON directly.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// audioProbe is what episode_set_audio caches onto the episode row.
type audioProbe struct {
	URL             string
	Bytes           int64
	DurationSeconds int64
	MimeType        string
	Visibility      string
	Warning         string // non-fatal: e.g. media hasn't probed duration yet
}

// probeAudio resolves a storage file id into the facts the RSS
// enclosure needs: byte length + mime + a public URL from storage, and
// exact duration from media. media's indexer probes asynchronously, so
// a freshly uploaded file may not have a duration yet — that's a
// warning, not an error; feed_validate surfaces it and the caller can
// re-run episode_set_audio.
func probeAudio(ctx *sdk.AppCtx, fileID string) (*audioProbe, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil, errors.New("audio_file_id required")
	}
	numericID, err := strconv.ParseInt(fileID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("audio_file_id %q is not a numeric storage file id", fileID)
	}

	// storage.files_get — byte length, mime, canonical URL, visibility.
	var sres struct {
		Found bool `json:"found"`
		File  *struct {
			SizeBytes   int64  `json:"size_bytes"`
			ContentType string `json:"content_type"`
			URL         string `json:"url"`
			Visibility  string `json:"visibility"`
		} `json:"file"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get",
		map[string]any{"id": numericID}, &sres); err != nil {
		return nil, fmt.Errorf("storage.files_get: %w", err)
	}
	if !sres.Found || sres.File == nil {
		return nil, fmt.Errorf("storage file %d not found", numericID)
	}

	probe := &audioProbe{
		URL:        sres.File.URL,
		Bytes:      sres.File.SizeBytes,
		MimeType:   sres.File.ContentType,
		Visibility: sres.File.Visibility,
	}
	if probe.MimeType == "" {
		probe.MimeType = "audio/mpeg"
	}
	if probe.Visibility != "public" {
		probe.Warning = fmt.Sprintf("storage file %d visibility is %q — podcast clients need a publicly fetchable enclosure; set it to public",
			numericID, probe.Visibility)
	}

	// media.media_get — exact duration. Best-effort: a not-yet-indexed
	// file just means duration stays 0 until the next probe.
	var mres struct {
		Found bool `json:"found"`
		Media *struct {
			DurationMs int64 `json:"duration_ms"`
			HasAudio   bool  `json:"has_audio"`
		} `json:"media"`
	}
	if err := ctx.PlatformAPI().CallAppResult("media", "media_get",
		map[string]any{"file_id": fileID}, &mres); err != nil {
		probe.Warning = strings.TrimSpace(probe.Warning + " media.media_get failed: " + err.Error() + " — duration unknown")
		return probe, nil
	}
	switch {
	case !mres.Found || mres.Media == nil:
		probe.Warning = strings.TrimSpace(probe.Warning + " media hasn't probed this file yet — duration unknown; re-run episode_set_audio shortly")
	case !mres.Media.HasAudio:
		probe.Warning = strings.TrimSpace(probe.Warning + " media reports no audio stream on this file")
	default:
		probe.DurationSeconds = mres.Media.DurationMs / 1000
	}
	return probe, nil
}

// trackDownload forwards an IAB-style download event to the analytics
// app. Soft dependency: any failure (analytics not installed, no
// permission) is swallowed — the download itself still succeeds.
func trackDownload(ctx *sdk.AppCtx, show *Show, ep *Episode, r *http.Request) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return
	}
	var resp struct {
		ID int64 `json:"id"`
	}
	err := ctx.PlatformAPI().CallAppResult("analytics", "analytics_track", map[string]any{
		"event": "podcast.download",
		"props": map[string]any{
			"show_id":      show.ID,
			"show_slug":    show.Slug,
			"episode_id":   ep.ID,
			"episode_guid": ep.GUID,
			"user_agent":   r.UserAgent(),
			"referer":      r.Referer(),
		},
	}, &resp)
	if err != nil {
		ctx.Logger().Info("analytics_track skipped", "episode", ep.ID, "err", err.Error())
	}
}

// ─── routes + domains: custom feed hostname wiring ─────────────────

// wireHostname claims the hostname for this sidecar with routes, and
// upserts a CNAME via domains when the hostname is known there. Returns
// a human-readable warning (or "" on full success); wiring failures
// never roll back the show write — the panel surfaces the warning and
// the operator can retry.
func wireHostname(ctx *sdk.AppCtx, hostname string) string {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return ""
	}
	var warnings []string
	if err := registerRoute(ctx, hostname); err != nil {
		warnings = append(warnings, "routes: "+err.Error())
	}
	if err := maybeUpsertCNAME(ctx, hostname); err != nil {
		warnings = append(warnings, "domains: "+err.Error())
	}
	return strings.Join(warnings, "; ")
}

// maybeUnwireHostname unregisters the route when no remaining show in
// the same project uses the hostname. DNS is never deleted — a CNAME
// may be shared.
func maybeUnwireHostname(ctx *sdk.AppCtx, hostname, projectID string) {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return
	}
	var n int
	err := ctx.AppDB().QueryRow("SELECT COUNT(*) FROM shows WHERE hostname=? AND project_id=?",
		hostname, projectID).Scan(&n)
	if err != nil {
		ctx.Logger().Warn("maybeUnwireHostname count", "host", hostname, "err", err.Error())
		return
	}
	if n > 0 {
		return
	}
	if err := unregisterRoute(ctx, hostname); err != nil {
		ctx.Logger().Info("maybeUnwireHostname unregister", "host", hostname, "err", err.Error())
	}
}

func registerRoute(ctx *sdk.AppCtx, hostname string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform api unavailable")
	}
	installID := myInstallID()
	if installID == 0 {
		return errors.New("APTEVA_INSTALL_ID unset; cannot register route")
	}
	var resp struct {
		Action string `json:"action"`
	}
	return ctx.PlatformAPI().CallAppResult("routes", "routes_register", map[string]any{
		"hostname":         hostname,
		"target":           sidecarTarget(),
		"owner_install_id": installID,
		"owner_kind":       "podcast",
	}, &resp)
}

func unregisterRoute(ctx *sdk.AppCtx, hostname string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform api unavailable")
	}
	installID := myInstallID()
	if installID == 0 {
		return errors.New("APTEVA_INSTALL_ID unset; cannot unregister route")
	}
	var resp struct {
		Removed bool `json:"removed"`
	}
	return ctx.PlatformAPI().CallAppResult("routes", "routes_unregister", map[string]any{
		"hostname":         hostname,
		"owner_install_id": installID,
	}, &resp)
}

// maybeUpsertCNAME points the hostname at the platform when domains
// manages its apex. Skips silently when domains isn't installed or
// doesn't know the apex — the operator manages DNS elsewhere.
func maybeUpsertCNAME(ctx *sdk.AppCtx, hostname string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil
	}
	apex := apexOf(hostname)
	sub := strings.TrimSuffix(strings.TrimSuffix(hostname, apex), ".")
	if sub == "" {
		sub = "@"
	}
	var probe struct {
		Domain map[string]any `json:"domain"`
	}
	if err := ctx.PlatformAPI().CallAppResult("domains", "domain_get",
		map[string]any{"name": apex}, &probe); err != nil {
		ctx.Logger().Info("domain_get probe failed (skipping CNAME)", "host", hostname, "err", err.Error())
		return nil
	}
	if probe.Domain == nil {
		return nil
	}
	target := platformPublicHost()
	if target == "" {
		return errors.New("APTEVA_PUBLIC_HOST unset; can't pick a CNAME target")
	}
	var setResp struct {
		Record any `json:"record"`
	}
	if err := ctx.PlatformAPI().CallAppResult("domains", "domain_records_set", map[string]any{
		"domain": apex,
		"name":   sub,
		"type":   "CNAME",
		"value":  target,
	}, &setResp); err != nil {
		return fmt.Errorf("domain_records_set: %w", err)
	}
	return nil
}

// ─── env-derived addressing ────────────────────────────────────────

func myInstallID() int64 {
	n, _ := strconv.ParseInt(os.Getenv("APTEVA_INSTALL_ID"), 10, 64)
	return n
}

func sidecarTarget() string {
	port := os.Getenv("APTEVA_PORT")
	if port == "" {
		port = "8080"
	}
	return "http://127.0.0.1:" + port
}

// platformPublicHost is the public host of the apteva-server this
// sidecar runs behind — the CNAME target and the path-based feed host.
func platformPublicHost() string {
	if v := strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_HOST")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("PUBLIC_URL")); v != "" {
		v = strings.TrimPrefix(v, "https://")
		v = strings.TrimPrefix(v, "http://")
		return strings.TrimSuffix(v, "/")
	}
	return ""
}

// apexOf is a naive "last two labels" registrable-apex guess — good
// enough to pick which domain to query. Real PSL handling lives in the
// domains app; multi-label TLDs (.co.uk) should be wired from the panel.
func apexOf(hostname string) string {
	parts := strings.Split(hostname, ".")
	if len(parts) < 2 {
		return hostname
	}
	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}
