package main

import (
	"database/sql"
	"strings"
)

// A card reads Catalog's existing hosting evidence without checking a provider
// or starting a transfer. Full hosting details remain on the asset page.
type HostingSummary struct {
	ID             string   `json:"id"`
	Provider       string   `json:"provider"`
	ConnectionID   int64    `json:"connection_id"`
	Status         string   `json:"status"`
	RemoteID       string   `json:"remote_id"`
	LastCheckedAt  string   `json:"last_checked_at"`
	EncodeProgress *float64 `json:"encode_progress"`
	ProviderStage  string   `json:"provider_stage"`
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
		asset.HostingIntents = []HostingIntent{}
		placeholders[i] = "?"
		args = append(args, asset.ID)
		byID[asset.ID] = asset
	}
	rows, err := db.Query(`SELECT asset_id,id,provider,connection_id,status,remote_id,last_checked_at,encode_progress,provider_stage FROM hostings WHERE project_id=? AND asset_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY created_at DESC,id DESC`, args...)
	if err != nil {
		return err
	}

	for rows.Next() {
		var assetID string
		var summary HostingSummary
		if err := rows.Scan(&assetID, &summary.ID, &summary.Provider, &summary.ConnectionID, &summary.Status, &summary.RemoteID, &summary.LastCheckedAt, &summary.EncodeProgress, &summary.ProviderStage); err != nil {
			rows.Close()
			return err
		}
		byID[assetID].Hostings = append(byID[assetID].Hostings, summary)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	intentRows, err := db.Query(`SELECT `+intentColumns+` FROM hosting_intents WHERE project_id=? AND asset_id IN (`+strings.Join(placeholders, ",")+`) AND status='waiting_checksum' ORDER BY created_at DESC`, args...)
	if err != nil {
		return err
	}
	defer intentRows.Close()
	for intentRows.Next() {
		i, e := scanHostingIntent(intentRows)
		if e != nil {
			return e
		}
		byID[i.AssetID].HostingIntents = append(byID[i.AssetID].HostingIntents, *i)
	}
	return intentRows.Err()
}
