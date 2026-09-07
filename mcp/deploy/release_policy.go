package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"slices"
	"time"
)

type releasePolicy struct {
	Version            string                 `json:"version"`
	AutoChannel        string                 `json:"auto_channel,omitempty"`
	Channels           map[string]releaseRule `json:"channels"`
	ApproverAgentIDs   []int64                `json:"approver_agent_ids,omitempty"`
	ApproverSubjectIDs []string               `json:"approver_subject_ids,omitempty"`
}
type releaseRule struct {
	Automatic                    bool     `json:"automatic,omitempty"`
	RequiredTests                []string `json:"required_tests,omitempty"`
	FromChannel                  string   `json:"from_channel,omitempty"`
	MinimumAgeSeconds            int      `json:"minimum_age_seconds,omitempty"`
	RequireApproval              bool     `json:"require_approval,omitempty"`
	RequireAvailable             bool     `json:"require_available,omitempty"`
	MaximumObservationAgeSeconds int      `json:"maximum_observation_age_seconds,omitempty"`
}

func sealBuildArtifact(b *Build) error {
	if b == nil {
		return nil
	}
	digest, e := artifactTreeDigest(b.ArtifactPath)
	if e != nil {
		return e
	}
	p, err := pipelineConfig(b.TargetConfigJSON)
	if err != nil {
		return err
	}
	if p != nil {
		var m artifactManifest
		if json.Unmarshal([]byte(b.ArtifactManifestJSON), &m) != nil || m.Pipeline == nil || m.Pipeline.ArtifactSHA256 != digest || m.Pipeline.ConfigSHA256 != jsonDigest(p) {
			return errors.New("build pipeline evidence is missing or does not match artifact/configuration")
		}
		expected := []string{}
		for _, test := range p.Tests {
			expected = append(expected, test.Name)
		}
		if !slices.Equal(expected, m.Pipeline.Tests) {
			return errors.New("build pipeline test evidence does not match declared tests")
		}
	}
	_, e = globalCtx.AppDB().Exec(`INSERT INTO build_attestations(build_id,artifact_sha256,manifest_json,target_json,created_at) VALUES(?,?,?,?,?) ON CONFLICT(build_id) DO NOTHING`, b.ID, digest, b.ArtifactManifestJSON, b.TargetConfigJSON, nowUTC())
	return e
}
func verifiedBuildEvidence(b *Build) (string, *pipelineEvidence, error) {
	var digest, manifest string
	e := globalCtx.AppDB().QueryRow(`SELECT artifact_sha256,manifest_json FROM build_attestations WHERE build_id=?`, b.ID).Scan(&digest, &manifest)
	if e != nil {
		return "", nil, errors.New("build has no retained artifact attestation; rebuild before using release policies")
	}
	actual, e := artifactTreeDigest(b.ArtifactPath)
	if e != nil {
		return "", nil, fmt.Errorf("read attested artifact: %w", e)
	}
	if actual != digest {
		return "", nil, errors.New("release artifact differs from attested build")
	}
	var m artifactManifest
	if e = json.Unmarshal([]byte(manifest), &m); e != nil {
		return "", nil, e
	}
	if m.Pipeline != nil && m.Pipeline.ArtifactSHA256 != digest {
		return "", nil, errors.New("test evidence does not match release artifact")
	}
	return digest, m.Pipeline, nil
}
func releaseChannel(d *Deployment, opts releaseOptions) string {
	if opts.Channel != "" {
		return opts.Channel
	}
	if d.TargetKind == "android" || d.TargetKind == "ios" {
		return "internal"
	}
	return defaultStr(d.EnvironmentName, "production")
}
func releaseApprovalDigest(d *Deployment, b *Build, channel, artifact string, opts releaseOptions) (string, error) {
	frozen, e := freezeTargetConnections(d.TargetConfigJSON)
	if e != nil {
		return "", e
	}
	return jsonDigest(map[string]any{"deployment": d.ID, "environment": d.EnvironmentID, "build": b.ID, "channel": channel, "artifact": artifact, "target": frozen, "options": opts}), nil
}
func checkReleasePolicy(d *Deployment, b *Build, opts releaseOptions, ignoreApproval bool) (string, error) {
	t, e := genericTarget(d.TargetConfigJSON)
	if e != nil {
		return "", e
	}
	p := t.ReleasePolicy
	if p == nil {
		return "", nil
	}
	if p.Version == "" {
		return "", errors.New("release policy requires a version")
	}
	channel := releaseChannel(d, opts)
	rule, ok := p.Channels[channel]
	if !ok {
		return "", fmt.Errorf("release policy does not permit channel %s", channel)
	}
	if d.AutomaticRelease && !rule.Automatic {
		return "", errors.New("policy requires an explicit release request for this channel")
	}
	digest, ev, e := verifiedBuildEvidence(b)
	if e != nil {
		return "", e
	}
	for _, name := range rule.RequiredTests {
		if ev == nil || !slices.Contains(ev.Tests, name) {
			return "", fmt.Errorf("missing passing artifact test %s", name)
		}
	}
	if ev != nil {
		pipeline, e := pipelineConfig(d.TargetConfigJSON)
		if e != nil || pipeline == nil || jsonDigest(pipeline) != ev.ConfigSHA256 {
			return "", errors.New("pipeline changed since tested build")
		}
	}
	if rule.FromChannel != "" {
		rows, e := dbListReleases(globalCtx.AppDB(), d.ID, 10000)
		if e != nil {
			return "", e
		}
		found := false
		for _, r := range rows {
			if r.BuildID != b.ID || r.Channel != rule.FromChannel || r.Status != "live" {
				continue
			}
			var meta struct {
				Availability *availabilityObservation `json:"availability"`
			}
			json.Unmarshal([]byte(r.ReleaseMetaJSON), &meta)
			if rule.MinimumAgeSeconds > 0 {
				since := r.StartedAt
				if r.Provider != "" {
					if meta.Availability == nil {
						continue
					}
					since = meta.Availability.PublishedAt
				}
				published, err := time.Parse(time.RFC3339, since)
				if err != nil || time.Since(published) < time.Duration(rule.MinimumAgeSeconds)*time.Second {
					continue
				}
			}
			if rule.RequireAvailable && (meta.Availability == nil || meta.Availability.State != "available") {
				continue
			}
			if rule.RequireAvailable {
				age := rule.MaximumObservationAgeSeconds
				if age == 0 {
					age = 600
				}
				checked, err := time.Parse(time.RFC3339, meta.Availability.CheckedAt)
				if err != nil || time.Since(checked) > time.Duration(age)*time.Second {
					continue
				}
			}
			// A tested release cannot be promoted across publisher accounts or application identities.
			var saved string
			if e = globalCtx.AppDB().QueryRow(`SELECT config_json FROM release_workflows WHERE release_id=?`, r.ID).Scan(&saved); e == nil {
				var old Deployment
				json.Unmarshal([]byte(saved), &old)
				oldT, _ := genericTarget(old.TargetConfigJSON)
				fresh, _ := freezeTargetConnections(d.TargetConfigJSON)
				newT, _ := genericTarget(fresh)
				if jsonDigest(oldT.Connections) != jsonDigest(newT.Connections) || publisherIdentity(oldT.Publisher) != publisherIdentity(newT.Publisher) {
					continue
				}
			}
			found = true
			break
		}
		if !found {
			return "", errors.New("policy requires a qualifying release of this exact build in the source channel")
		}
	}
	approval, e := releaseApprovalDigest(d, b, channel, digest, opts)
	if e != nil {
		return "", e
	}
	if rule.RequireApproval && !ignoreApproval {
		var count int
		if e = globalCtx.AppDB().QueryRow(`SELECT count(*) FROM release_approvals WHERE digest=?`, approval).Scan(&count); e != nil {
			return "", e
		}
		if count == 0 {
			return approval, fmt.Errorf("release approval required for digest %s", approval)
		}
	}
	return approval, nil
}
func (a *App) toolApproveRelease(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	d, e := a.lookupDeployment(args)
	if e != nil {
		return nil, e
	}
	b, e := dbGetBuild(ctx.AppDB(), int64(intArg(args, "build_id")))
	if e != nil || b == nil || b.DeploymentID != d.ID {
		return nil, errors.New("build not found for deployment")
	}
	t, e := genericTarget(d.TargetConfigJSON)
	if e != nil || t.ReleasePolicy == nil {
		return nil, errors.New("target has no release policy")
	}
	caller := sdk.CallerFrom(call)
	if caller == nil || !(caller.AgentID > 0 && slices.Contains(t.ReleasePolicy.ApproverAgentIDs, caller.AgentID) || caller.SubjectID != "" && slices.Contains(t.ReleasePolicy.ApproverSubjectIDs, caller.SubjectID)) {
		return nil, errors.New("caller is not an authorized policy approver")
	}
	digest, e := checkReleasePolicy(d, b, releaseOptionsFromArgs(args), true)
	if e != nil {
		return nil, e
	}
	if strArg(args, "approval_digest") != digest {
		return map[string]any{"approval_digest": digest, "approved": false}, nil
	}
	_, e = ctx.AppDB().Exec(`INSERT INTO release_approvals(digest,actor_json,created_at) VALUES(?,?,?) ON CONFLICT(digest) DO NOTHING`, digest, mustJSON(map[string]any{"agent_id": caller.AgentID, "subject_id": caller.SubjectID, "tool_call_id": caller.ToolCallID}), nowUTC())
	return map[string]any{"approved": e == nil, "approval_digest": digest}, e
}

func (a *App) checkExistingReleasePolicy(rel *Release, opts releaseOptions) error {
	base, e := dbGetDeploymentByID(globalCtx.AppDB(), rel.DeploymentID)
	if e != nil || base == nil {
		return errors.New("release deployment unavailable")
	}
	d := base
	if rel.EnvironmentID > 0 {
		env, e := dbGetEnvironment(globalCtx.AppDB(), rel.EnvironmentID)
		if e != nil || env == nil {
			return errors.New("release environment unavailable")
		}
		d = effectiveDeploymentForEnvironment(base, env)
	}
	b, e := dbGetBuild(globalCtx.AppDB(), rel.BuildID)
	if e != nil {
		return e
	}
	if e = validateBuildDestination(d, b); e != nil {
		return e
	}
	_, e = checkReleasePolicy(d, b, opts, false)
	return e
}
