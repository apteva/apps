package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCodemagicAdapterTemplateIsGenericAndValidYAML(t *testing.T) {
	body, err := os.ReadFile("runners/codemagic/codemagic.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{
		"apteva-mobile-capsule",
		"APTEVA_PROTOCOL",
		"APTEVA_TARGET_KIND",
		`"$APTEVA_TARGET_KIND" = "ios"`,
		`"$APTEVA_TARGET_KIND" = "android"`,
		"app_store_connect",
		"google_play",
		"apteva-build.zip",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Codemagic adapter is missing %q", required)
		}
	}
}
