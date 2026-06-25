package som

import (
	"strings"
	"testing"
)

func TestEnabledAlwaysOnForComputerApp(t *testing.T) {
	if !Enabled() {
		t.Fatal("SoM must be enabled by default in the Computer app")
	}
}

func TestEnumScriptLabelsUploadTriggers(t *testing.T) {
	if !strings.Contains(EnumScript, "'[data-trigger]'") {
		t.Fatal("SoM must enumerate visible data-trigger upload controls so upload_file can use labels for hidden file inputs")
	}
}
