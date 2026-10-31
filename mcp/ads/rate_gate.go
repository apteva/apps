package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Provider rate limits belong to the physical ad account behind a connection,
// not to a project binding: five projects bound to one Meta account share one
// quota. Every provider call for that account passes through one gate, and a
// rate-limit response closes the gate for all of them. Calling Meta while it
// is limiting only extends the block, so nothing is sent until the cooldown
// ends; the first call after it is the probe.

const (
	providerCooldownMin = 5 * time.Minute
	providerCooldownMax = time.Hour
)

type providerCooldown struct {
	Until    time.Time
	Failures int
}

type providerGate struct {
	mu     sync.Mutex
	loaded map[string]bool
	state  map[string]providerCooldown
}

func providerKey(acct *adAccount) string {
	return fmt.Sprintf("%s:%d:%s", acct.Platform, acct.ConnectionID, strings.TrimPrefix(acct.NativeAccountID, "act_"))
}

// errProviderCoolingDown is returned instead of calling a provider whose
// account-wide cooldown has not ended.
type errProviderCoolingDown struct{ Until time.Time }

func (e errProviderCoolingDown) Error() string {
	return "provider rate limit cooldown until " + e.Until.UTC().Format(time.RFC3339)
}

func (a *App) now() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now().UTC()
}

func (a *App) cooldownFor(ctx *sdk.AppCtx, key string) providerCooldown {
	a.gate.mu.Lock()
	defer a.gate.mu.Unlock()
	if a.gate.state == nil {
		a.gate.state = map[string]providerCooldown{}
		a.gate.loaded = map[string]bool{}
	}
	if !a.gate.loaded[key] && ctx != nil && ctx.AppDB() != nil {
		var until string
		var failures int
		if ctx.AppDB().QueryRow(
			`SELECT cooldown_until, failure_count FROM ad_provider_cooldowns WHERE provider_key=?`, key,
		).Scan(&until, &failures) == nil {
			parsed, _ := time.Parse(time.RFC3339, until)
			a.gate.state[key] = providerCooldown{Until: parsed, Failures: failures}
		}
		a.gate.loaded[key] = true
	}
	return a.gate.state[key]
}

// providerCooldownUntil reports when the account's gate reopens, or the zero
// time when it is open.
func (a *App) providerCooldownUntil(ctx *sdk.AppCtx, acct *adAccount) time.Time {
	state := a.cooldownFor(ctx, providerKey(acct))
	if a.now().Before(state.Until) {
		return state.Until
	}
	return time.Time{}
}

func (a *App) recordProviderRateLimit(ctx *sdk.AppCtx, acct *adAccount, res *sdk.ExecuteResult) time.Time {
	key := providerKey(acct)
	state := a.cooldownFor(ctx, key)
	state.Failures++
	delay := providerCooldownMin * time.Duration(1<<min(state.Failures-1, 4))
	if hinted := providerRetryHint(res); hinted > delay {
		delay = hinted
	}
	if delay > providerCooldownMax {
		delay = providerCooldownMax
	}
	state.Until = a.now().Add(delay)
	a.gate.mu.Lock()
	a.gate.state[key] = state
	a.gate.mu.Unlock()
	if ctx != nil && ctx.AppDB() != nil {
		_, _ = ctx.AppDB().Exec(
			`INSERT INTO ad_provider_cooldowns (provider_key, cooldown_until, failure_count, updated_at)
			 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
			 ON CONFLICT(provider_key) DO UPDATE SET cooldown_until=excluded.cooldown_until,
			    failure_count=excluded.failure_count, updated_at=CURRENT_TIMESTAMP`,
			key, state.Until.Format(time.RFC3339), state.Failures,
		)
	}
	return state.Until
}

func (a *App) recordProviderSuccess(ctx *sdk.AppCtx, acct *adAccount) {
	key := providerKey(acct)
	if a.cooldownFor(ctx, key).Failures == 0 {
		return
	}
	a.gate.mu.Lock()
	delete(a.gate.state, key)
	a.gate.mu.Unlock()
	if ctx != nil && ctx.AppDB() != nil {
		_, _ = ctx.AppDB().Exec(`DELETE FROM ad_provider_cooldowns WHERE provider_key=?`, key)
	}
}

// callProvider is the only path to a provider for a bound ad account.
func (a *App) callProvider(ctx *sdk.AppCtx, acct *adAccount, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if until := a.providerCooldownUntil(ctx, acct); !until.IsZero() {
		return nil, errProviderCoolingDown{Until: until}
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnectionID, tool, input)
	if err != nil {
		return nil, err
	}
	if res != nil && res.Success {
		a.recordProviderSuccess(ctx, acct)
		return res, nil
	}
	if classifyProviderFailure(acct, tool, res)["code"] == "provider_rate_limited" {
		a.recordProviderRateLimit(ctx, acct, res)
	}
	return res, nil
}

func providerCooldownError(tool string, until time.Time) map[string]any {
	out := mcpError(tool + ": the provider is rate limiting this ad account; no request was sent. Retry after " + until.UTC().Format(time.RFC3339) + ".")
	out["code"] = "provider_rate_limited"
	out["provider_cooldown"] = true
	out["retry_at"] = until.UTC().Format(time.RFC3339)
	out["retryable"] = true
	return out
}

