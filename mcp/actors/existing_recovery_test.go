package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExistingRecoveryIsBoundAndCannotCreate(t *testing.T) {
	p := &actorExistingRecovery{Bindings: fixtureRecoveryBindings(), ResumeOperation: "publish_draft", InspectionOperation: "inspect_draft", URLPattern: `^https://example\.com/c/posts/(?:[^/]+-)?([0-9]+)/edit$`, InspectionLocators: []actorLocator{{Text: "Select tiers", Exact: true, SOMOnly: true}}}
	good := actorDefinition{Steps: []actorStep{{Action: "goto", URL: "{{creator_url}}"}, {Action: "goto", URL: "{{post_edit_url}}"}, {Action: "goto", URLFromField: "current_url"}, {Action: "click", Locator: p.InspectionLocators[0], ExpectedEffect: "open_configuration"}, {Action: "key", Key: "ESC"}, {Action: "click", Locator: actorLocator{Text: "Publish", Exact: true, SOMOnly: true}, OnceKey: "{{request_id}}", ExpectedEffect: "immediate_external_commit", ConfirmConsequence: "immediate_external_commit"}}}
	if err := validateResumeSteps(good, p); err != nil {
		t.Fatal(err)
	}
	for _, step := range []actorStep{{Action: "upload_file"}, {Action: "fill"}, {Action: "set_checked"}, {Action: "goto", URL: "https://example.com/new"}, {Action: "click", Locator: actorLocator{Text: "Create post"}, ExpectedEffect: "open_configuration"}, {Action: "key", Key: "ENTER"}} {
		changed := good
		changed.Steps = append(append([]actorStep(nil), good.Steps...), step)
		if validateResumeSteps(changed, p) == nil {
			t.Fatal("creation/editing allowed", step)
		}
	}
	vars := map[string]any{"title": "approved", "recovery_resource_id": "42", "recovery_resource_url": "https://example.com/c/posts/original-42/edit"}
	policy := &actorRecoveryPolicy{MatchFields: map[string]string{"title": "title"}, Existing: p}
	rows := []map[string]any{{"id": "42", "title": "approved", "status": "draft", "edit_url": "https://example.com/c/posts/42/edit"}}
	if err := checkRecoveryDuplicates(rows, policy, vars); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "published", "scheduled", "different_url", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			b, _ := json.Marshal(rows)
			var x []map[string]any
			json.Unmarshal(b, &x)
			switch mode {
			case "missing":
				x = nil
			case "published", "scheduled":
				x[0]["status"] = mode
			case "different_url":
				x[0]["edit_url"] = "https://example.com/c/posts/43/edit"
			case "duplicate":
				x = append(x, map[string]any{"id": "99", "title": "approved", "status": "draft"})
			}
			if checkRecoveryDuplicates(x, policy, vars) == nil {
				t.Fatal("unsafe duplicate evidence allowed")
			}
		})
	}
	binding := map[string]any{"kind": "recovery", "existing_resource": map[string]any{"resource_id": "42", "resource_url": vars["recovery_resource_url"], "id_field": "post_id", "ids_field": "attachment_ids", "attachment_ids": []any{"a", "b"}}}
	raw, _ := json.Marshal(binding)
	e := actorExecution{run: &actorQueuedRun{TriggerJSON: string(raw)}, currentURL: vars["recovery_resource_url"].(string), lastValues: map[string]any{"post_id": "42", "attachment_ids": []any{"a", "b"}}}
	commit := good.Steps[len(good.Steps)-1]
	if err := e.guardExistingRecovery(commit); err != nil {
		t.Fatal(err)
	}
	e.lastValues["attachment_ids"] = []any{"b", "a"}
	if e.guardExistingRecovery(commit) == nil {
		t.Fatal("reordered attachment binding accepted")
	}
}
func TestPrepareExistingRecoveryPreservesOriginalInputs(t *testing.T) {
	for _, mode := range []string{"good", "wrong_id", "extra", "reordered", "wrong_size", "not_ready", "wrong_copy", "wrong_context", "stale", "commit_attempted", "unknown_click", "unsafe_resume"} {
		t.Run(mode, func(t *testing.T) {
			plat := newFakePlatform()
			current := ""
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.computer_use":
					return map[string]any{"current_url": current}
				case "computer.browser_extract":
					return map[string]any{"rendered": true, "current_url": current, "html": `<pre>{"id":"42","title":"approved","published":null,"scheduled":null,"images":[{"id":"a","filename":"first.png","size":10,"state":"ready","type":"image"},{"id":"b","filename":"second.png","size":20,"state":"ready","type":"image"}]}</pre>`}
				}
				return nil
			}
			ctx, a := newTestCtx(t, plat)
			p := &actorExistingRecovery{Bindings: fixtureRecoveryBindings(), ResumeOperation: "publish_draft", InspectionOperation: "inspect_draft", URLPattern: `^https://example\.com/c/posts/(?:[^/]+-)?([0-9]+)/edit$`, PreparationLocators: []actorLocator{{Text: "Image", Exact: true, SOMOnly: true}}}
			policy := &actorRecoveryPolicy{InspectionOperation: "fetch_posts", RequiredViews: []string{"draft"}, MatchFields: map[string]string{"title": "post_title"}, Existing: p}
			fields := map[string]actorJSONField{"is_draft": {Path: "/published", IsNull: true, Required: true}, "is_unscheduled": {Path: "/scheduled", IsNull: true, Required: true}, "post_id": {Path: "/id", Required: true}, "published_at": {Path: "/published", Required: true}, "scheduled_for": {Path: "/scheduled", Required: true}}
			for _, pair := range []struct{ name, path string }{{"ids", "/id"}, {"filenames", "/filename"}, {"sizes", "/size"}, {"states", "/state"}, {"types", "/type"}} {
				fields["attachment_"+pair.name] = actorJSONField{Path: "/images", Select: pair.path, Required: true, Unique: pair.name == "ids"}
			}
			inspect := actorOperation{ReadOnly: true, Steps: []actorStep{{Action: "goto", URL: "{{post_edit_url}}"}, {Action: "observe_json", JSONDocument: &actorJSONDocument{Selector: "pre", Fields: fields}}}}
			publish := actorOperation{RecoveryOnly: true, Steps: []actorStep{{Action: "goto", URL: "{{post_edit_url}}"}, {Action: "click", OnceKey: "{{request_id}}", ExpectedEffect: "immediate_external_commit", ConfirmConsequence: "immediate_external_commit", Locator: actorLocator{Text: "Publish", Exact: true, SOMOnly: true}}}}
			source := actorOperation{Recovery: policy, Steps: []actorStep{{Action: "goto", URL: "{{creator_url}}"}, {Action: "click", Locator: actorLocator{Text: "Image", Exact: true, SOMOnly: true}}, {Action: "wait_for", Conditions: []actorWaitCondition{{Type: "selector_present", Selector: ".old-wrapper"}}}, {Action: "click", OnceKey: "{{request_id}}", ExpectedEffect: "immediate_external_commit", ConfirmConsequence: "immediate_external_commit", Locator: actorLocator{Text: "Publish", Exact: true, SOMOnly: true}}}}
			def := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Browser: actorBrowser{ContextID: "ctx"}, Defaults: map[string]any{"creator_url": "https://example.com/c", "expected_creator_identity": "c", "tier_labels": []any{}}, Operations: map[string]actorOperation{"create": source, "inspect_draft": inspect, "publish_draft": publish}}
			b, _ := json.Marshal(def)
			var fixture map[string]any
			json.Unmarshal(b, &fixture)
			rec := saveFixtureActor(t, ctx, a, fixture)
			inputs := map[string]any{"request_id": "original-request", "post_title": "approved", "post_body": "exact body", "audience": "public", "tier_labels": []any{}, "post_id": "42", "post_edit_url": "https://example.com/c/posts/saved-42/edit"}
			q, err := a.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "inspect_draft", "input": inputs})
			if err != nil {
				t.Fatal(err)
			}
			queued, _ := claimActorRun(ctx)
			a.executeActorRun(context.Background(), ctx, queued)
			checkID := q.(map[string]any)["run_id"].(int64)
			checked, _ := getActorRun(ctx, checkID)
			if checked["status"] != "completed" {
				t.Fatal(checked["error"])
			}
			delete(inputs, "post_id")
			delete(inputs, "post_edit_url")
			originalInput, _ := json.Marshal(map[string]any{"input": inputs, "operation": "create"})
			selected, _ := selectOperation(rec.Definition, "create")
			snapshot, _ := json.Marshal(selected)
			old, err := newActorExecution(context.Background(), a, ctx, &actorQueuedRun{InputJSON: string(originalInput), DefinitionSnapshot: string(snapshot)})
			if err != nil {
				t.Fatal(err)
			}
			original := map[string]any{"status": "failed", "completed_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), "output": map[string]any{"current_url": "https://example.com/c/posts/42/edit", "execution_progress": map[string]any{"last_started_step": 2, "commit_attempted": false}, "media": []any{map[string]any{"uploaded": true, "filename": "first.png", "size_bytes": float64(10)}, map[string]any{"uploaded": true, "filename": "second.png", "size_bytes": float64(20)}}}}
			output := mapFromAny(checked["output"])
			ev := mapFromAny(output["document_evidence"])
			switch mode {
			case "wrong_id":
				ev["post_id"] = "99"
			case "extra":
				ev["attachment_ids"] = []any{"a", "b", "c"}
			case "reordered":
				ev["attachment_filenames"] = []any{"second.png", "first.png"}
			case "wrong_size":
				ev["attachment_sizes"] = []any{float64(11), float64(20)}
			case "not_ready":
				ev["attachment_states"] = []any{"uploading", "ready"}
			case "wrong_copy":
				old.variables["post_body"] = "changed"
			case "wrong_context":
				old.definition.Browser.ContextID = "other"
			case "stale":
				ctx.AppDB().Exec(`UPDATE actors_runs SET completed_at=? WHERE id=?`, time.Now().Add(-time.Hour), checkID)
			case "commit_attempted":
				mapFromAny(mapFromAny(original["output"])["execution_progress"])["commit_attempted"] = true
			case "unknown_click":
				old.definition.Steps[1].Locator.Text = "unknown"
			case "unsafe_resume":
				bad := rec.Definition.Operations["publish_draft"]
				bad.Steps = append(bad.Steps, actorStep{Action: "fill"})
				rec.Definition.Operations["publish_draft"] = bad
			}
			if mode == "wrong_id" || mode == "extra" || mode == "reordered" || mode == "wrong_size" || mode == "not_ready" {
				outRaw, _ := json.Marshal(output)
				ctx.AppDB().Exec(`UPDATE actors_runs SET output_json=? WHERE id=?`, string(outRaw), checkID)
			}
			result, resumed, binding, err := a.prepareExistingRecovery(ctx, map[string]any{"resource_check_run_id": checkID}, rec, original, old, selected, originalInput)
			if mode != "good" {
				if err == nil {
					t.Fatal("unsafe recovery accepted", mode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var recovered map[string]any
			json.Unmarshal(result, &recovered)
			ri := mapFromAny(recovered["input"])
			for k, v := range inputs {
				if !reflect.DeepEqual(ri[k], v) {
					t.Fatal("original input changed", k)
				}
			}
			if recovered["operation"] != "create" || binding["resource_id"] != "42" || !resumed.RecoveryOnly {
				t.Fatal("identity/effect scope lost", recovered, binding)
			}
			if _, _, _, err = enqueueActorSnapshot(ctx, rec.ID, rec.Revision, string(result), mustJSONString(resumed), nil); err == nil {
				t.Fatal("recovery-only operation queued without recovery provenance")
			}
		})
	}
}
func mustJSONString(v any) string { b, _ := json.Marshal(v); return string(b) }
func TestReferenceChainPublicAndPaid(t *testing.T) {
	d := &actorJSONDocument{Selector: "pre", Fields: map[string]actorJSONField{"tiers": {Path: "/rules", Select: "/attributes/title", Required: true, Resolve: &actorJSONResolve{Pool: "/included", Keys: []string{"/id", "/type"}, Through: []string{"/relationships/tier/data"}, DropNull: true}}}}
	raw := `{"rules":[{"id":"p","type":"rule"},{"id":"r","type":"rule"}],"included":[{"id":"r","type":"rule","relationships":{"tier":{"data":{"id":"t","type":"reward"}}}},{"id":"t","type":"reward","attributes":{"title":"Creator chosen tier"}},{"id":"p","type":"rule","relationships":{"tier":{"data":null}}}]}`
	out, err := extractJSONDocument(d, raw)
	if err != nil || !reflect.DeepEqual(out["tiers"], []any{"Creator chosen tier"}) {
		t.Fatal(out, err)
	}
	_, err = extractJSONDocument(d, strings.Replace(raw, `"id":"t","type":"reward","attributes"`, `"id":"missing","type":"reward","attributes"`, 1))
	if err == nil {
		t.Fatal("unresolved tier accepted")
	}
}

