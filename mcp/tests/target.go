package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// TargetConfig selects where a suite runs. Without it, a suite keeps the 0.4.0
// behaviour: standalone checks, or an isolated release configured by hand.
type TargetConfig struct {
	Mode        string                    `json:"mode"`
	Targets     map[string]TargetEndpoint `json:"targets,omitempty"`
	Candidate   *CandidateRef             `json:"candidate,omitempty"`
	Browser     *BrowserConfig            `json:"browser,omitempty"`
	Environment *GeneratedEnvironment     `json:"environment,omitempty"`
}

// TargetEndpoint is an absolute base URL in existing mode, or an app and a
// public path inside the runtime in isolated mode.
type TargetEndpoint struct {
	BaseURL string `json:"base_url,omitempty"`
	App     string `json:"app,omitempty"`
	Path    string `json:"path,omitempty"`
}

// CandidateRef names a Deploy deployment and which of its builds is the candidate.
type CandidateRef struct {
	DeploymentID int64  `json:"deployment_id"`
	Environment  string `json:"environment,omitempty"`
	Build        any    `json:"build,omitempty"`
}

// GeneratedEnvironment is the isolated environment Tests defines and pins in
// Environments for the suite.
type GeneratedEnvironment struct {
	Apps           []string            `json:"apps"`
	AppVersions    map[string]string   `json:"app_versions,omitempty"`
	Data           *EnvironmentData    `json:"data,omitempty"`
	Seeds          []any               `json:"seeds,omitempty"`
	TTLSeconds     int                 `json:"ttl_seconds,omitempty"`
	CandidateProbe *ToolProbe          `json:"candidate_probe,omitempty"`
	ReadTools      map[string][]string `json:"read_tools,omitempty"`
	// Integrations bind simulated providers to app roles (Environments 0.12.0+).
	Integrations []SimulatedIntegration `json:"integrations,omitempty"`
	// Pinned is filled by Tests when the suite is saved.
	Pinned *PinnedEnvironment `json:"pinned,omitempty"`
}

type EnvironmentData struct {
	SnapshotID string `json:"snapshot_id,omitempty"`
}

type PinnedEnvironment struct {
	EnvironmentID string `json:"environment_id"`
	SpecVersion   int    `json:"spec_version"`
	SpecDigest    string `json:"spec_digest"`
}

// CredentialRef points a suite alias at an entry in the encrypted
// `credentials` install setting.
type CredentialRef struct {
	Ref string `json:"ref"`
}