// providerRetryHint reads Meta's usage headers and Retry-After, when the
// platform passes headers through.
func providerRetryHint(res *sdk.ExecuteResult) time.Duration {
	if res == nil {
		return 0
	}
	header := func(name string) string {
		for k, v := range res.Headers {
			if strings.EqualFold(k, name) {
				return v
			}
		}
		return ""
	}
	best := time.Duration(0)
	if seconds, err := strconv.Atoi(strings.TrimSpace(header("Retry-After"))); err == nil {
		best = time.Duration(seconds) * time.Second
	}
	// x-business-use-case-usage: {"<account>":[{"estimated_time_to_regain_access":<minutes>,...}]}
	var buc map[string][]map[string]any
	if json.Unmarshal([]byte(header("X-Business-Use-Case-Usage")), &buc) == nil {
		for _, entries := range buc {
			for _, entry := range entries {
				if d := time.Duration(numericArgAny(entry["estimated_time_to_regain_access"])) * time.Minute; d > best {
					best = d
				}
			}
		}
	}
	// x-ad-account-usage: {"acc_id_util_pct":..,"reset_time_duration":<seconds>}
	var account map[string]any
	if json.Unmarshal([]byte(header("X-Ad-Account-Usage")), &account) == nil {
		if d := time.Duration(numericArgAny(account["reset_time_duration"])) * time.Second; d > best {
			best = d
		}
	}
	return best
}

// Meta throttling codes: 4 application, 17 user, 32 page, 613 call rate,
// 80000-80014 business use case; subcode 2446079 is the Ads API
// "too many API calls" block.
func metaRateLimitError(code, subcode int) bool {
	switch {
	case code == 4, code == 17, code == 32, code == 613:
		return true
	case code >= 80000 && code <= 80014:
		return true
	case subcode == 2446079:
		return true
	}
	return false
}

// ---- Shared hierarchy snapshot ----

// Selected-mode reads scan every ad set and ad in the account, and each
// project bound to the account used to scan independently. One snapshot per
// provider account now serves every project for hierarchySnapshotTTL; project
// ownership filtering still happens per caller.
const hierarchySnapshotTTL = 10 * time.Minute

type hierarchySnapshot struct {
	groups    []map[string]any
	ads       []map[string]any
	fetchedAt time.Time
}

type hierarchyFetch struct {
	done     chan struct{}
	snapshot *hierarchySnapshot
	failure  map[string]any
}

type hierarchyCache struct {
	mu        sync.Mutex
	snapshots map[string]*hierarchySnapshot
	inFlight  map[string]*hierarchyFetch
}

func (a *App) accountHierarchy(ctx *sdk.AppCtx, acct *adAccount, def *platformDef) (*hierarchySnapshot, map[string]any) {
	key := providerKey(acct)
	c := &a.hierarchy
	c.mu.Lock()
	if c.snapshots == nil {
		c.snapshots = map[string]*hierarchySnapshot{}
		c.inFlight = map[string]*hierarchyFetch{}
	}
	if snap := c.snapshots[key]; snap != nil && a.now().Sub(snap.fetchedAt) < hierarchySnapshotTTL {
		c.mu.Unlock()
		return snap, nil
	}
	if running := c.inFlight[key]; running != nil {
		c.mu.Unlock()
		select {
		case <-running.done:
			return running.snapshot, running.failure
		case <-ctx.Done():
			return nil, mcpError("hierarchy refresh cancelled")
		}
	}
	fetch := &hierarchyFetch{done: make(chan struct{})}
	c.inFlight[key] = fetch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.inFlight, key)
		if fetch.snapshot != nil {
			c.snapshots[key] = fetch.snapshot
		}
		close(fetch.done)
		c.mu.Unlock()
	}()
	groups, failure := a.listAllProviderChildren(ctx, acct, def, "ad_group", map[string]any{})
	if failure != nil {
		fetch.failure = failure
		return nil, failure
	}
	ads, failure := a.listAllProviderChildren(ctx, acct, def, "ad", map[string]any{})
	if failure != nil {
		fetch.failure = failure
		return nil, failure
	}
	fetch.snapshot = &hierarchySnapshot{groups: groups, ads: ads, fetchedAt: a.now()}
	return fetch.snapshot, nil
}

// invalidateHierarchy drops the shared snapshot after a provider mutation, so
// the next read sees the change.
func (a *App) invalidateHierarchy(acct *adAccount) {
	a.hierarchy.mu.Lock()
	delete(a.hierarchy.snapshots, providerKey(acct))
	a.hierarchy.mu.Unlock()
}

// ---- Cached list reads ----

const liveListMemory = 15 * time.Minute

// cachedEntityList serves adset_list/ad_list cached=true from ad_entities.
// An empty cache that was not filled by a recent live list is reported as
// cache_empty so the caller can decide to refresh explicitly.
func (a *App) cachedEntityList(ctx *sdk.AppCtx, acct *adAccount, level, campaignID, adGroupID string) (map[string]any, error) {
	rows, err := a.scopedEntityRows(ctx, acct, level, campaignID, adGroupID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"data": rows, "next_cursor": nil, "source": "cache"}
	if len(rows) == 0 {
		out["cache_empty"] = !a.listedRecently(acct, level, campaignID+"/"+adGroupID)
	}
	return out, nil
}

func (a *App) markListed(acct *adAccount, level, parent string) {
	a.listedMu.Lock()
	defer a.listedMu.Unlock()
	if a.listedAt == nil {
		a.listedAt = map[string]time.Time{}
	}
	a.listedAt[fmt.Sprintf("%s:%d:%s:%s", acct.ProjectID, acct.ID, level, parent)] = a.now()
}

func (a *App) listedRecently(acct *adAccount, level, parent string) bool {
	a.listedMu.Lock()
	defer a.listedMu.Unlock()
	at, ok := a.listedAt[fmt.Sprintf("%s:%d:%s:%s", acct.ProjectID, acct.ID, level, parent)]
	return ok && a.now().Sub(at) < liveListMemory
}
