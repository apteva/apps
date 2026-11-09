// inbox_coverage — what each account's inbox actually covers: supported
// actions, missing access, last successful sync and whether coverage is
// complete. inbox_sync records the outcome of every account sync here;
// inbox_coverage reads it back.
package main

import (
	"database/sql"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	inboxCoverageCursor    = "coverage"
	inboxLastSuccessCursor = "last_success"
	inboxScheduledArg      = "_scheduled"

	coverageComplete       = "complete"
	coveragePartial        = "partial"
	coverageNeedsReconnect = "needs_reconnect"
	coverageNeedsUpdate    = "needs_platform_update"
	coverageUnsupported    = "unsupported"
	coverageError          = "error"
	coverageNotSynced      = "not_synced"
)

type inboxAccountCoverage struct {
	SocialAccountID int64           `json:"social_account_id"`
	Platform        string          `json:"platform"`
	Provider        string          `json:"provider"`
	DisplayName     string          `json:"display_name,omitempty"`
	Coverage        string          `json:"coverage"`
	Supports        map[string]bool `json:"supports"`
	Missing         []string        `json:"missing,omitempty"`
	Notes           []string        `json:"notes,omitempty"`
	LastSyncAt      string          `json:"last_sync_at,omitempty"`
	LastSuccessAt   string          `json:"last_success_at,omitempty"`
	LastError       string          `json:"last_error,omitempty"`
}

// inboxPlatformLimits are the gaps no reconnect can fix for a platform.
var inboxPlatformLimits = map[string]struct{ Missing, Notes []string }{
	"youtube": {
		Notes: []string{
			"YouTube has no direct-message API.",
			"Scheduled syncs run every 30 minutes to save quota; older threads are rechecked one page per sync.",
		},
	},
	"tiktok": {
		Missing: []string{
			"TikTok comments need TikTok API for Business access for this account.",
			"TikTok DMs need TikTok Business Messaging access for this account.",
		},
	},
}

func inboxSupports(platform string) map[string]bool {
	c := platforms[platform].Inbox
	return map[string]bool{
		"comments_read":   c.CommentsRead,
		"comments_write":  c.CommentsWrite,
		"comments_hide":   c.CommentsHide,
		"comments_delete": c.CommentsDelete,
		"comments_like":   c.CommentsLike,
		"private_reply":   c.PrivateReply,
		"dm_read":         c.DMsRead,
		"dm_write":        c.DMsWrite,
		"mentions_read":   c.MentionsRead,
		"reviews_read":    c.ReviewsRead,
		"reviews_reply":   c.ReviewsReply,
	}
}

func inboxHasSupport(platform string) bool {
	for _, ok := range inboxSupports(platform) {
		if ok {
			return true
		}
	}
	return false
}

// inboxCoverageState maps one sync result to a coverage state.
func inboxCoverageState(status, detail string) string {
	lower := strings.ToLower(detail)
	permission := strings.Contains(lower, "permission_required") || strings.Contains(lower, "reconnect")
	switch {
	case status == "ok":
		return coverageComplete
	case status == "unsupported":
		return coverageUnsupported
	case strings.Contains(lower, "catalog_update_required"):
		return coverageNeedsUpdate
	case permission:
		return coverageNeedsReconnect
	case status == "partial":
		return coveragePartial
	}
	return coverageError
}

