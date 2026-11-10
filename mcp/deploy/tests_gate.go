package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type testsReleaseGate struct {
	SuiteID         int64  `json:"suite_id"`
	SuiteRevision   string `json:"suite_revision"`
	FixtureRevision string `json:"fixture_revision"`
}

var testsDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (gate *testsReleaseGate) validate() error {
	if gate != nil && (gate.SuiteID < 1 || !testsDigest.MatchString(gate.SuiteRevision) || !testsDigest.MatchString(gate.FixtureRevision)) {
		return errors.New("tests_gate requires suite_id, suite_revision and fixture_revision")
	}
	return nil
}

func checkTestsGate(d *Deployment, b *Build, gate *testsReleaseGate, queue bool) error {
	if gate == nil {
		return nil
	}
	if err := gate.validate(); err != nil {
		return err
	}
	if d.ProjectID == "" || globalCtx == nil || b == nil || b.Status != "succeeded" || b.DeploymentID != d.ID {
		return errors.New("release test gate requires a succeeded build in the target project")
	}
	digest, _, err := verifiedBuildEvidence(b)
	if err != nil {
		return fmt.Errorf("release test gate: %w", err)
	}
	platform := globalCtx.WithProject(d.ProjectID).PlatformAPI()
	if platform == nil {
		return errors.New("release test gate requires a bound Tests app")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	args := map[string]any{
		"suite_id": gate.SuiteID, "suite_revision": gate.SuiteRevision,
		"fixture_revision": gate.FixtureRevision, "candidate_digest": digest,
	}
	var verdict struct {
		Allowed bool   `json:"allowed"`
		State   string `json:"state"`
		RunID   int64  `json:"run_id"`
		Release struct {
			Candidate string `json:"candidate_digest"`
			Observed  string `json:"observed_digest"`
			Suite     string `json:"suite_revision"`
			Fixture   string `json:"fixture_revision"`
		} `json:"release"`
	}
	if err = sdk.CallAppResultContext(ctx, platform, "tests", "tests_release_verdict", args, &verdict); err != nil {
		return errors.New("could not read the release verdict from the bound Tests app")
	}
	if verdict.Allowed && verdict.State == "passed" && verdict.RunID > 0 &&
		verdict.Release.Candidate == digest && verdict.Release.Observed == digest &&
		verdict.Release.Suite == gate.SuiteRevision && verdict.Release.Fixture == gate.FixtureRevision {
		return nil
	}
	if verdict.State == "missing" && queue {
		args["request_key"] = "deploy:" + jsonDigest(args)
		var queued struct {
			Run struct {
				ID int64 `json:"id"`
			} `json:"run"`
		}
		if err = sdk.CallAppResultContext(ctx, platform, "tests", "tests_run", args, &queued); err != nil {
			return errors.New("could not queue the required release test")
		}
		return fmt.Errorf("release blocked while Tests run %d evaluates the candidate", queued.Run.ID)
	}
	switch verdict.State {
	case "queued", "running", "failed":
		return fmt.Errorf("release blocked by Tests run %d (%s)", verdict.RunID, verdict.State)
	default:
		return errors.New("release blocked without a passing, matching Tests verdict")
	}
}

func (a *App) toolCandidateDigest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	build, err := dbGetBuild(ctx.AppDB(), int64(intArg(args, "build_id")))
	if err != nil || build == nil || build.Status != "succeeded" {
		return nil, errors.New("succeeded build not found")
	}
	deployment, err := dbGetDeployment(ctx.AppDB(), project, build.DeploymentID)
	if err != nil || deployment == nil {
		return nil, errors.New("succeeded build not found in this project")
	}
	a.artifactMu.RLock()
	defer a.artifactMu.RUnlock()
	digest, _, err := verifiedBuildEvidence(build)
	if err != nil {
		return nil, err
	}
	return map[string]any{"build_id": build.ID, "candidate_digest": digest}, nil
}
