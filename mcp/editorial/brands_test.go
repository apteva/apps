package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestBrandsLifecycleAndLegacyContent(t *testing.T) {
	a, c := testApp(t, nil)
	legacy := create(t, a, c, map[string]any{"title": "Existing content"})
	if _, e := c.AppDB().Exec("UPDATE editorial_items SET data=json_remove(data,'$.brand_id') WHERE id=?", legacy.ID); e != nil {
		t.Fatal(e)
	}
	brands := []Brand{{ID: "acme", Name: "Acme", Color: "#123456"}, {ID: "orbit", Name: "Orbit"}}
	v, e := a.dispatch(c, "settings_update", map[string]any{"revision": 0, "brands": brands})
	if e != nil {
		t.Fatal(e)
	}
	settings := v.(Settings)
	branded := create(t, a, c, map[string]any{"title": "Acme launch", "brand_id": "acme", "campaign": "Launch", "approval": "approved", "reviewer": "Editor"})
	create(t, a, c, map[string]any{"title": "Orbit launch", "brand_id": "orbit", "campaign": "Launch"})
	if _, e = a.dispatch(c.WithProject("beta"), "items_create", map[string]any{"title": "Wrong project", "brand_id": "acme"}); e == nil {
		t.Fatal("cross-project brand accepted")
	}
	for filter, want := range map[string]int{"": 3, "acme": 1, "orbit": 1, "unassigned": 1} {
		v, e := a.dispatch(c, "items_list", map[string]any{"brand_id": filter})
		if e != nil {
			t.Fatal(e)
		}
		if v.(map[string]any)["total"].(int) != want {
			t.Fatal(filter, v)
		}
	}
	brands[0].Name = "Acme renamed"
	v, e = a.dispatch(c, "settings_update", map[string]any{"revision": settings.Revision, "brands": brands})
	if e != nil {
		t.Fatal(e)
	}
	settings = v.(Settings)
	got, _ := readItem(c.AppDB(), "alpha", branded.ID)
	if got.BrandID != "acme" {
		t.Fatal("rename lost assignment")
	}
	if _, e = a.dispatch(c, "settings_update", map[string]any{"revision": settings.Revision - 1, "brands": brands}); !errors.Is(e, errConflict) {
		t.Fatal("stale settings accepted", e)
	}
	v, e = a.dispatch(c, "items_update", map[string]any{"id": branded.ID, "revision": branded.Revision, "patch": map[string]any{"archived": true}})
	if e != nil {
		t.Fatal(e)
	}
	branded = v.(Item)
	if _, e = a.dispatch(c, "settings_update", map[string]any{"revision": settings.Revision, "brands": brands[1:]}); e == nil {
		t.Fatal("removed referenced archived brand")
	}
	v, e = a.dispatch(c, "items_update", map[string]any{"id": branded.ID, "revision": branded.Revision, "patch": map[string]any{"brand_id": ""}})
	if e != nil {
		t.Fatal(e)
	}
	if v.(Item).Approval != "pending" {
		t.Fatal("brand reassignment retained approval")
	}
	if _, e = a.dispatch(c, "settings_update", map[string]any{"revision": settings.Revision, "brands": brands[1:]}); e != nil {
		t.Fatal(e)
	}
	// HTTP and agent tools share filtering and project isolation.
	a.ctx = c
	w := httptest.NewRecorder()
	a.http(w, httptest.NewRequest("GET", "/items?brand_id=orbit&project_id=beta", nil))
	var result struct {
		Items []Item
		Total int
	}
	if e = json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if w.Code != 200 || result.Total != 1 || result.Items[0].BrandID != "orbit" {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestBrandValidation(t *testing.T) {
	for name, brands := range map[string][]Brand{
		"duplicate IDs":      {{ID: "a", Name: "A"}, {ID: "a", Name: "B"}},
		"duplicate names":    {{ID: "a", Name: "A"}, {ID: "b", Name: "a"}},
		"reserved ID":        {{ID: "unassigned", Name: "A"}},
		"invalid color":      {{ID: "a", Name: "A", Color: "red"}},
		"invalid logo":       {{ID: "a", Name: "A", LogoURL: "javascript:alert(1)"}},
		"invalid account":    {{ID: "a", Name: "A", SocialAccountIDs: []int64{-1}}},
		"duplicate campaign": {{ID: "a", Name: "A", CampaignIDs: []int64{1, 1}}},
	} {
		t.Run(name, func(t *testing.T) {
			a, c := testApp(t, nil)
			if _, e := a.dispatch(c, "settings_update", map[string]any{"revision": 0, "brands": brands}); e == nil {
				t.Fatal("invalid brands accepted")
			}
		})
	}
}
func TestBrandConnectionFilteringAndRefresh(t *testing.T) {
	p := &platform{bindings: map[string]any{"social": float64(7), "campaigns": float64(8)}}
	a, c := testApp(t, p)
	_, e := a.dispatch(c, "settings_update", map[string]any{"revision": 0, "brands": []Brand{{ID: "acme", Name: "Acme", SocialAccountIDs: []int64{10}, CampaignIDs: []int64{30}}}})
	if e != nil {
		t.Fatal(e)
	}
	i := create(t, a, c, map[string]any{"title": "Sample", "brand_id": "acme"})
	post := func(id, account int) map[string]any {
		return map[string]any{"id": id, "targets": []any{map[string]any{"social_account_id": account}}}
	}
	p.result = map[string]any{"posts": []any{post(21, 10), post(22, 11)}}
	v, e := a.dispatch(c, "integrations", map[string]any{"app": "social", "brand_id": "acme"})
	if e != nil {
		t.Fatal(e)
	}
	if len(v.(map[string]any)["posts"].([]any)) != 1 {
		t.Fatal(v)
	}
	if _, e = a.dispatch(c.WithProject("beta"), "integrations", map[string]any{"app": "social", "brand_id": "acme"}); e == nil {
		t.Fatal("cross-project browse accepted")
	}
	v, e = a.dispatch(c, "releases_create", map[string]any{"item_id": i.ID, "channel": "LinkedIn", "app": "social", "external_id": 21})
	if e != nil {
		t.Fatal(e)
	}
	r := v.(Release)
	v, e = a.dispatch(c, "releases_refresh", map[string]any{"id": r.ID})
	if e != nil {
		t.Fatal(e)
	}
	r = v.(Release)
	p.result = map[string]any{"posts": []any{post(21, 11)}}
	if _, e = a.dispatch(c, "releases_refresh", map[string]any{"id": r.ID}); e == nil {
		t.Fatal("wrong brand snapshot accepted")
	}
	got, _ := readRelease(c.AppDB(), "alpha", r.ID)
	if got.Revision != r.Revision || got.SyncedAt == "" {
		t.Fatal("snapshot lost")
	}
	p.result = map[string]any{"campaigns": []any{map[string]any{"id": 30}, map[string]any{"id": 31}}}
	v, e = a.dispatch(c, "integrations", map[string]any{"app": "campaigns", "brand_id": "acme"})
	if e != nil {
		t.Fatal(e)
	}
	if len(v.(map[string]any)["campaigns"].([]any)) != 1 {
		t.Fatal(v)
	}
	if p.project != "alpha" {
		t.Fatal(p.project)
	}
	for _, call := range p.calls {
		if call != "social/post_list" && call != "campaigns/campaigns_list" {
			t.Fatal("unexpected external action", call)
		}
	}
	b := &Brand{SocialAccountIDs: []int64{10}}
	if brandAllows(b, "social", map[string]any{"targets": []any{map[string]any{"social_account_id": 10}, map[string]any{"social_account_id": 11}}}) {
		t.Fatal("mixed-brand multicast accepted")
	}
}