func TestExistingRecoveryChecksFreshPageBeforeDispatch(t *testing.T) {
	p := newFakePlatform()
	clicks := 0
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		if tool == "computer.computer_use" {
			if in["action"] == "screenshot" {
				return map[string]any{"current_url": "https://example.com/posts/99/edit", "som_revision": 1, "som": []any{map[string]any{"id": "publish", "tag": "button", "text": "Publish", "accessible_name": "Publish", "effect": "immediate_external_commit"}}}
			}
			if in["action"] == "click" {
				clicks++
			}
		}
		return nil
	}
	ctx, a := newTestCtx(t, p)
	trigger := mustJSONString(map[string]any{"kind": "recovery", "existing_resource": map[string]any{"resource_id": "42", "resource_url": "https://example.com/posts/42/edit", "id_field": "post_id", "ids_field": "attachment_ids", "attachment_ids": []any{"a"}}})
	e := actorExecution{app: a, ctx: ctx, workerCtx: context.Background(), run: &actorQueuedRun{TriggerJSON: trigger}, session: &browserSession{SessionID: "fixture"}, currentURL: "https://example.com/posts/42/edit", lastValues: map[string]any{"post_id": "42", "attachment_ids": []any{"a"}}}
	err := e.click(actorStep{Action: "click", Locator: actorLocator{Text: "Publish", Exact: true, SOMOnly: true}, ExpectedEffect: "immediate_external_commit"})
	if err == nil || clicks != 0 {
		t.Fatal("page switch was not detected before dispatch", clicks, err)
	}
}

func fixtureRecoveryBindings() actorRecoveryBindings {
	return actorRecoveryBindings{ResourceIDField: "post_id", ResourceIDInput: "post_id", ResourceURLInput: "post_edit_url", CreatorURLInput: "creator_url", ExpectedIDsInput: "expected_attachment_ids", ExpectedNamesInput: "expected_attachment_filenames", ExpectedSizesInput: "expected_attachment_sizes", IDsField: "attachment_ids", NamesField: "attachment_filenames", SizesField: "attachment_sizes", StatesField: "attachment_states", TypesField: "attachment_types", ReadyValue: "ready", TypeValue: "image", DuplicateIDField: "id", DuplicateURLField: "edit_url", DuplicateStatusField: "status", DraftStatus: "draft", StateAssertions: map[string]actorAssertion{"is_draft": {EqualsExact: true}, "is_unscheduled": {EqualsExact: true}}}
}
