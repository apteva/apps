package main

import (
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const historyCoverageVersion = 2
const historyReconciliationPageLimit = 100

// Choose different effective boundaries within the provider's supported range.
// Account identity, filters and request/page budgets remain the same. The pair
// is persisted with the run; a provider cursor is never moved between sizes.
func historyPageSizes(account historyAccount, requested int) (int, int) {
	minimum, maximum := 1, 50
	if account.Provider == "native" || account.Provider == "" {
		switch account.Platform {
		case "tiktok":
			maximum = 20
		case "twitter":
			minimum = 5
		}
	}
	first := max(minimum, min(requested, maximum))
	second := first - 1
	if second < minimum {
		second = first + 1
	}
	return first, second
}

type historyProviderTotal struct {
	Source     string `json:"source"`
	Total      int64  `json:"total"`
	ObservedAt string `json:"observed_at"`
}
type historyPassCounts struct {
	TraversalUnique          int `json:"traversal_unique_posts"`
	ReconciliationUnique     int `json:"reconciliation_unique_posts"`
	ReconciledUnique         int `json:"reconciled_unique_posts"`
	TraversalDuplicates      int `json:"traversal_duplicate_entries"`
	ReconciliationDuplicates int `json:"reconciliation_duplicate_entries"`
}

func requireHistoryReconciliation(run *historyRun, reason string) {
	run.ReconciliationFull = true
	if !containsFold(run.ReconciliationReasons, reason) {
		run.ReconciliationReasons = append(run.ReconciliationReasons, reason)
	}
}

// Listing identities are evidence even when a tombstone suppresses an import.
// Replaying a page after a crash cannot inflate traversal or duplicate counts.
func observeHistoryPage(ctx *sdk.AppCtx, run *historyRun, page nativePage) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids := page.ListedIDs
	if ids == nil {
		for _, post := range page.Posts {
			if post.ID != "" {
				ids = append(ids, post.ID)
			}
		}
	}
	listed := len(ids)
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO social_history_pass_posts(run_id,phase,native_post_id) VALUES(?,?,?)`, run.ID, run.Phase, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO social_history_pass_pages(run_id,phase,cursor_hash,listed_count) VALUES(?,?,?,?) ON CONFLICT(run_id,phase,cursor_hash) DO UPDATE SET listed_count=excluded.listed_count`, run.ID, run.Phase, uploadURLHash(run.Token), listed); err != nil {
		return err
	}
	var scanRows, reconcileRows int
	if err = tx.QueryRow(`SELECT count(DISTINCT native_post_id),COALESCE(sum(phase='scan'),0),COALESCE(sum(phase='overlap'),0) FROM social_history_pass_posts WHERE run_id=?`, run.ID).Scan(&run.Counts.ReconciledUnique, &run.Counts.TraversalUnique, &run.Counts.ReconciliationUnique); err != nil {
		return err
	}
	if err = tx.QueryRow(`SELECT COALESCE(sum(CASE WHEN phase='scan' THEN listed_count ELSE 0 END),0),COALESCE(sum(CASE WHEN phase='overlap' THEN listed_count ELSE 0 END),0) FROM social_history_pass_pages WHERE run_id=?`, run.ID).Scan(&scanRows, &reconcileRows); err != nil {
		return err
	}
	run.Counts.TraversalDuplicates = max(0, scanRows-run.Counts.TraversalUnique)
	run.Counts.ReconciliationDuplicates = max(0, reconcileRows-run.Counts.ReconciliationUnique)
	if err = tx.Commit(); err != nil {
		return err
	}
	if page.Total != nil {
		if run.ProviderTotal != nil && *run.ProviderTotal != *page.Total {
			run.ProviderTotalsChanged = true
			requireHistoryReconciliation(run, "provider_total_changed")
		}
		run.ProviderTotal = bytePointer(*page.Total)
		found := false
		for i, total := range run.ProviderTotals {
			if total.Source == page.TotalSource && total.Total == *page.Total {
				run.ProviderTotals[i].ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
				found = true
				break
			}
		}
		if !found && len(run.ProviderTotals) < 100 {
			run.ProviderTotals = append(run.ProviderTotals, historyProviderTotal{Source: page.TotalSource, Total: *page.Total, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)})
		}
	}
	if run.Counts.TraversalDuplicates > 0 || run.Counts.ReconciliationDuplicates > 0 {
		requireHistoryReconciliation(run, "overlapping_provider_pages")
	}
	if run.Phase == "overlap" && run.Counts.ReconciledUnique > run.Counts.TraversalUnique {
		requireHistoryReconciliation(run, "additional_native_ids")
	}
	return nil
}

func finishHistoryCoverage(run *historyRun) {
	run.Status = "complete"
	if run.ProviderTotal == nil {
		addHistoryLimitation(run, "Provider did not expose a comparable listing total; coverage is based on accessible pagination, not a lifetime count proof.")
	}
	if run.ProviderTotal != nil && int64(run.Counts.ReconciledUnique) != *run.ProviderTotal {
		run.Incomplete = true
		addHistoryLimitation(run, fmt.Sprintf("Unexplained provider count discrepancy after bounded reconciliation: provider reported %d, first traversal found %d, reconciled union found %d.", *run.ProviderTotal, run.Counts.TraversalUnique, run.Counts.ReconciledUnique))
	}
	if run.ProviderTotalsChanged {
		run.Incomplete = true
		addHistoryLimitation(run, "Provider listing totals changed during sync; a consistent complete snapshot could not be established.")
	}
	if run.Incomplete {
		run.Status = "limited"
	}
	now := run.ObservedAt
	run.CompletedAt = &now
}
func historyCoverage(run historyRun) map[string]any {
	complete := run.CoverageVersion == historyCoverageVersion && run.Status == "complete" && run.Mode == "full" && !run.Incomplete && run.TraversalExhausted
	var gap *int64
	if run.ProviderTotal != nil {
		gap = bytePointer(*run.ProviderTotal - int64(run.Counts.ReconciledUnique))
	}
	return map[string]any{
		"complete": complete, "scope": run.Scope, "mode": run.Mode, "coverage_version": run.CoverageVersion,
		"traversal_complete": run.TraversalExhausted, "pagination_exhausted": run.TraversalExhausted,
		"traversal_page_size": run.TraversalPageSize, "traversal_unique_posts": run.Counts.TraversalUnique, "reconciled_unique_posts": run.Counts.ReconciledUnique,
		"provider_total": run.ProviderTotal, "provider_totals": run.ProviderTotals, "provider_totals_changed": run.ProviderTotalsChanged,
		"count_verified": run.ProviderTotal != nil && gap != nil && *gap == 0 && !run.ProviderTotalsChanged, "unexplained_count_gap": gap,
		"reconciliation":    map[string]any{"attempted": run.OverlapPages > 0, "page_size": run.ReconciliationPageSize, "full_pass_required": run.ReconciliationFull, "reasons": run.ReconciliationReasons, "pages": run.OverlapPages, "max_pages": historyReconciliationPageLimit, "pagination_exhausted": run.ReconciliationExhausted, "unique_posts": run.Counts.ReconciliationUnique, "added_posts": run.Counts.ReconciledUnique - run.Counts.TraversalUnique},
		"duplicate_entries": run.Counts.TraversalDuplicates + run.Counts.ReconciliationDuplicates, "overlap_pages": run.OverlapPages,
		"all_listed_ids_resolved": !run.Incomplete, "optional_fields_may_be_unknown": true, "consistency": "best_effort_provider_pagination_with_bounded_reconciliation",
	}
}
