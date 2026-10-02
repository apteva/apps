package main

import (
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"gopkg.in/yaml.v3"
)

// Tier 1 — the embedded manifest must always parse and round-trip
// the surface the binary actually exposes. If this drifts the binary
// won't survive sdk.Run's ValidateManifest at boot.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "code" {
		t.Errorf("manifest.Name=%q, want code", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if m.Runtime.Source == nil || m.Runtime.Source.Ref != "code/v"+m.Version {
		t.Fatalf("runtime source must point to code/v%s; installing another ref builds a different release", m.Version)
	}
	if len(m.Provides.MCPTools) != 73 {
		t.Errorf("expected 73 MCP tools in manifest, got %d", len(m.Provides.MCPTools))
	}
	if len(m.Provides.UIComponents) != 3 {
		t.Errorf("expected 3 UI components in manifest, got %d", len(m.Provides.UIComponents))
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
	workspacesOptional := false
	for _, dependency := range m.Requires.Apps {
		if dependency.Name == "workspaces" && dependency.Version == ">=0.6.1" && dependency.Optional {
			workspacesOptional = true
		}
	}
	if !workspacesOptional {
		t.Error("Workspaces >=0.6.1 must remain an optional dependency")
	}
}

func TestCodeSkillManifestAndSourceAgree(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	if len(manifest.Provides.Skills) != 1 {
		t.Fatalf("expected one exported Code skill, got %d", len(manifest.Provides.Skills))
	}
	skill := manifest.Provides.Skills[0]
	if skill.Name != "how-to-use-code" || skill.Command != "/code" || skill.Description == "" || skill.BodyFile != "skills/how-to-use-code.md" || skill.Body != "" {
		t.Fatalf("incomplete exported skill: %+v", skill)
	}
	raw, err := os.ReadFile(skill.BodyFile)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(raw), "---", 3)
	if len(parts) != 3 || strings.TrimSpace(parts[0]) != "" || strings.TrimSpace(parts[2]) == "" {
		t.Fatal("skill requires YAML frontmatter and a non-empty body")
	}
	var front struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Command     string   `yaml:"command"`
		Triggers    []string `yaml:"triggers"`
	}
	if err := yaml.Unmarshal([]byte(parts[1]), &front); err != nil {
		t.Fatal(err)
	}
	if front.Name != skill.Name || front.Command != skill.Command || front.Description == "" {
		t.Fatal("skill frontmatter must preserve the manifest's install identity and command")
	}
	tools := map[string]bool{}
	for _, tool := range app.MCPTools() {
		tools[tool.Name] = true
	}
	for _, trigger := range front.Triggers {
		if (strings.HasPrefix(trigger, "repos_") || strings.HasPrefix(trigger, "code_")) && !tools[trigger] {
			t.Errorf("skill references unavailable tool %q", trigger)
		}
	}
}

func TestUIComponents_CompleteAndDiskManifestMatches(t *testing.T) {
	embedded := (&App{}).Manifest()
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Version != embedded.Version {
		t.Fatalf("disk version %q does not match embedded version %q", disk.Version, embedded.Version)
	}

	want := map[string]string{
		"repository-card":  "/ui/RepositoryCard.mjs",
		"source-file-card": "/ui/SourceFileCard.mjs",
		"issue-card":       "/ui/IssueCard.mjs",
	}
	for _, manifest := range []sdk.Manifest{embedded, *disk} {
		if len(manifest.Provides.UIComponents) != len(want) {
			t.Fatalf("manifest has %d components, want %d", len(manifest.Provides.UIComponents), len(want))
		}
		for _, component := range manifest.Provides.UIComponents {
			entry, ok := want[component.Name]
			if !ok {
				t.Errorf("unexpected component %q", component.Name)
				continue
			}
			if component.Entry != entry {
				t.Errorf("component %q entry=%q, want %q", component.Name, component.Entry, entry)
			}
			if _, err := os.Stat("." + component.Entry); err != nil {
				t.Errorf("component %q bundle is missing: %v", component.Name, err)
			}
			if len(component.Slots) != 1 || component.Slots[0] != "chat.message_attachment" {
				t.Errorf("component %q has invalid slots: %v", component.Name, component.Slots)
			}
			if len(component.PropsSchema) == 0 || component.PropsSchema["required"] == nil {
				t.Errorf("component %q has no required props schema", component.Name)
			}
			if component.PreviewProps["preview"] != true {
				t.Errorf("component %q has no live preview props", component.Name)
			}
		}
	}
}

