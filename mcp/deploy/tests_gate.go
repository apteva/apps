package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type testsReleaseGate struct {
	SuiteID         int64  `json:"suite_id"`
	SuiteRevision   string `json:"suite_revision"`
	FixtureRevision string `json:"fixture_revision"`
	// Required protects the gate itself: removing it, turning required off or
	// pointing it at another suite needs tests_gate_change_reason.
	Required bool `json:"required,omitempty"`
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
	// The source digest lets Tests check that the verdict was earned by the
	// same source; Tests versions that do not know it ignore the argument.
	sourceDigest := buildSourceDigest(b.ID)
	if sourceDigest != "" {
		args["source_digest"] = sourceDigest
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
			Source    string `json:"source_digest"`
		} `json:"release"`
	}
	if err = sdk.CallAppResultContext(ctx, platform, "tests", "tests_release_verdict", args, &verdict); err != nil {
		return errors.New("could not read the release verdict from the bound Tests app")
	}
	if verdict.Release.Source != "" && verdict.Release.Source != sourceDigest {
		return fmt.Errorf("release blocked: Tests run %d verified source %s, but build %d was made from %s", verdict.RunID, verdict.Release.Source, b.ID, defaultStr(sourceDigest, "unrecorded source"))
	}
	if verdict.Allowed && verdict.State == "passed" && verdict.RunID > 0 &&
		verdict.Release.Candidate == digest && verdict.Release.Observed == digest &&
		verdict.Release.Suite == gate.SuiteRevision && verdict.Release.Fixture == gate.FixtureRevision {
		pinTestedBuild(b.ID)
		return nil
	}
	// A candidate under test or awaiting release must survive retention.
	if verdict.State == "queued" || verdict.State == "running" || (verdict.State == "missing" && queue) {
		pinTestedBuild(b.ID)
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
	build, _, err := a.resolveBuildRef(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	a.artifactMu.RLock()
	defer a.artifactMu.RUnlock()
	digest, _, err := verifiedBuildEvidence(build)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"build_id": build.ID, "candidate_digest": digest}
	if src, _ := dbBuildSource(ctx.AppDB(), build.ID); src != nil {
		out["source_digest"] = src.SourceDigest
		out["source"] = sourceSummary(src)
	}
	return out, nil
}

func pinTestedBuild(buildID int64) {
	if err := pinBuild(globalCtx.AppDB(), buildID, pinReasonTests, time.Now().Add(testsVerdictPinTTL)); err != nil {
		log.Printf("pin tested build %d: %v", buildID, err)
	}
}

// ─── Emergency override ───────────────────────────────────────────

const minTestsOverrideReason = 10

type testsOverride struct {
	Reason    string `json:"reason"`
	BlockedBy string `json:"blocked_by"`
	BuildID   int64  `json:"build_id"`
	At        string `json:"at"`
}

// checkTestsGateOrOverride applies the release test gate. When the gate
// blocks, an explicit reason lets the release through and is reported; a
// build already released past the gate in this environment keeps that
// override (restarts, boot recovery and the later promotion of a starting
// release use it).
func checkTestsGateOrOverride(d *Deployment, b *Build, gate *testsReleaseGate, queue bool, reason string) (*testsOverride, error) {
	err := checkTestsGate(d, b, gate, queue)
	if err == nil {
		return nil, nil
	}
	reason = strings.TrimSpace(reason)
	if reason != "" {
		if len([]rune(reason)) < minTestsOverrideReason {
			return nil, fmt.Errorf("%v; tests_override_reason must explain the emergency (at least %d characters)", err, minTestsOverrideReason)
		}
		o := &testsOverride{Reason: reason, BlockedBy: err.Error(), BuildID: b.ID, At: nowUTC()}
		emit("deploy.tests_gate.overridden", map[string]any{
			"deployment_id": d.ID, "environment_id": d.EnvironmentID, "environment": d.EnvironmentName,
			"build_id": b.ID, "reason": reason, "blocked_by": o.BlockedBy,
		})
		globalCtx.Logger().Warn("release test gate overridden", "deployment_id", d.ID, "environment_id", d.EnvironmentID, "build_id", b.ID, "reason", reason, "blocked_by", o.BlockedBy)
		return o, nil
	}
	if prior := priorTestsOverride(d.EnvironmentID, b.ID); prior != nil {
		return prior, nil
	}
	if gate != nil {
		return nil, fmt.Errorf("%v (an operator can release anyway with tests_override_reason)", err)
	}
	return nil, err
}

