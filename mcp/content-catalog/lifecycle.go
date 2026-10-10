package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

type LifecycleFields struct {
	Lifecycle     string `json:"lifecycle"`
	ArchiveReason string `json:"archive_reason"`
	ArchivedAt    string `json:"archived_at"`
	Revision      int64  `json:"revision"`
}

func lifecycleScope(args map[string]any) (string, error) {
	scope := str(args, "lifecycle")
	if scope == "" {
		scope = "active"
	}
	if !oneOf(scope, "active", "archived", "all") {
		return "", errors.New("lifecycle must be active, archived, or all")
	}
	return scope, nil
}
func lifecyclePredicate(scope, asset, session string) string {
	active := session + ".lifecycle='active'"
	if asset != "" {
		active += " AND " + asset + ".lifecycle='active'"
	}
	if scope == "all" {
		return ""
	}
	if scope == "archived" {
		return " AND NOT (" + active + ")"
	}
	return " AND (" + active + ")"
}
func loadSessionLifecycle(db *sql.DB, pid string, s *Session) error {
	return db.QueryRow(`SELECT lifecycle,archive_reason,archived_at,revision FROM sessions WHERE project_id=? AND id=?`, pid, s.ID).Scan(&s.Lifecycle, &s.ArchiveReason, &s.ArchivedAt, &s.Revision)
}
func loadAssetLifecycle(db *sql.DB, pid string, assets []*Asset) error {
	if len(assets) == 0 {
		return nil
	}
	values := []any{pid}
	byID := map[string]*Asset{}
	marks := []string{}
	for _, a := range assets {
		values = append(values, a.ID)
		marks = append(marks, "?")
		byID[a.ID] = a
	}
	rows, err := db.Query(`SELECT a.id,a.lifecycle,a.archive_reason,a.archived_at,a.original_session_id,a.revision,s.lifecycle,s.revision,a.role,a.output_type FROM assets a JOIN sessions s ON s.project_id=a.project_id AND s.id=a.session_id WHERE a.project_id=? AND a.id IN (`+strings.Join(marks, ",")+`)`, values...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var f LifecycleFields
		var original, sessionState, role, outputType string
		var sessionRevision int64
		if err = rows.Scan(&id, &f.Lifecycle, &f.ArchiveReason, &f.ArchivedAt, &original, &f.Revision, &sessionState, &sessionRevision, &role, &outputType); err != nil {
			return err
		}
		a := byID[id]
		a.LifecycleFields = f
		a.OriginalSessionID = original
		a.SessionLifecycle = sessionState
		a.SessionRevision = sessionRevision
		a.Role = role
		a.OutputType = outputType
		a.Eligible = role != "intermediate" && f.Lifecycle == "active" && sessionState == "active"
	}
	return rows.Err()
}
func requireActiveAsset(db *sql.DB, pid, id string) error {
	var state, parent, role string
	if err := db.QueryRow(`SELECT a.lifecycle,s.lifecycle,a.role FROM assets a JOIN sessions s ON s.project_id=a.project_id AND s.id=a.session_id WHERE a.project_id=? AND a.id=?`, pid, id).Scan(&state, &parent, &role); err != nil {
		return err
	}
	if role == "intermediate" {
		return errors.New("intermediate asset is not eligible for content selection; change its purpose explicitly first")
	}
	if state != "active" || parent != "active" {
		return errors.New("asset or its session is archived; restore it to an active session first")
	}
	return nil
}
func requireActiveSession(s *Session) error {
	if s.Lifecycle != "active" {
		return errors.New("session is archived; restore it first")
	}
	return nil
}

