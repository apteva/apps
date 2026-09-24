package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// HostProvider keeps cloud hosting separate from Catalog's records. A future
// provider implements these two operations without changing the schema or UI.
type HostProvider interface {
	Start(ctx *sdk.AppCtx, connectionID int64, sourceURL, title, collectionID string) (string, error)
	Check(ctx *sdk.AppCtx, connectionID int64, remoteID, libraryID string) (HostObservation, error)
}
type HostObservation struct {
	Status    string
	RemoteID  string
	LibraryID string
	EmbedURL  string
	Error     string
}

type HostAdapter struct {
	IntegrationSlug string
	Provider        HostProvider
	SupportedKinds  map[string]bool
}

var hostProviders = map[string]HostAdapter{"bunny": {IntegrationSlug: "bunny-stream", Provider: bunnyProvider{}, SupportedKinds: map[string]bool{"video": true}}}

type bunnyProvider struct{}

func (bunnyProvider) Start(ctx *sdk.AppCtx, connectionID int64, sourceURL, title, collectionID string) (string, error) {
	input := map[string]any{"url": sourceURL, "title": title}
	if collectionID != "" {
		input["collectionId"] = collectionID
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "fetch_video", input)
	if err != nil {
		return "", err
	}
	if res == nil || !res.Success {
		return "", fmt.Errorf("Bunny fetch failed: %s", integrationError(res))
	}
	var body map[string]any
	if err = json.Unmarshal(res.Data, &body); err != nil {
		return "", fmt.Errorf("decode Bunny fetch: %w", err)
	}
	id := stringValue(body, "id")
	if id == "" {
		id = stringValue(body, "guid")
	}
	if id == "" {
		return "", errors.New("Bunny fetch returned no video ID")
	}
	return id, nil
}
func (bunnyProvider) Check(ctx *sdk.AppCtx, connectionID int64, remoteID, libraryID string) (HostObservation, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "get_video", map[string]any{"videoId": remoteID})
	if err != nil {
		return HostObservation{}, err
	}
	if res == nil || !res.Success {
		return HostObservation{}, fmt.Errorf("Bunny get_video failed: %s", integrationError(res))
	}
	var body map[string]any
	if err = json.Unmarshal(res.Data, &body); err != nil {
		return HostObservation{}, fmt.Errorf("decode Bunny video: %w", err)
	}
	if id := stringValue(body, "guid"); id != "" && id != remoteID {
		return HostObservation{}, errors.New("Bunny returned a different video ID")
	}
	if id := stringValue(body, "videoLibraryId"); id != "" {
		if libraryID != "" && libraryID != id {
			return HostObservation{}, errors.New("Bunny video belongs to a different library")
		}
		libraryID = id
	}
	status := intValue(body, "status")
	obs := HostObservation{Status: "processing", RemoteID: remoteID, LibraryID: libraryID}
	if status == 4 {
		obs.Status = "ready"
		if libraryID != "" {
			obs.EmbedURL = "https://iframe.mediadelivery.net/embed/" + libraryID + "/" + remoteID
		}
	}
	if status == 5 {
		obs.Status = "failed"
		obs.Error = "Bunny reported video processing failure"
	}
	return obs, nil
}
func integrationError(res *sdk.ExecuteResult) string {
	if res == nil {
		return "empty integration response"
	}
	if len(res.Data) > 0 {
		return string(res.Data)
	}
	return fmt.Sprintf("HTTP %d", res.Status)
}
func stringValue(m map[string]any, key string) string {
	v := m[key]
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
func intValue(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return -1
}

// Multiple video_host bindings let one project route brands to different
// libraries or providers. A stored connection must still be bound at call time.
func validateHostBinding(ctx *sdk.AppCtx, providerName string, connectionID int64) error {
	adapter, ok := hostProviders[providerName]
	if !ok {
		return fmt.Errorf("unsupported video host provider %q", providerName)
	}
	for _, bound := range ctx.IntegrationsFor("video_host") {
		if bound != nil && bound.ConnectionID == connectionID && bound.AppSlug == adapter.IntegrationSlug {
			return nil
		}
	}
	return fmt.Errorf("%s connection is not bound to Content Catalog", providerName)
}

type Hosting struct {
	ID            string `json:"id"`
	AssetID       string `json:"asset_id"`
	Provider      string `json:"provider"`
	ConnectionID  int64  `json:"connection_id"`
	LibraryID     string `json:"library_id"`
	CollectionID  string `json:"collection_id"`
	SourceSHA256  string `json:"source_sha256"`
	RemoteID      string `json:"remote_id"`
	EmbedURL      string `json:"embed_url"`
	Status        string `json:"status"`
	Error         string `json:"error"`
	LastCheckedAt string `json:"last_checked_at"`
}

func hostingByID(db *sql.DB, pid, id string) (*Hosting, error) {
	h := &Hosting{}
	err := db.QueryRow(`SELECT id,asset_id,provider,connection_id,library_id,collection_id,source_sha256,remote_id,embed_url,status,error,last_checked_at FROM hostings WHERE project_id=? AND id=?`, pid, id).Scan(&h.ID, &h.AssetID, &h.Provider, &h.ConnectionID, &h.LibraryID, &h.CollectionID, &h.SourceSHA256, &h.RemoteID, &h.EmbedURL, &h.Status, &h.Error, &h.LastCheckedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("hosting record not found")
	}
	return h, err
}
func (a *App) hostingsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "asset_id"); err != nil {
		return nil, err
	}
	if _, err = assetByID(ctx.AppDB(), pid, str(args, "asset_id")); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id FROM hostings WHERE project_id=? AND asset_id=? ORDER BY created_at DESC,id DESC`, pid, str(args, "asset_id"))
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Hosting{}
	for _, id := range ids {
		h, err := hostingByID(ctx.AppDB(), pid, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return map[string]any{"hostings": out}, nil
}
func (a *App) hostingRequest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "asset_id"); err != nil {
		return nil, err
	}
	asset, err := assetByID(ctx.AppDB(), pid, str(args, "asset_id"))
	if err != nil {
		return nil, err
	}
	if asset.ReviewStatus != "approved" {
		return nil, errors.New("asset must be approved before hosting")
	}
	if asset.SHA256 == "" {
		return nil, errors.New("asset has no Storage checksum")
	}
	fileID, err := strconv.ParseInt(asset.StorageFileID, 10, 64)
	if err != nil || fileID <= 0 {
		return nil, errors.New("asset has an invalid Storage file ID")
	}
	session, err := sessionByID(ctx.AppDB(), pid, asset.SessionID)
	if err != nil {
		return nil, err
	}
	brand, err := brandByID(ctx.AppDB(), pid, session.BrandID)
	if err != nil {
		return nil, err
	}
	adapter, ok := hostProviders[brand.HostProvider]
	if !ok {
		return nil, errors.New("brand has no supported video host")
	}
	if !adapter.SupportedKinds[asset.Kind] {
		return nil, fmt.Errorf("%s hosting does not support %s assets", brand.HostProvider, asset.Kind)
	}
	if err = validateHostBinding(ctx, brand.HostProvider, brand.HostConnectionID); err != nil {
		return nil, err
	}
	var existingID string
	err = ctx.AppDB().QueryRow(`SELECT id FROM hostings WHERE project_id=? AND asset_id=? AND provider=? AND connection_id=? AND library_id=? AND collection_id=? AND source_sha256=?`, pid, asset.ID, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID, brand.HostCollectionID, asset.SHA256).Scan(&existingID)
	if err == nil {
		existing, _ := hostingByID(ctx.AppDB(), pid, existingID)
		return map[string]any{"hosting": existing, "was_existing": true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// A file can be linked to several sessions. Reuse an already started
	// transfer for the same bytes and destination across those assets.
	var sharedID string
	err = ctx.AppDB().QueryRow(`SELECT id FROM hostings WHERE project_id=? AND provider=? AND connection_id=? AND library_id=? AND collection_id=? AND source_sha256=? ORDER BY CASE WHEN remote_id<>'' THEN 0 ELSE 1 END,created_at LIMIT 1`, pid, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID, brand.HostCollectionID, asset.SHA256).Scan(&sharedID)
	if err == nil {
		shared, getErr := hostingByID(ctx.AppDB(), pid, sharedID)
		if getErr != nil {
			return nil, getErr
		}
		if shared.RemoteID == "" {
			return map[string]any{"hosting": shared, "was_existing": true, "warning": "same checksum has an unresolved hosting request; reconcile it before uploading again"}, nil
		}
		id := newID()
		_, err = ctx.AppDB().Exec(`INSERT OR IGNORE INTO hostings(id,project_id,asset_id,provider,connection_id,library_id,collection_id,source_sha256,remote_id,embed_url,status,error,last_checked_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, pid, asset.ID, shared.Provider, shared.ConnectionID, shared.LibraryID, shared.CollectionID, shared.SourceSHA256, shared.RemoteID, shared.EmbedURL, shared.Status, shared.Error, shared.LastCheckedAt)
		if err != nil {
			return nil, err
		}
		var actual string
		if err = ctx.AppDB().QueryRow(`SELECT id FROM hostings WHERE project_id=? AND asset_id=? AND provider=? AND connection_id=? AND library_id=? AND collection_id=? AND source_sha256=?`, pid, asset.ID, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID, brand.HostCollectionID, asset.SHA256).Scan(&actual); err != nil {
			return nil, err
		}
		reused, err := hostingByID(ctx.AppDB(), pid, actual)
		return map[string]any{"hosting": reused, "was_existing": actual != id, "reused_remote": true}, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// Reserve before any external call. An ambiguous provider error remains
	// uncertain; another request returns this row instead of uploading again.
	id := newID()
	_, err = ctx.AppDB().Exec(`INSERT INTO hostings(id,project_id,asset_id,provider,connection_id,library_id,collection_id,source_sha256,status) VALUES(?,?,?,?,?,?,?,?,?)`, id, pid, asset.ID, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID, brand.HostCollectionID, asset.SHA256, "reserved")
	if err != nil {
		// A concurrent caller may have won the unique reservation. Returning its
		// record preserves idempotency without making a second provider call.
		var winner string
		lookupErr := ctx.AppDB().QueryRow(`SELECT id FROM hostings WHERE project_id=? AND asset_id=? AND provider=? AND connection_id=? AND library_id=? AND collection_id=? AND source_sha256=?`, pid, asset.ID, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID, brand.HostCollectionID, asset.SHA256).Scan(&winner)
		if lookupErr == nil {
			existing, getErr := hostingByID(ctx.AppDB(), pid, winner)
			return map[string]any{"hosting": existing, "was_existing": true}, getErr
		}
		return nil, err
	}
	var urlResult struct {
		URL string `json:"url"`
	}
	err = ctx.PlatformAPI().CallAppResult("storage", "files_get_url", map[string]any{"_project_id": pid, "id": fileID, "ttl_seconds": 86400}, &urlResult)
	if err == nil {
		parsed, parseErr := url.Parse(urlResult.URL)
		if parseErr != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			err = errors.New("Storage did not return an externally fetchable absolute URL")
		}
	}
	if err != nil || urlResult.URL == "" {
		msg := "Storage URL unavailable"
		if err != nil {
			msg = err.Error()
		}
		_, _ = ctx.AppDB().Exec(`UPDATE hostings SET status='failed',error=?,updated_at=? WHERE id=? AND project_id=?`, msg, now(), id, pid)
		return nil, fmt.Errorf("hosting not started: %s", msg)
	}
	remoteID, startErr := adapter.Provider.Start(ctx, brand.HostConnectionID, urlResult.URL, asset.Name, brand.HostCollectionID)
	if startErr != nil {
		_, _ = ctx.AppDB().Exec(`UPDATE hostings SET status='uncertain',error=?,updated_at=? WHERE id=? AND project_id=?`, startErr.Error(), now(), id, pid)
		h, _ := hostingByID(ctx.AppDB(), pid, id)
		return map[string]any{"hosting": h, "warning": "provider result uncertain; review before retry"}, nil
	}
	_, err = ctx.AppDB().Exec(`UPDATE hostings SET remote_id=?,status='processing',error='',updated_at=? WHERE id=? AND project_id=?`, remoteID, now(), id, pid)
	if err != nil {
		return nil, err
	}
	ctx.EmitWithProject("content-catalog.hosting.updated", pid, map[string]any{"id": id, "status": "processing"})
	h, err := hostingByID(ctx.AppDB(), pid, id)
	return map[string]any{"hosting": h, "was_existing": false}, err
}
func (a *App) hostingCheck(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "id"); err != nil {
		return nil, err
	}
	h, err := hostingByID(ctx.AppDB(), pid, str(args, "id"))
	if err != nil {
		return nil, err
	}
	if h.RemoteID == "" {
		return map[string]any{"hosting": h, "warning": "no remote ID; reconcile manually before retry"}, nil
	}
	if err = validateHostBinding(ctx, h.Provider, h.ConnectionID); err != nil {
		return nil, err
	}
	adapter, ok := hostProviders[h.Provider]
	if !ok {
		return nil, errors.New("hosting provider no longer supported")
	}
	obs, err := adapter.Provider.Check(ctx, h.ConnectionID, h.RemoteID, h.LibraryID)
	if err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(`UPDATE hostings SET status=?,library_id=?,embed_url=?,error=?,last_checked_at=?,updated_at=? WHERE id=? AND project_id=?`, obs.Status, obs.LibraryID, obs.EmbedURL, obs.Error, now(), now(), h.ID, pid)
	if err != nil {
		return nil, err
	}
	ctx.EmitWithProject("content-catalog.hosting.updated", pid, map[string]any{"id": h.ID, "status": obs.Status})
	h, err = hostingByID(ctx.AppDB(), pid, h.ID)
	return map[string]any{"hosting": h}, err
}
