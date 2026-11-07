package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
		"APTEVA_SIGNING_CONTRACT",
		"apteva.mobile-signing/v1",
		`"signing_verified"`,
		`"signing_contract"`,
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
	if !strings.Contains(text, `printf 'APTEVA_ANDROID_SIGNER_SHA256=%s\n' "$actual_fingerprint" >> "$CM_ENV"`) {
		t.Fatal("Codemagic runner does not persist Android signing evidence between scripts")
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

func TestCodemagicAppleProjectGeneration(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "deploy-pipeline")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile pipeline runner: %v\n%s", err, output)
	}
	command := exec.Command("python3", "-B", "-m", "unittest", "discover", "-s", "runners/codemagic/tests", "-v")
	command.Env = append(os.Environ(), "TEST_DEPLOY_PIPELINE_RUNNER="+binary)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Apple runner regression tests: %v\n%s", err, output)
	}
}

func TestSharedNativeStagesStayInSyncWithMaintainedWorkflow(t *testing.T) {
	body, err := os.ReadFile("runners/codemagic/codemagic.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Workflows map[string]struct {
			Scripts []struct {
				Name   string `yaml:"name"`
				Script string `yaml:"script"`
			} `yaml:"scripts"`
		} `yaml:"workflows"`
	}
	if err := yaml.Unmarshal(body, &source); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile("runners/shared/scripts/mobile_steps.json")
	if err != nil {
		t.Fatal(err)
	}
	var shared []struct {
		Name   string `json:"name"`
		Script string `json:"script"`
	}
	if err := json.Unmarshal(body, &shared); err != nil {
		t.Fatal(err)
	}
	canonical := source.Workflows["apteva-mobile-capsule"].Scripts
	if len(canonical) != len(shared) {
		t.Fatal("native stage count differs")
	}
	replacement := regexp.MustCompile(`(?m)^( +)app-store-connect fetch-signing-files[^\n]+\n +keychain add-certificates\n +xcode-project use-profiles`)
	for index, stage := range canonical {
		script := strings.ReplaceAll(strings.ReplaceAll(stage.Script, "CM_BUILD_DIR", "APTEVA_BUILD_DIR"), "CM_ENV", "APTEVA_ENV_FILE")
		script = replacement.ReplaceAllStringFunc(script, func(match string) string {
			indent := replacement.FindStringSubmatch(match)[1]
			return indent + `python3 "$APTEVA_BUILD_DIR/scripts/install_managed_signing.py"` + "\n" + indent + `xcode-project use-profiles --profile "$APTEVA_BUILD_DIR/apteva-signing/profile.mobileprovision"`
		})
		script = strings.ReplaceAll(script, `zip -qry "$APTEVA_BUILD_DIR/apteva-build.zip" .`, `zip -qry "$APTEVA_BUILD_DIR/${APTEVA_ARTIFACT_NAME:-apteva-build}.zip" .`)
		if shared[index].Name != stage.Name || shared[index].Script != script {
			t.Fatalf("shared native stage %q differs; regenerate mobile_steps.json", stage.Name)
		}
	}
}
