package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	"github.com/apteva/apps/mcp/computer/internal/browser/browserbase"
	"github.com/apteva/apps/mcp/computer/internal/browser/replay"
	"github.com/apteva/apps/mcp/computer/internal/browser/steel"
)

var (
	errRecordingProcessing = errors.New("recording is still processing")
	newReplayResolver      = defaultReplayResolver
)

type replayResolverFactory func(*sdk.AppCtx, string) (replay.Resolver, error)

func defaultReplayResolver(ctx *sdk.AppCtx, backend string) (replay.Resolver, error) {
	cfg := backendConfig(ctx, map[string]any{}, backend, 0, 0, backends.EnvironmentOptions{})
	switch backend {
	case "browserbase":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, errors.New("browserbase: api_key is required for recording retrieval")
		}
		return browserbase.NewReplayResolver(cfg.APIKey), nil
	case "steel":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, errors.New("steel: api_key is required for recording retrieval")
		}
		return steel.NewReplayResolver(cfg.APIKey), nil
	default:
		return nil, nil
	}
}

func replayResolverForSession(ctx *sdk.AppCtx, row *ComputerSession) (replay.Resolver, error) {
	lease, err := leaseForSession(ctx.AppDB(), row.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return newReplayResolver(ctx, row.Backend)
	}
	if err != nil {
		return nil, err
	}
	cfg, err := leaseConfig(ctx, lease)
	if err != nil {
		return nil, err
	}
	switch row.Backend {
	case "browserbase":
		return browserbase.NewReplayResolver(cfg.APIKey), nil
	case "steel":
		return steel.NewReplayResolver(cfg.APIKey), nil
	}
	return nil, fmt.Errorf("unsupported recording backend %s", row.Backend)
}

func (a *App) toolBrowserRecording(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strings.TrimSpace(stringArg(args, "session_id"))
	if id == "" {
		return nil, errors.New("session_id required")
	}
	requestCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.recordingMetadata(requestCtx, ctx, id)
}

func (a *App) recordingMetadata(requestCtx context.Context, ctx *sdk.AppCtx, id string) (map[string]any, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("computer session history is unavailable")
	}
	row, err := dbGetSessionMetadata(ctx.AppDB(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("session %s not found", id)
	}
	if err != nil {
		return nil, err
	}
	if !recordingSupported(row.Backend) {
		return recordingOutput(ctx, row, "unsupported", nil, ""), nil
	}
	if row.Status == "active" {
		return recordingOutput(ctx, row, "recording", nil, ""), nil
	}
	if row.BackendSessionID == "" {
		_ = a.updateRecordingStatus(ctx, row, "unavailable", "")
		return recordingOutput(ctx, row, "unavailable", nil, "provider session id is unavailable"), nil
	}

	resolver, err := replayResolverForSession(ctx, row)
	if err != nil {
		_ = a.updateRecordingStatus(ctx, row, "failed", err.Error())
		return recordingOutput(ctx, row, "failed", nil, err.Error()), nil
	}
	recording, err := resolver.Metadata(requestCtx, row.BackendSessionID)
	if errors.Is(err, replay.ErrNotFound) {
		status := missingRecordingStatus(row, time.Now())
		_ = a.updateRecordingStatus(ctx, row, status, "")
		return recordingOutput(ctx, row, status, nil, ""), nil
	}
	if err != nil {
		_ = a.updateRecordingStatus(ctx, row, "failed", err.Error())
		return recordingOutput(ctx, row, "failed", nil, err.Error()), nil
	}
	status := recording.Status
	if status == "" {
		status = "ready"
	}
	_ = a.updateRecordingStatus(ctx, row, status, "")
	return recordingOutput(ctx, row, status, recording.Streams, ""), nil
}

func missingRecordingStatus(row *ComputerSession, now time.Time) string {
	if row == nil || row.ClosedAt == nil {
		return "processing"
	}
	closedAt, err := time.Parse(time.RFC3339Nano, *row.ClosedAt)
	if err != nil || now.Sub(closedAt) <= recordingProcessingWindow {
		return "processing"
	}
	return "unavailable"
}

