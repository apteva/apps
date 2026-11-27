package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withSandboxFacts(t *testing.T, f sandboxFacts) {
	t.Helper()
	previous := probeSandboxFacts
	probeSandboxFacts = func() sandboxFacts { return f }
	resetSandboxFacts()
	t.Cleanup(func() {
		probeSandboxFacts = previous
		resetSandboxFacts()
	})
}

func TestResolveSandboxOrderAndOverrides(t *testing.T) {
	cases := []struct {
		name    string
		facts   sandboxFacts
		mode    string
		network string
		want    sandboxKind
		errHas  string
	}{
		{"macOS uses Seatbelt", sandboxFacts{Seatbelt: true}, "auto", "open", sandboxSeatbelt, ""},
		{"working bwrap wins over container", sandboxFacts{Bwrap: true, BwrapNoNet: true, InContainer: true, SetUID: true}, "auto", "none", sandboxBwrap, ""},
		{"bwrap without netns refuses none", sandboxFacts{Bwrap: true}, "auto", "none", sandboxNone, "network namespace"},
		{"bare host with failing bwrap is refused", sandboxFacts{}, "auto", "open", sandboxNone, "OS sandbox"},
		{"bare host as root is still refused", sandboxFacts{SetUID: true}, "auto", "open", sandboxNone, "OS sandbox"},
		{"container fallback", sandboxFacts{InContainer: true, SetUID: true}, "auto", "open", sandboxContainer, ""},
		{"container without setuid is refused", sandboxFacts{InContainer: true}, "auto", "open", sandboxNone, "root"},
		{"container refuses network none", sandboxFacts{InContainer: true, SetUID: true}, "auto", "none", sandboxNone, "local_network none"},
		{"os override never uses container", sandboxFacts{InContainer: true, SetUID: true}, "os", "open", sandboxNone, "OS sandbox"},
		{"container override in a container", sandboxFacts{Bwrap: true, InContainer: true, SetUID: true}, "container", "open", sandboxContainer, ""},
		{"container override refused on a host", sandboxFacts{SetUID: true}, "container", "open", sandboxNone, "not running inside a container"},
		{"unknown mode", sandboxFacts{Seatbelt: true}, "loose", "open", sandboxNone, "local_sandbox must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withSandboxFacts(t, c.facts)
			got, err := resolveSandbox(c.mode, c.network)
			if got != c.want {
				t.Fatalf("kind = %q, want %q (err %v)", got, c.want, err)
			}
			if c.errHas == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.errHas != "" && (err == nil || !strings.Contains(err.Error(), c.errHas)) {
				t.Fatalf("err = %v, want it to mention %q", err, c.errHas)
			}
		})
	}
}

func TestSandboxFactsAreProbedOnce(t *testing.T) {
	calls := 0
	previous := probeSandboxFacts
	probeSandboxFacts = func() sandboxFacts { calls++; return sandboxFacts{Seatbelt: true} }
	resetSandboxFacts()
	t.Cleanup(func() { probeSandboxFacts = previous; resetSandboxFacts() })
	for i := 0; i < 5; i++ {
		_, _ = resolveSandbox("auto", "open")
	}
	if calls != 1 {
		t.Fatalf("probed %d times", calls)
	}
}

func TestContainerSandboxRunsArgvUnwrapped(t *testing.T) {
	argv, err := sandboxArgv(sandboxContainer, "/w", "open", "/w/workspace", []string{"go", "test"})
	if err != nil || strings.Join(argv, " ") != "go test" {
		t.Fatalf("argv = %v, %v", argv, err)
	}
	if _, err := sandboxArgv(sandboxNone, "/w", "open", "/w", []string{"true"}); err == nil {
		t.Fatal("no sandbox must refuse to run")
	}
}

func TestContainerEnvDropsApptevaIdentity(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "secret-token")
	t.Setenv("USER", "root")
	d := newLocalDriver(t.TempDir(), sandboxContainer, "open")
	wl := &localWorkload{ID: "lwk_000000000000000000000001", Env: map[string]string{"HOME": "/workspace/.home"}}
	env := strings.Join(d.commandEnv(wl, sandboxContainer, d.pathMap(wl.ID), nil), "\n")
	if strings.Contains(env, "secret-token") || strings.Contains(env, "USER=root") {
		t.Fatalf("env leaks Apteva's identity:\n%s", env)
	}
	if !strings.Contains(env, "USER=workspace") || !strings.Contains(env, "HOME="+d.workspaceDir(wl.ID)+"/.home") {
		t.Fatalf("env = \n%s", env)
	}
}

// testLocalRoot is a driver root for kind. Container mode needs every parent
// to let other users through, which t.TempDir() (0700) does not.
func testLocalRoot(t *testing.T, kind sandboxKind) string {
	t.Helper()
	if kind != sandboxContainer {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/var/tmp", "ws-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := prepareContainerRoot(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadableByOthersNeedsEveryParentEnterable(t *testing.T) {
	open := filepath.Join(t.TempDir(), "x") // t.TempDir's parent is 0700 on some systems
	_ = os.WriteFile(open, []byte("x"), 0o644)
	dir, err := os.MkdirTemp("/var/tmp", "ws-expose-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	_ = os.Chmod(dir, 0o755)
	db := filepath.Join(dir, "apteva.db")
	_ = os.WriteFile(db, []byte("x"), 0o644)
	if !readableByOthers(db) {
		t.Fatal("0644 file under 0755 folders should be readable by others")
	}
	_ = os.Chmod(db, 0o600)
	if readableByOthers(db) {
		t.Fatal("0600 file reported readable")
	}
	_ = os.Chmod(db, 0o644)
	_ = os.Chmod(dir, 0o700)
	if readableByOthers(db) {
		t.Fatal("file under a 0700 folder reported readable")
	}
	t.Setenv("DATA_DIR", dir)
	t.Setenv("DB_PATH", db)
	_ = os.Chmod(dir, 0o755)
	if got := containerExposures(); len(got) != 1 || got[0] != db {
		t.Fatalf("exposures = %v", got)
	}
	_ = open
}

func TestMkdirAllTraversableOpensOnlyCreatedFolders(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "apteva-workspaces", "install-7")
	if err := mkdirAllTraversable(root); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Dir(root), root} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o711 {
			t.Fatalf("%s mode = %o, want 711", dir, got)
		}
	}
	if info, _ := os.Stat(base); info.Mode().Perm() != 0o700 {
		t.Fatalf("existing parent changed to %o", info.Mode().Perm())
	}
	if err := mkdirAllTraversable(root); err != nil {
		t.Fatalf("second call: %v", err)
	}
}
