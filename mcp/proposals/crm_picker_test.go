package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

type crmPickerPlatform struct {
	sdk.PlatformClient
	bindings map[string]any
	calls    int
	input    map[string]any
}

func (p *crmPickerPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: p.bindings}, nil
}
func (p *crmPickerPlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	p.calls++
	p.input = in
	raw := []byte(`{"contacts":[{"id":42,"first_name":"Nora","last_name":"Wind","primary_email":"nora@example.test","company":"Northwind","attributes":[{"value":"PRIVATE"}]}],"total":21}`)
	return json.Unmarshal(raw, out)
}

func TestCRMPickerUsesOnlyLinkedCRMAndProjectsContactFields(t *testing.T) {
	p := &crmPickerPlatform{bindings: map[string]any{}}
	appCtx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("picker-project"), testkit.WithPlatform(p))
	a := &App{}
	if err := a.OnMount(appCtx); err != nil {
		t.Fatal(err)
	}
	page, err := a.searchCRMContacts(input{Q: "Nora"})
	if err != nil || page.Linked || len(page.Contacts) != 0 || p.calls != 0 {
		t.Fatalf("unlinked search: %+v %v calls %d", page, err, p.calls)
	}
	p.bindings["crm"] = float64(18)
	page, err = a.searchCRMContacts(input{Q: " Nora ", Limit: 20})
	if err != nil || !page.Linked || len(page.Contacts) != 1 || page.NextOffset != 1 {
		t.Fatalf("linked search: %+v %v", page, err)
	}
	if page.Contacts[0].Name != "Nora Wind" || page.Contacts[0].Company != "Northwind" || p.input["q"] != "Nora" || p.input["_project_id"] != "picker-project" {
		t.Fatalf("wrong projection or project-scoped input: %+v, %+v", page, p.input)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "attributes") {
		t.Fatal("search returned unrelated CRM fields")
	}
	if _, err := a.searchCRMContacts(input{Offset: -1}); err == nil {
		t.Fatal("negative offset accepted")
	}
}