func recordingOutput(ctx *sdk.AppCtx, row *ComputerSession, status string, streams []replay.RecordingStream, message string) map[string]any {
	streamOutput := make([]map[string]any, 0, len(streams))
	for _, stream := range streams {
		streamOutput = append(streamOutput, map[string]any{
			"id":           stream.ID,
			"start_ms":     stream.StartMS,
			"end_ms":       stream.EndMS,
			"playlist_url": recordingPlaylistURL(ctx, row.ID, stream.ID),
		})
	}
	out := map[string]any{
		"session_id":          row.ID,
		"backend":             row.Backend,
		"status":              status,
		"recording_supported": recordingSupported(row.Backend),
		"streams":             streamOutput,
	}
	if message != "" {
		out["message"] = message
	}
	return out
}

func (a *App) updateRecordingStatus(ctx *sdk.AppCtx, row *ComputerSession, status, message string) error {
	if row == nil || row.RecordingStatus == status {
		return nil
	}
	if err := dbUpdateRecordingStatus(ctx.AppDB(), row.ID, status, time.Now()); err != nil {
		return err
	}
	previous := row.RecordingStatus
	row.RecordingStatus = status
	payload := map[string]any{
		"session_id":         row.ID,
		"backend":            row.Backend,
		"backend_session_id": row.BackendSessionID,
		"recording_status":   status,
		"previous_status":    previous,
	}
	if message != "" {
		payload["error"] = message
	}
	switch status {
	case "ready":
		emitEvent(ctx, "recording.ready", payload)
	case "failed":
		emitEvent(ctx, "recording.failed", payload)
	}
	return nil
}

func recordingPlaylistURL(ctx *sdk.AppCtx, sessionID, streamID string) string {
	path := "/api/apps/computer/sessions/" + url.PathEscape(sessionID) + "/recording/" + url.PathEscape(streamID) + ".m3u8"
	return withRecordingQuery(path, ctx, "", "")
}

func recordingAssetURL(ctx *sdk.AppCtx, sessionID, streamID, token string) string {
	path := "/api/apps/computer/sessions/" + url.PathEscape(sessionID) + "/recording/" + url.PathEscape(streamID) + "/asset"
	return withRecordingQuery(path, ctx, "token", token)
}

