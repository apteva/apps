package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type HostingIntent struct {
	ID               string `json:"id"`
	AssetID          string `json:"asset_id"`
	SessionID        string `json:"session_id"`
	StorageInstallID int64  `json:"storage_install_id"`
	StorageFileID    string `json:"storage_file_id"`
	Provider         string `json:"provider"`
	ConnectionID     int64  `json:"connection_id"`
	LibraryID        string `json:"library_id"`
	CollectionID     string `json:"collection_id"`
	Title            string `json:"title"`
	Status           string `json:"status"`
	ChecksumStatus   string `json:"checksum_status"`
	Error            string `json:"error"`
	HostingID        string `json:"hosting_id"`
	Attempts         int    `json:"attempts"`
	NextCheckAt      string `json:"next_check_at"`
}

const intentColumns = `id,asset_id,session_id,storage_install_id,storage_file_id,provider,connection_id,library_id,collection_id,title,status,checksum_status,error,hosting_id,attempts,next_check_at`

func scanHostingIntent(row interface{ Scan(...any) error }) (*HostingIntent, error) {
	i := &HostingIntent{}
	err := row.Scan(&i.ID, &i.AssetID, &i.SessionID, &i.StorageInstallID, &i.StorageFileID, &i.Provider, &i.ConnectionID, &i.LibraryID, &i.CollectionID, &i.Title, &i.Status, &i.ChecksumStatus, &i.Error, &i.HostingID, &i.Attempts, &i.NextCheckAt)
	return i, err
}
func hostingIntentByID(db *sql.DB, pid, id string) (*HostingIntent, error) {
	return scanHostingIntent(db.QueryRow(`SELECT `+intentColumns+` FROM hosting_intents WHERE project_id=? AND id=?`, pid, id))
}
func effectiveCollection(s *Session, b *Brand) string {
	if s.HostCollectionID != "" {
		return s.HostCollectionID
	}
	return b.HostCollectionID
}

// All callers hold the lifecycle read lock and host policy mutex. The partial
// unique index also prevents a second process from creating a duplicate intent.
func prepareHostingIntent(ctx *sdk.AppCtx, pid string, asset *Asset, s *Session, b *Brand, title string) (*HostingIntent, error) {
	i, err := scanHostingIntent(ctx.AppDB().QueryRow(`SELECT `+intentColumns+` FROM hosting_intents WHERE project_id=? AND asset_id=? AND status='waiting_checksum'`, pid, asset.ID))
	if err == nil {
		return i, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	i = &HostingIntent{ID: newID(), AssetID: asset.ID, SessionID: s.ID, StorageInstallID: asset.StorageInstallID, StorageFileID: asset.StorageFileID, Provider: b.HostProvider, ConnectionID: b.HostConnectionID, LibraryID: b.HostLibraryID, CollectionID: effectiveCollection(s, b), Title: title, Status: "waiting_checksum"}
	_, err = ctx.AppDB().Exec(`INSERT INTO hosting_intents(id,project_id,asset_id,session_id,storage_install_id,storage_file_id,provider,connection_id,library_id,collection_id,title,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,'waiting_checksum',?,?)`, i.ID, pid, i.AssetID, i.SessionID, i.StorageInstallID, i.StorageFileID, i.Provider, i.ConnectionID, i.LibraryID, i.CollectionID, i.Title, now(), now())
	if err != nil {
		winner, e := scanHostingIntent(ctx.AppDB().QueryRow(`SELECT `+intentColumns+` FROM hosting_intents WHERE project_id=? AND asset_id=? AND status='waiting_checksum'`, pid, asset.ID))
		if e == nil {
			return winner, nil
		}
		return nil, err
	}
	return i, nil
}

func validateIntentRoute(ctx *sdk.AppCtx, pid string, i *HostingIntent, asset *Asset, s *Session, b *Brand) error {
	storage := ctx.IntegrationFor("storage")
	if storage == nil || storage.InstallID != i.StorageInstallID {
		return errors.New("Storage binding changed; request hosting again explicitly")
	}
	if i.SessionID != s.ID || i.StorageInstallID != asset.StorageInstallID || i.StorageFileID != asset.StorageFileID || i.Provider != b.HostProvider || i.ConnectionID != b.HostConnectionID || i.LibraryID != b.HostLibraryID {
		return errors.New("asset or hosting destination changed; request hosting again explicitly")
	}
	collection := effectiveCollection(s, b)
	if collection != i.CollectionID {
		// Another approved asset can have resolved this session's automatic
		// collection while this file was waiting for its checksum.
		var id string
		err := ctx.AppDB().QueryRow(`SELECT remote_id FROM host_collections WHERE project_id=? AND session_id=? AND provider=? AND connection_id=? AND library_id=?`, pid, s.ID, b.HostProvider, b.HostConnectionID, b.HostLibraryID).Scan(&id)
		if i.CollectionID != "" || err != nil || id == "" || id != collection {
			return errors.New("session collection changed; request hosting again explicitly")
		}
	}
	return nil
}

func updateHostingIntent(ctx *sdk.AppCtx, pid string, i *HostingIntent, status, checksum, message, hostingID string) error {
	expectedStatus := i.Status
	i.Status, i.ChecksumStatus, i.Error, i.HostingID = status, checksum, message, hostingID
	i.Attempts++
	i.NextCheckAt = time.Now().UTC().Add(hostingBackoff(i.Attempts)).Format(time.RFC3339Nano)
	result, err := ctx.AppDB().Exec(`UPDATE hosting_intents SET status=?,checksum_status=?,error=?,hosting_id=?,attempts=?,next_check_at=?,updated_at=? WHERE project_id=? AND id=? AND status=?`, status, checksum, message, hostingID, i.Attempts, i.NextCheckAt, now(), pid, i.ID, expectedStatus)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		fresh, e := hostingIntentByID(ctx.AppDB(), pid, i.ID)
		if e != nil {
			return e
		}
		*i = *fresh
		return nil
	}
	ctx.EmitWithProject("content-catalog.hosting.updated", pid, map[string]any{"intent_id": i.ID, "asset_id": i.AssetID, "status": status})
	return err
}

