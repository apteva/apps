package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type hostedRuntimePlatform struct {
	tk.BasePlatformClient
	calls      []string
	uploaded   []byte
	commandErr error
}

func wrappedToolResult(v any) json.RawMessage {
	body, _ := json.Marshal(v)
	envelope, _ := json.Marshal(map[string]any{
		"result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(body)}},
		},
	})
	return envelope
}

func (p *hostedRuntimePlatform) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	p.calls = append(p.calls, appName+"."+tool)
	if appName != "instances" {
		return nil, errors.New("unexpected app " + appName)
	}
	switch tool {
	case "instance_run_command":
		if p.commandErr != nil {
			return nil, p.commandErr
		}
		return wrappedToolResult(map[string]any{
			"output":    "FLEET_RUNTIME_READY node=v20.1.0 npm=10.1.0 go=go1.24.6 available_kb=9000000\n",
			"exit_code": 0,
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
			"content_b64": base64.StdEncoding.EncodeToString(p.uploaded),
			"bytes":       len(p.uploaded),
		}), nil
	case "instance_open_tunnel":
		return wrappedToolResult(map[string]any{"local_host": "127.0.0.1", "local_port": 43123}), nil
	case "instance_close_tunnel":
		return wrappedToolResult(map[string]any{"closed": true}), nil
	default:
		return nil, errors.New("unexpected tool " + tool)
	}
}

func TestEnsureHostedRuntimeVerifiesInstancesCapabilitiesAndCaches(t *testing.T) {
	platform := &hostedRuntimePlatform{}
	app, ctx := newTestApp(t, tk.WithPlatform(platform))
	if err := app.ensureHostedRuntime(ctx, 3); err != nil {
		t.Fatalf("ensure hosted runtime: %v", err)
	}
	wantTools := []string{
		"instances.instance_run_command",
		"instances.instance_upload_file",
		"instances.instance_download_file",
		"instances.instance_run_command",
		"instances.instance_open_tunnel",
		"instances.instance_close_tunnel",
	}
	if strings.Join(platform.calls, "\n") != strings.Join(wantTools, "\n") {
		t.Fatalf("calls:\n%v\nwant:\n%v", platform.calls, wantTools)
	}
	before := len(platform.calls)
	if err := app.ensureHostedRuntime(ctx, 3); err != nil {
		t.Fatalf("cached ensure: %v", err)
	}
	if len(platform.calls) != before {
		t.Fatalf("cached ensure made %d additional calls", len(platform.calls)-before)
	}
}

func TestEnsureHostedRuntimeStopsAfterCommandFailure(t *testing.T) {
	platform := &hostedRuntimePlatform{commandErr: errors.New("ssh unavailable")}
	app, ctx := newTestApp(t, tk.WithPlatform(platform))
	err := app.ensureHostedRuntime(ctx, 3)
	if err == nil || !strings.Contains(err.Error(), "ssh unavailable") {
		t.Fatalf("error = %v", err)
	}
	if len(platform.calls) != 1 || platform.calls[0] != "instances.instance_run_command" {
		t.Fatalf("preflight continued after command failure: %v", platform.calls)
	}
}

func TestHostedRuntimeBootstrapSupportsExpectedSystems(t *testing.T) {
	for _, required := range []string{
		"apt-get", "apk", "dnf", "yum", "setup_22.x", "python3", "git", "setsid",
		"go_ready", "go_minor", "golang-go", "go.dev/dl/?mode=json", "sha256sum", "/usr/local/go",
		"available_kb", "2097152", "FLEET_RUNTIME_READY",
	} {
		if !strings.Contains(hostedRuntimeBootstrapScript, required) {
			t.Errorf("bootstrap script missing %q", required)
		}
	}
}

func TestHostedRuntimeBootstrapInstallsGitForEveryPackageManager(t *testing.T) {
	for _, install := range []string{
		"apt-get install -y -qq ca-certificates curl git ",
		"apk add --no-cache nodejs npm python3 tar gzip curl git ",
		"dnf install -y nodejs npm python3 tar gzip curl git ",
		"yum install -y nodejs npm python3 tar gzip curl git ",
	} {
		if !strings.Contains(hostedRuntimeBootstrapScript, install) {
			t.Errorf("bootstrap script missing Git install path %q", install)
		}
	}
}

func TestHostedRuntimeBootstrapShellSyntax(t *testing.T) {
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(hostedRuntimeBootstrapScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap shell syntax: %v: %s", err, out)
	}
}

var _ sdk.PlatformClient = (*hostedRuntimePlatform)(nil)