// Atomic, compare-and-swap batch actions with durable request replay. The
// transaction touches only Catalog records, never Storage or another app.
func (a *App) lifecycleMutation(entity, action string) sdk.ToolHandler {
	return func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		a.lifecycleMu.Lock()
		defer a.lifecycleMu.Unlock()
		pid, err := project(ctx)
		if err != nil {
			return nil, err
		}
		if err = required(args, "operation_id"); err != nil {
			return nil, err
		}
		reason := str(args, "reason")
		if action == "archive" && reason == "" {
			return nil, errors.New("archive reason required")
		}
		encoded, err := json.Marshal(map[string]any{"entity": entity, "action": action, "args": args})
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(encoded)
		hash := hex.EncodeToString(sum[:])
		tx, err := ctx.AppDB().Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		var oldHash, result string
		err = tx.QueryRow(`SELECT request_hash,result FROM lifecycle_operations WHERE project_id=? AND operation_id=?`, pid, str(args, "operation_id")).Scan(&oldHash, &result)
		if err == nil {
			if oldHash != hash {
				return nil, errors.New("operation_id already used for a different request")
			}
			var out any
			err = json.Unmarshal([]byte(result), &out)
			return out, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		items := []map[string]any{}
		if entity == "session" {
			items = append(items, map[string]any{"id": str(args, "session_id"), "revision": number(args, "expected_revision")})
		} else {
			ids, e := assetIDsArg(args["asset_ids"])
			if e != nil {
				return nil, e
			}
			raw, e := json.Marshal(args["expected_revisions"])
			if e != nil {
				return nil, e
			}
			var revisions map[string]int64
			if e = json.Unmarshal(raw, &revisions); e != nil {
				return nil, errors.New("expected_revisions must map every asset ID to an integer revision")
			}
			if len(ids) > 100 {
				return nil, errors.New("at most 100 assets per operation")
			}
			for _, id := range ids {
				items = append(items, map[string]any{"id": id, "revision": revisions[id]})
			}
		}
		changed := []map[string]any{}
		for _, item := range items {
			id := str(item, "id")
			rev := number(item, "revision")
			if id == "" || rev < 1 {
				return nil, errors.New("ID and positive expected revision required for every record")
			}
			var state, previousReason, archivedAt string
			var revision int64
			before := map[string]any{}
			after := map[string]any{}
			if entity == "session" {
				var status string
				if err = tx.QueryRow(`SELECT lifecycle,archive_reason,archived_at,revision,status FROM sessions WHERE project_id=? AND id=?`, pid, id).Scan(&state, &previousReason, &archivedAt, &revision, &status); err != nil {
					return nil, err
				}
				if revision != rev {
					return nil, errors.New("revision conflict; reload the session")
				}
				if action == "restore" && status == "archived" {
					status = str(args, "status")
					if !oneOf(status, "planned", "active", "completed") {
						return nil, errors.New("legacy archived session requires explicit production status on restoration")
					}
				}
				before = map[string]any{"lifecycle": state, "revision": rev, "archive_reason": previousReason, "archived_at": archivedAt}
				if action == "archive" {
					state = "archived"
					archivedAt = now()
					previousReason = reason
				} else {
					state = "active"
					previousReason = ""
					archivedAt = ""
				}
				res, e := tx.Exec(`UPDATE sessions SET lifecycle=?,archive_reason=?,archived_at=?,revision=revision+1,status=?,updated_at=? WHERE project_id=? AND id=? AND revision=?`, state, previousReason, archivedAt, status, now(), pid, id, rev)
				if e != nil {
					return nil, e
				}
				n, _ := res.RowsAffected()
				if n != 1 {
					return nil, errors.New("revision conflict")
				}
				after = map[string]any{"id": id, "lifecycle": state, "revision": rev + 1, "archive_reason": previousReason, "archived_at": archivedAt}
			} else {
				var sessionID, original, brand string
				if err = tx.QueryRow(`SELECT a.lifecycle,a.archive_reason,a.archived_at,a.revision,a.session_id,a.original_session_id,s.brand_id FROM assets a JOIN sessions s ON s.project_id=a.project_id AND s.id=a.session_id WHERE a.project_id=? AND a.id=?`, pid, id).Scan(&state, &previousReason, &archivedAt, &revision, &sessionID, &original, &brand); err != nil {
					return nil, err
				}
				if revision != rev {
					return nil, errors.New("revision conflict; reload the assets")
				}
				before = map[string]any{"session_id": sessionID, "lifecycle": state, "revision": rev, "original_session_id": original, "archive_reason": previousReason, "archived_at": archivedAt}
				destination := str(args, "destination_session_id")
				if action == "move" && destination == "" {
					return nil, errors.New("destination_session_id required")
				}
				if action == "restore" && destination == "" {
					destination = original
					if destination == "" {
						destination = sessionID
					}
				}
				if destination == "" {
					destination = sessionID
				}
				var destinationBrand, destinationState string
				var destinationRevision int64
				if err = tx.QueryRow(`SELECT brand_id,lifecycle,revision FROM sessions WHERE project_id=? AND id=?`, pid, destination).Scan(&destinationBrand, &destinationState, &destinationRevision); err != nil {
					return nil, fmt.Errorf("destination session: %w", err)
				}
				if destinationBrand != brand {
					return nil, errors.New("destination must have the same brand")
				}
				expectedDestination := number(args, "destination_revision")
				if raw := args["destination_revisions"]; raw != nil {
					encoded, e := json.Marshal(raw)
					if e != nil {
						return nil, e
					}
					var revisions map[string]int64
					if e = json.Unmarshal(encoded, &revisions); e != nil {
						return nil, errors.New("destination_revisions must map session IDs to revisions")
					}
					expectedDestination = revisions[destination]
				}
				if destination != sessionID && expectedDestination != destinationRevision {
					return nil, errors.New("destination revision conflict; reload the session")
				}
				if action == "restore" && destinationState != "active" {
					return nil, errors.New("restoration destination is archived; restore the session or choose an active destination")
				}
				if action == "archive" {
					if state != "archived" {
						original = sessionID
						archivedAt = now()
					}
					state = "archived"
					previousReason = reason
				}
				if action == "restore" {
					state = "active"
					previousReason = ""
					archivedAt = ""
				}
				res, e := tx.Exec(`UPDATE assets SET session_id=?,lifecycle=?,archive_reason=?,archived_at=?,original_session_id=?,revision=revision+1,updated_at=? WHERE project_id=? AND id=? AND revision=?`, destination, state, previousReason, archivedAt, original, now(), pid, id, rev)
				if e != nil {
					return nil, fmt.Errorf("move/archive conflict (no records changed): %w", e)
				}
				n, _ := res.RowsAffected()
				if n != 1 {
					return nil, errors.New("revision conflict")
				}
				after = map[string]any{"id": id, "session_id": destination, "lifecycle": state, "revision": rev + 1, "original_session_id": original, "archive_reason": previousReason, "archived_at": archivedAt}
			}
			b, _ := json.Marshal(before)
			c, _ := json.Marshal(after)
			if _, err = tx.Exec(`INSERT INTO lifecycle_events(id,project_id,operation_id,entity_type,entity_id,action,before_json,after_json,reason,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, newID(), pid, str(args, "operation_id"), entity, id, action, string(b), string(c), reason, now()); err != nil {
				return nil, err
			}
			changed = append(changed, after)
		}
		out := map[string]any{"records": changed, "operation_id": str(args, "operation_id")}
		b, _ := json.Marshal(out)
		if _, err = tx.Exec(`INSERT INTO lifecycle_operations(project_id,operation_id,request_hash,result,created_at) VALUES(?,?,?,?,?)`, pid, str(args, "operation_id"), hash, string(b), now()); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		ctx.EmitWithProject("content-catalog.lifecycle.changed", pid, map[string]any{"entity_type": entity, "action": action, "records": changed})
		return out, nil
	}
}
func (a *App) lifecycleHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	entity := str(args, "entity_type")
	if !oneOf(entity, "asset", "session") {
		return nil, errors.New("entity_type must be asset or session")
	}
	rows, err := ctx.AppDB().Query(`SELECT operation_id,action,before_json,after_json,reason,created_at FROM lifecycle_events WHERE project_id=? AND entity_type=? AND entity_id=? ORDER BY created_at DESC,rowid DESC LIMIT 200`, pid, entity, str(args, "id"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []map[string]any{}
	for rows.Next() {
		var op, action, b, c, reason, at string
		if err = rows.Scan(&op, &action, &b, &c, &reason, &at); err != nil {
			return nil, err
		}
		var before, after any
		json.Unmarshal([]byte(b), &before)
		json.Unmarshal([]byte(c), &after)
		events = append(events, map[string]any{"operation_id": op, "action": action, "before": before, "after": after, "reason": reason, "created_at": at})
	}
	return map[string]any{"events": events}, rows.Err()
}

// Resolve explicit asset identities or Storage files in one project. Mixed
// active/archived links require an explicit active asset context, never a guess.
func (a *App) assetsEligibility(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	if args["asset_ids"] != nil {
		ids, err = assetIDsArg(args["asset_ids"])
		if err != nil {
			return nil, err
		}
	}
	files := []string{}
	if args["file_ids"] != nil {
		files, err = assetIDsArg(args["file_ids"])
		if err != nil {
			return nil, err
		}
	}
	if len(ids)+len(files) == 0 || len(ids)+len(files) > 100 {
		return nil, errors.New("supply 1–100 explicit asset_ids or file_ids")
	}
	storage := number(args, "storage_install_id")
	out := []map[string]any{}
	for _, id := range ids {
		asset, e := assetByID(ctx.AppDB(), pid, id)
		if e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"asset_id": id, "file_id": asset.StorageFileID, "storage_install_id": asset.StorageInstallID, "eligible": asset.Eligible, "role": asset.Role, "lifecycle": asset.Lifecycle, "session_lifecycle": asset.SessionLifecycle, "revision": asset.Revision, "session_revision": asset.SessionRevision})
	}
	for _, file := range files {
		if storage <= 0 {
			return nil, errors.New("storage_install_id required for file lookup")
		}
		var active, ineligible int
		err = ctx.AppDB().QueryRow(`SELECT COALESCE(SUM(CASE WHEN a.lifecycle='active' AND s.lifecycle='active' AND a.role<>'intermediate' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN a.lifecycle!='active' OR s.lifecycle!='active' OR a.role='intermediate' THEN 1 ELSE 0 END),0) FROM assets a JOIN sessions s ON s.project_id=a.project_id AND s.id=a.session_id WHERE a.project_id=? AND a.storage_install_id=? AND a.storage_file_id=?`, pid, storage, file).Scan(&active, &ineligible)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"file_id": file, "storage_install_id": storage, "managed": active+ineligible > 0, "eligible": ineligible == 0, "requires_asset_context": active > 0 && ineligible > 0})
	}
	return map[string]any{"items": out}, nil
}

func lifecycleListSchema(required ...string) map[string]any {
	s := schema(required...)
	s["properties"] = map[string]any{"id": map[string]any{"type": "string"}, "session_id": map[string]any{"type": "string"}, "brand_id": map[string]any{"type": "string"}, "lifecycle": map[string]any{"type": "string", "enum": []string{"active", "archived", "all"}, "default": "active"}}
	return s
}
func eligibilitySchema() map[string]any {
	s := schema()
	s["properties"] = map[string]any{"asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 100}, "file_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 100}, "storage_install_id": map[string]any{"type": "integer"}}
	return s
}
func lifecycleMutationSchema(entity, action string) map[string]any {
	required := []string{"operation_id"}
	properties := map[string]any{"operation_id": map[string]any{"type": "string", "description": "Stable request ID reused only for an exact retry."}, "reason": map[string]any{"type": "string"}, "destination_session_id": map[string]any{"type": "string"}, "destination_revision": map[string]any{"type": "integer", "minimum": 1, "description": "Required when moving/restoring to a different session."}, "status": map[string]any{"type": "string", "enum": []string{"planned", "active", "completed"}}}
	if entity == "asset" {
		required = append(required, "asset_ids", "expected_revisions")
		properties["destination_revisions"] = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer", "minimum": 1}, "description": "Per-session revisions for restoring a batch to its original sessions."}
		properties["asset_ids"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 100}
		properties["expected_revisions"] = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer", "minimum": 1}}
	} else {
		required = append(required, "session_id", "expected_revision")
		properties["session_id"] = map[string]any{"type": "string"}
		properties["expected_revision"] = map[string]any{"type": "integer", "minimum": 1}
	}
	if action == "archive" {
		required = append(required, "reason")
	}
	if action == "move" {
		required = append(required, "destination_session_id", "destination_revision")
	}
	s := schema(required...)
	s["properties"] = properties
	return s
}
