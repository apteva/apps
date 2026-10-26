package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestExactHistoricalActorGet(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	rec := saveFixtureActor(t, ctx, app, testActorDefinition())
	old, err := app.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": rec.Revision})
	if err != nil {
		t.Fatal(err)
	}
	def := testActorDefinition()
	def["steps"] = []any{map[string]any{"action": "goto", "url": "https://example.com/new"}}
	_, err = app.toolActorSave(ctx, map[string]any{"id": rec.ID, "name": rec.Name, "expected_revision": rec.Revision, "definition": def})
	if err != nil {
		t.Fatal(err)
	}
	got, err := app.toolActorGet(ctx, map[string]any{"name": rec.Name, "revision": rec.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["retrieved_revision"] != rec.Revision || got.(map[string]any)["latest_revision"] != rec.Revision+1 {
		t.Fatal(got)
	}
	before, _ := json.Marshal(old.(map[string]any)["actor"].(*actorRecord).Definition)
	after, _ := json.Marshal(got.(map[string]any)["actor"].(*actorRecord).Definition)
	if string(before) != string(after) {
		t.Fatal("historical definition replaced by latest")
	}
	for _, revision := range []int{-1, 0, 999} {
		if _, err = app.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": revision}); err == nil {
			t.Fatal("invalid/missing revision silently used latest")
		}
	}
	isolated := ctx.WithProject("other")
	got, err = app.toolActorGet(isolated, map[string]any{"id": rec.ID, "revision": rec.Revision})
	if err != nil || got.(map[string]any)["found"] != false {
		t.Fatal("revision escaped project scope")
	}
}

func TestRecoveryRejectsUnknownLegacyOutcome(t *testing.T) {
	def := actorDefinition{Steps: []actorStep{{Action: "goto"}, {Action: "wait"}, {Action: "click"}, {Action: "click", OnceKey: "req", ExpectedEffect: "immediate_external_commit", ConfirmConsequence: "immediate_external_commit"}}}
	message := `locator not found in SOM: text="Create" role="button"`
	trace := []any{map[string]any{"step": float64(0), "status": "completed"}, map[string]any{"step": float64(1), "status": "completed"}, map[string]any{"step": float64(2), "status": "failed", "message": message}}
	run := map[string]any{"status": "failed", "error": message, "output": map[string]any{"trace_preview": trace}}
	if err := proveUncommitted(run, def); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"successful", "unknown_click", "truncated_trace", "reserved", "commit_started"} {
		t.Run(mode, func(t *testing.T) {
			raw, _ := json.Marshal(run)
			var changed map[string]any
			json.Unmarshal(raw, &changed)
			out := mapFromAny(changed["output"])
			switch mode {
			case "successful":
				changed["status"] = "completed"
			case "unknown_click":
				changed["error"] = "network timed out after click"
			case "truncated_trace":
				out["trace_preview"] = trace[:2]
			case "reserved":
				out["effects"] = []any{map[string]any{"state": "uncertain"}}
			case "commit_started":
				out["execution_progress"] = map[string]any{"last_started_step": 3, "commit_attempted": true}
			}
			if err := proveUncommitted(changed, def); err == nil {
				t.Fatal("uncertain prior outcome allowed")
			}
		})
	}
}

