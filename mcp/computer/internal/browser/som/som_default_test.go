package som

import "testing"

func TestEnabledAlwaysOnForComputerApp(t *testing.T) {
	if !Enabled() {
		t.Fatal("SoM must be enabled by default in the Computer app")
	}
}
