package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"gopkg.in/yaml.v3"
)

func TestAgentPluginMetadataMatchesNativeManifest(t *testing.T) {
	document, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var plugin struct {
		Schema     string `json:"$schema"`
		Name       string `json:"name"`
		Version    string `json:"version"`
		Extensions struct {
			Apteva struct {
				Manifest string `json:"manifest"`
			} `json:"com.apteva"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(document, &plugin); err != nil {
		t.Fatal(err)
	}
	if plugin.Schema != "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json" {
		t.Fatalf("schema=%q", plugin.Schema)
	}
	if plugin.Extensions.Apteva.Manifest != "apteva.yaml" {
		t.Fatalf("com.apteva.manifest=%q", plugin.Extensions.Apteva.Manifest)
	}

	nativeDocument, err := os.ReadFile(plugin.Extensions.Apteva.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	native, err := sdk.ParseManifest(nativeDocument)
	if err != nil {
		t.Fatal(err)
	}
	if plugin.Name != native.Name || plugin.Version != native.Version {
		t.Fatalf("plugin identity %s@%s != native identity %s@%s",
			plugin.Name, plugin.Version, native.Name, native.Version)
	}
	if len(native.Provides.Skills) != 1 || native.Provides.Skills[0].BodyFile != "skills/crm/SKILL.md" {
		t.Fatalf("native skills=%+v", native.Provides.Skills)
	}
	embedded := (&App{}).Manifest()
	if len(embedded.Provides.Skills) != 1 || embedded.Provides.Skills[0].BodyFile != "skills/crm/SKILL.md" {
		t.Fatalf("embedded skills=%+v", embedded.Provides.Skills)
	}
}

func TestPortableCRMSkillIsCanonicalAgentSkill(t *testing.T) {
	document, err := os.ReadFile("skills/crm/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(document))
	if !strings.HasPrefix(text, "---\n") {
		t.Fatal("SKILL.md has no YAML frontmatter")
	}
	parts := strings.SplitN(strings.TrimPrefix(text, "---\n"), "\n---\n", 2)
	if len(parts) != 2 {
		t.Fatal("SKILL.md has no closing frontmatter delimiter")
	}
	var front struct {
		Name        string            `yaml:"name"`
		Description string            `yaml:"description"`
		Metadata    map[string]string `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(parts[0]), &front); err != nil {
		t.Fatal(err)
	}
	if front.Name != "crm" || len(front.Description) == 0 || len(front.Description) > 1024 {
		t.Fatalf("frontmatter=%+v", front)
	}
	if strings.TrimSpace(parts[1]) == "" {
		t.Fatal("SKILL.md body is empty")
	}
	for _, requiredGuidance := range []string{
		"contacts_get", "contacts_get_context", "collection_info", "truncated",
		"template_id: 0", "omit", "unused optional field",
	} {
		if !strings.Contains(parts[1], requiredGuidance) {
			t.Errorf("SKILL.md missing contact-read guidance %q", requiredGuidance)
		}
	}
	// CRM's authenticated MCP endpoint is installation- and project-scoped.
	// Do not publish a fake portable URL: Agent Plugins permits a skills-only
	// package, while Apteva continues to expose the same tools through its
	// native app bridge selected by extensions.com.apteva.manifest.
	if _, err := os.Stat("mcp.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected portable mcp.json: %v", err)
	}
}