func TestReviewedRecoveryWithFreshDuplicateInspection(t *testing.T) {
	for _, mode := range []string{"success", "not_reviewed", "bad_key", "bad_status", "guard_reserved", "stale_check", "incomplete_coverage", "wrong_creator", "duplicate_found", "fresh_duplicate", "fresh_unavailable"} {
		t.Run(mode, func(t *testing.T) {
			plat := newFakePlatform()
			current := ""
			fixed := false
			fresh := false
			commits := 0
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						current = stringFromAny(in["url"])
						return map[string]any{"current_url": current}
					case "screenshot":
						var targets []any
						if fixed {
							targets = []any{map[string]any{"id": "open", "tag": "button", "accessible_name": "Create post", "text": "Create post", "effect": "open_configuration"}, map[string]any{"id": "publish", "tag": "button", "accessible_name": "Publish", "effect": "immediate_external_commit"}}
						}
						return map[string]any{"current_url": current, "som_revision": 1, "som": targets}
					case "click":
						if in["target_id"] == "publish" {
							commits++
						}
						return map[string]any{"current_url": current}
					}
				case "computer.browser_extract":
					if fresh && mode == "fresh_unavailable" {
						return map[string]any{"html": `<body><a id="identity" href="/other"></a></body>`, "current_url": current, "rendered": true}
					}
					title := "Different"
					if mode == "duplicate_found" || (fresh && mode == "fresh_duplicate") {
						title = "Approved title"
					}
					return map[string]any{"html": `<body><a id="identity" href="/creator"></a><div id="rows"><div class="post" data-id="42"><b>` + title + `</b></div></div><span id="end">End</span></body>`, "current_url": current, "rendered": true}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			inspect := map[string]any{"read_only": true, "steps": []any{map[string]any{"action": "inspect_views", "read_views": map[string]any{"entry_url": "https://example.com/library", "identity_url": "{{creator_url}}", "identity": map[string]any{"selector": "#identity", "type": "url", "attribute": "href", "required": true}, "timeout_ms": 1000, "views": []any{map[string]any{"name": "records", "covers": []any{"published", "draft", "scheduled"}, "use_entry_page": true, "url_pattern": "^https://example.com/library$", "ready_selector": "#rows", "empty_selector": ".empty", "loading_selector": ".loading", "items": ".post", "fields": map[string]any{"id": map[string]any{"attribute": "data-id", "required": true}, "title": map[string]any{"selector": "b", "required": true}}, "key_field": "id", "pagination": map[string]any{"mode": "next", "next": map[string]any{"selector": "#more", "text": "More", "exact": true, "som_only": true}, "end_when_next_absent": true, "end_selector": "#end", "max_pages": 1, "stable_rounds": 2, "settle_ms": 500}}}}}}}
			publish := func(label, key string) map[string]any {
				return map[string]any{"recovery": map[string]any{"inspection_operation": "fetch_posts", "required_views": []any{"published", "scheduled", "draft"}, "match_fields": map[string]any{"title": "post_title"}}, "steps": []any{map[string]any{"action": "goto", "url": "{{creator_url}}"}, map[string]any{"action": "click", "locator": map[string]any{"text": label, "role": "button", "exact": true, "som_only": true}, "expected_effect": "open_configuration"}, map[string]any{"action": "click", "locator": map[string]any{"text": "Publish", "role": "button", "exact": true, "som_only": true}, "expected_text": "Publish", "expected_effect": "immediate_external_commit", "confirm_consequence": "immediate_external_commit", "once_key": key}}}
			}
			def := map[string]any{"schema_version": 1, "allowed_hosts": []any{"example.com"}, "browser": map[string]any{"context_id": "{{context_id}}"}, "defaults": map[string]any{"context_id": "test-context", "creator_url": "https://example.com/creator"}, "limits": map[string]any{"max_pages": 10, "max_items": 100, "max_duration_seconds": 60, "step_retries": 0}, "operations": map[string]any{"text_post": publish("Create", "{{request_id}}"), "fetch_posts": inspect}}
			rec := saveFixtureActor(t, ctx, app, def)
			input := map[string]any{"post_title": "Approved title", "request_id": "same-logical-post"}
			queue, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "text_post", "input": input, "idempotency_key": "original-logical-post"})
			if err != nil {
				t.Fatal(err)
			}
			originalID := queue.(map[string]any)["run_id"].(int64)
			claimed, _ := claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, claimed)
			original, _ := getActorRun(ctx, originalID)
			if original["status"] != "failed" {
				t.Fatal(original)
			}
			fixed = true
			key := "{{request_id}}"
			if mode == "bad_key" {
				key = "changed-request"
			}
			def["operations"].(map[string]any)["text_post"] = publish("Create post", key)
			saved, err := app.toolActorSave(ctx, map[string]any{"id": rec.ID, "name": rec.Name, "expected_revision": rec.Revision, "definition": def})
			if err != nil {
				t.Fatal(err)
			}
			revised := saved.(map[string]any)["actor"].(*actorRecord)
			queue, err = app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "revision": revised.Revision, "operation": "fetch_posts", "input": input})
			if err != nil {
				t.Fatal(err)
			}
			checkID := queue.(map[string]any)["run_id"].(int64)
			claimed, _ = claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, claimed)
			checkedBefore, _ := getActorRun(ctx, checkID)
			if checkedBefore["status"] != "completed" {
				t.Fatalf("fixture inspection failed: %v %v", checkedBefore["error"], checkedBefore["output"])
			}
			switch mode {
			case "bad_status":
				ctx.AppDB().Exec(`UPDATE actors_runs SET status='completed' WHERE id=?`, originalID)
			case "guard_reserved":
				ctx.AppDB().Exec(`INSERT INTO actors_effect_guards(project_id,actor_id,operation,once_key,run_id,expected_effect,state) VALUES(?,?,?,?,?,?,'uncertain')`, projectID(ctx), rec.ID, "text_post", "same-logical-post", originalID, "immediate_external_commit")
			case "stale_check":
				ctx.AppDB().Exec(`UPDATE actors_runs SET completed_at=? WHERE id=?`, time.Now().Add(-time.Hour), checkID)
			case "incomplete_coverage", "wrong_creator":
				checked, _ := getActorRun(ctx, checkID)
				out := mapFromAny(checked["output"])
				cov := mapFromAny(out["coverage"])
				if mode == "incomplete_coverage" {
					cov["inspection_complete"] = false
				} else {
					cov["creator_url"] = "https://example.com/other"
				}
				raw, _ := json.Marshal(out)
				ctx.AppDB().Exec(`UPDATE actors_runs SET output_json=? WHERE id=?`, string(raw), checkID)
			}
			args := map[string]any{"id": originalID, "revision": revised.Revision, "reviewed": mode != "not_reviewed", "duplicate_check_run_id": checkID}
			recovered, err := app.toolActorRunRecover(ctx, args)
			accepted := mode == "success" || mode == "fresh_duplicate" || mode == "fresh_unavailable"
			if (err == nil) != accepted {
				t.Fatalf("%s: %v", mode, err)
			}
			if accepted {
				fresh = true
				claimed, _ = claimActorRun(ctx)
				app.executeActorRun(context.Background(), ctx, claimed)
				rid := recovered.(map[string]any)["run_id"].(int64)
				r, _ := getActorRun(ctx, rid)
				if (r["status"] == "completed") != (mode == "success") {
					t.Fatalf("recovered outcome: %v", r["error"])
				}
				if mode == "success" {
					if commits != 1 || mapFromAny(r["trigger"])["recovery_of"] != float64(originalID) {
						t.Fatal("logical recovery link or commit failed", r)
					}
					if _, err := app.toolActorRunRecover(ctx, args); err == nil || !strings.Contains(err.Error(), "consequence") {
						t.Fatal("successful recovery allowed another attempt", err)
					}
					if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "revision": revised.Revision, "operation": "text_post", "input": input, "idempotency_key": "original-logical-post"}); err == nil {
						t.Fatal("changed snapshot reused original execution key")
					}
				}
			}
			if mode != "success" && commits != 0 {
				t.Fatal("blocked recovery committed")
			}
		})
	}
}

