package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type integrationAction struct {
	Tool  string         `json:"tool"`
	Input map[string]any `json:"input"`
	// The adapter may expose an accepted request that still requires confirmation.
	PendingStatuses []int `json:"pending_statuses,omitempty"`
}
type observationCheck struct {
	Pointer string `json:"pointer"`
	Equals  any    `json:"equals"`
}
type integrationObserver struct {
	Action    integrationAction  `json:"action"`
	Published []observationCheck `json:"published,omitempty"`
	Available []observationCheck `json:"available,omitempty"`
	Audience  string             `json:"audience,omitempty"`
	Region    string             `json:"region,omitempty"`
}
type integrationPublisher struct {
	Role        string               `json:"role"`
	Provider    string               `json:"provider"`
	Identity    map[string]any       `json:"identity"`
	Upload      []commandStage       `json:"upload,omitempty"`
	ReceiptFile string               `json:"receipt_file,omitempty"`
	Publish     integrationAction    `json:"publish"`
	Observe     *integrationObserver `json:"observe,omitempty"`
}
type availabilityObservation struct {
	PublishedAt string          `json:"published_at,omitempty"`
	State       string          `json:"state"`
	Publication string          `json:"publication"`
	Audience    string          `json:"audience,omitempty"`
	Region      string          `json:"region,omitempty"`
	CheckedAt   string          `json:"checked_at"`
	Evidence    json.RawMessage `json:"evidence,omitempty"`
}
type publisherActionResult struct {
	Tool   string          `json:"tool"`
	Status int             `json:"status"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type workflowState struct {
	LastAction     *publisherActionResult   `json:"last_action,omitempty"`
	Phase          string                   `json:"phase"`
	Values         map[string]any           `json:"values"`
	ArtifactSHA256 string                   `json:"artifact_sha256"`
	ApprovalDigest string                   `json:"approval_digest,omitempty"`
	Availability   *availabilityObservation `json:"availability,omitempty"`
}

func publisherIdentity(p *integrationPublisher) string {
	if p == nil {
		return ""
	}
	return jsonDigest(map[string]any{"provider": p.Provider, "role": p.Role, "identity": p.Identity})
}

// A full-string $variable preserves numbers and objects without shell interpolation.
func resolveReleaseValue(v any, values map[string]any) (any, error) {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "$") {
			key := strings.TrimPrefix(x, "$")
			value, ok := values[key]
			if !ok {
				return nil, fmt.Errorf("missing release value %s", key)
			}
			return value, nil
		}
		return x, nil
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			resolved, e := resolveReleaseValue(v, values)
			if e != nil {
				return nil, e
			}
			out[k] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			resolved, e := resolveReleaseValue(v, values)
			if e != nil {
				return nil, e
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return v, nil
	}
}
func executeReleaseAction(bound *sdk.BoundIntegration, action integrationAction, values map[string]any) (*sdk.ExecuteResult, error) {
	if action.Tool == "" {
		return nil, errors.New("publisher tool required")
	}
	input, e := resolveReleaseValue(action.Input, values)
	if e != nil {
		return nil, e
	}
	args, _ := input.(map[string]any)
	result, e := globalCtx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, action.Tool, args)
	if e != nil {
		return nil, fmt.Errorf("publisher %s request failed", action.Tool)
	}
	if result == nil || !result.Success || result.Status >= 400 {
		return nil, fmt.Errorf("publisher %s rejected request", action.Tool)
	}
	return result, nil
}
func jsonPointer(value any, pointer string, values map[string]any) (any, bool) {
	if pointer == "" {
		return value, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	for _, part := range strings.Split(pointer[1:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		if strings.HasPrefix(part, "$") {
			v, ok := values[part[1:]]
			if !ok {
				return nil, false
			}
			part = fmt.Sprint(v)
		}
		switch x := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = x[part]
			if !ok {
				return nil, false
			}
		case []any:
			i, e := strconv.Atoi(part)
			if e != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			value = x[i]
		default:
			return nil, false
		}
	}
	return value, true
}
func observationMatches(body any, checks []observationCheck, values map[string]any) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		got, ok := jsonPointer(body, check.Pointer, values)
		want, e := resolveReleaseValue(check.Equals, values)
		if !ok || e != nil || jsonDigest(got) != jsonDigest(want) {
			return false
		}
	}
	return true
}
func saveWorkflow(rel *Release, d *Deployment, s workflowState) error {
	_, e := globalCtx.AppDB().Exec(`INSERT INTO release_workflows(release_id,config_json,state_json,updated_at) VALUES(?,?,?,?) ON CONFLICT(release_id) DO UPDATE SET state_json=excluded.state_json,updated_at=excluded.updated_at`, rel.ID, mustJSON(d), mustJSON(s), nowUTC())
	return e
}
func (a *App) runIntegrationRelease(d *Deployment, b *Build, opts releaseOptions) (*Release, error) {
	unlock := a.lockEnvironment(d.ID, d.EnvironmentID)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	a.artifactMu.RLock()
	defer a.artifactMu.RUnlock()
	fresh, e := a.refreshDeployment(d)
	if e != nil {
		return nil, e
	}
	if fresh.ArchivedAt != "" {
		return nil, errors.New("deployment is archived")
	}
	if b.Status != "succeeded" || b.DeploymentID != d.ID {
		return nil, errors.New("succeeded build for this deployment required")
	}
	if e := validateBuildDestination(d, b); e != nil {
		return nil, e
	}
	target, e := genericTarget(d.TargetConfigJSON)
	if e != nil {
		return nil, e
	}
	if target.Publisher == nil {
		return nil, errors.New("artifact target requires publisher configuration")
	}
	p := target.Publisher
	if p.Provider == "" || p.Role == "" || len(p.Identity) == 0 {
		return nil, errors.New("publisher requires provider, role and application identity")
	}
	frozen, e := freezeTargetConnections(d.TargetConfigJSON)
	if e != nil {
		return nil, e
	}
	copyD := *d
	copyD.TargetConfigJSON = frozen
	d = &copyD
	bound, e := selectedIntegration(p.Role, frozen)
	if e != nil {
		return nil, e
	}
	if bound.AppSlug != p.Provider {
		return nil, errors.New("selected connection does not match configured publisher")
	}
	digest, _, e := verifiedBuildEvidence(b)
	if e != nil {
		return nil, e
	}
	approval, e := checkReleasePolicy(d, b, opts, false)
	if e != nil {
		return nil, e
	}
	channel := releaseChannel(d, opts)
	// Any prior upload of this build to the same account/app is reusable for promotion.
	values := map[string]any{"channel": channel, "build_id": b.ID, "artifact_sha256": digest}
	for k, v := range p.Identity {
		values["identity."+k] = v
	}
	var prior string
	rows, e := dbListReleases(globalCtx.AppDB(), d.ID, 10000)
	if e != nil {
		return nil, e
	}
	for _, r := range rows {
		if r.BuildID != b.ID || r.Provider != p.Provider || r.Status == "stopped" || r.Status == "failed" {
			continue
		}
		var config, state string
		if globalCtx.AppDB().QueryRow(`SELECT config_json,state_json FROM release_workflows WHERE release_id=?`, r.ID).Scan(&config, &state) != nil {
			continue
		}
		var oldD Deployment
		var oldS workflowState
		json.Unmarshal([]byte(config), &oldD)
		json.Unmarshal([]byte(state), &oldS)
		oldT, _ := genericTarget(oldD.TargetConfigJSON)
		if oldS.ArtifactSHA256 != digest || publisherIdentity(oldT.Publisher) != publisherIdentity(p) || oldT.Connections[p.Role] != targetConnectionID(frozen, p.Role) {
			continue
		}
		if oldS.Phase == "uploading" || oldS.Phase == "publishing" {
			return nil, errors.New("previous upload or publish outcome is uncertain; reconcile that release before retrying")
		}
		if oldS.Phase == "uploaded" || oldS.Phase == "observing" {
			prior = state
			break
		}
	}
	rel, created, e := a.createOperationRelease(d, b)
	if e != nil || !created {
		return rel, e
	}
	state := workflowState{Phase: "uploading", Values: values, ArtifactSHA256: digest, ApprovalDigest: approval}
	if prior != "" {
		var old workflowState
		json.Unmarshal([]byte(prior), &old)
		for k, v := range old.Values {
			if strings.HasPrefix(k, "upload.") {
				values[k] = v
			}
		}
		state.Phase = "uploaded"
	}
	if e = saveWorkflow(rel, d, state); e != nil {
		return nil, e
	}
	if e = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{"provider": p.Provider, "channel": channel, "external_status": state.Phase, "started_at": nowUTC(), "release_meta_json": mustJSON(state)}); e != nil {
		return nil, e
	}
	_, log, e := a.openMobileReleaseLog(rel.ID)
	if e != nil {
		return nil, e
	}
	defer log.Close()
	ctx, finish := a.registerLocalBuild(-rel.ID)
	defer finish()
	unlock()
	locked = false
	fail := func(err error) (*Release, error) {
		dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{"error": err.Error(), "external_status": "action_required"})
		return nil, err
	}
	if state.Phase == "uploading" {
		// Upload commands operate on a scratch copy; the tested artifact is kept intact.
		scratch := filepath.Join(a.dataDir, "releases", strconv.FormatInt(rel.ID, 10), "upload")
		if e = os.MkdirAll(scratch, 0700); e != nil {
			return fail(e)
		}
		defer os.RemoveAll(scratch)
		content := filepath.Join(scratch, "artifact")
		if e = os.MkdirAll(content, 0700); e != nil {
			return fail(e)
		}
		if e = copyTreeAll(b.ArtifactPath, content); e != nil {
			return fail(e)
		}

		for _, stage := range p.Upload {
			if e = runCommandStage(ctx, stage, scratch, content, content, parseEnvJSON(d.EnvJSON), newBoundedLogWriter(log)); e != nil {
				return fail(e)
			}
		}
		if p.ReceiptFile != "" {
			receiptRoot := scratch
			if len(p.Upload) == 0 {
				receiptRoot = content
			}
			path, e := pipelinePath(receiptRoot, p.ReceiptFile)
			if e != nil {
				return fail(e)
			}
			info, e := os.Stat(path)
			if e != nil || info.Size() > 1<<20 {
				return fail(errors.New("invalid upload receipt size"))
			}
			raw, e := os.ReadFile(path)
			if e != nil {
				return fail(e)
			}
			var receipt map[string]any
			if e = json.Unmarshal(raw, &receipt); e != nil {
				return fail(e)
			}
			for k, v := range receipt {
				values["upload."+k] = v
			}
		}
		if after, checkErr := artifactTreeDigest(content); checkErr != nil || after != digest {
			return fail(errors.New("upload modified tested artifacts; publication blocked"))
		}
		state.Phase = "uploaded"
		if e = saveWorkflow(rel, d, state); e != nil {
			return fail(e)
		}
	}
	if e = ctx.Err(); e != nil {
		return fail(e)
	}
	freshRelease, readErr := dbGetRelease(globalCtx.AppDB(), rel.ID)
	if readErr != nil {
		return fail(readErr)
	}
	if freshRelease.Status == "stopped" {
		return fail(errors.New("release stopped"))
	}
	state.Phase = "publishing"
	if e = saveWorkflow(rel, d, state); e != nil {
		return fail(e)
	}
	result, e := executeReleaseAction(bound, p.Publish, values)
	if e != nil {
		return fail(e)
	}
	if len(result.Data) > 1<<20 {
		return fail(errors.New("publisher response exceeds 1 MiB budget"))
	}
	state.LastAction = &publisherActionResult{Tool: p.Publish.Tool, Status: result.Status, Data: result.Data}
	status := "accepted"
	for _, code := range p.Publish.PendingStatuses {
		if result.Status == code {
			status = "awaiting_confirmation"
		}
	}
	state.Phase = "observing"
	state.Availability = &availabilityObservation{State: "unconfirmed", Publication: status, CheckedAt: nowUTC()}
	if e = saveWorkflow(rel, d, state); e != nil {
		return fail(e)
	}
	if e = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{"release_meta_json": mustJSON(state), "external_status": status}); e != nil {
		return fail(e)
	}
	if p.Observe != nil {
		if e = a.syncIntegrationRelease(rel); e != nil {
			return fail(e)
		}
	}
	return dbGetRelease(globalCtx.AppDB(), rel.ID)
}
func targetConnectionID(raw, role string) int64 {
	t, _ := genericTarget(raw)
	return t.Connections[role]
}
func (a *App) syncIntegrationRelease(rel *Release) error {
	if rel.Status == "stopped" || rel.Status == "failed" {
		return nil
	}
	var config, raw string
	if e := globalCtx.AppDB().QueryRow(`SELECT config_json,state_json FROM release_workflows WHERE release_id=?`, rel.ID).Scan(&config, &raw); e != nil {
		return e
	}
	var d Deployment
	var state workflowState
	if e := json.Unmarshal([]byte(config), &d); e != nil {
		return e
	}
	if e := json.Unmarshal([]byte(raw), &state); e != nil {
		return e
	}
	unlock := a.lockEnvironment(d.ID, d.EnvironmentID)
	defer unlock()
	current, readErr := dbGetRelease(globalCtx.AppDB(), rel.ID)
	if readErr != nil {
		return readErr
	}
	if current.Status == "stopped" || current.Status == "failed" {
		return nil
	}
	t, e := genericTarget(d.TargetConfigJSON)
	if e != nil {
		return e
	}
	if t.Publisher == nil || t.Publisher.Observe == nil {
		return errors.New("publisher has no availability observer")
	}
	p := t.Publisher
	bound, e := selectedIntegration(p.Role, d.TargetConfigJSON)
	if e != nil {
		return e
	}
	if bound.AppSlug != p.Provider {
		return errors.New("publisher connection type changed")
	}
	result, e := executeReleaseAction(bound, p.Observe.Action, state.Values)
	if e != nil {
		return e
	}
	if len(result.Data) > 1<<20 {
		return errors.New("availability response exceeds 1 MiB budget")
	}
	var body any
	if e = json.Unmarshal(result.Data, &body); e != nil {
		return e
	}
	obs := &availabilityObservation{State: "unconfirmed", Publication: "unconfirmed", Audience: p.Observe.Audience, Region: p.Observe.Region, CheckedAt: nowUTC(), Evidence: result.Data}
	if state.Availability != nil && state.Availability.Publication == "awaiting_confirmation" {
		obs.Publication = "awaiting_confirmation"
	}
	status := "starting"
	if observationMatches(body, p.Observe.Published, state.Values) {
		obs.Publication = "published"
		obs.PublishedAt = nowUTC()
		if state.Availability != nil && state.Availability.Publication == "published" && state.Availability.PublishedAt != "" {
			obs.PublishedAt = state.Availability.PublishedAt
		}
		status = "live"
	}
	if observationMatches(body, p.Observe.Available, state.Values) {
		obs.State = "available"
	}
	state.Availability = obs
	// Observation can reconcile an uncertain branch request but never repeats a mutation.
	if obs.Publication == "published" {
		state.Phase = "observing"
	}
	if e = saveWorkflow(rel, &d, state); e != nil {
		return e
	}
	fresh, err := dbGetRelease(globalCtx.AppDB(), rel.ID)
	if err != nil {
		return err
	}
	if fresh.Status == "stopped" || fresh.Status == "failed" {
		return nil
	}
	if e = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{"status": status, "external_status": obs.Publication, "release_meta_json": mustJSON(state), "error": ""}); e != nil {
		return e
	}
	if status == "live" {
		return setDeploymentCurrentRelease(globalCtx.AppDB(), &d, &rel.ID)
	}
	return nil
}

func setDeploymentCurrentRelease(db *sql.DB, d *Deployment, id *int64) error {
	if id == nil {
		return errors.New("release id required")
	}
	table, key := "deployments", d.ID
	if d.EnvironmentID > 0 {
		table, key = "deployment_environments", d.EnvironmentID
	}
	_, err := db.Exec("UPDATE "+table+" SET current_release_id=?,updated_at=? WHERE id=? AND (current_release_id IS NULL OR current_release_id<=?)", *id, nowUTC(), key, *id)
	return err
}

func (a *App) toolPromoteArtifact(ctx *sdk.AppCtx, base *Deployment, args map[string]any) (any, error) {
	rel, e := releaseFromArgs(ctx, args, int64(intArg(args, "release_id")))
	if e != nil || rel == nil || rel.DeploymentID != base.ID {
		return nil, errors.New("source release_id required for artifact promotion")
	}
	b, e := dbGetBuild(ctx.AppDB(), rel.BuildID)
	if e != nil {
		return nil, e
	}
	env, e := dbGetEnvironment(ctx.AppDB(), rel.EnvironmentID)
	if e != nil || env == nil {
		return nil, errors.New("source environment not found")
	}
	d := effectiveDeploymentForEnvironment(base, env)
	if name := strArg(args, "target_environment"); name != "" {
		env, e = dbGetEnvironmentByName(ctx.AppDB(), base.ID, name)
		if e != nil || env == nil {
			return nil, errors.New("target environment not found")
		}
		d = effectiveDeploymentForEnvironment(base, env)
	}
	opts := releaseOptionsFromArgs(args)
	opts.Channel = strArg(args, "target_channel")
	if opts.Channel == "" {
		return nil, errors.New("target_channel required")
	}
	promoted, e := a.runReleaseWithOptions(d, b, opts)
	return map[string]any{"release": promoted, "build": b, "source_release": rel}, e
}
