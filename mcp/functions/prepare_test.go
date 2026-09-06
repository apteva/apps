package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestPreparationRestartRelocationAndMissingArtifacts(t *testing.T) {
	for _, scenario := range []string{"restart", "relocation", "missing", "incompatible", "legacy-relocation"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("APTEVA_DATA_DIR", dir)
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
			app := mountApp(t, ctx)
			t.Cleanup(func() { app.OnUnmount(ctx); removeTree(dir) })
			fn := createFn(t, app, ctx, map[string]any{"name": "entry", "source": echoHandler, "function_url": map[string]any{"enabled": true}})
			version, _ := dbGetVersion(ctx.AppDB(), testProj, versionID(fn))
			if scenario == "legacy-relocation" {
				if _, err := ctx.AppDB().Exec(`UPDATE function_versions SET source_kind='repo',source='' WHERE id=?`, version.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := app.OnUnmount(ctx); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "relocation", "legacy-relocation":
				moved := filepath.Join(t.TempDir(), "restored")
				if err := os.Rename(dir, moved); err != nil {
					t.Fatal(err)
				}
				dir = moved
				t.Setenv("APTEVA_DATA_DIR", dir)
			case "missing":
				if err := removeTree(version.BuildDir); err != nil {
					t.Fatal(err)
				}
			case "incompatible":
				marker := filepath.Join(version.BuildDir, ".ready")
				if err := os.Chmod(marker, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, []byte("older-harness"), 0400); err != nil {
					t.Fatal(err)
				}
			}
			if err := app.OnMount(ctx); err != nil {
				t.Fatal(err)
			}
			p := currentPool()
			deadline, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := p.awaitPreparation(deadline, fn); err != nil {
				t.Fatal(err)
			}
			before := p.artifactBuilds.Load()
			expected := uint64(0)
			if scenario == "missing" || scenario == "incompatible" {
				expected = 1
			}
			if before != expected {
				t.Fatalf("builds %d, want %d", before, expected)
			}
			status := p.runtimeReadiness(fn)
			if status.State != "ready" || !status.BootValidated {
				t.Fatalf("not ready: %+v", status)
			}
			updated, _ := dbGetVersion(ctx.AppDB(), testProj, version.ID)
			if updated.BuildDir != versionDir(p.buildBase, updated) {
				t.Fatalf("stale build path: %q", updated.BuildDir)
			}
			if scenario == "legacy-relocation" && updated.Source != echoHandler {
				t.Fatal("lost relocated immutable source")
			}
			result, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"restored": true}, "manual")
			if err != nil || result.Status != "ok" {
				t.Fatalf("invoke %v %+v", err, result)
			}
			if p.artifactBuilds.Load() != before {
				t.Fatal("ready first request compiled")
			}
			p.activateVersion(fn.ID, -1) // Ordinary idle-worker eviction must not compile.
			result, err = invokeFunction(ctx, context.Background(), fn, nil, "manual")
			if err != nil || result.Status != "ok" || p.artifactBuilds.Load() != before {
				t.Fatalf("eviction recompiled: %v %+v", err, result)
			}
		})
	}
}

func TestPreparationConcurrentRequestsAndNoHandlerInvocation(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "shared", "source": echoHandler})
	p := currentPool()
	v, _ := dbGetVersion(ctx.AppDB(), testProj, versionID(fn))
	if err := removeTree(v.BuildDir); err != nil {
		t.Fatal(err)
	}
	p.activateVersion(fn.ID, -1)
	before := p.artifactBuilds.Load()
	var wg sync.WaitGroup
	failures := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if p.artifactBuilds.Load()-before != 1 {
		t.Fatal("concurrent callers did not share one build")
	}
	noCall := createFn(t, app, ctx, map[string]any{"name": "no-business-call", "source": "export default async () => { throw new Error('business handler ran'); }"})
	p.activateVersion(noCall.ID, -1)
	out, err := app.toolPrepare(context.Background(), ctx, map[string]any{"id": noCall.ID, "wait": true, "warm": true})
	if err != nil || out.(map[string]any)["runtime_readiness"].(*RuntimeReadiness).State != "ready" {
		t.Fatalf("prepare %v %v", out, err)
	}
	var count int
	ctx.AppDB().QueryRow("SELECT count(*) FROM function_invocations WHERE function_id=?", noCall.ID).Scan(&count)
	if count != 0 {
		t.Fatal("preparation invoked business handler")
	}
}

