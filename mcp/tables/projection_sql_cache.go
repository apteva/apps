package main

import (
	"crypto/sha256"
	"fmt"
	"sync/atomic"

	sdk "github.com/apteva/app-sdk"
)

// projectionSQLValidation caches only parsing and read-only validation. It
// deliberately does not cache authorization: loadQueryTable and authorizeQuery
// still run for every caller, so a permission change takes effect immediately.
// The epoch is bumped on every table DDL and projection definition change.
type projectionSQLValidation struct {
	tokens []sqlToken
}

func projectionSQLCacheKey(ctx *sdk.AppCtx, epoch uint64, raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%d:%d:%x", ctx.AppDBGeneration(), epoch, digest[:])
}

func (a *App) cachedProjectionSQL(ctx *sdk.AppCtx, raw string) ([]sqlToken, error) {
	epoch := atomic.LoadUint64(&a.projectionSQLEpoch)
	key := projectionSQLCacheKey(ctx, epoch, raw)
	a.projectionSQLMu.RLock()
	entry, ok := a.projectionSQLCache[key]
	a.projectionSQLMu.RUnlock()
	if ok {
		return append([]sqlToken(nil), entry.tokens...), nil
	}
	if err := validateReadOnlySQL(raw); err != nil {
		return nil, err
	}
	tokens, err := sqlTokens(raw)
	if err != nil {
		return nil, err
	}
	a.projectionSQLMu.Lock()
	if a.projectionSQLCache == nil {
		a.projectionSQLCache = make(map[string]projectionSQLValidation)
	}
	if len(a.projectionSQLCache) >= maxQueryPlanEntries {
		// SQL validation is bounded and disposable. Do not let a tenant control
		// process memory through unique projection definitions.
		a.projectionSQLCache = make(map[string]projectionSQLValidation)
	}
	a.projectionSQLCache[key] = projectionSQLValidation{tokens: append([]sqlToken(nil), tokens...)}
	a.projectionSQLMu.Unlock()
	return tokens, nil
}

func (a *App) invalidateProjectionSQL() {
	a.projectionSQLMu.Lock()
	a.projectionSQLCache = nil
	a.projectionSQLMu.Unlock()
	atomic.AddUint64(&a.projectionSQLEpoch, 1)
}

func (a *App) invalidateSQLCaches() {
	a.invalidateProjectionSQL()
	a.authorizationMu.Lock()
	a.authorizationCache = nil
	a.authorizationMu.Unlock()
	a.plans.invalidateAll()
}

func (a *App) authorizationKey(ctx *sdk.AppCtx, resolved string) string {
	digest := sha256.Sum256([]byte(resolved))
	return fmt.Sprintf("%d:%d:%x", ctx.AppDBGeneration(), atomic.LoadUint64(&a.projectionSQLEpoch), digest[:])
}

func (a *App) authorizationCached(key string) bool {
	a.authorizationMu.RLock()
	_, ok := a.authorizationCache[key]
	a.authorizationMu.RUnlock()
	return ok
}

func (a *App) rememberAuthorization(key string) {
	a.authorizationMu.Lock()
	if a.authorizationCache == nil {
		a.authorizationCache = make(map[string]struct{})
	}
	if len(a.authorizationCache) >= maxQueryPlanEntries {
		a.authorizationCache = make(map[string]struct{})
	}
	a.authorizationCache[key] = struct{}{}
	a.authorizationMu.Unlock()
}

func placeholderNamesFromTokens(tokens []sqlToken) []string {
	set := map[string]bool{}
	for _, t := range tokens {
		if t.kind == "placeholder" {
			set[t.value] = true
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	// The old helper sorts names; keep deterministic authorization order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
