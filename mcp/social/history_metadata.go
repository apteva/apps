package main

import (
	"database/sql"
	"errors"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const historyMetadataReadbackLimit = 100
const historyMetadataAttemptsPerPost = 2

func refreshHistoryMetadataCounts(ctx *sdk.AppCtx, run *historyRun) error {
	// Explicitly unavailable IDs are explained omissions: they are neither
	// unresolved nor retried, and they are never treated as deleted.
	err := ctx.AppDB().QueryRow(`SELECT COALESCE(sum(metadata_missing=1 AND metadata_resolution!='unavailable'),0),COALESCE(sum(metadata_missing=1 AND metadata_resolution='unavailable'),0),COALESCE(sum(metadata_missing=1 AND metadata_resolution='' AND metadata_attempts<?),0),COALESCE(sum(metadata_attempts),0) FROM social_history_seen WHERE run_id=?`, historyMetadataAttemptsPerPost, run.ID).Scan(&run.MetadataUnresolved, &run.MetadataUnavailable, &run.MetadataRetryable, &run.MetadataReadbacks)
	if err != nil {
		return err
	}
	if run.MetadataUnresolvedIDs, err = historyMetadataSample(ctx, run.ID, `metadata_resolution!='unavailable'`); err != nil {
		return err
	}
	run.MetadataUnavailableIDs, err = historyMetadataSample(ctx, run.ID, `metadata_resolution='unavailable'`)
	return err
}

func historyMetadataSample(ctx *sdk.AppCtx, runID, condition string) ([]string, error) {
	out := []string{}
	rows, err := ctx.AppDB().Query(`SELECT native_post_id FROM social_history_seen WHERE run_id=? AND metadata_missing=1 AND `+condition+` ORDER BY native_post_id LIMIT 10`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// A metadata step consumes the same per-call step/time budget as a listing page.
// The ledger claims an attempt before reading: interruption/lost responses cannot
// reset the per-ID or per-run budgets. No provider pagination token is involved.
func (a *App) reconcileHistoryMetadata(ctx *sdk.AppCtx, b historyAccount, run *historyRun) error {
	if err := refreshHistoryMetadataCounts(ctx, run); err != nil {
		return err
	}
	if run.MetadataReadbacks >= historyMetadataReadbackLimit || run.MetadataRetryable == 0 {
		finishHistoryCoverage(run)
		return nil
	}
	var id string
	err := ctx.AppDB().QueryRow(`SELECT native_post_id FROM social_history_seen WHERE run_id=? AND metadata_missing=1 AND metadata_resolution='' AND metadata_attempts<? ORDER BY metadata_attempts,native_post_id LIMIT 1`, run.ID, historyMetadataAttemptsPerPost).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		finishHistoryCoverage(run)
		return nil
	}
	if err != nil {
		return err
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := ctx.AppDB().Exec(`UPDATE social_history_seen SET metadata_attempts=metadata_attempts+1,metadata_observed_at=? WHERE run_id=? AND native_post_id=? AND metadata_missing=1 AND metadata_resolution='' AND metadata_attempts<?`, at, run.ID, id, historyMetadataAttemptsPerPost)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("history metadata retry claim changed")
	}
	var posts []nativePost
	if (b.Provider == "native" || b.Provider == "") && b.Platform == "youtube" {
		// A successful full-parts batch can omit records. Retry only the missing ID
		// using basic parts, retaining unknown optional owner-only fields as unknown.
		err = a.youtubeHistoryIdentity(ctx, b, run)
		if err == nil {
			posts, err = a.youtubeNativePostsWithParts(ctx, b, run, []string{id}, true)
		}
	} else {
		posts, err = a.inspectNativeHistory(ctx, b, run, id)
	}
	if err == nil {
		current, loadErr := loadHistoryAccount(ctx, b.ID)
		if loadErr != nil || current.binding() != b.binding() {
			err = errors.New("account connection changed during metadata readback; start a new sync")
		}
	}
	if err == nil {
		for _, post := range posts {
			if post.ID != id {
				err = errors.New("metadata readback returned a different native ID")
				break
			}
			post.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if _, _, _, err = a.upsertNativeHistory(ctx, b, post, run.ID); err != nil {
				break
			}
			run.Scanned++
		}
	}
	if err == nil && (b.Provider == "native" || b.Provider == "") && b.Platform == "youtube" {
		err = a.resolveOmittedYouTubeMetadata(ctx, b, run, id)
	}
	if err != nil {
		_, saveErr := ctx.AppDB().Exec(`UPDATE social_history_seen SET metadata_error=? WHERE run_id=? AND native_post_id=?`, redactDeliverySecrets(err.Error()), run.ID, id)
		if saveErr != nil {
			return saveErr
		}
	}
	if countErr := refreshHistoryMetadataCounts(ctx, run); countErr != nil {
		return countErr
	}
	if err != nil {
		return err
	}
	run.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	finishHistoryCoverage(run)
	return nil
}
