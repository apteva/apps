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
	Status             string
	RemoteID           string
	LibraryID          string
	ReportedLibraryID  string
	CollectionID       string
	CollectionReported bool
	DurationSeconds    int64
	DurationReported   bool
	GUIDConfirmed      bool
	EmbedURL           string
	Error              string
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
	guid := stringValue(body, "guid")
	if guid != "" && !strings.EqualFold(guid, remoteID) {
		return HostObservation{}, errors.New("Bunny returned a different video ID")
	}
	reportedLibrary := stringValue(body, "videoLibraryId")
	if id := stringValue(body, "videoLibraryId"); id != "" {
		if libraryID != "" && libraryID != id {
			return HostObservation{}, errors.New("Bunny video belongs to a different library")
		}
		libraryID = id
	}
	status := intValue(body, "status")
	_, collectionReported := body["collectionId"]
	duration, durationReported := body["length"]
	obs := HostObservation{Status: "processing", RemoteID: remoteID, LibraryID: libraryID, ReportedLibraryID: reportedLibrary,
		CollectionID: stringValue(body, "collectionId"), CollectionReported: collectionReported,
		DurationSeconds: intValue(body, "length"), DurationReported: durationReported && duration != nil, GUIDConfirmed: guid != "" && strings.EqualFold(guid, remoteID)}
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
	ID                  string `json:"id"`
	AssetID             string `json:"asset_id"`
	Provider            string `json:"provider"`
	ConnectionID        int64  `json:"connection_id"`
	LibraryID           string `json:"library_id"`
	CollectionID        string `json:"collection_id"`
	SourceSHA256        string `json:"source_sha256"`
	RemoteID            string `json:"remote_id"`
	EmbedURL            string `json:"embed_url"`
	Status              string `json:"status"`
	Error               string `json:"error"`
	LastCheckedAt       string `json:"last_checked_at"`
	LinkOrigin          string `json:"link_origin"`
	DurationSeconds     int64  `json:"duration_seconds"`
	ProviderVerifiedAt  string `json:"provider_verified_at"`
	SourceEvidence      string `json:"source_evidence"`
	ProviderSourceMatch string `json:"provider_source_match"`
}

