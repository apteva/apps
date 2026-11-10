package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type releaseTestsPlatform struct {
	sdk.PlatformClient
	sdk.AppContextClient
	state  string
	tested string
	queued int
}

func (p *releaseTestsPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{}, nil
}

func (p *releaseTestsPlatform) CallAppResultContext(_ context.Context, app, tool string, args map[string]any, out any) error {
	if app != "tests" || args["_project_id"] != "p1" {
		panic("unexpected Tests binding")
	}
	if tool == "tests_run" {
		p.queued++
		return json.Unmarshal([]byte(`{"run":{"id":19}}`), out)
	}
	return json.Unmarshal([]byte(mustJSON(map[string]any{
		"allowed": p.state == "passed", "state": p.state, "run_id": 19,
		"release": map[string]any{
			"candidate_digest": p.tested, "observed_digest": p.tested,
			"suite_revision": args["suite_revision"], "fixture_revision": args["fixture_revision"],
		},
	})), out)
}

func TestServicePromotionRequiresExactTestsVerdict(t *testing.T) {
	platform := &releaseTestsPlatform{state: "missing"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	setBootRecoveryGlobalCtx(t, ctx)
	runtime := &auditFastRuntime{}
	a := &App{dataDir: t.TempDir(), runtime: runtime, registry: NewSupervisorRegistry(), portRangeStart: availableTestPort(t), retainRollbacks: 3}
	a.portRangeEnd = a.portRangeStart
	runtime.app = a
	d, env := createBootRecoveryDeployment(t, ctx)
	gate := &testsReleaseGate{SuiteID: 5, SuiteRevision: strings.Repeat("a", 64), FixtureRevision: strings.Repeat("b", 64)}
	if err := dbUpdateEnvironment(ctx.AppDB(), env.ID, map[string]any{"target_config_json": mustJSON(genericTargetConfig{TestsGate: gate})}); err != nil {
		t.Fatal(err)
	}
	env, _ = dbGetEnvironment(ctx.AppDB(), env.ID)
	target := effectiveDeploymentForEnvironment(d, env)
	good := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, env.ID, "good")
	bad := createBootRecoveryBuild(t, ctx, a.dataDir, d.ID, env.ID, "bad")
	for _, build := range []*Build{good, bad} {
		if err := sealBuildArtifact(build); err != nil {
			t.Fatal(err)
		}
	}
	digest, _, err := verifiedBuildEvidence(good)
	if err != nil {
		t.Fatal(err)
	}
	platform.tested = digest
	if _, err = a.runRelease(target, good); err == nil || platform.queued != 1 {
		t.Fatalf("missing verdict: %v, queued %d", err, platform.queued)
	}
	platform.state = "failed"
	if _, err = a.runRelease(target, good); err == nil || len(runtime.started()) != 0 {
		t.Fatal("failed verdict reached the runtime")
	}
	platform.state = "passed"
	if _, err = a.runRelease(target, bad); err == nil || len(runtime.started()) != 0 {
		t.Fatal("old verdict allowed a different artifact")
	}
	if release, err := a.runRelease(target, good); err != nil || release.Status != "live" {
		t.Fatalf("matching verdict did not promote: %+v %v", release, err)
	}
}
