package main

import (
	"crypto/sha256"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"

	"fmt"

	"time"
)

const (
	referenceUniverseAlpaca = "ALPACA_TRADABLE_US"
)

// referenceManifest describes the reference data a backtest window relies
// on. Trading supplies it through research_reference; the manifest is frozen
// into the run.
func referenceManifest(ctx *sdk.AppCtx, pid string, symbols []string, start, end, adjustment string) (map[string]any, error) {
	ref, err := newTradingReader(ctx).Reference(pid, symbols, start, end, adjustment)
	if err != nil {
		return nil, fmt.Errorf("Trading reference data: %w", err)
	}
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
	manifest["corporate_action_records"] = len(ref.Tables["corporate_actions"])
	manifest["session_records"] = len(ref.Tables["exchange_sessions"])
	if len(ref.SecurityIDs) > 0 {
		manifest["security_ids"] = ref.SecurityIDs
	}
	status := ref.Status
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
	return manifest, nil
}