func hostingByID(db *sql.DB, pid, id string) (*Hosting, error) {
	h := &Hosting{}
	err := db.QueryRow(`SELECT id,asset_id,provider,connection_id,library_id,collection_id,source_sha256,remote_id,embed_url,status,error,last_checked_at,link_origin,duration_seconds,provider_verified_at,source_evidence,provider_source_match FROM hostings WHERE project_id=? AND id=?`, pid, id).Scan(&h.ID, &h.AssetID, &h.Provider, &h.ConnectionID, &h.LibraryID, &h.CollectionID, &h.SourceSHA256, &h.RemoteID, &h.EmbedURL, &h.Status, &h.Error, &h.LastCheckedAt, &h.LinkOrigin, &h.DurationSeconds, &h.ProviderVerifiedAt, &h.SourceEvidence, &h.ProviderSourceMatch)
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
	collectionID := brand.HostCollectionID
	if session.HostCollectionID != "" {
		collectionID = session.HostCollectionID
	}
	// A manually linked video is already hosted. A later collection-policy edit
	// must not start another transfer of that asset by accident.
	var linkedID string
	err = ctx.AppDB().QueryRow(`SELECT id FROM hostings WHERE project_id=? AND asset_id=? AND provider=? AND connection_id=? AND library_id=? AND link_origin='existing_link' AND remote_id<>'' LIMIT 1`, pid, asset.ID, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID).Scan(&linkedID)
	if err == nil {
		linked, e := hostingByID(ctx.AppDB(), pid, linkedID)
		return map[string]any{"hosting": linked, "was_existing": true, "warning": "existing provider video is linked; no transfer started"}, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var existingID string
	err = ctx.AppDB().QueryRow(`SELECT id FROM hostings WHERE project_id=? AND asset_id=? AND provider=? AND connection_id=? AND library_id=? AND collection_id=? AND source_sha256=?`, pid, asset.ID, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID, collectionID, asset.SHA256).Scan(&existingID)
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
	err = ctx.AppDB().QueryRow(`SELECT id FROM hostings WHERE project_id=? AND provider=? AND connection_id=? AND library_id=? AND collection_id=? AND source_sha256=? ORDER BY CASE WHEN remote_id<>'' THEN 0 ELSE 1 END,created_at LIMIT 1`, pid, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID, collectionID, asset.SHA256).Scan(&sharedID)
	if err == nil {
		shared, getErr := hostingByID(ctx.AppDB(), pid, sharedID)
		if getErr != nil {
			return nil, getErr
		}
		if shared.RemoteID == "" {
			return map[string]any{"hosting": shared, "was_existing": true, "warning": "same checksum has an unresolved hosting request; reconcile it before uploading again"}, nil
		}
		id := newID()
		_, err = ctx.AppDB().Exec(`INSERT OR IGNORE INTO hostings(id,project_id,asset_id,provider,connection_id,library_id,collection_id,source_sha256,remote_id,embed_url,status,error,last_checked_at,link_origin,duration_seconds,provider_verified_at,source_evidence,provider_source_match) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, pid, asset.ID, shared.Provider, shared.ConnectionID, shared.LibraryID, shared.CollectionID, shared.SourceSHA256, shared.RemoteID, shared.EmbedURL, shared.Status, shared.Error, shared.LastCheckedAt, shared.LinkOrigin, shared.DurationSeconds, shared.ProviderVerifiedAt, shared.SourceEvidence, shared.ProviderSourceMatch)
		if err != nil {
			return nil, err
		}
		var actual string
		if err = ctx.AppDB().QueryRow(`SELECT id FROM hostings WHERE project_id=? AND asset_id=? AND provider=? AND connection_id=? AND library_id=? AND collection_id=? AND source_sha256=?`, pid, asset.ID, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID, collectionID, asset.SHA256).Scan(&actual); err != nil {
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
	_, err = ctx.AppDB().Exec(`INSERT INTO hostings(id,project_id,asset_id,provider,connection_id,library_id,collection_id,source_sha256,status) VALUES(?,?,?,?,?,?,?,?,?)`, id, pid, asset.ID, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID, collectionID, asset.SHA256, "reserved")
	if err != nil {
		// A concurrent caller may have won the unique reservation. Returning its
		// record preserves idempotency without making a second provider call.
		var winner string
		lookupErr := ctx.AppDB().QueryRow(`SELECT id FROM hostings WHERE project_id=? AND asset_id=? AND provider=? AND connection_id=? AND library_id=? AND collection_id=? AND source_sha256=?`, pid, asset.ID, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID, collectionID, asset.SHA256).Scan(&winner)
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
	remoteID, startErr := adapter.Provider.Start(ctx, brand.HostConnectionID, urlResult.URL, asset.Name, collectionID)
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
	if h.CollectionID != "" && obs.CollectionReported && !strings.EqualFold(obs.CollectionID, h.CollectionID) {
		return nil, errors.New("hosted video moved to a different collection")
	}
	_, err = ctx.AppDB().Exec(`UPDATE hostings SET status=?,library_id=?,embed_url=?,error=?,last_checked_at=?,duration_seconds=CASE WHEN ? THEN ? ELSE duration_seconds END,provider_verified_at=?,updated_at=? WHERE id=? AND project_id=?`, obs.Status, obs.LibraryID, obs.EmbedURL, obs.Error, now(), obs.DurationReported, obs.DurationSeconds, now(), now(), h.ID, pid)
	if err != nil {
		return nil, err
	}
	ctx.EmitWithProject("content-catalog.hosting.updated", pid, map[string]any{"id": h.ID, "status": obs.Status})
	h, err = hostingByID(ctx.AppDB(), pid, h.ID)
	return map[string]any{"hosting": h}, err
}

// Backfill a provider video by observation. This never invokes Start/fetch_video
// and makes no claim that Bunny cryptographically matched the Storage bytes.
func (a *App) hostingLinkExisting(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "asset_id", "remote_id"); err != nil {
		return nil, err
	}
	asset, err := assetByID(ctx.AppDB(), pid, str(args, "asset_id"))
	if err != nil {
		return nil, err
	}
	if asset.Kind != "video" {
		return nil, errors.New("existing Bunny videos can only be linked to video assets")
	}
	remoteID := strings.TrimSpace(str(args, "remote_id"))
	if remoteID == "" || len(remoteID) > 128 || strings.ContainsAny(remoteID, " /\\\r\n\t?#%:@") {
		return nil, errors.New("invalid Bunny video GUID")
	}
	session, err := sessionByID(ctx.AppDB(), pid, asset.SessionID)
	if err != nil {
		return nil, err
	}
	brand, err := brandByID(ctx.AppDB(), pid, session.BrandID)
	if err != nil {
		return nil, err
	}
	if brand.HostProvider != "bunny" || brand.HostLibraryID == "" || brand.HostConnectionID <= 0 {
		return nil, errors.New("brand needs a Bunny connection and library before linking")
	}
	connectionID := number(args, "connection_id")
	if connectionID != brand.HostConnectionID {
		return nil, errors.New("connection_id must match this brand's Bunny connection")
	}
	if err = validateHostBinding(ctx, "bunny", connectionID); err != nil {
		return nil, err
	}
	expectedCollection := brand.HostCollectionID
	if session.HostCollectionID != "" {
		expectedCollection = session.HostCollectionID
	}
	obs, err := hostProviders["bunny"].Provider.Check(ctx, connectionID, remoteID, brand.HostLibraryID)
	if err != nil {
		return nil, err
	}
	if !obs.GUIDConfirmed || obs.ReportedLibraryID == "" || !obs.DurationReported || obs.DurationSeconds <= 0 || !obs.CollectionReported {
		return nil, errors.New("Bunny did not return complete GUID, library, collection, and duration metadata")
	}
	if obs.Status != "ready" {
		return nil, fmt.Errorf("Bunny video is %s, not ready", obs.Status)
	}
	if expectedCollection != "" && !strings.EqualFold(obs.CollectionID, expectedCollection) {
		return nil, errors.New("Bunny video belongs to a different collection")
	}
	// The optional Media read corroborates only the Catalog Storage asset.
	// It does not compare Bunny's hosted bytes, which get_video cannot expose.
	sourceEvidence := "media_checksum_unavailable"
	if ctx.IntegrationFor("media") != nil {
		var media struct {
			Found bool `json:"found"`
			Media struct {
				FileID       string `json:"file_id"`
				SourceSHA256 string `json:"source_sha256"`
			} `json:"media"`
		}
		if e := ctx.PlatformAPI().CallAppResult("media", "media_get", map[string]any{"_project_id": pid, "file_id": asset.StorageFileID}, &media); e == nil && media.Found && media.Media.FileID == asset.StorageFileID {
			if media.Media.SourceSHA256 != "" && media.Media.SourceSHA256 == asset.SHA256 {
				sourceEvidence = "media_storage_checksum_agrees"
			} else {
				sourceEvidence = "media_storage_checksum_unconfirmed"
			}
		}
	}
	var existingID string
	err = ctx.AppDB().QueryRow(`SELECT id FROM hostings WHERE project_id=? AND asset_id=? AND provider='bunny' AND connection_id=? AND library_id=? AND remote_id<>'' LIMIT 1`, pid, asset.ID, connectionID, brand.HostLibraryID).Scan(&existingID)
	if err == nil {
		existing, e := hostingByID(ctx.AppDB(), pid, existingID)
		if e != nil {
			return nil, e
		}
		if existing.RemoteID != remoteID {
			return nil, errors.New("asset is already linked to a different Bunny video")
		}
		_, err = ctx.AppDB().Exec(`UPDATE hostings SET collection_id=?,embed_url=?,status='ready',error='',last_checked_at=?,duration_seconds=?,provider_verified_at=?,source_evidence=?,updated_at=? WHERE project_id=? AND id=?`, obs.CollectionID, obs.EmbedURL, now(), obs.DurationSeconds, now(), sourceEvidence, now(), pid, existing.ID)
		if err != nil {
			return nil, err
		}
		fresh, err := hostingByID(ctx.AppDB(), pid, existing.ID)
		return map[string]any{"hosting": fresh, "was_existing": true}, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var otherAssetSHA string
	err = ctx.AppDB().QueryRow(`SELECT source_sha256 FROM hostings WHERE project_id=? AND provider='bunny' AND connection_id=? AND library_id=? AND remote_id=? LIMIT 1`, pid, connectionID, brand.HostLibraryID, remoteID).Scan(&otherAssetSHA)
	if err == nil && (otherAssetSHA != asset.SHA256 || asset.SHA256 == "") {
		return nil, errors.New("Bunny GUID is already linked to an asset with a different Storage checksum")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// Reconcile an unresolved reservation for the same asset and destination.
	var reservationID string
	err = ctx.AppDB().QueryRow(`SELECT id FROM hostings WHERE project_id=? AND asset_id=? AND provider='bunny' AND connection_id=? AND library_id=? AND collection_id=? AND source_sha256=? AND remote_id='' LIMIT 1`, pid, asset.ID, connectionID, brand.HostLibraryID, obs.CollectionID, asset.SHA256).Scan(&reservationID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	id := reservationID
	if id == "" {
		id = newID()
	}
	if reservationID == "" {
		_, err = ctx.AppDB().Exec(`INSERT INTO hostings(id,project_id,asset_id,provider,connection_id,library_id,collection_id,source_sha256,remote_id,embed_url,status,last_checked_at,link_origin,duration_seconds,provider_verified_at,source_evidence,provider_source_match) VALUES(?,?,?,'bunny',?,?,?,?,?,?,?,?,'existing_link',?,?,?,'unverified')`, id, pid, asset.ID, connectionID, brand.HostLibraryID, obs.CollectionID, asset.SHA256, remoteID, obs.EmbedURL, "ready", now(), obs.DurationSeconds, now(), sourceEvidence)
	} else {
		var result sql.Result
		result, err = ctx.AppDB().Exec(`UPDATE hostings SET remote_id=?,embed_url=?,status='ready',error='',last_checked_at=?,link_origin='existing_link',duration_seconds=?,provider_verified_at=?,source_evidence=?,provider_source_match='unverified',updated_at=? WHERE project_id=? AND id=? AND remote_id=''`, remoteID, obs.EmbedURL, now(), obs.DurationSeconds, now(), sourceEvidence, now(), pid, id)
		if err == nil {
			changed, e := result.RowsAffected()
			if e != nil {
				return nil, e
			}
			if changed == 0 {
				return nil, errors.New("hosting reservation changed concurrently; retry linking")
			}
		}
	}
	if err != nil {
		// A concurrent link may have won the same unique destination slot.
		var winner string
		lookupErr := ctx.AppDB().QueryRow(`SELECT id FROM hostings WHERE project_id=? AND asset_id=? AND provider='bunny' AND connection_id=? AND library_id=? AND collection_id=? AND source_sha256=?`, pid, asset.ID, connectionID, brand.HostLibraryID, obs.CollectionID, asset.SHA256).Scan(&winner)
		if lookupErr == nil {
			h, getErr := hostingByID(ctx.AppDB(), pid, winner)
			if getErr != nil {
				return nil, getErr
			}
			if h.RemoteID != remoteID {
				return nil, errors.New("asset was concurrently linked to a different Bunny video")
			}
			return map[string]any{"hosting": h, "was_existing": true}, nil
		}
		return nil, err
	}
	ctx.EmitWithProject("content-catalog.hosting.updated", pid, map[string]any{"id": id, "status": "ready", "link_origin": "existing_link"})
	h, err := hostingByID(ctx.AppDB(), pid, id)
	return map[string]any{"hosting": h, "was_existing": false}, err
}
