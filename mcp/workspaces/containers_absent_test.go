package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// serverNotBoundText is apteva-server's answer (apps_dynamic_call.go) when
// Workspaces calls an optional Containers app that is not installed or bound.
const serverNotBoundText = "http 403: app not bound: containers"

func init() {
	// Each test toggles Containers on the stub; a cached probe would leak
	// between them. TestContainersProbeIsCached turns it back on.
	containersProbeTTL = 0
}

func TestContainersErrorsClassified(t *testing.T) {
	for _, tc := range []struct {
		text            string
		absent, missing bool
	}{
		{serverNotBoundText, true, true},
		{"platform: app containers is not installed", true, true},
		{"platform: target unreachable: containers", false, true},
		{"workload not found", false, false},
	} {
		err := errors.New(tc.text)
		if containersAbsent(err) != tc.absent || containersMissing(err) != tc.missing {
			t.Fatalf("%q: absent=%v missing=%v", tc.text, containersAbsent(err), containersMissing(err))
		}
	}
}

func TestNotBoundContainersDefaultsToLocal(t *testing.T) {
	a, ctx, platform := localWorkspaceFixture(t, true)
	platform.containersGone = true
	if containersInstalled(ctx) {
		t.Fatal("app not bound must count as Containers missing")
	}
	w, err := a.createWorkspace(agentContext("p", 5, "t1", "nb-1"), ctx, map[string]any{"name": "no-docker"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.Driver != driverLocal {
		t.Fatalf("driver = %q, want local", w.Driver)
	}
	_, err = a.createWorkspace(agentContext("p", 5, "t1", "nb-2"), ctx, map[string]any{"name": "docker", "driver": "containers"}, false)
	if err == nil || !strings.Contains(err.Error(), "driver local") || strings.Contains(err.Error(), "403") {
		t.Fatalf("explicit containers err = %v", err)
	}
}

func TestContainersProbeIsCached(t *testing.T) {
	containersProbeTTL = time.Minute
	resetContainersProbe()
	t.Cleanup(func() { containersProbeTTL = 0; resetContainersProbe() })
	platform := &platformStub{containersGone: true}
	ctx, _ := newTestContext(t, platform)
	if containersInstalled(ctx) {
		t.Fatal("first probe should see Containers missing")
	}
	platform.containersGone = false
	if containersInstalled(ctx) {
		t.Fatal("second probe within the TTL should reuse the cached answer")
	}
	resetContainersProbe()
	if !containersInstalled(ctx) {
		t.Fatal("probe after reset should see Containers")
	}
}

func TestExpiryWithoutContainersSettlesDockerWorkspace(t *testing.T) {
	platform := &platformStub{}
	ctx, _ := newTestContext(t, platform)
	app := &App{}
	created, err := app.toolCreate(agentContext("project-a", 5, "main", "create"), ctx, map[string]any{"name": "Docker workspace"})
	if err != nil {
		t.Fatal(err)
	}
	w := created.(map[string]any)["workspace"].(*Workspace)
	if w.Driver != driverContainers {
		t.Fatalf("driver = %q", w.Driver)
	}
	platform.containersGone = true
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if err := updateWorkspace(ctx.AppDB(), w.ID, map[string]any{"expires_at": past, "delete_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := app.expire(context.Background(), ctx); err != nil {
		t.Fatalf("expiry without Containers should not error: %v", err)
	}
	got, _ := requireWorkspace(ctx.AppDB(), "project-a", w.ID)
	if got.LifecycleStatus != statusExpired || !strings.Contains(got.LastError, "Containers app is not available") {
		t.Fatalf("after TTL: %+v", got)
	}
	if err := app.expire(context.Background(), ctx); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if err := updateWorkspace(ctx.AppDB(), w.ID, map[string]any{"delete_at": past}); err != nil {
		t.Fatal(err)
	}
	if err := app.expire(context.Background(), ctx); err != nil {
		t.Fatalf("retention tick: %v", err)
	}
	got, _ = requireWorkspace(ctx.AppDB(), "project-a", w.ID)
	if got.LifecycleStatus != statusDestroyed || got.DestroyedAt == "" {
		t.Fatalf("after retention: %+v", got)
	}
}

func TestUnreachableContainersStillReportsExpiryError(t *testing.T) {
	err := &containersUnavailableError{msg: "x", absent: false}
	if errors.Is(err, errContainersAbsent) {
		t.Fatal("an unreachable (restarting) Containers must not settle workspaces")
	}
}

func TestLocalAcceptsAnyProfileAndWarnsAboutMissingTools(t *testing.T) {
	a, ctx, _ := localWorkspaceFixture(t, true)
	empty := t.TempDir()
	t.Setenv("PATH", empty+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	w, err := a.createWorkspace(agentContext("p", 5, "t1", "pf-1"), ctx, map[string]any{"name": "apteva-local", "driver": "local", "profile": "apteva"}, false)
	if err != nil {
		t.Fatalf("apteva profile on local: %v", err)
	}
	if w.Profile != "apteva" || len(w.Warnings) != 0 {
		t.Fatalf("apteva: profile=%q warnings=%v", w.Profile, w.Warnings)
	}
	w, err = a.createWorkspace(agentContext("p", 5, "t1", "pf-2"), ctx, map[string]any{"name": "bun-local", "driver": "local", "profile": "bun", "image": "oven/bun:1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(w.Warnings, " | ")
	if !strings.Contains(joined, "bun is not installed") || !strings.Contains(joined, "image is ignored") {
		t.Fatalf("warnings = %v", w.Warnings)
	}
	if stored, _ := requireWorkspace(ctx.AppDB(), "p", w.ID); len(stored.Warnings) != 0 {
		t.Fatal("warnings must not be stored")
	}
}

func TestAptevaProfileWithoutImageExplainsOnContainers(t *testing.T) {
	platform := &platformStub{}
	ctx, _ := newTestContext(t, platform)
	_, _, err := resolveProfile(ctx, "apteva", "", driverContainers)
	if err == nil || !strings.Contains(err.Error(), "apteva_image") || !strings.Contains(err.Error(), "driver local") {
		t.Fatalf("err = %v", err)
	}
}
