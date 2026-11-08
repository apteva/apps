package main

import (
	"slices"
	"testing"
)

// The platform permits project_id "*" only for tools that never write,
// regardless of their arguments. PDF rendering is excluded because it can
// upload to Storage; obligation creation can insert or link a bill.
func readOnlyToolNames() []string {
	return []string{
		"bill_payments_list", "bills_get", "bills_get_obligation", "bills_search",
		"vendors_get", "vendors_get_context", "vendors_search",
	}
}

func TestProjectGrants_ReadOnlyManifest(t *testing.T) {
	app := &App{}
	var got []string
	for _, tool := range app.Manifest().Provides.MCPTools {
		if tool.ReadOnly {
			got = append(got, tool.Name)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, readOnlyToolNames()) {
		t.Fatalf("read-only tools = %v, want %v", got, readOnlyToolNames())
	}
	for _, tool := range app.MCPTools() {
		props := tool.InputSchema["properties"].(map[string]any)
		if _, ok := props["project_id"]; ok {
			t.Errorf("%s declares project_id, which the platform reserves for grant routing", tool.Name)
		}
	}
}
