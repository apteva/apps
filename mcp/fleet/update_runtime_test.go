package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func writeFakeVersionedRuntime(t *testing.T, versionsDir, version string) aptevaRuntimePaths {
	t.Helper()
	runtime := versionedRuntimePaths(filepath.Join(versionsDir, version))
	if err := os.MkdirAll(filepath.Dir(runtime.CLI), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(runtime.CLI), "package.json"), []byte(`{"version":"`+version+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"apteva": runtime.CLI, "apteva-server": runtime.Server, "apteva-core": runtime.Core,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "#!/bin/sh\nprintf '%s\\n' '" + name + " " + version + "'\n"
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return runtime
}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, item := range env {
		if key, value, ok := strings.Cut(item, "="); ok {
			out[key] = value
		}
	}
	return out
}

func TestVersionedRuntimeUsesPackageBinariesAndPinsChildren(t *testing.T) {
	versionsDir := t.TempDir()
	runtime := writeFakeVersionedRuntime(t, versionsDir, "0.41.1")
	t.Setenv("FLEET_VERSIONS_ROOT", versionsDir)

	if got := tenantAptevaBin("0.41.1"); got != runtime.CLI {
		t.Fatalf("tenantAptevaBin = %q, want %q", got, runtime.CLI)
	}
	if strings.Contains(tenantAptevaBin("0.41.1"), string(filepath.Separator)+".bin"+string(filepath.Separator)) {
		t.Fatal("versioned runtime still uses the npm .bin launcher")
	}
	actual, err := verifyVersionedRuntime(context.Background(), runtime, "0.41.1")
	if err != nil || actual != "0.41.1" {
		t.Fatalf("verify runtime = %q, %v", actual, err)
	}
	env := envMap(applyVersionedRuntimeEnv(tenantSpawnEnv(t.TempDir(), 7100, "tenant-1"), runtime))
	if env["APTEVA_SERVER_BIN"] != runtime.Server || env["APTEVA_CORE_BIN"] != runtime.Core {
		t.Fatalf("versioned child paths not pinned: server=%q core=%q", env["APTEVA_SERVER_BIN"], env["APTEVA_CORE_BIN"])
	}
}

