package main

import (
	"database/sql"
	"strings"
)

// A card reads Catalog's existing hosting evidence without checking a provider
// or starting a transfer. Full hosting details remain on the asset page.
type HostingSummary struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	ConnectionID  int64  `json:"connection_id"`
	Status        string `json:"status"`
	RemoteID      string `json:"remote_id"`
	LastCheckedAt string `json:"last_checked_at"`
}

func loadAssetHostings(db *sql.DB, pid string, assets []*Asset) error {
	if len(assets) == 0 {
		return nil
	}
	placeholders := make([]string, len(assets))
	args := []any{pid}
	byID := make(map[string]*Asset, len(assets))
	for i, asset := range assets {
		asset.Hostings = []HostingSummary{}
		placeholders[i] = "?"
		args = append(args, asset.ID)
		byID[asset.ID] = asset
	}
	rows, err := db.Query(`SELECT asset_id,id,provider,connection_id,status,remote_id,last_checked_at FROM hostings WHERE project_id=? AND asset_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY created_at DESC,id DESC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var assetID string
		var summary HostingSummary
		if err := rows.Scan(&assetID, &summary.ID, &summary.Provider, &summary.ConnectionID, &summary.Status, &summary.RemoteID, &summary.LastCheckedAt); err != nil {
			return err
		}
		byID[assetID].Hostings = append(byID[assetID].Hostings, summary)
	}
	return rows.Err()
}