func withRecordingQuery(path string, ctx *sdk.AppCtx, key, value string) string {
	query := url.Values{}
	if ctx != nil && ctx.CurrentProject() != "" {
		query.Set("project_id", ctx.CurrentProject())
	}
	if key != "" {
		query.Set(key, value)
	}
	if encoded := query.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func (a *App) recordingPlaylist(requestCtx context.Context, ctx *sdk.AppCtx, id, streamID string) ([]byte, string, error) {
	row, resolver, err := recordingResolver(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if row.Status == "active" {
		return nil, "", errRecordingProcessing
	}
	body, contentType, err := resolver.Playlist(requestCtx, row.BackendSessionID, streamID)
	if err != nil {
		return nil, "", err
	}
	if resourceResolver, ok := resolver.(replay.ResourceResolver); ok {
		body, err = rewriteAuthenticatedPlaylist(body, resourceResolver, row.BackendSessionID, func(token string) string {
			return recordingAssetURL(ctx, id, streamID, token)
		})
		if err != nil {
			return nil, "", err
		}
	}
	if contentType == "" {
		contentType = "application/vnd.apple.mpegurl"
	}
	return body, contentType, nil
}

func recordingResolver(ctx *sdk.AppCtx, id string) (*ComputerSession, replay.Resolver, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, nil, errors.New("computer session history is unavailable")
	}
	row, err := dbGetSessionMetadata(ctx.AppDB(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("session %s not found", id)
	}
	if err != nil {
		return nil, nil, err
	}
	if !recordingSupported(row.Backend) {
		return row, nil, fmt.Errorf("backend %q does not support recordings", row.Backend)
	}
	if row.BackendSessionID == "" {
		return row, nil, errors.New("provider session id is unavailable")
	}
	resolver, err := replayResolverForSession(ctx, row)
	return row, resolver, err
}

func rewriteAuthenticatedPlaylist(body []byte, resolver replay.ResourceResolver, providerSessionID string, route func(string) string) ([]byte, error) {
	lines := strings.Split(string(body), "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			rewritten, err := rewritePlaylistURIAttributes(line, resolver, providerSessionID, route)
			if err != nil {
				return nil, err
			}
			lines[index] = rewritten
			continue
		}
		rewritten, err := rewritePlaylistURI(trimmed, resolver, providerSessionID, route)
		if err != nil {
			return nil, err
		}
		lines[index] = rewritten
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func rewritePlaylistURI(raw string, resolver replay.ResourceResolver, providerSessionID string, route func(string) string) (string, error) {
	token, err := resolver.SignResource(providerSessionID, raw)
	if errors.Is(err, replay.ErrExternalResource) {
		return raw, nil
	}
	if err != nil {
		return "", err
	}
	return route(token), nil
}

func rewritePlaylistURIAttributes(line string, resolver replay.ResourceResolver, providerSessionID string, route func(string) string) (string, error) {
	const marker = `URI="`
	from := 0
	for {
		start := strings.Index(line[from:], marker)
		if start < 0 {
			return line, nil
		}
		start += from + len(marker)
		end := strings.IndexByte(line[start:], '"')
		if end < 0 {
			return line, nil
		}
		end += start
		rewritten, err := rewritePlaylistURI(line[start:end], resolver, providerSessionID, route)
		if err != nil {
			return "", err
		}
		line = line[:start] + rewritten + line[end:]
		from = start + len(rewritten) + 1
	}
}

func (a *App) handleRecordingMetadata(w http.ResponseWriter, r *http.Request) {
	id, _, ok := parseRecordingPath(r.URL.Path, "metadata")
	if !ok {
		httpErr(w, http.StatusBadRequest, "session id required")
		return
	}
	ctx := appCtxForRequest(r, nil)
	out, err := a.recordingMetadata(r.Context(), ctx, id)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, out)
}

func (a *App) handleRecordingPlaylist(w http.ResponseWriter, r *http.Request) {
	id, streamID, ok := parseRecordingPath(r.URL.Path, "playlist")
	if !ok {
		httpErr(w, http.StatusBadRequest, "session id and recording stream id required")
		return
	}
	body, _, err := a.recordingPlaylist(r.Context(), appCtxForRequest(r, nil), id, streamID)
	if err != nil {
		writeRecordingHTTPError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(body)
}

func (a *App) handleRecordingAsset(w http.ResponseWriter, r *http.Request) {
	id, streamID, ok := parseRecordingPath(r.URL.Path, "asset")
	if !ok {
		httpErr(w, http.StatusBadRequest, "session id and recording stream id required")
		return
	}
	ctx := appCtxForRequest(r, nil)
	row, resolver, err := recordingResolver(ctx, id)
	if err != nil {
		writeRecordingHTTPError(w, err)
		return
	}
	resourceResolver, ok := resolver.(replay.ResourceResolver)
	if !ok {
		httpErr(w, http.StatusNotFound, "recording resource not found")
		return
	}
	body, contentType, err := resourceResolver.Resource(r.Context(), row.BackendSessionID, r.URL.Query().Get("token"))
	if err != nil {
		writeRecordingHTTPError(w, err)
		return
	}
	if strings.Contains(strings.ToLower(contentType), "mpegurl") || strings.HasPrefix(strings.TrimSpace(string(body)), "#EXTM3U") {
		body, err = rewriteAuthenticatedPlaylist(body, resourceResolver, row.BackendSessionID, func(token string) string {
			return recordingAssetURL(ctx, id, streamID, token)
		})
		if err != nil {
			writeRecordingHTTPError(w, err)
			return
		}
		contentType = "application/vnd.apple.mpegurl"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(body)
}

func parseRecordingPath(path, kind string) (string, string, bool) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, "/sessions/"), "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] != "recording" {
		return "", "", false
	}
	switch kind {
	case "metadata":
		return parts[0], "", len(parts) == 2
	case "playlist":
		if len(parts) != 3 || !strings.HasSuffix(parts[2], ".m3u8") {
			return "", "", false
		}
		return parts[0], strings.TrimSuffix(parts[2], ".m3u8"), true
	case "asset":
		if len(parts) != 4 || parts[3] != "asset" || parts[2] == "" {
			return "", "", false
		}
		return parts[0], parts[2], true
	default:
		return "", "", false
	}
}

func writeRecordingHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRecordingProcessing):
		httpErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, replay.ErrNotFound):
		httpErr(w, http.StatusNotFound, err.Error())
	default:
		httpErr(w, http.StatusBadGateway, err.Error())
	}
}
