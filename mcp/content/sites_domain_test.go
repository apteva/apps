package main

import (
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type domainListPlatform struct {
	tk.BasePlatformClient
	gotApp   string
	gotTool  string
	gotInput map[string]any
}

func (p *domainListPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.gotApp = appName
	p.gotTool = tool
	p.gotInput = input
	payload := map[string]any{
		"domains": []map[string]any{
			{"id": 2, "name": "Example.co.uk.", "dns_provider_slug": "porkbun"},
			{"id": 1, "name": "example.com"},
		},
	}
	b, _ := json.Marshal(payload)
	return json.Unmarshal(b, out)
}

func TestListManagedDomainsNormalizesSortsAndThreadsProject(t *testing.T) {
	pf := &domainListPlatform{}
	ctx := sdk.NewAppCtxForTest(nil, nil, nil, pf, nil)

	got, err := listManagedDomains(ctx, "proj-1")
	if err != nil {
		t.Fatal(err)
	}
	if pf.gotApp != "domains" || pf.gotTool != "domain_list" {
		t.Fatalf("called %s.%s, want domains.domain_list", pf.gotApp, pf.gotTool)
	}
	if pf.gotInput["_project_id"] != "proj-1" {
		t.Fatalf("_project_id = %v, want proj-1", pf.gotInput["_project_id"])
	}
	if len(got) != 2 || got[0].Name != "example.co.uk" || got[1].Name != "example.com" {
		t.Fatalf("domains = %#v, want normalized sorted names", got)
	}
}

func TestMatchManagedDomainUsesLongestRegisteredSuffix(t *testing.T) {
	domains := []DomainOption{
		{Name: "example.com"},
		{Name: "example.co.uk"},
	}
	apex, sub, ok := matchManagedDomain("docs.Example.co.uk.", domains)
	if !ok {
		t.Fatal("expected match")
	}
	if apex != "example.co.uk" || sub != "docs" {
		t.Fatalf("apex/sub = %q/%q, want example.co.uk/docs", apex, sub)
	}
}

func TestMatchManagedDomainRoot(t *testing.T) {
	apex, sub, ok := matchManagedDomain("example.com", []DomainOption{{Name: "example.com"}})
	if !ok {
		t.Fatal("expected match")
	}
	if apex != "example.com" || sub != "" {
		t.Fatalf("apex/sub = %q/%q, want example.com/empty", apex, sub)
	}
}

func TestSuggestDNSRecordRootHostnameLeavesManualIP(t *testing.T) {
	got := suggestDNSRecord("", "agents.example.com")
	if got.Type != "A" || got.Value != "" {
		t.Fatalf("record = %#v, want apex A with empty manual IP value", got)
	}
}

func TestInferDNSTargetKeepsPublicURLIP(t *testing.T) {
	t.Setenv("APTEVA_PUBLIC_URL", "https://91.99.117.197:5280/base")
	if got := inferDNSTarget(nil); got != "91.99.117.197" {
		t.Fatalf("inferDNSTarget() = %q, want 91.99.117.197", got)
	}
}
