package main

import (
	"os"
	"os/exec"
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
		"ANDROID_UPLOAD_KEYSTORE_BASE64",
		"ANDROID_UPLOAD_CERT_SHA256",
		"APTEVA_ANDROID_SIGNER_SHA256",
		`"signing_verified"`,
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
	if strings.Contains(text, "CM_KEYSTORE_PATH") {
		t.Fatal("Codemagic runner still depends on provider-managed Android signing")
	}
	var signingScript string
	var findSigningScript func(any)
	findSigningScript = func(value any) {
		switch current := value.(type) {
		case map[string]any:
			for key, child := range current {
				if key == "script" {
					if script, ok := child.(string); ok && strings.Contains(script, "ANDROID_UPLOAD_KEYSTORE_BASE64") {
						signingScript = script
					}
				}
				findSigningScript(child)
			}
		case []any:
			for _, child := range current {
				findSigningScript(child)
			}
		}
	}
	findSigningScript(document)
	if signingScript == "" {
		t.Fatal("Codemagic Android signing script was not found")
	}
	command := exec.Command("bash", "-n")
	command.Stdin = strings.NewReader(signingScript)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Codemagic Android signing script is not valid Bash: %v\n%s", err, output)
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
