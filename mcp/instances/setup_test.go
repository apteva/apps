package main

import (
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"golang.org/x/crypto/ssh"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type onboardingPlatform struct {
	peerName string
	tk.BasePlatformClient
	mu      sync.Mutex
	calls   []string
	peer    bool
	revoked bool
}

func (p *onboardingPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"vpn": float64(12)}}, nil
}
func (p *onboardingPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, app+"."+tool)
	var result any
	switch tool {
	case "vpn_status":
		result = map[string]any{"installed": true, "backend": "wireguard", "network_cidr": "10.13.13.0/24"}
	case "vpn_peer_list":
		peers := []any{}
		if p.peer {
			revoked := 0
			if p.revoked {
				revoked = 1
			}
			peers = append(peers, map[string]any{"name": p.peerName, "revoked_at": revoked})
		}
		result = map[string]any{"peers": peers}
	case "vpn_peer_add", "vpn_peer_config":
		p.peer = true
		p.peerName, _ = args["name"].(string)
		result = map[string]any{"address": "10.13.13.2/32", "config": testVPNConfig}
	case "vpn_peer_remove":
		p.revoked = true
		result = map[string]any{"revoked": args["name"]}
	default:
		return errors.New("unexpected tool " + tool)
	}
	data, _ := json.Marshal(result)
	return json.Unmarshal(data, out)
}

const testVPNConfig = "[Interface]\nPrivateKey = secret-vpn-key\nAddress = 10.13.13.2/32\nDNS = 1.1.1.1\n[Peer]\nPublicKey = public-vpn-key\nAllowedIPs = 0.0.0.0/0,::/0\nEndpoint = vpn.example.test:51820\nPersistentKeepalive = 25\n"

