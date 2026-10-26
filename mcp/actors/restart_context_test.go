package main

import (
	"testing"
)

func TestRestartReleasesTerminalContextLocksWithoutReplayingWrites(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []any{"example.com"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com"}}, "output_schema": map[string]any{}})
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
		t.Fatal(err)
	}
	q, err := claimActorRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ctx.AppDB().Exec(`INSERT INTO actors_context_locks(project_id,context_id,run_id,expires_at) VALUES(?,?,?,?)`, projectID(ctx), "ctx-test", q.ID, 9999999999); err != nil {
		t.Fatal(err)
	}
	if err = recoverInterruptedRuns(ctx); err != nil {
		t.Fatal(err)
	}
	r, _ := getActorRun(ctx, q.ID)
	if r["status"] != "failed" || r["error"] != "interrupted by app restart" {
		t.Fatalf("write replayed after restart: %#v", r)
	}
	var n int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM actors_context_locks WHERE run_id=?`, q.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("terminal run still owns context")
	}
}
