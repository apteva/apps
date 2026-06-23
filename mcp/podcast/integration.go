package main

// integration.go — every cross-app call lives here. The podcast app
// owns shows/episodes/feeds; bytes, probing and analytics belong to
// sibling apps reached via ctx.PlatformAPI(). Public hostname ingress
// and TLS certificates are server-native platform callbacks. DNS
// remains a Domains app concern, matching deploy/fleet.
//
//   storage   (required) — files_get: byte length, mime, enclosure URL
//   media     (required) — media_get: exact duration
//   analytics (optional) — analytics_track: per-download events
//   ingress   (platform) — ExposeIngress: claim a feed hostname + cert
//   domains   (optional) — domain_records_set/delete: DNS records
//
// Cross-app calls use CallAppResult so the SDK strips the MCP envelope
// and unmarshals the inner JSON directly.

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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

// ─── platform ingress: custom feed hostname wiring ─────────────────

// wireHostname claims the show's feed hostname for this sidecar through
// server-native ingress, then writes DNS through the optional Domains
// app. The server owns host routing and certificate allowance; Domains
// points the hostname at the platform host when bound. Wiring
// failures never roll back the show write — the panel surfaces the
// warning and the operator can retry by saving the feed domain again.
func wireHostname(ctx *sdk.AppCtx, show *Show) string {
	if show == nil || strings.TrimSpace(show.Hostname) == "" {
		return ""
	}
	var warnings []string
	if err := exposeFeedIngress(ctx, show); err != nil {
		warnings = append(warnings, "ingress: "+err.Error())
	}
	if err := upsertFeedDNSViaDomains(ctx, show); err != nil {
		warnings = append(warnings, "domains: "+err.Error())
	}
	return strings.Join(warnings, "; ")
}

// maybeUnwireHostname unregisters the ingress route and best-effort
// removes DNS when no remaining show in the same project uses the
// hostname.
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
	if err := unexposeFeedIngress(ctx, hostname); err != nil {
		ctx.Logger().Info("maybeUnwireHostname unregister", "host", hostname, "err", err.Error())
	}
	if err := deleteFeedDNSViaDomains(ctx, hostname, projectID); err != nil {
		ctx.Logger().Info("maybeUnwireHostname dns delete", "host", hostname, "err", err.Error())
	}
}

func exposeFeedIngress(ctx *sdk.AppCtx, show *Show) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform api unavailable")
	}
	hostname := strings.TrimSpace(show.Hostname)
	if hostname == "" {
		return errors.New("hostname required")
	}
	_, err := ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
		Hostname:  hostname,
		Target:    sidecarTarget(),
		ProjectID: show.ProjectID,
		OwnerKind: "podcast",
		CertFQDN:  hostname,
	})
	return err
}

func unexposeFeedIngress(ctx *sdk.AppCtx, hostname string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform api unavailable")
	}
	return ctx.PlatformAPI().UnexposeIngress(hostname)
}

func upsertFeedDNSViaDomains(ctx *sdk.AppCtx, show *Show) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform api unavailable")
	}
	target := platformDNSHost(ctx)
	if target == "" {
		return errors.New("platform public host unavailable; set APTEVA_PUBLIC_HOST/PUBLIC_URL or platform public_url")
	}
	domain, name, err := resolveManagedApex(ctx, show.ProjectID, show.Hostname)
	if err != nil {
		return err
	}
	rtype := "CNAME"
	if net.ParseIP(target) != nil {
		rtype = "A"
	}
	if rtype == "CNAME" && name == "@" {
		return errors.New("apex CNAME is not supported; use a subdomain or point the apex manually with an A/ALIAS record")
	}
	var out map[string]any
	return ctx.PlatformAPI().CallAppResult("domains", "domain_records_set", map[string]any{
		"_project_id": show.ProjectID,
		"domain":      domain,
		"name":        name,
		"type":        rtype,
		"value":       target,
		"ttl":         600,
	}, &out)
}

func deleteFeedDNSViaDomains(ctx *sdk.AppCtx, hostname, projectID string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform api unavailable")
	}
	domain, name, err := resolveManagedApex(ctx, projectID, hostname)
	if err != nil {
		return err
	}
	rtype := "CNAME"
	if target := platformDNSHost(ctx); net.ParseIP(target) != nil {
		rtype = "A"
	}
	var out map[string]any
	return ctx.PlatformAPI().CallAppResult("domains", "domain_records_delete", map[string]any{
		"_project_id": projectID,
		"domain":      domain,
		"name":        name,
		"type":        rtype,
	}, &out)
}

func resolveManagedApex(ctx *sdk.AppCtx, projectID, hostname string) (domain, name string, err error) {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	if host == "" {
		return "", "", errors.New("hostname required")
	}
	var resp struct {
		Domains []struct {
			Name string `json:"name"`
		} `json:"domains"`
	}
	if err := ctx.PlatformAPI().CallAppResult("domains", "domain_list", map[string]any{
		"_project_id": projectID,
	}, &resp); err != nil {
		return "", "", fmt.Errorf("domains.domain_list: %w", err)
	}
	var best string
	for _, domain := range resp.Domains {
		d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain.Name), "."))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			if len(d) > len(best) {
				best = d
			}
		}
	}
	if best == "" {
		return "", "", fmt.Errorf("no managed domain matches %q; add the apex in Domains or point DNS at %q manually", host, platformDNSHost(ctx))
	}
	if host == best {
		return best, "@", nil
	}
	return best, strings.TrimSuffix(host[:len(host)-len(best)], "."), nil
}

// ─── env-derived addressing ────────────────────────────────────────

func sidecarTarget() string {
	port := os.Getenv("APTEVA_PORT")
	if port == "" {
		port = "8080"
	}
	return "http://127.0.0.1:" + port
}

// platformPublicHost is the public host of the apteva-server this
// sidecar runs behind — used for path-based feed URLs.
func platformPublicHost() string {
	if v := strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_HOST")); v != "" {
		return normalizeHost(v)
	}
	if v := strings.TrimSpace(os.Getenv("PUBLIC_URL")); v != "" {
		return normalizeHost(v)
	}
	return ""
}

func platformDNSHost(ctx *sdk.AppCtx) string {
	if h := platformPublicHost(); h != "" {
		return h
	}
	if ctx == nil || ctx.PlatformAPI() == nil {
		return ""
	}
	info, err := ctx.PlatformAPI().PlatformInfo()
	if err != nil || info == nil {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(info.PublicURL))
	if err != nil {
		return ""
	}
	host := u.Host
	if host == "" {
		host = u.Path
	}
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func normalizeHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err == nil && u.Host != "" {
		raw = u.Host
	}
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimSuffix(raw, "/")
	if i := strings.LastIndexByte(raw, ':'); i > 0 {
		raw = raw[:i]
	}
	return strings.ToLower(strings.TrimSuffix(raw, "."))
}
