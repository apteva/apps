package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
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
		`signal_owned TERM`,
		`CONTROL_PIDS`,
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

func TestFailedHostedStopDurablyFencesHealthAndRestart(t *testing.T) {
	platform := &hostedStopPlatform{replies: []hostedStopReply{
		{exitCode: -1, errText: "wait: remote command exited without exit status or exit signal"},
		{exitCode: -1, errText: "wait: remote command exited without exit status or exit signal"},
	}}
	a, ctx := newTestApp(t, tk.WithPlatform(platform))
	id := seedTenant(t, a, "stop-recovery", StatusActive)
	_, err := a.store.db.Exec(`UPDATE fleet_tenants SET instance_id=3,config_dir='/var/lib/apteva-fleet/stop-recovery' WHERE id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.toolStop(ctx, map[string]any{"tenant_id": id}); !errors.Is(err, errHostedStopIndeterminate) {
		t.Fatalf("stop error=%v", err)
	}
	for _, controller := range []*App{a, {store: a.store, keys: a.keys}} {
		op, err := controller.store.activeOperation(id)
		if err != nil || op == nil || op.Operation != "stop" || op.Phase != "recovery_required" {
			t.Fatalf("stop intent lost: %+v, %v", op, err)
		}
		tenant, _, _ := controller.store.get(id)
		controller.probeOnce(context.Background(), ctx, tenant)
		controller.tryRespawnHosted(context.Background(), ctx, tenant)
		if platform.calls != 2 {
			t.Fatalf("health tried to contact/restart tenant: %d calls", platform.calls)
		}
		if _, err := controller.toolStart(ctx, map[string]any{"tenant_id": id}); err == nil {
			t.Fatal("start bypassed recovery fence")
		}
	}
	// The existing explicit recovery path retries idempotent stop/inspection,
	// then permits Start only after all recorded runtimes are stopped.
	platform.replies = append(platform.replies,
		hostedStopReply{output: "FLEET_STOP_STATE stopped"},
		hostedStopReply{output: "FLEET_STOP_STATE stopped"},
	)
	w := httptest.NewRecorder()
	a.httpRecoverOperation(w, httptest.NewRequest("POST", "/tenants/"+id+"/recover-operation", strings.NewReader(`{"confirm":true}`)))
	if w.Code != 200 {
		t.Fatalf("recovery: %d %s", w.Code, w.Body.String())
	}
	tenant, _, _ := a.store.get(id)
	if tenant.Status != StatusStopped || a.tenantOperation(id) != "" {
		t.Fatalf("recovery did not commit stopped state: %+v", tenant)
	}
}

func TestSuccessfulHostedStopReleasesFence(t *testing.T) {
	platform := &hostedStopPlatform{replies: []hostedStopReply{{output: "FLEET_STOP_STATE stopped"}}}
	a, ctx := newTestApp(t, tk.WithPlatform(platform))
	id := seedTenant(t, a, "stop-ok", StatusActive)
	if _, err := a.store.db.Exec(`UPDATE fleet_tenants SET instance_id=3 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.toolStop(ctx, map[string]any{"tenant_id": id}); err != nil {
		t.Fatal(err)
	}
	op, err := a.store.activeOperation(id)
	tenant, _, _ := a.store.get(id)
	if err != nil || op != nil || tenant.Status != StatusStopped {
		t.Fatalf("op=%+v tenant=%+v err=%v", op, tenant, err)
	}
}