func mockHost(t *testing.T, fn func(*Instance, string, time.Duration) (string, int, error)) {
	t.Helper()
	oldRun, oldMetrics, oldProbe := setupRunSSH, setupCollectMetrics, probeSSHReadyFn
	oldUpload, oldDownload := setupUploadSSH, setupDownloadSSH
	setupRunSSH = fn
	setupCollectMetrics = func(*Instance) (*Metrics, error) {
		return &Metrics{CPU: CPUMetrics{Cores: 4}, Mem: MemMetrics{TotalBytes: 8 << 30}}, nil
	}
	probeSSHReadyFn = func(*Instance, time.Duration) error { return nil }
	var payload string
	setupUploadSSH = func(_ *Instance, _ string, data string) (int, error) { payload = data; return len(data), nil }
	setupDownloadSSH = func(*Instance, string) (string, int, error) { return payload, len(payload), nil }
	t.Cleanup(func() {
		setupRunSSH = oldRun
		setupCollectMetrics = oldMetrics
		probeSSHReadyFn = oldProbe
		setupUploadSSH = oldUpload
		setupDownloadSSH = oldDownload
	})
}
func healthyHost(_ *Instance, script string, _ time.Duration) (string, int, error) {
	if script == hostDiscoveryScript {
		return "Linux\naarch64\nubuntu\n", 0, nil
	}
	if script == dockerVerifyScript {
		return "28.0.0\n", 0, nil
	}
	if script == runtimesSetupScript {
		return "node=v22.0.0\nnpm=10.0.0\ngo=go version go1.25.1 linux/arm64\n", 0, nil
	}
	return "", 0, nil
}
func registeredHost(t *testing.T, ctx *sdk.AppCtx, setup map[string]any) *Instance {
	t.Helper()
	result, err := registerHost(ctx, map[string]any{"name": "Home", "ssh_host": "192.168.1.20", "ssh_user": "apteva", "setup": setup})
	if err != nil {
		t.Fatal(err)
	}
	return result.(map[string]any)["instance"].(*Instance)
}
func TestExternalReadinessRunsSharedSetupAndPersistsCapabilities(t *testing.T) {
	mockHost(t, healthyHost)
	platform := &onboardingPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	defer stopInstanceWorkers(ctx)
	inst := registeredHost(t, ctx, map[string]any{"docker": true, "runtimes": true})
	if instanceCapabilities(inst).Docker {
		t.Fatal("unverified docker advertised")
	}
	_, err := waitReadyWithOptions(context.Background(), ctx, map[string]any{"id": inst.ID, "timeout_s": 2})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "ready" || fresh.Platform != "linux" || fresh.Setup.Facts.Architecture != "arm64" || !fresh.Setup.Verified.Files || !fresh.Setup.Verified.Metrics {
		t.Fatalf("incomplete setup: %+v", fresh)
	}
	cap := instanceCapabilities(fresh)
	if !cap.Docker || !cap.Runtimes || !cap.Metrics {
		t.Fatalf("capabilities=%+v", cap)
	}
	if len(platform.calls) != 0 {
		t.Fatalf("host setup must not delegate to another app: %v", platform.calls)
	}
	// Fresh DB read is the restart-facing contract, not an in-memory task map.
	wire, _ := json.Marshal(fresh.stripSecrets())
	if strings.Contains(string(wire), fresh.SSHPrivateKey) {
		t.Fatal("SSH private key leaked")
	}
}
func TestSetupFailureRetryAndProviderParity(t *testing.T) {
	fail := true
	mockHost(t, func(inst *Instance, script string, timeout time.Duration) (string, int, error) {
		if script == dockerSetupScript && fail {
			return "package unavailable", 42, nil
		}
		return healthyHost(inst, script, timeout)
	})
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	defer stopInstanceWorkers(ctx)
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: "cloud", Provider: "hetzner", ProviderID: "123", Status: "provisioning", Setup: &SetupOptions{Docker: true}})
	if err != nil {
		t.Fatal(err)
	}
	// Every cloud provider reaches this same transition after provider readiness.
	if _, _, err = transitionInstanceAndEmit(ctx, inst.ID, []string{"provisioning"}, "ready", nil); err == nil {
		t.Fatal("expected failed installation")
	}
	failed, _ := dbGetInstance(ctx.AppDB(), inst.ID)
	if failed.Status != "error" || failed.Setup.Stage != "Docker" || instanceCapabilities(failed).Docker {
		t.Fatalf("failed=%+v", failed)
	}
	fail = false
	if err = retryInstanceSetup(ctx, inst.ID, nil); err != nil {
		t.Fatal(err)
	}
	ready, err := waitInstanceReady(context.Background(), ctx, inst.ID, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ready.ProviderID != "123" || !ready.Setup.Verified.Docker {
		t.Fatal("retry replaced the host or did not finish setup")
	}
}
func TestSetupDoesNotInstallOnUnsupportedOS(t *testing.T) {
	calls := 0
	mockHost(t, func(*Instance, string, time.Duration) (string, int, error) {
		calls++
		return "Darwin\narm64\nmacos\n", 0, nil
	})
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(&onboardingPlatform{}))
	inst := registeredHost(t, ctx, map[string]any{"docker": true})
	_, _, err := transitionInstanceAndEmit(ctx, inst.ID, []string{"provisioning"}, "ready", nil)
	if err == nil || calls != 1 {
		t.Fatalf("unsupported OS was modified: calls=%d err=%v", calls, err)
	}
}
func TestResumeEnrollmentReusesKeyAndVPNPeerWithoutPersistingCredential(t *testing.T) {
	p := &onboardingPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	result, err := registerHost(ctx, map[string]any{"name": "Home", "ssh_user": "apteva", "vpn": true, "setup": map[string]any{"docker": true}})
	if err != nil {
		t.Fatal(err)
	}
	first := result.(map[string]any)
	inst := first["instance"].(*Instance)
	if inst.SSHHost != "10.13.13.2" {
		t.Fatalf("host=%s", inst.SSHHost)
	}
	again, err := registerHost(ctx, map[string]any{"id": inst.ID})
	if err != nil {
		t.Fatal(err)
	}
	if again.(map[string]any)["instance"].(*Instance).SSHPublicKey != inst.SSHPublicKey {
		t.Fatal("resume rotated SSH identity")
	}
	added := 0
	for _, call := range p.calls {
		if call == "vpn.vpn_peer_add" {
			added++
		}
	}
	if added != 1 {
		t.Fatalf("peer added %d times", added)
	}
	stored, _ := dbGetInstance(ctx.AppDB(), inst.ID)
	data, _ := json.Marshal(stored)
	if strings.Contains(string(data), "secret-vpn-key") || strings.Contains(stored.SetupJSON, "secret-vpn-key") {
		t.Fatal("VPN credential persisted")
	}
	command := first["authorization"].(map[string]any)["command"].(string)
	check := exec.Command("sh", "-n")
	check.Stdin = strings.NewReader(command)
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("invalid enrollment shell: %s %v", out, err)
	}
	if err = destroyProviderInstance(ctx, stored); err != nil || !p.revoked {
		t.Fatalf("VPN peer not revoked: %v", err)
	}
}
func TestWireGuardConfigurationRejectsHooksAndLimitsRoutes(t *testing.T) {
	out, err := safeWireGuardConfig(testVPNConfig, "10.13.13.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "0.0.0.0/0") || strings.Contains(out, "DNS") {
		t.Fatal("client would hijack default routing/DNS")
	}
	if _, err = safeWireGuardConfig(strings.Replace(testVPNConfig, "Address =", "PostUp = touch /tmp/unwanted\nAddress =", 1), "10.13.13.0/24"); err == nil {
		t.Fatal("accepted shell hook")
	}
}
func TestReadinessResumesExternalRegistrationAfterRestart(t *testing.T) {
	mockHost(t, healthyHost)
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	defer stopInstanceWorkers(ctx)
	inst := registeredHost(t, ctx, map[string]any{})
	reconcileExternalHosts(ctx)
	ready, err := waitInstanceReady(context.Background(), ctx, inst.ID, 2*time.Second)
	if err != nil || ready.Status != "ready" {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
}
func TestSetupCannotResurrectDestroyingHost(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	mockHost(t, func(i *Instance, s string, d time.Duration) (string, int, error) {
		_ = dbUpdateInstance(ctx.AppDB(), i.ID, map[string]any{"status": "destroying"})
		return healthyHost(i, s, d)
	})
	inst := registeredHost(t, ctx, map[string]any{"baseline": true})
	_, ok, err := transitionInstanceAndEmit(ctx, inst.ID, []string{"provisioning"}, "ready", nil)
	if ok || err == nil {
		t.Fatal("setup overwrote destroy")
	}
	fresh, _ := dbGetInstance(ctx.AppDB(), inst.ID)
	if fresh.Status != "destroying" {
		t.Fatal("destroy status lost")
	}
}
func TestEnrollmentHTTPAndMCPReturnSameShape(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	req := httptest.NewRequest("POST", "/api/instances-register", strings.NewReader(`{"name":"Home","ssh_host":"192.168.1.20","ssh_user":"apteva","setup":{"baseline":true}}`))
	res := httptest.NewRecorder()
	(&App{}).httpRegister(res, req)
	if res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	var result struct {
		Instance      Instance       `json:"instance"`
		Authorization map[string]any `json:"authorization"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Instance.Setup == nil || !result.Instance.Setup.Requested.Baseline || result.Authorization["command"] == nil {
		t.Fatal("HTTP did not reuse registration setup")
	}
}
func TestAllSetupScriptsParse(t *testing.T) {
	for _, script := range []string{hostDiscoveryScript, baselineSetupScript, dockerSetupScript, dockerVerifyScript, runtimesSetupScript, fileCheckScript} {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("shell: %v %s", err, out)
		}
	}
}

func TestPublicCommandsStillRequireReadyDuringSetup(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	inst := registeredHost(t, ctx, map[string]any{})
	inst.Setup.Status = "running"
	inst.Setup.Stage = "Docker"
	inst.Setup.Verified.SSH = true
	if err := saveSetup(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolRunCommand(ctx, map[string]any{"id": inst.ID, "cmd": "true"}); err == nil {
		t.Fatal("public command accepted before setup finished")
	}
}

// Exercise the real discovery, SSH command, upload/download and metrics code
// over the local SSH fixture. No installation options are enabled in this test.
func TestDiscoveryOnlySetupThroughRealSSHTransport(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	oldDial := dialAdministrativeSSH
	dialAdministrativeSSH = func(*Instance, time.Duration) (*ssh.Client, error) { return auditSSHClient(t, t.TempDir()), nil }
	defer func() { dialAdministrativeSSH = oldDial }()
	inst := registeredHost(t, ctx, map[string]any{})
	ready, ok, err := transitionInstanceAndEmit(ctx, inst.ID, []string{"provisioning"}, "ready", map[string]any{"ready_at": nowUTC()})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || ready.Status != "ready" || !ready.Setup.Verified.Files || !ready.Setup.Verified.Metrics {
		t.Fatalf("real SSH setup failed: %+v", ready)
	}
}

func TestLanguageSetupPreservesExistingNodeInstallation(t *testing.T) {
	dir := t.TempDir()
	scripts := map[string]string{
		"id":   "#!/bin/sh\nprintf '0\\n'\n",
		"node": "#!/bin/sh\nprintf 'v22.0.0\\n'\n",
		"npm":  "#!/bin/sh\nprintf '10.0.0\\n'\n",
		"env":  "#!/bin/sh\nexec /usr/bin/env \"$@\"\n",
	}
	scripts["apt-get"] = "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + quoteShellArg(filepath.Join(dir, "packages")) + "\nif [ \"$1\" = install ]; then\nprintf '#!/bin/sh\\nprintf go-version\\n' > " + quoteShellArg(filepath.Join(dir, "go")) + "\n/bin/chmod +x " + quoteShellArg(filepath.Join(dir, "go")) + "\nfi\n"
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/bin/sh", "-c", runtimesSetupScript)
	cmd.Env = append(os.Environ(), "PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup failed: %v %s", err, output)
	}
	log, err := os.ReadFile(filepath.Join(dir, "packages"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "install -y golang-go") || strings.Contains(string(log), "nodejs") || strings.Contains(string(log), " npm") {
		t.Fatalf("existing runtime would be replaced: %s", log)
	}
}