// recordInboxCoverage stores the result of one account sync and emits
// inbox.coverage_changed when the state moves. Skipped syncs change
// nothing.
func recordInboxCoverage(ctx *sdk.AppCtx, pid string, accountID int64, platform, status, detail string) {
	if status == "skipped" {
		return
	}
	db := ctx.AppDB()
	state := inboxCoverageState(status, detail)
	var previous string
	_ = db.QueryRow(`SELECT COALESCE(cursor,'') FROM inbox_cursors WHERE social_account_id=? AND kind=?`, accountID, inboxCoverageCursor).Scan(&previous)
	_, _ = db.Exec(`INSERT INTO inbox_cursors(social_account_id,kind,cursor,last_sync_at,last_error) VALUES (?,?,?,CURRENT_TIMESTAMP,?)
		ON CONFLICT(social_account_id,kind) DO UPDATE SET cursor=excluded.cursor,last_sync_at=excluded.last_sync_at,last_error=excluded.last_error`,
		accountID, inboxCoverageCursor, state, nullable(detail))
	if state == coverageComplete {
		_, _ = db.Exec(`INSERT INTO inbox_cursors(social_account_id,kind,cursor,last_sync_at) VALUES (?,?,'',CURRENT_TIMESTAMP)
			ON CONFLICT(social_account_id,kind) DO UPDATE SET last_sync_at=excluded.last_sync_at`, accountID, inboxLastSuccessCursor)
	}
	if previous == "" {
		previous = coverageComplete // first record: only a problem is news
	}
	if previous != state {
		payload := map[string]any{"social_account_id": accountID, "platform": platform, "coverage": state, "previous": previous}
		if detail != "" {
			payload["detail"] = detail
		}
		ctx.EmitWithProject(inboxEventCoverage, pid, payload)
	}
}

func loadInboxCoverage(db *sql.DB, pid string, accountID int64) (*inboxAccountCoverage, error) {
	out := &inboxAccountCoverage{SocialAccountID: accountID}
	err := db.QueryRow(`SELECT platform, COALESCE(provider_slug,'native'), COALESCE(display_name,'') FROM social_accounts WHERE id=? AND project_id=?`, accountID, pid).
		Scan(&out.Platform, &out.Provider, &out.DisplayName)
	if err != nil {
		return nil, err
	}
	out.Supports = inboxSupports(out.Platform)
	limits := inboxPlatformLimits[out.Platform]
	out.Missing = append(out.Missing, limits.Missing...)
	out.Notes = append(out.Notes, limits.Notes...)
	if out.Provider == zernioProviderSlug {
		out.Notes = append(out.Notes, "Synced through Zernio.")
	}
	var state, lastSync, lastError string
	_ = db.QueryRow(`SELECT COALESCE(cursor,''), COALESCE(last_sync_at,''), COALESCE(last_error,'') FROM inbox_cursors WHERE social_account_id=? AND kind=?`, accountID, inboxCoverageCursor).Scan(&state, &lastSync, &lastError)
	_ = db.QueryRow(`SELECT COALESCE(last_sync_at,'') FROM inbox_cursors WHERE social_account_id=? AND kind=?`, accountID, inboxLastSuccessCursor).Scan(&out.LastSuccessAt)
	out.LastSyncAt, out.LastError = lastSync, lastError
	switch {
	case !inboxHasSupport(out.Platform):
		out.Coverage = coverageUnsupported
		if len(out.Missing) == 0 {
			out.Missing = append(out.Missing, "Social has no inbox support for this platform yet.")
		}
	case state == "":
		out.Coverage = coverageNotSynced
	default:
		out.Coverage = state
	}
	switch out.Coverage {
	case coverageNeedsReconnect:
		switch {
		case out.Provider == "native" && out.Platform == "youtube":
			out.Missing = append(out.Missing, "Reconnect from the Social account card to grant comment permissions.")
		case out.Provider == "native":
			out.Missing = append(out.Missing, "Reconnect from the Social account card to grant inbox permissions.")
		default:
			out.Missing = append(out.Missing, "Reconnect the account through its provider to grant inbox permissions.")
		}
	case coverageNeedsUpdate:
		out.Missing = append(out.Missing, "The platform's integration catalog is older than this Social version; update the platform.")
	}
	return out, nil
}

func (a *App) toolInboxCoverage(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	pid := projectScope(ctx, args)
	var ids []int64
	for _, raw := range toAnySlice(args["social_account_ids"]) {
		if id := toInt64Loose(raw); id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		rows, err := ctx.AppDB().Query(`SELECT id FROM social_accounts WHERE project_id=? AND status='active' ORDER BY id`, pid)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
	}
	accounts := make([]*inboxAccountCoverage, 0, len(ids))
	for _, id := range ids {
		coverage, err := loadInboxCoverage(ctx.AppDB(), pid, id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, coverage)
	}
	return map[string]any{"accounts": accounts, "count": len(accounts)}, nil
}