// priorTestsOverride finds an override recorded on a release of this build
// in this environment.
func priorTestsOverride(environmentID, buildID int64) *testsOverride {
	if globalCtx == nil || buildID < 1 {
		return nil
	}
	var raw string
	err := globalCtx.AppDB().QueryRow(`SELECT json_extract(release_meta_json,'$.tests_override') FROM releases
		WHERE build_id=? AND COALESCE(environment_id,0)=? AND json_valid(release_meta_json)
		AND json_extract(release_meta_json,'$.tests_override.reason') IS NOT NULL ORDER BY id DESC LIMIT 1`, buildID, environmentID).Scan(&raw)
	if err != nil {
		return nil
	}
	var o testsOverride
	if json.Unmarshal([]byte(raw), &o) != nil || o.Reason == "" {
		return nil
	}
	return &o
}

// recordTestsOverride stores the override on the release (release meta plus
// a release event), so it is visible next to the release it allowed.
func recordTestsOverride(releaseID int64, o *testsOverride) error {
	if o == nil || releaseID < 1 {
		return nil
	}
	db := globalCtx.AppDB()
	var raw string
	_ = db.QueryRow(`SELECT COALESCE(release_meta_json,'') FROM releases WHERE id=?`, releaseID).Scan(&raw)
	meta := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &meta)
	}
	if existing, ok := meta["tests_override"].(map[string]any); ok && existing["reason"] == o.Reason {
		return nil
	}
	meta["tests_override"] = o
	if err := dbUpdateRelease(db, releaseID, map[string]any{"release_meta_json": mustJSON(meta)}); err != nil {
		return err
	}
	return dbAppendReleaseEvent(db, releaseID, "tests_gate_overridden", mustJSON(o))
}

// guardTestsGateChange refuses to weaken a required gate without a reason:
// removing it, turning required off or switching to another suite. Updating
// the suite or fixture revisions is routine and always allowed.
func guardTestsGateChange(db *sql.DB, environmentID int64, fields map[string]any) error {
	next, ok := fields["target_config_json"].(string)
	if !ok {
		return nil
	}
	var current string
	if err := db.QueryRow(`SELECT COALESCE(target_config_json,'{}') FROM deployment_environments WHERE id=?`, environmentID).Scan(&current); err != nil {
		return nil
	}
	var before, after genericTargetConfig
	if json.Unmarshal([]byte(defaultStr(current, "{}")), &before) != nil || before.TestsGate == nil || !before.TestsGate.Required {
		return nil
	}
	_ = json.Unmarshal([]byte(defaultStr(next, "{}")), &after)
	if after.TestsGate != nil && after.TestsGate.Required && after.TestsGate.SuiteID == before.TestsGate.SuiteID {
		return nil
	}
	reason, _ := fields["tests_gate_change_reason"].(string)
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) < minTestsOverrideReason {
		return fmt.Errorf("this environment requires its Tests gate (suite %d); removing it, turning required off or changing the suite needs tests_gate_change_reason (at least %d characters)", before.TestsGate.SuiteID, minTestsOverrideReason)
	}
	emit("deploy.tests_gate.changed", map[string]any{"environment_id": environmentID, "suite_id": before.TestsGate.SuiteID, "reason": reason})
	if globalCtx != nil {
		globalCtx.Logger().Warn("required release test gate changed", "environment_id", environmentID, "suite_id", before.TestsGate.SuiteID, "reason", reason)
	}
	return nil
}