func TestExactCopyIncludesBoundariesAndTypes(t *testing.T) {
	for _, actual := range []any{"Title", " Title", "Title ", true} {
		e := &actorExecution{lastValues: map[string]any{"copy": actual}}
		err := e.assertValues(actorStep{Assertions: map[string]actorAssertion{"copy": {EqualsExact: "Title"}}})
		if (err == nil) != (actual == "Title") {
			t.Fatal("non-exact copy accepted", actual, err)
		}
	}
}

func TestRecoveryModernProgressRejectsUnclassifiedDispatch(t *testing.T) {
	def := actorDefinition{Steps: []actorStep{{Action: "goto"}, {Action: "click"}, {Action: "wait"}, {Action: "click", OnceKey: "req", ExpectedEffect: "immediate_external_commit"}}}
	run := map[string]any{"status": "failed", "error": "wait timed out", "output": map[string]any{"execution_progress": map[string]any{"last_started_step": 2, "completed_steps": 2, "commit_attempted": false}}}
	if err := proveUncommitted(run, def); err == nil {
		t.Fatal("unclassified dispatched click allowed recovery")
	}
	run["error"] = `locator not found in SOM: text="Create" role="button"`
	run["output"].(map[string]any)["execution_progress"] = map[string]any{"last_started_step": 1, "completed_steps": 1, "commit_attempted": false}
	if err := proveUncommitted(run, def); err != nil {
		t.Fatal("pre-dispatch terminal failure rejected", err)
	}
}

func TestConsequenceProgressIsDurableBeforeDispatch(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []any{"example.com"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com/new"}, map[string]any{"action": "click", "locator": map[string]any{"selector": "button.publish"}, "expected_effect": "immediate_external_commit", "confirm_consequence": "immediate_external_commit", "once_key": "logical-request"}}})
	q, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	id := q.(map[string]any)["run_id"].(int64)
	seen := false
	plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		if tool == "computer.computer_use" && in["action"] == "click" {
			r, err := getActorRun(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			p := mapFromAny(mapFromAny(r["output"])["execution_progress"])
			if p["commit_attempted"] != true || intArg(p, "last_started_step") != 1 {
				t.Fatal("dispatch preceded durable evidence", p)
			}
			seen = true
			return map[string]any{"current_url": "https://example.com/new"}
		}
		return nil
	}
	claimed, _ := claimActorRun(ctx)
	if err := app.executeActorRun(context.Background(), ctx, claimed); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("test never dispatched")
	}
}

func TestHistoricalRevisionDoesNotMergeLatestOperations(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	def := testActorDefinition()
	steps := def["steps"]
	delete(def, "steps")
	def["operations"] = map[string]any{"original": map[string]any{"steps": steps}}
	rec := saveFixtureActor(t, ctx, app, def)
	def["operations"].(map[string]any)["newer"] = map[string]any{"read_only": true, "steps": steps}
	if _, err := app.toolActorSave(ctx, map[string]any{"id": rec.ID, "name": rec.Name, "expected_revision": rec.Revision, "definition": def}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": rec.Revision})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["actor"].(*actorRecord)
	if len(got.Definition.Operations) != 1 || got.Definition.Operations["newer"].Steps != nil {
		t.Fatal("historical definition merged latest operations", got.Definition.Operations)
	}
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "revision": rec.Revision, "operation": "newer"}); err == nil {
		t.Fatal("run merged an operation absent from historical revision")
	}
	if _, err := app.toolActorSchedule(ctx, map[string]any{"actor_id": rec.ID, "revision": rec.Revision, "operation": "newer"}); err == nil {
		t.Fatal("schedule merged an operation absent from historical revision")
	}
}
