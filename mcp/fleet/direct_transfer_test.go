package main

import (
	"strings"
	"testing"
)

func TestCrossHostDirectTransferScriptStreamsAndResumesWithoutSnapshotStage(t *testing.T) {
	script := crossHostDirectTransferScript(
		"/var/lib/apteva-fleet/source",
		"/var/lib/apteva-fleet/.clone-transfer-copy",
		"/var/lib/apteva-fleet/.transfers/direct-test",
		hostedSSHDestination{user: "root", host: "203.0.113.8", port: 22},
		"203.0.113.8 ssh-ed25519 AAAATEST",
	)
	for _, want := range []string{
		"rsync -rlptD", "--partial", "--info=progress2", `"$SQLITE_RSYNC" -vv`, "--exclude='*.db'",
		"phase=ordinary-files", "phase=sqlite", "root@203.0.113.8",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("direct transfer script missing %q\n%s", want, script)
		}
	}
	for _, forbidden := range []string{"fleet-clone-snap", "tar czf", "mktemp -d /tmp"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("direct transfer script contains source-side staging operation %q", forbidden)
		}
	}
}

func TestSameHostDirectTransferUsesSQLiteRsyncAndPersistentDestinationStage(t *testing.T) {
	script := sameHostDirectTransferScript(
		"/var/lib/apteva-fleet/source",
		"/var/lib/apteva-fleet/copy",
		"/var/lib/apteva-fleet/.clone-transfer-copy",
	)
	if !strings.Contains(script, "--partial-dir=.fleet-rsync-partial") || !strings.Contains(script, `"$SQLITE_RSYNC" -v`) {
		t.Fatalf("same-host transfer is not resumable/sqlite-aware:\n%s", script)
	}
	if strings.Contains(script, "/tmp/") || strings.Contains(script, "tar ") {
		t.Fatalf("same-host transfer stages or archives source data:\n%s", script)
	}
}

func TestDirectTransferToolBootstrapIsPinnedAndUsesFleetDisk(t *testing.T) {
	script := hostedDirectTransferToolsScript()
	for _, want := range []string{
		"sqlite-src-3530400.zip", sqliteRsyncSourceSHA3,
		"sqlite-amalgamation-3530400.zip", sqliteRsyncAmalgamSHA3,
		`mktemp -d "$TOOLS/.sqlite-build-XXXXXX"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("tool bootstrap missing %q", want)
		}
	}
	if strings.Contains(script, "mktemp -d /tmp") {
		t.Fatal("tool bootstrap uses production /tmp")
	}
}

func TestTargetTransferWrapperRestrictsWritesToCloneStage(t *testing.T) {
	script := targetTransferWrapper("/var/lib/apteva-fleet/.clone-transfer-copy")
	for _, want := range []string{
		`SSH_ORIGINAL_COMMAND`, `replica_index = argv.index("--replica")`, `dest.startswith(stage + os.sep)`, `argv[0] == "rsync"`,
		`"--replica" in argv`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("target authorization script missing %q\n%s", want, script)
		}
	}
}

func TestHostedSSHTargetRejectsCommandInjection(t *testing.T) {
	_, err := hostedSSHTarget(fleetHost{InstanceID: 2, Info: &instanceInfo{
		PublicIPv4: "203.0.113.8", SSHUser: "root;touch /tmp/pwn", SSHPort: 22,
	}})
	if err == nil {
		t.Fatal("unsafe SSH username was accepted")
	}
	got, err := hostedSSHTarget(fleetHost{InstanceID: 2, Info: &instanceInfo{
		PublicIPv4: "203.0.113.8", SSHUser: "root", SSHPort: 2202,
	}})
	if err != nil || got.host != "203.0.113.8" || got.port != 2202 || got.user != "root" {
		t.Fatalf("valid SSH target = %+v, err=%v", got, err)
	}
}
