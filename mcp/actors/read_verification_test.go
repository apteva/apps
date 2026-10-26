package main

import (
	"context"
	"encoding/json"
	"golang.org/x/net/html"
	"strings"
	"testing"
)

func TestReadVerificationRejectsWrongAndAmbiguousFilters(t *testing.T) {
	for _, mode := range []string{"correct", "wrong", "ambiguous", "missing"} {
		t.Run(mode, func(t *testing.T) {
			field := actorField{Selector: `#search select[name="country"] option[selected]`, Attribute: "value", Required: true}
			v := actorReadView{Name: "search", Verification: &actorReadVerification{Fields: map[string]actorField{"country": field}, Assertions: map[string]actorAssertion{"country": {EqualsExact: "ES"}}}}
			if err := validateReadVerification(v.Verification); err != nil {
				t.Fatal(err)
			}
			markup := `<body><form id="search" style="margin-top:4000px"><select name="country"><option value="ES" selected>Spain</option></select></form></body>`
			switch mode {
			case "wrong":
				markup = strings.ReplaceAll(markup, `value="ES"`, `value="GB"`)
			case "ambiguous":
				markup = strings.ReplaceAll(markup, `</select>`, `<option value="GB" selected>UK</option></select>`)
			case "missing":
				markup = `<body></body>`
			}
			root, _ := html.Parse(strings.NewReader(markup))
			coverage := &actorViewCoverage{Name: "search"}
			e := &actorExecution{currentURL: "https://example.com/search", coverage: &actorReadCoverage{Views: []*actorViewCoverage{coverage}}, lastValues: map[string]any{"previous": "kept"}}
			err := e.verifyReadPage(v, root)
			if (err == nil) != (mode == "correct") {
				t.Fatalf("mode %s error %v", mode, err)
			}
			if e.lastValues["previous"] != "kept" {
				t.Fatal("verification changed previous values")
			}
			if mode == "correct" && (!coverage.Verification.Verified || coverage.Verification.RequiresVisible || coverage.Verification.Values["country"] != "ES") {
				t.Fatalf("missing evidence: %#v", coverage.Verification)
			}
		})
	}
}

func TestReadVerificationPrecedesCandidateEmission(t *testing.T) {
	for _, mode := range []string{"valid", "wrong_country", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			p := newFakePlatform()
			current := ""
			p.readViewsResponse = func(tool string, args map[string]any) map[string]any {
				if tool == "computer.browser_open" {
					current = stringFromAny(args["url"])
				}
				if tool == "computer.browser_extract" {
					h := readFixtureHTML(1, 2)
					options := `<option value="ES" selected>Spain</option>`
					if mode == "wrong_country" {
						options = `<option value="GB" selected>UK</option>`
					}
					if mode == "ambiguous" {
						options += `<option value="GB" selected>UK</option>`
					}
					h = strings.Replace(h, "</body>", `<select id="country">`+options+`</select><input id="inactive" value="London"></body>`, 1)
					return map[string]any{"html": h, "current_url": current, "rendered": true}
				}
				return nil
			}
			ctx, app := newTestCtx(t, p)
			c := fixtureReadViews()
			c.Views = c.Views[:1]
			c.Views[0].UseEntryPage = true
			c.Views[0].URLPattern = `^https://example.com/library$`
			c.AllowPartial = true
			c.Views[0].Pagination.MaxPages = 1
			c.Views[0].Verification = &actorReadVerification{Fields: map[string]actorField{"country": {Selector: "#country option[selected]", Attribute: "value", Required: true}, "location": {Selector: "#inactive", Attribute: "value"}}, Assertions: map[string]actorAssertion{"country": {EqualsExact: "ES"}}, InactiveFields: []string{"location"}}
			b, _ := json.Marshal(c)
			var conf map[string]any
			json.Unmarshal(b, &conf)
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"}, "limits": map[string]any{"step_retries": 0}, "steps": []any{map[string]any{"action": "inspect_views", "read_views": conf}}})
			if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
				t.Fatal(err)
			}
			q, err := claimActorRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			app.executeActorRun(context.Background(), ctx, q)
			r, _ := getActorRun(ctx, q.ID)
			out := mapFromAny(r["output"])
			if mode != "valid" {
				if r["status"] != "failed" || intFromAny(out["item_count"]) != 0 {
					t.Fatalf("unverified records emitted: %#v", r)
				}
			} else {
				if intFromAny(out["item_count"]) != 2 {
					t.Fatal(out)
				}
				cov := mapFromAny(out["coverage"])
				views := cov["views"].([]any)
				evidence := mapFromAny(mapFromAny(views[0])["verification"])
				if mapFromAny(evidence["inactive_values"])["location"] != "London" || mapFromAny(evidence["applied_values"])["location"] != nil {
					t.Fatalf("inactive location reported as applied: %#v", evidence)
				}
			}
		})
	}
}
