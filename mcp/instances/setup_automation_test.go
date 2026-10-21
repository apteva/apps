package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAutomationMOTDChangesOnlyOptionalMOTDSession(t *testing.T) {
	command := automationMOTDCommand("/etc/pam.d/sshd")
	_, transform, ok := strings.Cut(command, "awk '")
	if !ok {
		t.Fatal("missing transform")
	}
	transform, _, ok = strings.Cut(transform, "' \"$original\"")
	if !ok {
		t.Fatal("missing transform end")
	}
	before := `# retain auth/account/session protections
auth required pam_securetty.so
@include common-auth
account required pam_nologin.so
session required pam_limits.so
session optional pam_motd.so motd=/run/motd.dynamic # noupdate is supported
session optional pam_motd.so noupdate
`
	expected := strings.Replace(before, "motd=/run/motd.dynamic #", "motd=/run/motd.dynamic noupdate #", 1)
	apply := func(input string) string {
		cmd := exec.Command("awk", transform)
		cmd.Stdin = strings.NewReader(input)
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatal(string(out), e)
		}
		return string(out)
	}
	if got := apply(before); got != expected {
		t.Fatal("PAM rules changed unexpectedly", got)
	}
	if got := apply(expected); got != expected {
		t.Fatal("provisioning retry changed PAM config")
	}
	if strings.Contains(command, "UsePAM") || strings.Contains(command, "restart") {
		t.Fatal("script changes SSH authentication")
	}
	opts, e := parseSetup(map[string]any{"setup": map[string]any{"suppress_dynamic_motd": true}})
	if e != nil || !opts.SuppressDynamicMOTD {
		t.Fatal(opts, e)
	}
	opts, e = parseSetup(map[string]any{"setup": map[string]any{}})
	if e != nil || opts.SuppressDynamicMOTD {
		t.Fatal("MOTD optimization must be opt in")
	}
}
