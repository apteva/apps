package main

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestHostedProcessControlScriptCoversWholeSessionSafely(t *testing.T) {
	script := hostedProcessControlScript("/var/lib/apteva-fleet/acme", 7100, 10, true)
	for _, required := range []string{
		`APTEVA_HOME=$DATA_DIR`,
		`proc_sid`,
		`for proc in /proc/[0-9]*`,
		`is_static_group "$pid" && continue`,
		`kill -TERM $OWNED`,
		`FLEET_STOP_STATE`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("hosted stop script missing %q", required)
		}
	}
	if strings.Contains(script, `kill -TERM -$PGID`) || strings.Contains(script, `kill -KILL -$PGID`) {
		t.Fatal("hosted stop still relies on only the launcher's process group")
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hosted stop shell syntax: %v: %s", err, out)
	}
}

type hostedStopReply struct {
	output   string
	exitCode int
	errText  string
}

type hostedStopPlatform struct {
	tk.BasePlatformClient
	replies []hostedStopReply
	calls   int
}

func (p *hostedStopPlatform) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	if appName != "instances" || tool != "instance_run_command" {
		return nil, errors.New("unexpected app tool call")
	}
	if p.calls >= len(p.replies) {
		return nil, errors.New("unexpected extra stop inspection")
	}
	reply := p.replies[p.calls]
	p.calls++
	return wrappedToolResult(map[string]any{
		"output": reply.output, "exit_code": reply.exitCode, "error": reply.errText,
	}), nil
}

func TestStopHostedTenantVerifiesAmbiguousCompletion(t *testing.T) {
	tests := []struct {
		name      string
		followUp  hostedStopReply
		wantError string
		wantClass error
	}{
		{name: "confirmed stopped", followUp: hostedStopReply{output: "FLEET_STOP_STATE stopped root=none sid=none listeners=none owned=none reason=none\n"}},
		{name: "confirmed running", followUp: hostedStopReply{output: "FLEET_STOP_STATE running root=10 sid=10 listeners=11 owned=10 11 reason=none\n", exitCode: 3}, wantError: "verified tenant is still running"},
		{name: "still unknown", followUp: hostedStopReply{output: "FLEET_STOP_STATE unknown root=10 sid=none listeners=11 owned=none reason=listener mismatch\n", exitCode: 4}, wantError: "indeterminate", wantClass: errHostedStopIndeterminate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			platform := &hostedStopPlatform{replies: []hostedStopReply{
				{exitCode: -1, errText: "ssh wait returned no exit status"},
				tt.followUp,
			}}
			_, ctx := newTestApp(t, tk.WithPlatform(platform))
			err := stopHostedTenant(ctx, 3, "acme", 7100, time.Second)
			if tt.wantError == "" && err != nil {
				t.Fatalf("stopHostedTenant: %v", err)
			}
			if tt.wantError != "" && (err == nil || !strings.Contains(err.Error(), tt.wantError)) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantError)
			}
			if tt.wantClass != nil && !errors.Is(err, tt.wantClass) {
				t.Fatalf("error = %v, want class %v", err, tt.wantClass)
			}
			if platform.calls != 2 {
				t.Fatalf("instance_run_command calls = %d, want 2", platform.calls)
			}
		})
	}
}

var _ sdk.PlatformClient = (*hostedStopPlatform)(nil)