func (a *App) hostingCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.hostPolicyMu.Lock()
	defer a.hostPolicyMu.Unlock()
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "intent_id"); err != nil {
		return nil, err
	}
	i, err := hostingIntentByID(ctx.AppDB(), pid, str(args, "intent_id"))
	if err != nil {
		return nil, err
	}
	if i.Status == "waiting_checksum" {
		err = updateHostingIntent(ctx, pid, i, "cancelled", i.ChecksumStatus, "Cancelled before upload", "")
		if err == nil && i.Status != "cancelled" {
			return nil, errors.New("hosting started concurrently; it was not cancelled")
		}
	} else if i.Status != "cancelled" {
		return nil, errors.New("hosting has already started or the intent is terminal; it was not cancelled")
	}
	return map[string]any{"hosting_intent": i}, err
}

func hostingBackoff(attempts int) time.Duration {
	if attempts > 5 {
		attempts = 5
	}
	if attempts < 1 {
		attempts = 1
	}
	return time.Duration(15*(1<<(attempts-1))) * time.Second
}

func (a *App) resumeHostingIntent(ctx *sdk.AppCtx, pid, id string) error {
	a.lifecycleMu.RLock()
	defer a.lifecycleMu.RUnlock()
	a.hostPolicyMu.Lock()
	defer a.hostPolicyMu.Unlock()
	i, err := hostingIntentByID(ctx.AppDB(), pid, id)
	if err != nil {
		return err
	}
	if i.Status != "waiting_checksum" {
		return nil
	}
	_, err = a.hostingRequestLocked(ctx.WithProject(pid), map[string]any{"asset_id": i.AssetID}, i)
	return err
}

// Only durable explicit intents and existing remote processing records are
// enumerated. No Storage file/folder listing or implicit hosting is involved.
func (a *App) reconcileHosting(runCtx context.Context, ctx *sdk.AppCtx) error {
	type pending struct {
		pid, id string
		intent  bool
	}
	rows, err := ctx.AppDB().Query(`SELECT project_id,id,is_intent FROM (SELECT project_id,id,1 AS is_intent,next_check_at AS due FROM hosting_intents WHERE status='waiting_checksum' AND next_check_at<=? UNION ALL SELECT project_id,id,0 AS is_intent,next_check_at AS due FROM hostings WHERE status='processing' AND remote_id<>'' AND next_check_at<=?) ORDER BY due,id LIMIT 50`, now(), now())
	if err != nil {
		return err
	}
	items := []pending{}
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.pid, &p.id, &p.intent); err != nil {
			rows.Close()
			return err
		}
		items = append(items, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range items {
		if err := runCtx.Err(); err != nil {
			return err
		}
		if p.intent {
			err = a.resumeHostingIntent(ctx, p.pid, p.id)
		} else {
			_, err = a.hostingCheck(ctx.WithProject(p.pid), map[string]any{"id": p.id})
		}
		if err != nil {
			ctx.Logger().Warn("hosting reconciliation deferred", "id", p.id, "error", err.Error())
		}
	}
	return nil
}

func (a *App) resumeChecksumIntents(ctx *sdk.AppCtx, pid, fileID string) error {
	rows, err := ctx.AppDB().Query(`SELECT id FROM hosting_intents WHERE project_id=? AND storage_file_id=? AND status='waiting_checksum'`, pid, fileID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := a.resumeHostingIntent(ctx, pid, id); err != nil {
			ctx.Logger().Warn("checksum hosting resume deferred", "intent_id", id, "error", err.Error())
		}
	}
	return nil
}

// Default title is useful even without Media. Explicit title is stored in the
// intent so its eventual upload cannot silently pick up an unrelated rename.
func hostingTitle(ctx *sdk.AppCtx, args map[string]any, s *Session, asset *Asset) (string, error) {
	title := str(args, "title")
	if title == "" && ctx.IntegrationFor("media") != nil {
		var result struct {
			Found bool `json:"found"`
			Media struct {
				Title  string `json:"title"`
				FileID string `json:"file_id"`
			} `json:"media"`
		}
		if err := ctx.PlatformAPI().CallAppResult("media", "media_get", map[string]any{"_project_id": ctx.CurrentProject(), "file_id": asset.StorageFileID}, &result); err == nil && result.Found && result.Media.FileID == asset.StorageFileID {
			title = str(map[string]any{"title": result.Media.Title}, "title")
		}
	}
	if title == "" {
		title = s.Title + " — " + asset.Name
	}
	if len([]rune(title)) > 250 {
		return "", fmt.Errorf("hosting title must be at most %s characters", strconv.Itoa(250))
	}
	return title, nil
}

func hostingRequestSchema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"asset_id"}, "properties": map[string]any{"asset_id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string", "minLength": 1, "maxLength": 250, "description": "Optional title for the new hosted video. Default is Media title when available, otherwise session title and filename. Does not rename existing videos."}}}
}

func hostingListSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"asset_id": map[string]any{"type": "string"}, "session_id": map[string]any{"type": "string"}}, "oneOf": []any{map[string]any{"required": []string{"asset_id"}, "not": map[string]any{"required": []string{"session_id"}}}, map[string]any{"required": []string{"session_id"}, "not": map[string]any{"required": []string{"asset_id"}}}}}
}
