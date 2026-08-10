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
		`print(f"{key}={value}")`,
		`${APTEVA_XCODE_WORKSPACE:-}`,
		`${APTEVA_VERSION_CODE:-}`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Codemagic adapter is missing %q", required)
		}
	}
	if strings.Contains(text, "shlex.quote") {
		t.Fatal("Codemagic CM_ENV values must not contain shell quote literals")
	}
	if strings.Contains(text, "${APTEVA_VARIANT^}") {
		t.Fatal("Codemagic runner must remain compatible with macOS Bash 3")
	}
	for _, required := range []string{
		`variant_task="$(printf '%s' "$APTEVA_VARIANT" | awk`,
		`task=":${APTEVA_MODULE}:bundle${variant_task}"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Codemagic Android task construction is missing %q", required)
		}
	}
}
