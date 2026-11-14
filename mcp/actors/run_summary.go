package main

import (
	"errors"

	sdk "github.com/apteva/app-sdk"
)

// A run summary is the compact terminal receipt for a run: status, actor
// identity, timings, dataset count, failure/effect evidence and compact read
// coverage. Definition snapshots, input, preview rows, traces, artifacts and
// per-round pagination IDs stay in the full run read.

const maxSummaryEvidenceChars = 300

var (
	actorRunSummaryKeys    = []string{"id", "kind", "status", "error", "summary", "actor_id", "actor_revision", "created_at", "started_at", "completed_at", "cancel_requested_at", "duration_ms", "queue_wait_ms", "execution_ms", "wait_reason"}
	actorRunSummaryOutputs = []string{"platform", "item_count", "page_count", "inspection_complete", "failure_reason", "failed_condition", "reconciliation_required", "dataset_truncated", "current_url", "preset", "effects", "execution_progress", "dataset_artifact_id", "dataset_url", "csv_artifact_id", "csv_url"}
	coverageSummaryKeys    = []string{"inspection_complete", "read_only", "more_results_remaining", "checked_views", "required_views", "creator_url", "creator_identity", "context_id", "read_scope", "resumed_from"}
	viewSummaryKeys        = []string{"name", "covers", "url", "checked", "inspection_complete", "more_results_remaining", "pages_checked", "unique_items", "expected_total", "stop_reason", "error"}
)

func actorRunDetail(args map[string]any) (string, error) {
	switch detail := stringFromAny(args["detail"]); detail {
	case "", "full":
		return "full", nil
	case "summary":
		return detail, nil
	default:
		return "", errors.New("detail must be full or summary")
	}
}

func terminalActorRunStatus(status string) bool {
	return status != "queued" && status != "running"
}

func summarizeActorRun(ctx *sdk.AppCtx, run map[string]any) (map[string]any, error) {
	out := map[string]any{"detail": "summary"}
	copyKeys(out, run, actorRunSummaryKeys)
	input := mapFromAny(run["input"])
	if operation := stringFromAny(input["operation"]); operation != "" {
		out["operation"] = operation
	}
	output := mapFromAny(run["output"])
	copyKeys(out, output, actorRunSummaryOutputs)
	if coverage := summarizeReadCoverage(output["coverage"]); coverage != nil {
		out["coverage"] = coverage
	}
	if check := mapFromAny(output["recovery_precheck"]); len(check) > 0 {
		precheck := map[string]any{}
		copyKeys(precheck, check, []string{"run_id", "status", "duplicate_check_run_id"})
		if coverage := summarizeReadCoverage(check["coverage"]); coverage != nil {
			precheck["coverage"] = coverage
		}
		out["recovery_precheck"] = precheck
	}
	count, err := actorRunDatasetCount(ctx, int64(intFromAny(run["id"])), intFromAny(mapFromAny(run["definition_snapshot"])["schema_version"]))
	if err != nil {
		return nil, err
	}
	out["dataset_count"] = count
	return out, nil
}

func actorRunDatasetCount(ctx *sdk.AppCtx, runID int64, schemaVersion int) (int, error) {
	table := "actors_dataset_items"
	if schemaVersion == 2 {
		table = "actors_crawl_records"
	}
	var count int
	err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE run_id=? AND project_id=?`, runID, projectID(ctx)).Scan(&count)
	return count, err
}

// summarizeReadCoverage keeps every field duplicate checks rely on (required
// and checked views, expected/observed counts, completion, remaining results,
// identity, context and end evidence) and reduces pagination history to its
// round count and final round.
func summarizeReadCoverage(raw any) map[string]any {
	coverage, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	copyKeys(out, coverage, coverageSummaryKeys)
	views := []map[string]any{}
	rawViews, _ := coverage["views"].([]any)
	for _, rawView := range rawViews {
		view, ok := rawView.(map[string]any)
		if !ok {
			continue
		}
		compact := map[string]any{}
		copyKeys(compact, view, viewSummaryKeys)
		if evidence := stringFromAny(view["end_evidence"]); evidence != "" {
			compact["end_evidence"] = truncateString(evidence, maxSummaryEvidenceChars)
		}
		if verification, ok := view["verification"].(map[string]any); ok {
			compact["verification"] = map[string]any{"verified": verification["verified"], "error": verification["error"]}
		}
		if rounds, ok := view["pagination"].([]any); ok && len(rounds) > 0 {
			compact["pagination"] = summarizeReadPagination(rounds)
		}
		views = append(views, compact)
	}
	out["views"] = views
	return out
}

func summarizeReadPagination(rounds []any) map[string]any {
	last := mapFromAny(rounds[len(rounds)-1])
	final := map[string]any{}
	copyKeys(final, last, []string{"status", "stop_reason", "control_present", "loading", "loading_observed", "error_text"})
	final["new_count"] = readDiagnosticCount(last, "new")
	final["after_count"] = readDiagnosticCount(last, "after")
	return map[string]any{"rounds": len(rounds), "last": final}
}

// readDiagnosticCount prefers the stored count, since long ID lists are
// bounded when saved; runs recorded before counts existed keep full lists.
func readDiagnosticCount(round map[string]any, name string) int {
	if count, ok := round[name+"_count"]; ok {
		return intFromAny(count)
	}
	ids, _ := round[name+"_ids"].([]any)
	return len(ids)
}

func copyKeys(dst, src map[string]any, keys []string) {
	for _, key := range keys {
		if value, ok := src[key]; ok {
			dst[key] = value
		}
	}
}
