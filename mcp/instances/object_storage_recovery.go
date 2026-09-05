package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

// Commit the newly active key and the previous key's revocation intent together.
// Only identifiers are durable; one-time secrets never enter the database.
func commitObjectStorageRotation(ctx *sdk.AppCtx, item *ObjectStorage, key string, meta objectStorageMetadata) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if item.AccessKeyID != "" && item.AccessKeyID != key {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO object_storage_key_cleanup(object_storage_id,connection_id,access_key_id) VALUES(?,?,?)`, item.ID, item.ProviderConnectionID, item.AccessKeyID); err != nil {
			return err
		}
	}
	encoded, _ := json.Marshal(meta)
	if _, err = tx.Exec(`UPDATE object_storages SET access_key_id=?,provider_metadata_json=?,error_message='' WHERE id=?`, key, string(encoded), item.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func cleanupObjectStorageKeys(ctx *sdk.AppCtx, id int64) []string {
	rows, err := ctx.AppDB().Query(`SELECT connection_id,access_key_id FROM object_storage_key_cleanup WHERE object_storage_id=?`, id)
	if err != nil {
		return []string{err.Error()}
	}
	type pending struct {
		conn int64
		key  string
	}
	all := []pending{}
	for rows.Next() {
		var p pending
		if rows.Scan(&p.conn, &p.key) == nil {
			all = append(all, p)
		}
	}
	rows.Close()
	warnings := []string{}
	for _, p := range all {
		_, err = executeObjectStorageTool(ctx, p.conn, "scaleway", "iam_api_key_delete", map[string]any{"access_key": p.key})
		if err != nil && !strings.Contains(err.Error(), "status=404") {
			msg := "Previous key revocation pending: " + err.Error()
			warnings = append(warnings, msg)
			_, _ = ctx.AppDB().Exec(`UPDATE object_storage_key_cleanup SET error=? WHERE connection_id=? AND access_key_id=?`, msg, p.conn, p.key)
		} else {
			_, _ = ctx.AppDB().Exec(`DELETE FROM object_storage_key_cleanup WHERE connection_id=? AND access_key_id=?`, p.conn, p.key)
		}
	}
	_ = dbUpdateObjectStorage(ctx.AppDB(), id, map[string]any{"error_message": strings.Join(warnings, "; ")})
	return warnings
}

func reconcileObjectStorage(ctx *sdk.AppCtx) {
	items, err := dbListObjectStorages(ctx.AppDB(), "")
	if err != nil {
		return
	}
	for _, item := range items {
		if item.Status == "deleting" {
			if _, err := destroyObjectStorage(ctx, item); err != nil {
				_ = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"error_message": err.Error()})
			}
			continue
		}
		unlock, err := lockResource(ctx.AppDB(), "object_storage", item.ID)
		if err != nil {
			continue
		}
		if item.Provider == "scaleway" {
			cleanupObjectStorageKeys(ctx, item.ID)
			if meta := parseObjectStorageMetadata(item); meta.PendingStep != "" {
				_ = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"status": "error", "error_message": "Interrupted at " + meta.PendingStep + "; known resources retained. Review provider state before retrying; rotate credentials if their response was lost."})
			}
		}
		if item.Provider == "vultr" && item.Status == "provisioning" && !strings.HasPrefix(item.ProviderID, "pending:") {
			data, e := executeObjectStorageTool(ctx, item.ProviderConnectionID, item.Provider, "object_storage_get", map[string]any{"object_storage_id": item.ProviderID})
			if e == nil {
				endpoint := findJSONScalar(data, "s3_hostname")
				state := findJSONScalar(data, "status")
				if endpoint != "" && (state == "active" || state == "ready") {
					if !strings.HasPrefix(endpoint, "http") {
						endpoint = "https://" + endpoint
					}
					e = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"status": "ready", "endpoint": endpoint, "error_message": "Ready; rotate credentials to obtain a new one-time secret"})
				}
			}
			if e != nil {
				_ = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"error_message": fmt.Sprint(e)})
			}
		}
		unlock()
	}
}
