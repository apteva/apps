package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestReadContinuationResumesWithoutOpeningOrRescanning(t *testing.T) {
	p := newFakePlatform()
	page, opens, clicks, closes := 0, 0, 0, 0
	current := ""
	p.readViewsResponse = func(tool string, args map[string]any) map[string]any {
		switch tool {
		case "computer.browser_open":
			opens++
			current = stringFromAny(args["url"])
		case "computer.browser_close":
			closes++
		case "computer.browser_session":
			return map[string]any{"session_id": "sess_1", "context_id": "provider-uuid", "app_context_id": "ctx-read", "backend": "local", "status": "active"}
		case "computer.browser_extract":
			h := readFixtureHTML(page*2+1, page*2+2, page*2+1)
			if page < 2 {
				h = strings.Replace(h, "</body>", `<a id="next">Next</a></body>`, 1)
			}
			return map[string]any{"html": h, "current_url": current, "rendered": true}
		case "computer.computer_use":
			if args["action"] == "screenshot" {
				return map[string]any{"current_url": current, "som_revision": 1, "som": []any{map[string]any{"id": "next", "text": "Next", "tag": "a", "effect": "navigation_only"}}}
			}
			if args["action"] == "click" {
				page++
				clicks++
				return map[string]any{"current_url": current}
			}
			if args["action"] == "navigate" {
				t.Fatal("resume navigated back to the entry page")
			}
		}
		return nil
	}
	ctx, app := newTestCtx(t, p)
	c := fixtureReadViews()
	c.Views = c.Views[:1]
	c.Views[0].UseEntryPage = true
	c.Views[0].Pagination = actorReadPagination{Mode: "next", Next: actorLocator{Text: "Next", Exact: true, SOMOnly: true, Selector: "#next"}, EndWhenNextAbsent: true, MaxPages: "{{pages}}", StableRounds: 2, SettleMS: 500, AdvanceTimeoutMS: 1000}
	c.AllowPartial = true
	c.ContinuationEnabled = true
	c.ContinuationRef = "{{continuation}}"
	b, _ := json.Marshal(c)
	var conf map[string]any
	json.Unmarshal(b, &conf)
	rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"}, "browser": map[string]any{"context_id": "ctx-read"}, "defaults": map[string]any{"continuation": "", "pages": 1}, "limits": map[string]any{"step_retries": 0}, "steps": []any{map[string]any{"action": "inspect_views", "read_views": conf}}})
	ref := ""
	firstRef := ""
	for i := 0; i < 3; i++ {
		_, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "input": map[string]any{"continuation": ref}})
		if err != nil {
			t.Fatal(err)
		}
		q, err := claimActorRun(ctx)
		if err != nil {
			t.Fatal(err)
		}
		app.executeActorRun(context.Background(), ctx, q)
		r, _ := getActorRun(ctx, q.ID)
		if r["status"] != "completed" {
			t.Fatal(r["error"])
		}
		out := mapFromAny(r["output"])
		if intFromAny(out["item_count"]) != 2 {
			t.Fatal("did not return exactly the next two records", out)
		}
		cov := mapFromAny(out["coverage"])
		if i < 2 {
			ref = stringFromAny(mapFromAny(out["continuation"])["ref"])
			if ref == "" || cov["inspection_complete"] != false || closes != 0 {
				t.Fatal("missing live continuation or incorrect coverage", out)
			}
			if i == 0 {
				firstRef = ref
			}
		}
		if i > 0 && cov["read_scope"] != "incremental" {
			t.Fatal("missing incremental scope")
		}
		if i == 2 && (out["continuation"] != nil || cov["more_results_remaining"] != false || closes != 1) {
			t.Fatal("end leaked browser or cursor", out)
		}
	}
	if opens != 1 || clicks != 2 {
		t.Fatal("rescan or duplicate pagination", opens, clicks)
	}
	_, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "input": map[string]any{"continuation": firstRef}})
	if err != nil {
		t.Fatal(err)
	}
	q, _ := claimActorRun(ctx)
	app.executeActorRun(context.Background(), ctx, q)
	r, _ := getActorRun(ctx, q.ID)
	if r["status"] != "failed" || !strings.Contains(stringFromAny(r["error"]), "consumed") {
		t.Fatal("reused consumed cursor", r)
	}
	if opens != 1 {
		t.Fatal("expired cursor silently restarted")
	}
}
func TestReadContinuationGuardsExpiryCloseAndConfiguration(t *testing.T) {
	p := newFakePlatform()
	ctx, app := newTestCtx(t, p)
	e := &actorExecution{app: app, ctx: ctx, run: &actorQueuedRun{ID: 10, ActorID: 5, ActorRevision: 7, InputJSON: `{"operation":"inbox"}`}, workerCtx: context.Background(), definition: actorDefinition{Browser: actorBrowser{ContextID: "ctx"}}, session: &browserSession{SessionID: "session", Backend: "local"}, coverage: &actorReadCoverage{MoreRemaining: true}}
	c := fixtureReadViews()
	c.Views = c.Views[:1]
	c.ContinuationEnabled = true
	c.ContinuationTTLSeconds = 60
	e.readConfig = &c
	e.readCheckpoint = &actorReadCheckpoint{Session: *e.session, SeenIDs: []string{"1"}, CurrentIDs: []string{"1"}, ResumeNext: true, URL: "https://example.com/library"}
	out, err := e.saveReadContinuation()
	if err != nil {
		t.Fatal(err)
	}
	ref := stringFromAny(out["ref"])
	c.ContinuationRef = ref
	e.definition.Browser.ContextID = "other"
	if e.resumeReadContinuation(c) == nil {
		t.Fatal("wrong context accepted")
	}
	e.definition.Browser.ContextID = "ctx"
	e.run.ActorRevision = 8
	if e.resumeReadContinuation(c) == nil {
		t.Fatal("wrong revision accepted")
	}
	e.run.ActorRevision = 7
	e.run.InputJSON = `{"operation":"other"}`
	if e.resumeReadContinuation(c) == nil {
		t.Fatal("wrong operation accepted")
	}
	e.run.InputJSON = `{"operation":"inbox"}`
	ctx.AppDB().Exec(`UPDATE actors_read_continuations SET expires_at=? WHERE ref=?`, time.Now().Unix()-1, ref)
	if e.resumeReadContinuation(c) == nil {
		t.Fatal("expired cursor accepted")
	}
	if err = app.cleanupReadContinuations(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	ctx.AppDB().QueryRow(`SELECT state FROM actors_read_continuations WHERE ref=?`, ref).Scan(&state)
	if state != "expired" {
		t.Fatal(state)
	}
	out, err = e.saveReadContinuation()
	if err != nil {
		t.Fatal(err)
	}
	ref = stringFromAny(out["ref"])
	if _, err = app.toolReadContinuationClose(ctx, map[string]any{"ref": ref}); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().QueryRow(`SELECT state FROM actors_read_continuations WHERE ref=?`, ref).Scan(&state)
	if state != "closed" {
		t.Fatal(state)
	}
	// Changing a batch size is permitted, while changing the source is not.
	a := e.readConfigHash(c)
	c.Views[0].Pagination.MaxPages = 99
	if e.readConfigHash(c) != a {
		t.Fatal("batch budget changed cursor scope")
	}
	c.EntryURL = "https://example.com/other"
	if e.readConfigHash(c) == a {
		t.Fatal("source not bound")
	}
}
func TestSelectorPaginationDiagnosticRecordsNoOp(t *testing.T) {
	p := newFakePlatform()
	page := readFixtureHTML(1, 2)
	page = strings.Replace(page, "</body>", `<div id="old" onclick="loadOlder()">Older</div></body>`, 1)
	p.readViewsResponse = func(tool string, args map[string]any) map[string]any {
		if tool == "computer.browser_extract" {
			return map[string]any{"html": page, "current_url": "https://example.com/library", "rendered": true}
		}
		if tool == "computer.computer_use" && args["action"] == "click" {
			if args["selector"] != "#old" || args["target_id"] != nil || args["coordinate"] != nil {
				t.Fatal("wrong pagination element")
			}
			return map[string]any{"current_url": "https://example.com/library"}
		}
		return nil
	}
	ctx, app := newTestCtx(t, p)
	c := fixtureReadViews()
	c.Views = c.Views[:1]
	c.Views[0].UseEntryPage = true
	c.AllowPartial = true
	c.Views[0].Pagination = actorReadPagination{Mode: "next", ClickMode: "selector", Next: actorLocator{Text: "Older", Selector: "#old", Exact: true, SOMOnly: true}, EndWhenNextAbsent: true, OnStall: "partial", AdvanceTimeoutMS: 500, MaxPages: 5, StableRounds: 2, SettleMS: 500}
	b, _ := json.Marshal(c)
	var conf map[string]any
	json.Unmarshal(b, &conf)
	rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"}, "steps": []any{map[string]any{"action": "inspect_views", "read_views": conf}}})
	app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
	q, _ := claimActorRun(ctx)
	app.executeActorRun(context.Background(), ctx, q)
	r, _ := getActorRun(ctx, q.ID)
	if r["status"] != "completed" {
		t.Fatal(r["error"])
	}
	b, _ = json.Marshal(mapFromAny(r["output"])["coverage"])
	var cov actorReadCoverage
	json.Unmarshal(b, &cov)
	v := cov.Views[0]
	if cov.Complete || !cov.MoreRemaining || len(v.Pagination) != 1 {
		t.Fatal(string(b))
	}
	d := v.Pagination[0]
	if !d.DispatchAttempted || !d.DispatchAccepted || !d.ControlPresent || d.StopReason != "no_change_control_present" || fmt.Sprint(d.BeforeIDs) != fmt.Sprint(d.AfterIDs) || len(d.NewIDs) != 0 || d.Target["dom_id"] != "old" {
		t.Fatal(d)
	}
}

func TestPaginationLoadingErrorAndWrongControlDiagnostics(t *testing.T) {
	for _, mode := range []string{"loading_advance", "site_error", "wrong_label", "ambiguous", "rejected_click"} {
		t.Run(mode, func(t *testing.T) {
			p := newFakePlatform()
			clicked := false
			polls := 0
			page := readFixtureHTML(1, 2)
			base := strings.Replace(page, "</body>", `<div id="older">Older</div></body>`, 1)
			if mode == "wrong_label" {
				base = strings.Replace(base, ">Older<", ">Send<", 1)
			}
			if mode == "ambiguous" {
				base = strings.Replace(base, "</body>", `<div id="older">Older</div></body>`, 1)
			}
			if mode == "rejected_click" {
				p.failAction = "click"
			}
			p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				if tool == "computer.computer_use" && in["action"] == "click" {
					clicked = true
					return map[string]any{"current_url": "https://example.com/library"}
				}
				if tool == "computer.browser_extract" {
					h := base
					if clicked {
						polls++
						if mode == "site_error" {
							h = strings.Replace(h, "</body>", `<div id="error">Request failed</div></body>`, 1)
						} else if polls == 1 {
							h = strings.Replace(h, "</body>", `<div id="loading">Loading</div></body>`, 1)
						} else {
							h = readFixtureHTML(1, 2, 3)
						}
					}
					return map[string]any{"html": h, "current_url": "https://example.com/library", "rendered": true}
				}
				return nil
			}
			ctx, app := newTestCtx(t, p)
			c := fixtureReadViews()
			c.Views = c.Views[:1]
			v := &c.Views[0]
			v.UseEntryPage = true
			v.LoadingSelector = "#loading"
			v.ErrorSelector = "#error"
			v.Pagination = actorReadPagination{Mode: "next", ClickMode: "selector", Next: actorLocator{Text: "Older", Selector: "#older", Exact: true, SOMOnly: true}, EndWhenNextAbsent: true, MaxPages: 6, StableRounds: 2, SettleMS: 500, AdvanceTimeoutMS: 3000}
			b, _ := json.Marshal(c)
			var conf map[string]any
			json.Unmarshal(b, &conf)
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"}, "steps": []any{map[string]any{"action": "inspect_views", "read_views": conf}}})
			app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
			q, _ := claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, q)
			r, _ := getActorRun(ctx, q.ID)
			out := mapFromAny(r["output"])
			b, _ = json.Marshal(out["coverage"])
			var cov actorReadCoverage
			json.Unmarshal(b, &cov)
			d := cov.Views[0].Pagination[0]
			if mode == "loading_advance" {
				if r["status"] != "completed" || !cov.Complete || !d.LoadingObserved || len(d.NewIDs) != 1 || d.StopReason != "new_ids_observed" {
					t.Fatal(r["status"], r["error"], d)
				}
			} else {
				if r["status"] != "failed" || cov.Complete {
					t.Fatal(r)
				}
				if mode == "site_error" {
					if d.StopReason != "site_error" || d.ErrorText != "Request failed" {
						t.Fatal(d)
					}
				} else if d.DispatchAccepted || d.StopReason != "click_rejected" {
					t.Fatal(d)
				}
			}
			if (mode == "wrong_label" || mode == "ambiguous") && clicked {
				t.Fatal("unverified target clicked")
			}
		})
	}
}

func TestReadContinuationCloseIsRegistered(t *testing.T) {
	a := &App{}
	for _, tool := range a.actorTools() {
		if tool.Name == "actors_read_close" {
			if tool.Handler == nil {
				t.Fatal("missing handler")
			}
			return
		}
	}
	t.Fatal("missing continuation close MCP tool")
}