// The manifest's mcp_tools list and the App.MCPTools() handler list
// must agree on count and names. A common mistake is adding a tool to
// one and forgetting the other; this test catches it before boot.
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
			t.Errorf("manifest declares %q but no handler implements it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("handler implements %q but manifest doesn't declare it", name)
		}
	}
}

// Every tool the editing surface relies on must be present — guards
// against silent removal during refactors.
func TestMCPTools_EditingSurfaceComplete(t *testing.T) {
	app := &App{}
	got := map[string]bool{}
	for _, tool := range app.MCPTools() {
		got[tool.Name] = true
	}
	must := []string{
		"repos_list", "repos_create", "repos_get", "repos_archive", "repos_set_deploy_hints", "repos_set_workspace_image", "repos_import_zip",
		"repos_run_command", "repos_workspace_changes", "repos_workspace_apply", "repos_workspace_destroy",
		"code_list_files", "code_glob", "code_grep",
		"code_read_file", "code_read_excerpt", "code_file_outline",
		"code_write_file", "code_import_file", "code_apply_patch", "code_edit_file", "code_multi_edit",
		"code_rename_path", "code_delete_file",
		"issues_list", "issues_search", "issues_get", "issues_create", "issues_update",
		"issues_claim", "issues_release", "issues_comment", "issues_close", "issues_reopen", "issues_link_path",
		"repos_git_import", "repos_git_connect", "repos_git_status", "repos_git_fetch",
		"repos_git_pull", "repos_git_commit", "repos_git_push", "repos_git_diff", "repos_git_log",
		"repos_git_branches", "repos_git_branch_create", "repos_git_switch",
		"repos_version_status", "repos_checkpoint", "repos_history", "repos_diff", "repos_branch_create",
		"repos_branch_switch", "repos_tag_create", "repos_tags_list", "repos_restore", "repos_export_ref",
	}
	for _, name := range must {
		if !got[name] {
			t.Errorf("missing required tool: %s", name)
		}
	}
}

// Every tool must declare a non-empty input schema with required
// fields where the handler logic depends on them. Catches the
// "schemaObject(props, nil)" copy-paste mistake.
func TestMCPTools_AllHaveSchemas(t *testing.T) {
	app := &App{}
	for _, tool := range app.MCPTools() {
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no InputSchema", tool.Name)
			continue
		}
		props, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Errorf("tool %q has empty/missing properties", tool.Name)
		}
		if tool.Handler == nil && tool.HandlerCtx == nil {
			t.Errorf("tool %q has no handler", tool.Name)
		}
	}
}

func TestApplyPatchToolDocumentsEverySupportedFormat(t *testing.T) {
	var apply sdk.Tool
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "code_apply_patch" {
			apply = tool
			break
		}
	}
	props, ok := apply.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("code_apply_patch properties missing")
	}
	patchSchema, ok := props["patch"].(map[string]any)
	if !ok {
		t.Fatal("code_apply_patch patch schema missing")
	}
	description, _ := patchSchema["description"].(string)
	for _, text := range []string{"--- a/file.txt", "+++ b/file.txt", "*** Begin Patch", "*** Update File: file.txt", "*** End Patch"} {
		if !strings.Contains(apply.Description, text) || !strings.Contains(description, text) {
			t.Errorf("copyable example %q must appear in both tool and patch-parameter descriptions", text)
		}
	}
}
