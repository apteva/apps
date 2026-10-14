package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
	"sync"
	"time"
)

type idleWorkerEntry struct {
	revision int64
	until    time.Time
}
type idleWorkerGate struct {
	mu      sync.Mutex
	entries map[string]idleWorkerEntry
}

// Only empty durable queues are deferred. New/direct SQL work advances the
// epoch; active queues retain the original one-second deadline/recovery checks.
// A bounded fallback also rediscovers work after restart or old installations.
func (a *App) runIdleGated(name string, run func(context.Context, *sdk.AppCtx) error, ctx context.Context, app *sdk.AppCtx) error {
	query := map[string]string{
		"ai-call-policies":       `SELECT EXISTS(SELECT 1 FROM ai_call_policies INDEXED BY ai_call_policies_active WHERE project_id=? AND stage NOT IN ('ended','disarmed'))`,
		"ai-handoffs":            `SELECT EXISTS(SELECT 1 FROM ai_handoffs INDEXED BY idx_ai_handoffs_work WHERE project_id=? AND (status IN ('running','retry') OR (status='failed' AND fallback_applied=0)))`,
		"carrier-media-recovery": `SELECT EXISTS(SELECT 1 FROM carrier_media_bridges WHERE project_id=? AND state IN ('connecting','connected','recovering','failed'))`,
		"carrier-activations":    `SELECT EXISTS(SELECT 1 FROM carrier_activations WHERE project_id=? AND status IN ('pending','waiting','failed')) OR EXISTS(SELECT 1 FROM calls INDEXED BY idx_calls_ai_activation_recovery WHERE project_id=? AND direction='inbound' AND carrier_slug='telnyx' AND peer_kind='realtime' AND status IN ('answering','answered','pending'))`,
	}[name]
	if query == "" {
		return run(ctx, app)
	}
	project := app.CurrentProject()
	var revision int64
	if err := app.AppDB().QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM telephony_work_versions WHERE project_id=?),0)`, project).Scan(&revision); err != nil {
		return err
	}
	key := name + "\x00" + project
	now := time.Now()
	g := &a.idleWorkers
	g.mu.Lock()
	cached := g.entries[key]
	g.mu.Unlock()
	if cached.revision == revision && now.Before(cached.until) {
		return nil
	}
	if err := run(ctx, app); err != nil {
		return err
	}
	var work bool
	args := []any{project}
	if name == "carrier-activations" {
		args = append(args, project)
	}
	if err := app.AppDB().QueryRowContext(ctx, query, args...).Scan(&work); err != nil {
		return err
	}
	if !work {
		g.mu.Lock()
		if g.entries == nil || len(g.entries) >= 512 {
			g.entries = make(map[string]idleWorkerEntry)
		}
		g.entries[key] = idleWorkerEntry{revision, now.Add(30 * time.Second)}
		g.mu.Unlock()
	}
	return nil
}