// SessionConfig signs in once per run and reuses the result.
type SessionConfig struct {
	Kind       string `json:"kind"`
	SetupCheck string `json:"setup_check"`
	Capture    string `json:"capture,omitempty"`
	Header     string `json:"header,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
}

// HeaderRef injects a credential into an HTTP header without exposing it in
// the check definition.
type HeaderRef struct {
	Ref    string `json:"ref"`
	Prefix string `json:"prefix,omitempty"`
}

const (
	maxTargets       = 8
	maxCredentialSet = 16
)

func originOf(u *url.URL) string {
	return u.Scheme + "://" + u.Host
}

func (t *TargetConfig) validate() error {
	switch t.Mode {
	case "existing":
		if t.Environment != nil {
			return errors.New("existing targets cannot declare an isolated environment")
		}
		if len(t.Targets) == 0 || len(t.Targets) > maxTargets {
			return fmt.Errorf("existing mode needs 1–%d named targets", maxTargets)
		}
		origins := map[string]bool{}
		for name, target := range t.Targets {
			if !slugPattern.MatchString(name) || target.App != "" || target.Path != "" {
				return fmt.Errorf("target %q needs only a base_url in existing mode", name)
			}
			u, err := url.Parse(target.BaseURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
				u.User != nil || u.RawQuery != "" || u.Fragment != "" || validateGatewayPath(u.Path) != nil {
				return fmt.Errorf("target %q needs an absolute HTTP(S) base_url without credentials, query or fragment", name)
			}
			origins[originOf(u)] = true
		}
		if len(origins) > maxTargets {
			return fmt.Errorf("existing mode allows at most %d origins", maxTargets)
		}
	case "isolated":
		if t.Environment == nil {
			return errors.New("isolated mode needs an environment")
		}
		if err := t.Environment.validate(); err != nil {
			return err
		}
		if len(t.Targets) > maxTargets {
			return fmt.Errorf("isolated mode allows at most %d targets", maxTargets)
		}
		for name, target := range t.Targets {
			if !slugPattern.MatchString(name) || target.BaseURL != "" || !slugPattern.MatchString(target.App) ||
				target.Path == "" || validateGatewayPath(target.Path) != nil {
				return fmt.Errorf("target %q needs an app and a public path in isolated mode", name)
			}
			if !containsString(t.Environment.Apps, target.App) {
				return fmt.Errorf("target %q uses app %q, which is not in the environment", name, target.App)
			}
		}
	case "source":
		// Source mode tests a Deploy build's own source with code checks; it
		// has no endpoints, browser or environment.
		if len(t.Targets) > 0 || t.Environment != nil || t.Browser != nil {
			return errors.New("source mode takes only a candidate: no targets, browser or environment")
		}
		if t.Candidate == nil {
			return errors.New("source mode needs target.candidate {deployment_id, build}")
		}
	default:
		return errors.New(`target.mode must be "existing", "isolated" or "source"`)
	}
	if t.Browser != nil {
		if err := t.Browser.validate(); err != nil {
			return err
		}
		if len(t.Browser.ValueRefs) > 0 && t.Mode == "existing" {
			return errors.New("value_refs resolve inside an isolated runtime; use credentials in existing mode")
		}
	}
	if t.Candidate != nil {
		selector, _, err := t.Candidate.normalize()
		if err != nil {
			return err
		}
		if t.Mode == "existing" && selector != "live" {
			return errors.New(`existing mode tests what is deployed; candidate.build must be "live" or omitted`)
		}
	}
	return nil
}

func (e *GeneratedEnvironment) validate() error {
	if len(e.Apps) == 0 || len(e.Apps) > 24 {
		return errors.New("environment.apps needs 1–24 app names")
	}
	seen := map[string]bool{}
	for _, app := range e.Apps {
		if !slugPattern.MatchString(app) || seen[app] {
			return fmt.Errorf("invalid or duplicate environment app %q", app)
		}
		seen[app] = true
	}
	for app, version := range e.AppVersions {
		if !seen[app] || version == "" || len(version) > 64 || strings.TrimSpace(version) != version {
			return fmt.Errorf("invalid version pin for %q", app)
		}
	}
	if e.TTLSeconds == 0 {
		e.TTLSeconds = 900
	}
	if e.TTLSeconds < 60 || e.TTLSeconds > 900 {
		return errors.New("environment.ttl_seconds must be 60–900")
	}
	if e.Data != nil && e.Data.SnapshotID != "" && !slugPattern.MatchString(e.Data.SnapshotID) {
		return errors.New("invalid environment snapshot_id")
	}
	if len(e.Seeds) > 64 {
		return errors.New("at most 64 environment seeds")
	}
	if err := e.validateIntegrations(); err != nil {
		return err
	}
	if e.CandidateProbe != nil && !validProbe(*e.CandidateProbe) {
		return errors.New("invalid candidate_probe")
	}
	for app, tools := range e.ReadTools {
		if !seen[app] || len(tools) > 32 {
			return fmt.Errorf("read_tools for %q must name an environment app and at most 32 tools", app)
		}
		for _, tool := range tools {
			if !slugPattern.MatchString(tool) {
				return fmt.Errorf("invalid read tool %q", tool)
			}
		}
	}
	return nil
}

// normalize returns the build selector: "live", "latest_succeeded", or a build ID.
func (c *CandidateRef) normalize() (string, int64, error) {
	if c.DeploymentID < 1 || len(c.Environment) > 64 || strings.ContainsAny(c.Environment, "\r\n") {
		return "", 0, errors.New("candidate needs a deployment_id")
	}
	switch v := c.Build.(type) {
	case nil:
		return "live", 0, nil
	case string:
		if v == "live" || v == "latest_succeeded" {
			return v, 0, nil
		}
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			return "id", id, nil
		}
	case float64:
		if v > 0 && v == float64(int64(v)) {
			return "id", int64(v), nil
		}
	case json.Number:
		if id, err := v.Int64(); err == nil && id > 0 {
			return "id", id, nil
		}
	}
	return "", 0, errors.New(`candidate.build must be "live", "latest_succeeded" or a build ID`)
}

func (s *SessionConfig) validate(mode string) error {
	if s.SetupCheck == "" || len(s.SetupCheck) > 200 {
		return errors.New("session.setup_check names the check that signs in")
	}
	switch s.Kind {
	case "browser":
		if mode != "existing" {
			return errors.New("browser sessions require existing mode; isolated runs start each check in a fresh browser")
		}
		if s.Capture != "" || s.Header != "" || s.Prefix != "" {
			return errors.New("browser sessions do not capture tokens")
		}
	case "bearer":
		if mode != "existing" {
			return errors.New("bearer sessions require existing mode; isolated runs use identities")
		}
		if !slugPattern.MatchString(s.Capture) {
			return errors.New("bearer sessions need the capture name holding the token")
		}
		if s.Header == "" {
			s.Header = "Authorization"
		}
		switch s.Header {
		case "Authorization", "X-API-Key", "Cookie":
		default:
			return errors.New("bearer session header must be Authorization, X-API-Key or Cookie")
		}
		if s.Header == "Authorization" && s.Prefix == "" {
			s.Prefix = "Bearer "
		}
		if strings.ContainsAny(s.Prefix, "\r\n") || len(s.Prefix) > 32 {
			return errors.New("invalid session prefix")
		}
	default:
		return errors.New(`session.kind must be "browser" or "bearer"`)
	}
	return nil
}

// validateSuiteExtensions checks the 0.5.0 suite fields together.
func validateSuiteExtensions(v *Suite) error {
	if v.Target != nil {
		if v.Release != nil {
			return errors.New("use either release (hand-written environment) or target, not both")
		}
		if err := v.Target.validate(); err != nil {
			return err
		}
	}
	if len(v.Credentials) > maxCredentialSet {
		return fmt.Errorf("at most %d credential aliases", maxCredentialSet)
	}
	for alias, ref := range v.Credentials {
		if !slugPattern.MatchString(alias) || !slugPattern.MatchString(ref.Ref) {
			return fmt.Errorf("invalid credential alias %q", alias)
		}
	}
	if v.ReleaseBlocking && v.Release == nil && (v.Target == nil || v.Target.Candidate == nil && v.Target.Mode != "isolated") {
		return errors.New("release_blocking suites need a verdict: a release configuration, an isolated target, or a target.candidate")
	}
	if v.Session != nil {
		mode := ""
		if v.Target != nil {
			mode = v.Target.Mode
		}
		if mode == "" {
			return errors.New("sessions require a target (existing or isolated)")
		}
		if err := v.Session.validate(mode); err != nil {
			return err
		}
	}
	return nil
}

// targetRevision pins what an existing-mode verdict was measured against.
func targetRevision(t *TargetConfig) string {
	return canonicalDigest(struct {
		Mode      string
		Targets   map[string]TargetEndpoint
		Candidate *CandidateRef
	}{t.Mode, t.Targets, t.Candidate})
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// credentialValue resolves "credentials.<alias>.<field>" against the install's
// encrypted `credentials` setting: {"<project_id>": {"<ref>": {"<field>": "..."}}}.
func credentialValue(app *sdk.AppCtx, suite *Suite, reference string) (string, error) {
	rest, ok := strings.CutPrefix(reference, "credentials.")
	alias, field, found := strings.Cut(rest, ".")
	if !ok || !found || alias == "" || field == "" {
		return "", fmt.Errorf("credential reference %q must be credentials.<alias>.<field>", reference)
	}
	ref, ok := suite.Credentials[alias]
	if !ok {
		return "", fmt.Errorf("credential alias %q is not declared on the suite", alias)
	}
	raw := ""
	if app != nil && app.Config() != nil {
		raw = strings.TrimSpace(app.Config().Get("credentials"))
	}
	if raw == "" {
		return "", errors.New("the credentials setting is empty; add values in the Tests install settings")
	}
	var projects map[string]map[string]map[string]string
	if err := json.Unmarshal([]byte(raw), &projects); err != nil {
		return "", errors.New("the credentials setting is not valid JSON")
	}
	value, ok := projects[app.CurrentProject()][ref.Ref][field]
	if !ok || value == "" {
		return "", fmt.Errorf("credential %q has no %q value for this project", ref.Ref, field)
	}
	if len(value) > 4096 || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("credential %q.%q is too long or contains line breaks", ref.Ref, field)
	}
	return value, nil
}

// credentialReferences lists every credential a check uses.
func credentialReferences(c Check) []string {
	var refs []string
	add := func(ref string) {
		if strings.HasPrefix(ref, "credentials.") {
			refs = append(refs, ref)
		}
	}
	for _, ref := range c.Definition.HeaderRefs {
		add(ref.Ref)
	}
	for _, s := range c.Definition.Steps {
		group := s.Parallel
		if len(group) == 0 {
			group = []Step{s}
		}
		for _, step := range group {
			if step.Browser != nil {
				add(step.Browser.ValueRef)
			}
			for _, ref := range step.HeaderRefs {
				add(ref.Ref)
			}
		}
	}
	return refs
}

func validateHeaderRefs(refs map[string]HeaderRef) error {
	if len(refs) > 8 {
		return errors.New("at most 8 header_refs")
	}
	for name, ref := range refs {
		switch strings.ToLower(name) {
		case "authorization", "x-api-key", "cookie":
		default:
			if !strings.HasPrefix(strings.ToLower(name), "x-") || strings.ContainsAny(name, " :\r\n") {
				return fmt.Errorf("header_refs supports Authorization, Cookie and X- headers, not %q", name)
			}
		}
		if !strings.HasPrefix(ref.Ref, "credentials.") || !browserRef.MatchString(ref.Ref) ||
			strings.ContainsAny(ref.Prefix, "\r\n") || len(ref.Prefix) > 32 {
			return fmt.Errorf("header_refs.%s needs a credentials.<alias>.<field> reference", name)
		}
	}
	return nil
}

// --- Deploy candidate resolution ---

type deployCandidate struct {
	BuildID      int64  `json:"build_id"`
	Digest       string `json:"candidate_digest"`
	SourceDigest string `json:"source_digest,omitempty"`
}

// candidateByDigest finds the Deploy build sealed with a candidate digest.
func candidateByDigest(ctx context.Context, app *sdk.AppCtx, digest string) (deployCandidate, error) {
	platform := app.PlatformAPI()
	if platform == nil {
		return deployCandidate{}, errors.New("the Deploy binding is unavailable; bind the Deploy app to Tests")
	}
	var out deployCandidate
	if err := sdk.CallAppResultContext(ctx, platform, "deploy", "deploy_candidate_digest",
		map[string]any{"candidate_digest": digest, "_project_id": app.CurrentProject()}, &out); err != nil {
		return out, fmt.Errorf("Deploy has no build with candidate digest %s: %s", short(digest), deployReason(err))
	}
	if out.BuildID < 1 || out.Digest != digest {
		return out, fmt.Errorf("Deploy returned build %d for candidate digest %s", out.BuildID, short(digest))
	}
	return out, nil
}

func resolveCandidate(ctx context.Context, app *sdk.AppCtx, ref *CandidateRef) (_ deployCandidate, failure error) {
	selector, buildID, err := ref.normalize()
	if err != nil {
		return deployCandidate{}, err
	}
	platform := app.PlatformAPI()
	if platform == nil {
		return deployCandidate{}, errors.New("the Deploy binding is unavailable; bind the Deploy app to Tests to use target.candidate")
	}
	if selector == "latest_succeeded" {
		// Deploy 0.32.0+ resolves the latest sealed build itself.
		args := map[string]any{"id": ref.DeploymentID, "build": "latest_succeeded", "_project_id": app.CurrentProject()}
		if ref.Environment != "" {
			args["environment"] = ref.Environment
		}
		var native deployCandidate
		err := sdk.CallAppResultContext(ctx, platform, "deploy", "deploy_candidate_digest", args, &native)
		if err == nil && native.BuildID > 0 && digestPattern.MatchString(native.Digest) {
			return native, nil
		}
		// Deploy before 0.32.0 has no build selector; fall back to reading the
		// build list. Keep Deploy's answer in case the fallback fails too.
		if err != nil {
			defer func() {
				if failure != nil && !strings.Contains(failure.Error(), deployReason(err)) {
					failure = fmt.Errorf("%w (latest_succeeded lookup: %s)", failure, deployReason(err))
				}
			}()
		}
	}
	if selector != "id" {
		args := map[string]any{"id": ref.DeploymentID, "_project_id": app.CurrentProject()}
		if ref.Environment != "" {
			args["environment"] = ref.Environment
		}
		var status struct {
			Builds []struct {
				ID     int64  `json:"id"`
				Status string `json:"status"`
			} `json:"builds"`
			Current *struct {
				BuildID int64 `json:"build_id"`
			} `json:"current_release"`
		}
		if err := sdk.CallAppResultContext(ctx, platform, "deploy", "deploy_status", args, &status); err != nil {
			return deployCandidate{}, fmt.Errorf("could not read Deploy deployment %d: %s. Check that the Deploy app is bound to Tests and the deployment id is right", ref.DeploymentID, deployReason(err))
		}
		switch selector {
		case "live":
			if status.Current == nil || status.Current.BuildID == 0 {
				return deployCandidate{}, fmt.Errorf("Deploy deployment %d has no live release", ref.DeploymentID)
			}
			buildID = status.Current.BuildID
		case "latest_succeeded":
			for _, build := range status.Builds {
				if build.Status == "succeeded" {
					buildID = build.ID
					break
				}
			}
			if buildID == 0 {
				return deployCandidate{}, fmt.Errorf("Deploy deployment %d has no succeeded build", ref.DeploymentID)
			}
		}
	}
	var out deployCandidate
	if err := sdk.CallAppResultContext(ctx, platform, "deploy", "deploy_candidate_digest",
		map[string]any{"build_id": buildID, "_project_id": app.CurrentProject()}, &out); err != nil {
		return deployCandidate{}, fmt.Errorf("Deploy could not verify build %d: %s", buildID, deployReason(err))
	}
	if out.BuildID != buildID || !digestPattern.MatchString(out.Digest) {
		return deployCandidate{}, fmt.Errorf("Deploy returned an invalid digest for build %d", buildID)
	}
	return out, nil
}

// checkDeployBinding confirms at save time that Tests can read the
// candidate's Deploy deployment, so a missing binding is reported before a run.
func checkDeployBinding(ctx context.Context, app *sdk.AppCtx, ref *CandidateRef) error {
	platform := app.PlatformAPI()
	if platform == nil {
		return errors.New("target.candidate needs the Deploy app: bind Deploy to Tests, then save the suite again")
	}
	args := map[string]any{"id": ref.DeploymentID, "_project_id": app.CurrentProject()}
	if ref.Environment != "" {
		args["environment"] = ref.Environment
	}
	var status map[string]any
	if err := sdk.CallAppResultContext(ctx, platform, "deploy", "deploy_status", args, &status); err != nil {
		return fmt.Errorf("target.candidate needs the Deploy app, and Deploy deployment %d could not be read: %s. Bind Deploy to Tests and check the deployment id, then save again", ref.DeploymentID, deployReason(err))
	}
	return nil
}

// deployReason returns Deploy's own error text, trimmed for a run message.
func deployReason(err error) string {
	text := strings.TrimSpace(err.Error())
	text, _, _ = strings.Cut(text, "\n")
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return text
}

func (t *TargetConfig) allowsRead(app, tool string) bool {
	if t.Environment == nil {
		return false
	}
	if probe := t.Environment.CandidateProbe; probe != nil && probe.App == app && probe.Tool == tool {
		return true
	}
	for _, permitted := range t.Environment.ReadTools[app] {
		if permitted == tool {
			return true
		}
	}
	return false
}

// queuePrep carries what queueing resolved through other apps.
type queuePrep struct {
	Fixture   environmentDefinition
	BuildID   int64
	Candidate *CandidateRef
}

func short(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	if digest == "" {
		return "(any)"
	}
	return digest
}

// generatedReleaseConfig derives the frozen runtime configuration for an
// isolated suite target.
func generatedReleaseConfig(t *TargetConfig, prep queuePrep) (*ReleaseConfig, error) {
	env := t.Environment
	if env.Pinned == nil {
		return nil, errors.New("the isolated environment is not pinned yet; save the suite again")
	}
	probe := env.CandidateProbe
	if probe == nil {
		if !containsString(env.Apps, "deploy") || prep.BuildID == 0 {
			return nil, errors.New("isolated mode needs a candidate_probe, or the deploy app plus a target.candidate")
		}
		probe = &ToolProbe{App: "deploy", Tool: "deploy_candidate_digest", Input: map[string]any{"build_id": prep.BuildID}, Path: "/candidate_digest"}
	}
	readTools := map[string][]string{}
	for app, tools := range env.ReadTools {
		readTools[app] = append([]string(nil), tools...)
	}
	if !containsString(readTools[probe.App], probe.Tool) {
		readTools[probe.App] = append(readTools[probe.App], probe.Tool)
	}
	browser := t.Browser
	if browser == nil {
		browser = &BrowserConfig{}
	}
	if err := browser.validate(); err != nil {
		return nil, err
	}
	return &ReleaseConfig{
		Browser: browser, EnvironmentID: env.Pinned.EnvironmentID, EnvironmentRevision: env.Pinned.SpecVersion,
		AppVersions: env.AppVersions, IdentityProbe: *probe, ReadTools: readTools, Generated: true,
	}, nil
}

// generatedSpec is the EnvironmentSpec Tests pins for an isolated target.
func generatedSpec(t *TargetConfig, installs map[string]int64) (map[string]any, error) {
	env := t.Environment
	ids := make([]int64, 0, len(env.Apps))
	for _, app := range env.Apps {
		id, ok := installs[app]
		if !ok {
			return nil, fmt.Errorf("app %q is not installed in this project", app)
		}
		ids = append(ids, id)
	}
	spec := map[string]any{
		"version": 1, "ttl_seconds": env.TTLSeconds, "app_install_ids": ids,
		"network_mode": "block", "integration_mode": "mock",
	}
	if len(t.Targets) > 0 {
		targets := map[string]any{}
		for name, target := range t.Targets {
			targets[name] = map[string]any{"app": target.App, "path": target.Path}
		}
		spec["targets"] = targets
	}
	if len(env.Seeds) > 0 {
		spec["seeds"] = env.Seeds
	}
	if len(env.Integrations) > 0 {
		spec["integration_bindings"] = env.bindingSpec()
	}
	if env.Data != nil && env.Data.SnapshotID != "" {
		spec["snapshot_id"] = env.Data.SnapshotID
	}
	return spec, nil
}

// pinEnvironment creates or updates the Environments definition for an
// isolated suite target and records its version and digest on the suite.
func (a *App) pinEnvironment(ctx context.Context, app *sdk.AppCtx, v *Suite, previous *Suite) error {
	if v.Target == nil || v.Target.Mode != "isolated" {
		return nil
	}
	if err := v.Target.validate(); err != nil {
		return err
	}
	env := environmentClient{app.PlatformAPI()}
	var catalog struct {
		Apps []struct {
			InstallID int64  `json:"install_id"`
			Name      string `json:"name"`
		} `json:"apps"`
	}
	if err := env.call(ctx, "environment_catalog", map[string]any{}, &catalog); err != nil {
		return errors.New("isolated targets need a bound Environments app (0.11.0 or newer)")
	}
	installs := map[string]int64{}
	for _, item := range catalog.Apps {
		installs[item.Name] = item.InstallID
	}
	spec, err := generatedSpec(v.Target, installs)
	if err != nil {
		return err
	}
	name := "tests: " + strings.TrimSpace(v.Name)
	if len(name) > 120 {
		name = name[:120]
	}
	var saved environmentDefinition
	if previous != nil && previous.Target != nil && previous.Target.Environment != nil && previous.Target.Environment.Pinned != nil {
		err = env.call(ctx, "environment_update", map[string]any{"id": previous.Target.Environment.Pinned.EnvironmentID, "name": name, "spec": spec}, &saved)
	} else {
		err = env.call(ctx, "environment_create", map[string]any{"name": name, "description": "Generated by the Tests app for an isolated suite target.", "spec": spec}, &saved)
	}
	if err != nil {
		return fmt.Errorf("Environments rejected the generated environment: %v", err)
	}
	if !slugPattern.MatchString(saved.ID) || saved.SpecVersion < 1 {
		return errors.New("Environments returned an invalid definition")
	}
	if !digestPattern.MatchString(saved.SpecDigest) {
		return errors.New("isolated targets need Environments 0.11.0 or newer (spec digests)")
	}
	v.Target.Environment.Pinned = &PinnedEnvironment{EnvironmentID: saved.ID, SpecVersion: saved.SpecVersion, SpecDigest: saved.SpecDigest}
	return nil
}

// loadPinnedEnvironment reads the pinned definition and reports drift precisely.
func loadPinnedEnvironment(ctx context.Context, app *sdk.AppCtx, pinned *PinnedEnvironment) (environmentDefinition, error) {
	var def environmentDefinition
	if pinned == nil {
		return def, errors.New("the isolated environment is not pinned yet; save the suite again")
	}
	if err := (environmentClient{app.PlatformAPI()}).call(ctx, "environment_get", map[string]any{"id": pinned.EnvironmentID}, &def); err != nil {
		return def, fmt.Errorf("cannot read pinned environment %s", pinned.EnvironmentID)
	}
	if def.SpecVersion != pinned.SpecVersion || def.SpecDigest != pinned.SpecDigest {
		return def, fmt.Errorf("environment %s: pinned v%d/%s, found v%d/%s; save the suite to re-pin",
			pinned.EnvironmentID, pinned.SpecVersion, short(pinned.SpecDigest), def.SpecVersion, short(def.SpecDigest))
	}
	if len(def.Spec) == 0 || string(def.Spec) == "null" {
		return def, fmt.Errorf("environment %s v%d has an empty spec", pinned.EnvironmentID, def.SpecVersion)
	}
	return def, nil
}
