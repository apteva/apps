package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Supporters are people paying an account recurring support (YouTube channel
// members, later patrons or subscribers). They are never mixed with followers.
// Each platform/provider contributes one adapter; storage, metrics, breakdowns
// and the panel only see the generic snapshot below.

const (
	supporterRefreshInterval = 24 * time.Hour
	supporterPageBudget      = 50 // pages per run; YouTube pages hold up to 1,000 members
	supporterSource          = "supporters"
)

type supporterLevelCount struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type supporterCount struct {
	Total         int64
	ProviderTotal *int64
	Complete      bool
	Levels        []supporterLevelCount
}

type supporterAdapter struct {
	Source string
	Count  func(*sdk.AppCtx, audienceAccount, int) (supporterCount, error)
}

var supporterAdapters = map[string]supporterAdapter{
	"native:youtube": {
		Source: "youtube.members",
		Count:  countYouTubeSupporters,
	},
}

func adapterForSupporters(platform, provider string) (supporterAdapter, bool) {
	if provider == "" {
		provider = "native"
	}
	adapter, ok := supporterAdapters[provider+":"+platform]
	return adapter, ok
}

// supporterSnapshot is one observation. Total is nil unless State is
// "available"; a missing count is never reported as zero.
type supporterSnapshot struct {
	SocialAccountID int64                 `json:"social_account_id"`
	Platform        string                `json:"platform"`
	State           string                `json:"state"` // available | access_required | scope_missing | unsupported | error
	Reason          string                `json:"reason,omitempty"`
	Source          string                `json:"source,omitempty"`
	ObservedAt      string                `json:"observed_at,omitempty"`
	Total           *int64                `json:"total"`
	ProviderTotal   *int64                `json:"provider_total,omitempty"`
	TotalsAgree     *bool                 `json:"totals_agree,omitempty"`
	Complete        bool                  `json:"complete"`
	Levels          []supporterLevelCount `json:"levels"`
}

var supporterReasons = map[string]string{
	"access_required": "The provider refused access. For YouTube, ask your YouTube Partner Manager to allowlist this channel for the members API and make sure channel memberships are on.",
	"scope_missing":   "This connection was authorized before supporter data was included. Reconnect the YouTube connection in Integrations once to grant it.",
	"unsupported":     "Supporter counts are not available for this account.",
	"error":           "Supporter counts could not be read; the last successful count is kept.",
}

// supporterState classifies an adapter error with the same rules as
// membership tier discovery.
func supporterState(err error) string {
	switch membershipErrorStatus(err) {
	case "permission_required":
		return "scope_missing"
	case "approval_required":
		return "access_required"
	case "catalog_update_required":
		return "unsupported"
	default:
		return "error"
	}
}

func countYouTubeSupporters(ctx *sdk.AppCtx, account audienceAccount, pageBudget int) (supporterCount, error) {
	identity, err := youtubeChannelIdentity(ctx, account)
	if err != nil {
		return supporterCount{}, err
	}
	levels, err := youtubeMembershipLevels(ctx, account, identity)
	if err != nil {
		return supporterCount{}, err
	}
	names := map[string]string{}
	counts := map[string]int64{}
	for _, level := range levels {
		names[level.ID] = level.Name
	}
	out := supporterCount{}
	token := ""
	for page := 0; page < pageBudget; page++ {
		input := map[string]any{"part": "snippet", "mode": "all_current", "maxResults": 1000}
		if token != "" {
			input["pageToken"] = token
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(account.ConnectionID, "list_members", input)
		if err != nil {
			return supporterCount{}, err
		}
		if res == nil || !res.Success {
			return supporterCount{}, upstreamError(res)
		}
		var response struct {
			NextPageToken string `json:"nextPageToken"`
			PageInfo      *struct {
				TotalResults *int64 `json:"totalResults"`
			} `json:"pageInfo"`
			Items []struct {
				Snippet struct {
					Creator     string `json:"creatorChannelId"`
					Memberships struct {
						Level     string `json:"highestAccessibleLevel"`
						LevelName string `json:"highestAccessibleLevelDisplayName"`
					} `json:"membershipsDetails"`
				} `json:"snippet"`
			} `json:"items"`
		}
		if json.Unmarshal(res.Data, &response) != nil || response.Items == nil {
			return supporterCount{}, errors.New("members response did not contain items")
		}
		if page == 0 && response.PageInfo != nil {
			out.ProviderTotal = response.PageInfo.TotalResults
		}
		for _, item := range response.Items {
			if item.Snippet.Creator != identity {
				return supporterCount{}, errors.New("member belongs to a different channel")
			}
			out.Total++
			level := item.Snippet.Memberships.Level
			counts[level]++
			if names[level] == "" {
				names[level] = item.Snippet.Memberships.LevelName
			}
		}
		if response.NextPageToken == "" {
			out.Complete = true
			break
		}
		token = response.NextPageToken
	}
	// Known tiers first (with zero counts), then any level seen only on members.
	order := levelIDs(levels)
	extra := []string{}
	for id := range counts {
		if !containsFold(order, id) {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	for _, id := range append(order, extra...) {
		name := names[id]
		if name == "" {
			name = "Unknown level"
		}
		out.Levels = append(out.Levels, supporterLevelCount{ID: id, Name: name, Count: counts[id]})
	}
	return out, nil
}

func levelIDs(levels []membershipLevel) []string {
	out := make([]string, 0, len(levels))
	for _, level := range levels {
		out = append(out, level.ID)
	}
	return out
}

// refreshSupporters counts one account and records the snapshot. Complete
// counts also become ordinary "supporters" metric points (total plus one
// point per supporter_level), so history, trends and breakdowns are generic.
func (a *App) refreshSupporters(ctx *sdk.AppCtx, pid string, accountID int64) (supporterSnapshot, error) {
	account, err := loadAudienceAccount(ctx, pid, accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return supporterSnapshot{}, errors.New("account not found in this project")
	}
	if err != nil {
		return supporterSnapshot{}, err
	}
	snap := supporterSnapshot{SocialAccountID: account.ID, Platform: account.Platform, Levels: []supporterLevelCount{}}
	adapter, ok := adapterForSupporters(account.Platform, account.Provider)
	if !ok {
		snap.State, snap.Reason = "unsupported", supporterReasons["unsupported"]
		return snap, nil
	}
	snap.Source = adapter.Source
	snap.ObservedAt = time.Now().UTC().Format(time.RFC3339)
	if account.Status != "active" {
		snap.State, snap.Reason = "error", "Social account is not active; reconnect it first."
	} else if count, err := adapter.Count(ctx, account, supporterPageBudget); err != nil {
		snap.State = supporterState(err)
		snap.Reason = supporterReasons[snap.State]
		if snap.State == "unsupported" {
			snap.Reason = "The connected integration does not expose supporter data yet; update the integration catalog."
		}
	} else {
		total := count.Total
		snap.State, snap.Total, snap.ProviderTotal, snap.Complete, snap.Levels = "available", &total, count.ProviderTotal, count.Complete, count.Levels
		if count.ProviderTotal != nil && count.Complete {
			agree := *count.ProviderTotal == count.Total
			snap.TotalsAgree = &agree
		}
		if !count.Complete {
			snap.Reason = "Counting stopped at the page budget; the total is a lower bound."
		}
	}
	if err := persistSupporterSnapshot(ctx, pid, snap); err != nil {
		return snap, err
	}
	return snap, nil
}

func persistSupporterSnapshot(ctx *sdk.AppCtx, pid string, snap supporterSnapshot) error {
	levels, _ := json.Marshal(snap.Levels)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO social_supporter_snapshots(project_id,social_account_id,observed_at,state,reason,source,total,provider_total,complete,levels_json) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		pid, snap.SocialAccountID, snap.ObservedAt, snap.State, snap.Reason, snap.Source, nullableCount(snap.Total), nullableCount(snap.ProviderTotal), snap.Complete, string(levels)); err != nil {
		return err
	}
	if snap.State == "available" && snap.Total != nil {
		// Partial counts are lower bounds: kept in the snapshot, never in the
		// metric series (history reads status='ok' only).
		status := "ok"
		if !snap.Complete {
			status = "partial"
		}
		var profileID int64
		_ = tx.QueryRow(`SELECT COALESCE(profile_id,0) FROM social_accounts WHERE id=? AND project_id=?`, snap.SocialAccountID, pid).Scan(&profileID)
		source := supporterSource + ":" + snap.Source
		if err = insertSocialMetricPointDimensionsDB(tx, pid, profileID, snap.SocialAccountID, 0, 0, snap.Platform, "account", "supporters", "snapshot", snap.ObservedAt, *snap.Total, source, status, "", nil); err != nil {
			return err
		}
		for _, level := range snap.Levels {
			if err = insertSocialMetricPointDimensionsDB(tx, pid, profileID, snap.SocialAccountID, 0, 0, snap.Platform, "account", "supporters", "snapshot", snap.ObservedAt, level.Count, source, status, "", map[string]string{"supporter_level": level.Name}); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func nullableCount(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func latestSupporterSnapshot(ctx *sdk.AppCtx, pid string, accountID int64) (supporterSnapshot, bool) {
	var snap supporterSnapshot
	var total, providerTotal sql.NullInt64
	var levels string
	err := ctx.AppDB().QueryRow(`SELECT s.social_account_id,a.platform,s.state,s.reason,s.source,s.observed_at,s.total,s.provider_total,s.complete,s.levels_json FROM social_supporter_snapshots s JOIN social_accounts a ON a.id=s.social_account_id AND a.project_id=s.project_id WHERE s.project_id=? AND s.social_account_id=? ORDER BY s.id DESC LIMIT 1`, pid, accountID).
		Scan(&snap.SocialAccountID, &snap.Platform, &snap.State, &snap.Reason, &snap.Source, &snap.ObservedAt, &total, &providerTotal, &snap.Complete, &levels)
	if err != nil {
		return snap, false
	}
	if total.Valid {
		snap.Total = &total.Int64
	}
	if providerTotal.Valid {
		snap.ProviderTotal = &providerTotal.Int64
		if snap.Total != nil && snap.Complete {
			agree := providerTotal.Int64 == *snap.Total
			snap.TotalsAgree = &agree
		}
	}
	snap.Levels = []supporterLevelCount{}
	_ = json.Unmarshal([]byte(levels), &snap.Levels)
	// When the latest read failed transiently, keep the last good count.
	if snap.State == "error" {
		var lastTotal sql.NullInt64
		var lastLevels string
		if ctx.AppDB().QueryRow(`SELECT total,levels_json FROM social_supporter_snapshots WHERE project_id=? AND social_account_id=? AND state='available' AND complete=1 ORDER BY id DESC LIMIT 1`, pid, accountID).Scan(&lastTotal, &lastLevels) == nil && lastTotal.Valid {
			snap.Total = &lastTotal.Int64
			_ = json.Unmarshal([]byte(lastLevels), &snap.Levels)
			snap.Reason = strings.TrimSpace(snap.Reason + " Showing the last successful count.")
		}
	}
	return snap, true
}

func supporterRefreshDue(ctx *sdk.AppCtx, pid string, accountID int64) bool {
	snap, ok := latestSupporterSnapshot(ctx, pid, accountID)
	if !ok {
		return true
	}
	at, err := time.Parse(time.RFC3339, snap.ObservedAt)
	return err != nil || time.Since(at) >= supporterRefreshInterval
}

// refreshSupportersIfDue is the daily collector hook. Accounts without an
// adapter are skipped entirely, so nothing is stored for them.
func (a *App) refreshSupportersIfDue(ctx *sdk.AppCtx, pid string, accountID int64) {
	var platform, provider string
	if ctx.AppDB().QueryRow(`SELECT platform,COALESCE(provider_slug,'native') FROM social_accounts WHERE id=? AND project_id=? AND status='active'`, accountID, pid).Scan(&platform, &provider) != nil {
		return
	}
	if _, ok := adapterForSupporters(platform, provider); !ok || !supporterRefreshDue(ctx, pid, accountID) {
		return
	}
	if _, err := a.refreshSupporters(ctx, pid, accountID); err != nil {
		ctx.Logger().Warn("supporters: refresh failed", "project", pid, "account", accountID, "err", err)
	}
}

func (a *App) toolAccountSupporters(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	pid := projectScope(ctx)
	accountID := int64(intArg(args, "social_account_id", 0))
	if accountID <= 0 {
		return mcpError("social_account_id required"), nil
	}
	var platform, provider string
	if err := ctx.AppDB().QueryRow(`SELECT platform,COALESCE(provider_slug,'native') FROM social_accounts WHERE id=? AND project_id=?`, accountID, pid).Scan(&platform, &provider); err != nil {
		return mcpError("account not found in this project"), nil
	}
	if _, ok := adapterForSupporters(platform, provider); !ok {
		return supporterSnapshot{SocialAccountID: accountID, Platform: platform, State: "unsupported", Reason: supporterReasons["unsupported"], Levels: []supporterLevelCount{}}, nil
	}
	if snap, ok := latestSupporterSnapshot(ctx, pid, accountID); ok && !boolArg(args, "refresh", false) {
		return snap, nil
	}
	return a.refreshSupporters(ctx, pid, accountID)
}

func accountSupportersTool(a *App) sdk.Tool {
	return sdk.Tool{
		Name:        "account_supporters",
		Description: "Paid supporters (e.g. YouTube channel members) for one Social account: total and per-level counts with an observation time, or a state explaining why no count is available (access_required, scope_missing, unsupported, error). Never reports zero for an unknown count. refresh=true runs one bounded, read-only count; otherwise the latest snapshot is returned. Counts are refreshed daily and also appear as the supporters metric and the supporter_level breakdown in account analytics.",
		InputSchema: schemaObject(map[string]any{
			"social_account_id": map[string]any{"type": "integer", "minimum": 1},
			"refresh":           map[string]any{"type": "boolean", "default": false},
		}, []string{"social_account_id"}),
		Handler: a.toolAccountSupporters,
	}
}

// supporterBreakdown is the supporter_level breakdown from the latest
// snapshot: rows when counted, otherwise the state and its reason.
func supporterBreakdown(snap *supporterSnapshot) analyticsBreakdown {
	b := analyticsBreakdown{Dimension: "supporter_level", Source: supporterSource}
	if snap == nil {
		b.Status, b.Reason = "unsupported", supporterReasons["unsupported"]
		return b
	}
	b.Source = supporterSource + ":" + snap.Source
	if snap.State != "available" || snap.Total == nil {
		b.Status, b.Reason = snap.State, snap.Reason
		return b
	}
	b.Status = "ok"
	if !snap.Complete {
		b.Status, b.Reason = "partial", snap.Reason
	}
	for _, level := range snap.Levels {
		b.Rows = append(b.Rows, analyticsBreakdownRow{Dimensions: map[string]string{"supporter_level": level.Name}, Metrics: map[string]int64{"supporters": level.Count}})
	}
	return b
}

// applySupporterMetrics puts the latest complete count on an account
// metrics result so "supporters" behaves like any other snapshot metric.
func applySupporterMetrics(ctx *sdk.AppCtx, pid string, out *accountMetricsResult, provider string) {
	if _, ok := adapterForSupporters(out.Platform, provider); !ok {
		return
	}
	snap, ok := latestSupporterSnapshot(ctx, pid, out.SocialAccountID)
	if !ok {
		snap = supporterSnapshot{State: "unsupported", Reason: "Supporters have not been counted yet."}
	}
	out.supporters = &snap
	out.SupportersState = snap.State
	if snap.State == "available" && snap.Total != nil && snap.Complete {
		out.Supporters = *snap.Total
	}
}

// GET /supporters/accounts/:id[?refresh=true] — the panel's read of
// account_supporters.
func (a *App) handleSupportersAccount(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/supporters/accounts/"), "/")
	if id == "" || strings.Contains(id, "/") || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	args := scopedQueryArgs(r, "refresh")
	args["social_account_id"] = id
	out, err := a.toolAccountSupporters(globalCtx, args)
	if err != nil {
		http.Error(w, "Supporters request failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}
