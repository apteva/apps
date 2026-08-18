package main

import (
	"os"
	"sort"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "webinars" {
		t.Errorf("manifest.Name=%q, want webinars", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if len(m.Provides.MCPTools) != 16 {
		t.Errorf("expected 16 MCP tools, got %d", len(m.Provides.MCPTools))
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Errorf("manifest.DB.Migrations missing")
	}
	gotScopes := map[string]bool{}
	for _, s := range m.Scopes {
		gotScopes[string(s)] = true
	}
	for _, want := range []string{"project", "global"} {
		if !gotScopes[want] {
			t.Errorf("manifest missing scope %q", want)
		}
	}
}

func TestMCPTools_ManifestMatchesHandlers(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	declared := map[string]bool{}
	for _, t := range m.Provides.MCPTools {
		declared[t.Name] = true
	}
	implemented := map[string]bool{}
	for _, t := range app.MCPTools() {
		implemented[t.Name] = true
	}
	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest declares tool %q but no handler implements it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("handler implements %q but manifest doesn't declare it", name)
		}
	}
}

// TestManifests_EmbeddedMatchesFile guards the two-manifest drift this
// app ships with: main.go carries an embedded copy for the runtime while
// apteva.yaml is what the platform reads at install time. Version skew
// between them is a known release-process failure here, and the embedded
// copy previously omitted config_schema entirely — meaning that if the
// platform builds its settings UI from the runtime manifest, none of
// this app's eight settings were configurable.
func TestManifests_EmbeddedMatchesFile(t *testing.T) {
	embedded := (&App{}).Manifest()

	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	onDisk, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}

	for _, tc := range []struct{ field, got, want string }{
		{"name", embedded.Name, onDisk.Name},
		{"version", embedded.Version, onDisk.Version},
		{"display_name", embedded.DisplayName, onDisk.DisplayName},
		{"homepage", embedded.Homepage, onDisk.Homepage},
		{"icon", embedded.Icon, onDisk.Icon},
		{"icon_style", embedded.IconStyle, onDisk.IconStyle},
		{"min_apteva_version", embedded.MinAptevaVersion, onDisk.MinAptevaVersion},
		{"upgrade_policy", string(embedded.UpgradePolicy), string(onDisk.UpgradePolicy)},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: embedded=%q, apteva.yaml=%q", tc.field, tc.got, tc.want)
		}
	}
	if embedded.Version == "" {
		t.Error("version must be set in both manifests")
	}

	if got, want := strings.Join(sortedStrings(embedded.Tags), ","),
		strings.Join(sortedStrings(onDisk.Tags), ","); got != want {
		t.Errorf("tags: embedded=[%s], apteva.yaml=[%s]", got, want)
	}
	if got, want := strings.Join(scopeNames(embedded.Scopes), ","),
		strings.Join(scopeNames(onDisk.Scopes), ","); got != want {
		t.Errorf("scopes: embedded=[%s], apteva.yaml=[%s]", got, want)
	}
	if got, want := strings.Join(toolNames(embedded.Provides.MCPTools), ","),
		strings.Join(toolNames(onDisk.Provides.MCPTools), ","); got != want {
		t.Errorf("mcp_tools drift:\n  embedded=%s\n  apteva.yaml=%s", got, want)
	}
	if got, want := strings.Join(configNames(embedded.ConfigSchema), ","),
		strings.Join(configNames(onDisk.ConfigSchema), ","); got != want {
		t.Errorf("config_schema drift:\n  embedded=%s\n  apteva.yaml=%s", got, want)
	}
	if len(embedded.ConfigSchema) == 0 {
		t.Error("embedded manifest declares no config_schema, so nothing is configurable at runtime")
	}
	if embedded.DB == nil || onDisk.DB == nil ||
		embedded.DB.Path != onDisk.DB.Path || embedded.DB.Migrations != onDisk.DB.Migrations {
		t.Errorf("db block drift: embedded=%+v apteva.yaml=%+v", embedded.DB, onDisk.DB)
	}
	if got, want := strings.Join(requiredAppNames(embedded), ","),
		strings.Join(requiredAppNames(*onDisk), ","); got != want {
		t.Errorf("requires.apps drift: embedded=[%s], apteva.yaml=[%s]", got, want)
	}
}

// TestMCPTools_SchemaCoversDocumentedArgs is the drift check the
// name-only comparison missed: webinars_create_slot read a
// `duration_minutes` argument that appeared in neither the InputSchema
// nor either manifest, so no agent could discover it. Every argument a
// tool documents in its "Args:" clause must exist in its InputSchema,
// and every InputSchema property must be documented.
func TestMCPTools_SchemaCoversDocumentedArgs(t *testing.T) {
	for _, tool := range (&App{}).MCPTools() {
		documented := parseDocumentedArgs(tool.Description)
		if len(documented) == 0 {
			t.Errorf("%s: description has no `Args:` clause", tool.Name)
			continue
		}
		props, _ := tool.InputSchema["properties"].(map[string]any)
		if props == nil {
			t.Errorf("%s: InputSchema has no properties", tool.Name)
			continue
		}
		for _, arg := range documented {
			if _, ok := props[arg]; !ok {
				t.Errorf("%s: documents arg %q that the InputSchema doesn't declare", tool.Name, arg)
			}
		}
		documentedSet := map[string]bool{}
		for _, arg := range documented {
			documentedSet[arg] = true
		}
		for name := range props {
			if !documentedSet[name] {
				t.Errorf("%s: InputSchema declares %q but the description doesn't document it", tool.Name, name)
			}
		}

		// Required args must be declared properties too.
		for _, req := range requiredNames(tool.InputSchema) {
			if _, ok := props[req]; !ok {
				t.Errorf("%s: required arg %q is not a declared property", tool.Name, req)
			}
		}
	}
}

// ─── helpers ──────────────────────────────────────────────────────

// parseDocumentedArgs pulls the argument names out of a tool
// description's trailing "Args: a, b? (note), c" clause. Commas inside
// parentheses are part of a note, not separators.
func parseDocumentedArgs(desc string) []string {
	idx := strings.LastIndex(desc, "Args:")
	if idx < 0 {
		return nil
	}
	clause := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(desc[idx+len("Args:"):]), "."))
	out := []string{}
	for _, piece := range splitTopLevelCommas(clause) {
		name := strings.TrimSpace(piece)
		name = strings.TrimSuffix(strings.SplitN(name, " ", 2)[0], "?")
		name = strings.TrimSuffix(name, ".")
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func splitTopLevelCommas(s string) []string {
	out := []string{}
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

func requiredNames(schema map[string]any) []string {
	raw, _ := schema["required"].([]string)
	return raw
}

func toolNames(tools []sdk.MCPToolSpec) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return sortedStrings(out)
}

func configNames(fields []sdk.ConfigField) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Name)
	}
	return sortedStrings(out)
}

func scopeNames(scopes []sdk.Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, string(s))
	}
	return sortedStrings(out)
}

func requiredAppNames(m sdk.Manifest) []string {
	out := make([]string, 0, len(m.Requires.Apps))
	for _, a := range m.Requires.Apps {
		out = append(out, a.Name+"@"+a.Version)
	}
	return sortedStrings(out)
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