func TestVersionedRuntimeValidationDoesNotExecuteCore(t *testing.T) {
	versionsDir := t.TempDir()
	runtime := writeFakeVersionedRuntime(t, versionsDir, "0.41.1")
	marker := filepath.Join(t.TempDir(), "core-was-executed")
	body := "#!/bin/sh\nprintf executed > " + marker + "\nexit 99\n"
	if err := os.WriteFile(runtime.Core, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyVersionedRuntime(context.Background(), runtime, "0.41.1"); err != nil {
		t.Fatalf("validate runtime without booting core: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("core was executed during offline validation: %v", err)
	}
}

func TestHostedVersionInstallScriptExecutesPinnedRuntimeValidation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("hosted flock execution requires Linux; script contract is tested separately")
	}
	versionsDir := t.TempDir()
	runtime := writeFakeVersionedRuntime(t, versionsDir, "0.41.1")
	versionDir := filepath.Join(versionsDir, "0.41.1")
	script := hostedVersionInstallScript(versionDir, "0.41.1")
	for _, want := range []string{runtime.CLI, runtime.Server, runtime.Core, "APTEVA_HOME=\"$INSTALL_HOME\"", "FLEET_VERSION_READY"} {
		if !strings.Contains(script, want) {
			t.Fatalf("hosted install script missing %q", want)
		}
	}
	if strings.Contains(script, "/node_modules/.bin/apteva") {
		t.Fatal("hosted install script uses npm .bin launcher")
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("execute hosted runtime validation: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "FLEET_VERSION_READY 0.41.1") {
		t.Fatalf("readiness output = %q", out)
	}
}

func TestRuntimeHealthVersionMismatchFailsExplicitly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"apteva":"0.34.5"}`))
	}))
	defer srv.Close()
	_, err := requireRuntimeHealthVersion(context.Background(), srv.URL, "0.41.1")
	if err == nil || !strings.Contains(err.Error(), "requested Apteva 0.41.1, but launched runtime reports 0.34.5") {
		t.Fatalf("health mismatch error = %v", err)
	}
}

func TestToolUpdateStoppedLocalTenantPreparesWithoutStarting(t *testing.T) {
	app, ctx := newTestApp(t)
	versionsDir := t.TempDir()
	writeFakeVersionedRuntime(t, versionsDir, "0.41.1")
	t.Setenv("FLEET_VERSIONS_ROOT", versionsDir)
	id := seedTenant(t, app, "offline-local", StatusStopped)
	if _, err := app.store.db.Exec(`UPDATE fleet_tenants SET current_version=?, target_version=? WHERE id=?`, "0.34.5", "0.34.5", id); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolUpdate(ctx, map[string]any{"tenant_id": id, "version": "0.41.1"})
	if err != nil {
		t.Fatalf("toolUpdate: %v", err)
	}
	result := out.(map[string]any)
	if result["started"] != false || result["status"] != StatusStopped {
		t.Fatalf("update result unexpectedly started tenant: %#v", result)
	}
	updated, _, err := app.store.get(id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != StatusStopped || updated.CurrentVersion != "0.34.5" || updated.TargetVersion != "0.41.1" {
		t.Fatalf("stopped tenant state changed incorrectly: %+v", updated)
	}
	app.procMu.Lock()
	_, spawned := app.procs[updated.Slug]
	app.procMu.Unlock()
	if spawned {
		t.Fatal("stopped local tenant acquired a process")
	}
}

type stoppedHostedUpdatePlatform struct {
	tk.BasePlatformClient
	commands []string
	uploaded []byte
}

func (p *stoppedHostedUpdatePlatform) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	if appName != "instances" {
		return nil, errors.New("unexpected app " + appName)
	}
	switch tool {
	case "instance_get":
		return wrappedToolResult(map[string]any{"instance": map[string]any{
			"id": 3, "name": "scaleway", "provider": "scaleway", "public_ipv4": "203.0.113.8", "status": "ready",
		}}), nil
	case "instance_run_command":
		command, _ := input["cmd"].(string)
		p.commands = append(p.commands, command)
		if strings.Contains(command, "FLEET_VERSION_READY") {
			return wrappedToolResult(map[string]any{"output": "FLEET_VERSION_READY 0.41.1\n", "exit_code": 0}), nil
		}
		return wrappedToolResult(map[string]any{
			"output": "FLEET_RUNTIME_READY node=v20.1.0 npm=10.1.0 go=go1.24.6 available_kb=9000000\n", "exit_code": 0,
		}), nil
	case "instance_upload_file":
		encoded, _ := input["content_b64"].(string)
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, err
		}
		p.uploaded = decoded
		return wrappedToolResult(map[string]any{"bytes_written": len(decoded)}), nil
	case "instance_download_file":
		return wrappedToolResult(map[string]any{
			"content_b64": base64.StdEncoding.EncodeToString(p.uploaded), "bytes": len(p.uploaded),
		}), nil
	case "instance_open_tunnel":
		return wrappedToolResult(map[string]any{"local_host": "127.0.0.1", "local_port": 43123}), nil
	case "instance_close_tunnel":
		return wrappedToolResult(map[string]any{"closed": true}), nil
	default:
		return nil, errors.New("unexpected tool " + tool)
	}
}

func TestToolUpdateStoppedHostedTenantPreparesWithoutStarting(t *testing.T) {
	platform := &stoppedHostedUpdatePlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(platform))
	id := seedTenant(t, app, "offline-hosted", StatusStopped)
	if _, err := app.store.db.Exec(`UPDATE fleet_tenants SET instance_id=3, base_url=?, config_dir=?, current_version=?, target_version=? WHERE id=?`,
		"http://203.0.113.8:7100", "/var/lib/apteva-fleet/offline-hosted", "0.34.5", "0.34.5", id); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolUpdate(ctx, map[string]any{"tenant_id": id, "version": "0.41.1"})
	if err != nil {
		t.Fatalf("toolUpdate: %v", err)
	}
	result := out.(map[string]any)
	if result["started"] != false || result["status"] != StatusStopped {
		t.Fatalf("update result unexpectedly started tenant: %#v", result)
	}
	updated, _, _ := app.store.get(id)
	if updated.Status != StatusStopped || updated.CurrentVersion != "0.34.5" || updated.TargetVersion != "0.41.1" {
		t.Fatalf("stopped hosted tenant state changed incorrectly: %+v", updated)
	}
	for _, command := range platform.commands {
		if strings.Contains(command, "setsid sh -c") || strings.Contains(command, "kill -TERM") || strings.Contains(command, "PID_FILE=") {
			t.Fatalf("stopped hosted update executed lifecycle command:\n%s", command)
		}
	}
}

var _ sdk.PlatformClient = (*stoppedHostedUpdatePlatform)(nil)
