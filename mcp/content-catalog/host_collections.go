package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
)

// Collection support is optional; providers without collections keep their
// existing upload contract. Catalog owns retry safety and session persistence.
type HostCollectionProvider interface {
	FindCollection(ctx *sdk.AppCtx, connectionID int64, libraryID, name string) (string, error)
	CreateCollection(ctx *sdk.AppCtx, connectionID int64, libraryID, name string) (string, error)
}

func validateBunnyCollection(body map[string]any, libraryID, name string) (string, error) {
	id := stringValue(body, "guid")
	if id == "" || stringValue(body, "videoLibraryId") != libraryID || stringValue(body, "name") != name {
		return "", errors.New("Bunny returned incomplete or mismatched collection metadata")
	}
	return id, nil
}

func (bunnyProvider) FindCollection(ctx *sdk.AppCtx, connectionID int64, libraryID, name string) (string, error) {
	found := ""
	seen := 0
	// Read every page before adopting a name: duplicate names are ambiguous.
	for page := 1; page <= 1000; page++ {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "list_collections", map[string]any{"page": page, "itemsPerPage": 100})
		if err != nil {
			return "", err
		}
		if res == nil || !res.Success {
			return "", fmt.Errorf("Bunny list_collections failed: %s", integrationError(res))
		}
		var body map[string]any
		if err = json.Unmarshal(res.Data, &body); err != nil {
			return "", fmt.Errorf("decode Bunny collections: %w", err)
		}
		items, ok := body["items"].([]any)
		total := intValue(body, "totalItems")
		if !ok || total < 0 || intValue(body, "currentPage") != int64(page) {
			return "", errors.New("Bunny returned incomplete collection pagination")
		}
		for _, item := range items {
			collection, ok := item.(map[string]any)
			if !ok {
				return "", errors.New("Bunny returned invalid collection metadata")
			}
			if stringValue(collection, "name") != name {
				continue
			}
			id, err := validateBunnyCollection(collection, libraryID, name)
			if err != nil {
				return "", err
			}
			if found != "" && found != id {
				return "", errors.New("multiple Bunny collections have this session name; set host_collection_id explicitly")
			}
			found = id
		}
		seen += len(items)
		if int64(seen) >= total {
			return found, nil
		}
		if len(items) == 0 {
			return "", errors.New("Bunny collection pagination ended before totalItems")
		}
	}
	return "", errors.New("Bunny collection pagination limit exceeded")
}

func (bunnyProvider) CreateCollection(ctx *sdk.AppCtx, connectionID int64, libraryID, name string) (string, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "create_collection", map[string]any{"name": name})
	if err != nil {
		return "", err
	}
	if res == nil || !res.Success {
		return "", fmt.Errorf("Bunny create_collection failed: %s", integrationError(res))
	}
	var body map[string]any
	if err = json.Unmarshal(res.Data, &body); err != nil {
		return "", fmt.Errorf("decode Bunny collection: %w", err)
	}
	return validateBunnyCollection(body, libraryID, name)
}

// Called only from the authorized hosting mutation after existing asset
// hosting checks. A durable reservation survives restarts and timeouts.
func ensureSessionCollection(ctx *sdk.AppCtx, pid string, session *Session, brand *Brand, provider HostProvider) (string, error) {
	if session.HostCollectionID != "" {
		return session.HostCollectionID, nil
	}
	if brand.HostCollectionID != "" {
		return brand.HostCollectionID, nil
	}
	collections, supported := provider.(HostCollectionProvider)
	if !supported {
		return "", nil
	}
	if brand.HostLibraryID == "" {
		return "", errors.New("brand needs a video-host library before creating a session collection")
	}
	name := strings.TrimSpace(session.Title)
	key := []any{pid, session.ID, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID}
	var id, reservedName string
	err := ctx.AppDB().QueryRow(`SELECT remote_id,name FROM host_collections WHERE project_id=? AND session_id=? AND provider=? AND connection_id=? AND library_id=?`, key...).Scan(&id, &reservedName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	reserved := err == nil
	if reserved {
		name = reservedName // A rename must not defeat recovery of an earlier call.
	}
	if id == "" {
		if name == "" || utf8.RuneCountInString(name) > 100 {
			return "", errors.New("session title must contain 1–100 characters for automatic collection creation; set host_collection_id explicitly otherwise")
		}
		id, err = collections.FindCollection(ctx, brand.HostConnectionID, brand.HostLibraryID, name)
		if err != nil {
			return "", fmt.Errorf("session collection lookup: %w", err)
		}
		if id == "" && reserved {
			return "", errors.New("session collection creation is pending or uncertain; retry observation later or set host_collection_id explicitly after checking the host; no upload started")
		}
		if !reserved {
			result, err := ctx.AppDB().Exec(`INSERT OR IGNORE INTO host_collections(project_id,session_id,provider,connection_id,library_id,name,remote_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, pid, session.ID, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID, name, id, now(), now())
			if err != nil {
				return "", err
			}
			inserted, err := result.RowsAffected()
			if err != nil {
				return "", err
			}
			if inserted == 0 {
				return "", errors.New("another request reserved the session collection; retry to reuse its result")
			}
			if id == "" {
				id, err = collections.CreateCollection(ctx, brand.HostConnectionID, brand.HostLibraryID, name)
				if err != nil {
					_, _ = ctx.AppDB().Exec(`UPDATE host_collections SET error=?,updated_at=? WHERE project_id=? AND session_id=? AND provider=? AND connection_id=? AND library_id=?`, append([]any{err.Error(), now()}, key...)...)
					return "", fmt.Errorf("session collection result uncertain; no upload started: %w", err)
				}
			}
		}
		// Persist the provider result before saving the session. A failed local
		// update can then retry without repeating the external mutation.
		result, err := ctx.AppDB().Exec(`UPDATE host_collections SET remote_id=?,error='',updated_at=? WHERE project_id=? AND session_id=? AND provider=? AND connection_id=? AND library_id=? AND (remote_id='' OR remote_id=?)`, append(append([]any{id, now()}, key...), id)...)
		if err != nil {
			return "", err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return "", err
		}
		if changed != 1 {
			return "", errors.New("session collection changed concurrently; retry observation")
		}
	}
	result, err := ctx.AppDB().Exec(`UPDATE sessions SET host_collection_id=?,revision=revision+1,updated_at=? WHERE project_id=? AND id=? AND revision=? AND host_collection_id='' AND lifecycle='active' AND EXISTS (SELECT 1 FROM brands WHERE brands.id=sessions.brand_id AND brands.project_id=sessions.project_id AND host_provider=? AND host_connection_id=? AND host_library_id=? AND host_collection_id='')`, id, now(), pid, session.ID, session.Revision, brand.HostProvider, brand.HostConnectionID, brand.HostLibraryID)
	if err != nil {
		return "", err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if changed != 1 {
		return "", errors.New("session or brand changed while resolving its collection; retry hosting to use the current settings")
	}
	session.HostCollectionID = id
	session.Revision++
	return id, nil
}
