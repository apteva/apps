package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"

	"fmt"

	"time"
)

const (
	alpacaTradingSlug       = "alpaca-trading"
	referenceProviderAlpaca = "alpaca"
	referenceUniverseAlpaca = "ALPACA_TRADABLE_US"
)

func referenceDataStatus(db *sql.DB) map[string]any {
	out := map[string]any{"provider": referenceProviderAlpaca, "survivorship": "forward_snapshot_only"}
	counts := map[string]string{
		"securities":        `SELECT COUNT(*) FROM securities`,
		"listings":          `SELECT COUNT(*) FROM security_listings`,
		"corporate_actions": `SELECT COUNT(*) FROM (SELECT provider,provider_event_id FROM corporate_actions GROUP BY provider,provider_event_id)`,
		"sessions":          `SELECT COUNT(*) FROM exchange_sessions`,
		"open_issues":       `SELECT COUNT(*) FROM reference_data_issues WHERE status='open'`,
	}
	for key, query := range counts {
		var count int
		if err := db.QueryRow(query).Scan(&count); err == nil {
			out[key] = count
		}
	}
	checkpoints := []map[string]any{}
	rows, err := db.Query(`SELECT provider,stream,cursor,watermark,COALESCE(last_ok_at,''),COALESCE(last_error_at,''),last_error,rows_ingested,updated_at FROM reference_data_checkpoints ORDER BY provider,stream`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var provider, stream, cursor, watermark, lastOK, lastErrorAt, lastError, updated string
			var ingested int
			if rows.Scan(&provider, &stream, &cursor, &watermark, &lastOK, &lastErrorAt, &lastError, &ingested, &updated) == nil {
				checkpoints = append(checkpoints, map[string]any{"provider": provider, "stream": stream, "cursor": cursor, "watermark": watermark, "last_ok_at": lastOK, "last_error_at": lastErrorAt, "last_error": lastError, "rows_ingested": ingested, "updated_at": updated})
			}
		}
	}
	out["checkpoints"] = checkpoints
	var coverage sql.NullString
	_ = db.QueryRow(`SELECT MIN(valid_from) FROM universe_memberships WHERE universe_id=?`, referenceUniverseAlpaca).Scan(&coverage)
	out["universe"] = map[string]any{"id": referenceUniverseAlpaca, "coverage_start": coverage.String, "historical_verified": false}
	return out
}

func referenceManifest(db *sql.DB, symbols []string, start, end, adjustment string) map[string]any {
	manifest := map[string]any{"captured_at": time.Now().UTC().Format(time.RFC3339Nano), "adjustment_mode": adjustment, "symbols": symbols, "range_start": start, "range_end": end}
	switch adjustment {
	case "raw":
		manifest["economic_continuity"] = false
		manifest["corporate_action_treatment"] = "unadjusted prices; events retained for audit but not injected into replay holdings"
		manifest["warning"] = "raw prices may contain corporate-action discontinuities"
	case "split_adjusted", "price_return":
		manifest["economic_continuity"] = true
		manifest["corporate_action_treatment"] = "provider split-adjusted prices; cash distributions excluded from return"
	case "total_return", "provider_adjusted":
		manifest["economic_continuity"] = true
		manifest["corporate_action_treatment"] = "provider-adjusted prices including supported splits and distributions"
	default:
		manifest["economic_continuity"] = false
		manifest["corporate_action_treatment"] = "unknown"
	}
	var actions, sessions int
	_ = db.QueryRow(`SELECT COUNT(*) FROM corporate_actions WHERE COALESCE(NULLIF(effective_date,''),NULLIF(ex_date,''),process_date) BETWEEN ? AND ?`, start, end).Scan(&actions)
	_ = db.QueryRow(`SELECT COUNT(*) FROM exchange_sessions WHERE session_date BETWEEN ? AND ?`, start, end).Scan(&sessions)
	manifest["corporate_action_records"] = actions
	manifest["session_records"] = sessions
	status := referenceDataStatus(db)
	manifest["checkpoints"] = status["checkpoints"]
	coverageStart := ""
	if universe, ok := status["universe"].(map[string]any); ok {
		coverageStart, _ = universe["coverage_start"].(string)
	}
	manifest["survivorship_safe"] = false
	manifest["membership_coverage_start"] = coverageStart
	manifest["survivorship_detail"] = "snapshot membership is not complete point-in-time coverage"
	manifest["universe_id"] = referenceUniverseAlpaca
	raw, _ := json.Marshal(manifest)
	manifest["manifest_sha256"] = fmt.Sprintf("%x", sha256.Sum256(raw))
	return manifest
}