func TestToolIdentityIgnoresLocationAndMtime(t *testing.T) {
	a := filepath.Join(t.TempDir(), "tool")
	b := filepath.Join(t.TempDir(), "tool")
	os.WriteFile(a, []byte("same executable"), 0700)
	os.WriteFile(b, []byte("same executable"), 0700)
	os.Chtimes(b, time.Unix(1, 0), time.Unix(1, 0))
	if toolContentIdentity(a) != toolContentIdentity(b) {
		t.Fatal("path/mtime invalidated identical executable")
	}
	node := runtimeFingerprint("node", false)
	if node == runtimeFingerprint("go", false) {
		t.Fatal("runtimes share fingerprint")
	}
	os.WriteFile(b, []byte("changed executable"), 0700)
	if toolContentIdentity(a) == toolContentIdentity(b) {
		t.Fatal("changed executable not invalidated")
	}
}

func TestInvocationCancellationClassification(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "slow", "source": "export default async () => { await new Promise(r=>setTimeout(r,1000)); return 1; }"})
	for _, kind := range []string{"canceled", "upstream_timeout", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			parent := context.Background()
			var cancel context.CancelFunc
			if kind == "upstream_timeout" {
				parent, cancel = context.WithTimeout(parent, 30*time.Millisecond)
			} else {
				parent, cancel = context.WithCancel(parent)
			}
			defer cancel()
			if kind == "canceled" {
				time.AfterFunc(30*time.Millisecond, cancel)
			}
			copyFn := *fn
			if kind == "timeout" {
				copyFn.TimeoutMS = 30
			}
			res, _ := invokeFunction(ctx, parent, &copyFn, nil, "manual")
			if res == nil || res.Status != kind {
				t.Fatalf("wanted %s: %+v", kind, res)
			}
		})
	}
}

func TestReadinessSurvivesSecretMasking(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "with-secret", "source": echoHandler, "env": map[string]any{"TOKEN": "secret-value"}})
	masked := maskFunction(fn)
	if masked.RuntimeReadiness == nil || masked.RuntimeReadiness.State != "ready" {
		t.Fatalf("masked detail lost readiness: %+v", masked.RuntimeReadiness)
	}
	page := functionPage([]*Function{fn}, 50)
	got := page["functions"].([]*Function)[0]
	if got.RuntimeReadiness.State != "ready" || got.Env != nil {
		t.Fatalf("list readiness/masking: %+v", got)
	}
}

func TestPreparationBuildOnlyThenWarm(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	p := currentPool()
	<-p.initialPreparationScan
	fn := createFn(t, app, ctx, map[string]any{"name": "artifact-only", "source": echoHandler})
	v, _ := dbGetVersion(ctx.AppDB(), testProj, versionID(fn))
	p.activateVersion(fn.ID, -1)
	if err := removeTree(v.BuildDir); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolPrepare(context.Background(), ctx, map[string]any{"id": fn.ID, "warm": false, "wait": true})
	if err != nil {
		t.Fatal(err)
	}
	status := out.(map[string]any)["runtime_readiness"].(*RuntimeReadiness)
	if status.State != "prepared" || status.BootValidated || status.WarmWorkers != 0 {
		t.Fatalf("build-only booted worker: %+v", status)
	}
	before := p.artifactBuilds.Load()
	out, err = app.toolPrepare(context.Background(), ctx, map[string]any{"id": fn.ID, "warm": true, "wait": true})
	if err != nil {
		t.Fatal(err)
	}
	status = out.(map[string]any)["runtime_readiness"].(*RuntimeReadiness)
	if status.State != "ready" || !status.BootValidated || p.artifactBuilds.Load() != before {
		t.Fatalf("warm operation recompiled: %+v", status)
	}
}
