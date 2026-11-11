package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type environmentClient struct {
	platform sdk.PlatformClient
}

func (e environmentClient) call(ctx context.Context, tool string, args map[string]any, out any) error {
	if e.platform == nil {
		return errors.New("Environments binding is unavailable")
	}
	return sdk.CallAppResultContext(ctx, e.platform, "environments", tool, args, out)
}

func (a *App) queue(ctx context.Context, app *sdk.AppCtx, suite int64, trigger, key string, opt QueueOptions) (Run, error) {
	s := store{app.AppDB(), app.CurrentProject()}
	if key != "" {
		r, err := s.existingRequest(suite, key, opt)
		if !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
	}
	checks, err := s.checks(suite)
	if err != nil {
		return Run{}, err
	}
	for _, check := range checks {
		if check.Enabled && hasBrowser(check) {
			if err = a.browserCapabilities(ctx, app); err != nil {
				return Run{}, err
			}
			break
		}
	}
	v, err := s.getSuite(suite)
	if err != nil {
		return Run{}, err
	}
	var definition environmentDefinition
	if v.Release != nil {
		if !digestPattern.MatchString(opt.CandidateDigest) {
			return Run{}, errors.New("release run requires a lowercase SHA-256 candidate_digest")
		}
		env := environmentClient{app.PlatformAPI()}
		if err = env.call(ctx, "environment_get", map[string]any{"id": v.Release.EnvironmentID}, &definition); err != nil {
			return Run{}, errors.New("cannot resolve the bound environment definition")
		}
	}
	return s.queueRelease(suite, trigger, key, opt, definition)
}

func (a *App) releaseSpec(ctx context.Context, app *sdk.AppCtx, suite int64) (map[string]any, error) {
	s := store{app.AppDB(), app.CurrentProject()}
	v, err := s.getSuite(suite)
	if err != nil {
		return nil, err
	}
	if v.Release == nil {
		return nil, errors.New("suite has no release configuration")
	}
	checks, err := s.checks(suite)
	if err != nil {
		return nil, err
	}
	var enabled []Check
	for _, check := range checks {
		if check.Enabled {
			enabled = append(enabled, check)
		}
	}
	env := environmentClient{app.PlatformAPI()}
	var definition environmentDefinition
	if err = env.call(ctx, "environment_get", map[string]any{"id": v.Release.EnvironmentID}, &definition); err != nil {
		return nil, errors.New("cannot resolve the bound environment definition")
	}
	if err = checkFixture(definition, v.Release); err != nil {
		return nil, err
	}
	return map[string]any{"suite_id": suite, "suite_revision": suiteRevision(v, enabled),
		"fixture_revision": canonicalDigest(definition.Spec)}, nil
}

type releaseSession struct {
	env           environmentClient
	id            string
	runtimeID     string
	config        *ReleaseConfig
	gateway       *url.URL
	contract      contract
	identities    map[string]string
	targets       map[string]string
	browserValues map[string]string
}

func (s *releaseSession) invoke(ctx context.Context, app, tool string, input map[string]any) (any, error) {
	var out any
	if err := s.env.call(ctx, "environment_call", map[string]any{
		"run_id": s.id, "app": app, "tool": tool, "input": input,
	}, &out); err != nil {
		return nil, errors.New("bound app call failed")
	}
	return out, nil
}

func (s *releaseSession) probe(ctx context.Context, p ToolProbe) (string, error) {
	value, err := s.invoke(ctx, p.App, p.Tool, p.Input)
	if err != nil {
		return "", err
	}
	got, _ := pointer(value, p.Path)
	text, ok := got.(string)
	if !ok || text == "" || len(text) > 8192 || strings.ContainsAny(text, "\r\n") {
		return "", errors.New("runtime probe did not return a usable string")
	}
	return text, nil
}

func runtimeGateway(raw, runtimeID, app, suffix string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.RawPath != "" || u.Port() == "" {
		return nil, errors.New("invalid environment Gateway endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	prefix := "/api/environment-app-public/" + runtimeID + "/api/apps/" + app + "/_install/"
	install := strings.TrimPrefix(u.Path, prefix)
	if ip == nil || !ip.IsLoopback() || !strings.HasPrefix(u.Path, prefix) ||
		install == "" || strings.Trim(install, "0123456789") != "" {
		return nil, errors.New("endpoint does not address the selected runtime app")
	}
	u.Path += strings.TrimSuffix(suffix, "/")
	return u, nil
}

func (a *App) prepareRelease(ctx context.Context, s store, r *Run, env environmentClient) (*releaseSession, error) {
	frozen, err := s.getReleaseSnapshot(r.ID)
	if err != nil || frozen.Config == nil {
		return nil, errors.New("release snapshot is unavailable")
	}
	session := &releaseSession{env: env, config: frozen.Config, identities: map[string]string{}}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return nil, err
	}
	r.Release.EnvironmentRun = "run_tests_" + hex.EncodeToString(id[:])
	session.id = r.Release.EnvironmentRun
	if err = s.saveReleaseRecord(r.ID, r.Release, true); err != nil {
		return nil, err
	}
	// The environment has a known ID before the call. A timed-out startup can
	// therefore be reconciled, and the environment's TTL bounds orphaned runs.
	var started struct {
		ID        string `json:"id"`
		RuntimeID string `json:"runtime_id"`
		Status    string `json:"status"`
	}
	var spec map[string]any
	if err = json.Unmarshal(frozen.Fixture, &spec); err != nil {
		return nil, errors.New("could not read the frozen environment fixture")
	}
	// Only seed inputs can reference the candidate. Runtime networking,
	// installations and connection settings remain the frozen policy.
	if seeds, ok := spec["seeds"].([]any); ok {
		for _, value := range seeds {
			seed, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("invalid environment seed")
			}
			if input, exists := seed["input"]; exists {
				expanded, err := expandValue(input, map[string]any{"candidate_digest": r.Release.CandidateDigest})
				if err != nil {
					return nil, err
				}
				seed["input"] = expanded
			}
		}
	}
	startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	err = env.call(startCtx, "environment_run_create", map[string]any{"kind": "test", "request_id": session.id, "spec": spec}, &started)
	cancel()
	if err != nil || started.ID != session.id || started.Status != "running" || !slugPattern.MatchString(started.RuntimeID) {
		return nil, errors.New("environment startup failed")
	}
	r.Release.RuntimeID = started.RuntimeID
	session.runtimeID = started.RuntimeID
	var live struct {
		Runtime *sdk.RuntimeSummary `json:"runtime"`
	}
	if err = env.call(ctx, "environment_run_get", map[string]any{"id": session.id}, &live); err != nil ||
		live.Runtime == nil || live.Runtime.ID != started.RuntimeID || live.Runtime.NetworkMode != sdk.RuntimeNetworkBlock ||
		live.Runtime.IntegrationMode != "mock" || len(live.Runtime.ManagedMCPs) > 0 {
		return nil, errors.New("runtime did not report the required isolation policy")
	}
	endpoints, versions, err := a.inspectRuntimeApps(ctx, session, live.Runtime.Apps)
	if err != nil {
		return nil, err
	}
	r.Release.AppVersions = versions
	gatewayCopy := *endpoints[frozen.Config.GatewayApp]
	session.gateway = &gatewayCopy
	session.gateway.Path += strings.TrimSuffix(frozen.Config.GatewayPath, "/")
	if frozen.Config.Browser != nil {
		var resolved struct {
			RuntimeID string            `json:"runtime_id"`
			Targets   map[string]string `json:"targets"`
		}
		if err = env.call(ctx, "environment_run_targets", map[string]any{"run_id": session.id}, &resolved); err != nil || resolved.RuntimeID != session.runtimeID {
			return nil, errors.New("could not resolve the isolated browser targets")
		}
		var fixture struct {
			Targets map[string]struct {
				App  string `json:"app"`
				Path string `json:"path"`
			} `json:"targets"`
		}
		if err = json.Unmarshal(frozen.Fixture, &fixture); err != nil || len(fixture.Targets) == 0 || len(fixture.Targets) != len(resolved.Targets) {
			return nil, errors.New("browser target snapshot is unavailable")
		}
		for name, target := range fixture.Targets {
			base, ok := endpoints[target.App]
			if !ok || validateGatewayPath(target.Path) != nil || target.Path == "" || target.Path == "/" {
				return nil, errors.New("invalid bound browser target")
			}
			copy := *base
			copy.Path += strings.TrimSuffix(target.Path, "/")
			if copy.String() != resolved.Targets[name] {
				return nil, errors.New("browser target does not match the frozen runtime")
			}
		}
		session.targets = resolved.Targets
		session.browserValues = map[string]string{}
		for name, probe := range frozen.Config.Browser.ValueRefs {
			value, err := session.probe(ctx, probe)
			if err != nil {
				return nil, errors.New("could not resolve a browser value reference")
			}
			session.browserValues[name] = value
		}
	}
	if len(frozen.Config.OpenAPI) > 0 {
		session.contract, err = parseContract(frozen.Config.OpenAPI)
		if err != nil {
			return nil, err
		}
	}
	if err = session.verifyCandidate(ctx, r.Release); err != nil {
		return nil, err
	}
	for name, identity := range frozen.Config.Identities {
		token, err := session.probe(ctx, identity.ToolProbe)
		if err != nil {
			return nil, fmt.Errorf("could not resolve runtime identity %q", name)
		}
		session.identities[name] = identity.Prefix + token
	}
	return session, s.saveReleaseRecord(r.ID, r.Release, true)
}

func (a *App) inspectRuntimeApps(ctx context.Context, session *releaseSession, apps []sdk.RuntimeApp) (map[string]*url.URL, map[string]string, error) {
	if len(apps) != len(session.config.AppVersions) {
		return nil, nil, errors.New("runtime app set does not match the version pins")
	}
	endpoints := make(map[string]*url.URL, len(apps))
	versions := make(map[string]string, len(apps))
	for _, app := range apps {
		want, ok := session.config.AppVersions[app.Name]
		if !ok || app.Kind != "install" || app.Status != "running" || endpoints[app.Name] != nil {
			return nil, nil, errors.New("runtime app set does not match the version pins")
		}
		var endpoint sdk.RuntimeAppEndpoint
		if err := session.env.call(ctx, "environment_run_endpoint", map[string]any{"run_id": session.id, "app": app.Name}, &endpoint); err != nil {
			return nil, nil, fmt.Errorf("could not resolve endpoint for runtime app %q", app.Name)
		}
		base, err := runtimeGateway(endpoint.AppURL, session.runtimeID, app.Name, "")
		if err != nil {
			return nil, nil, err
		}
		version, err := a.inspectAppVersion(ctx, base, app.Name)
		if err != nil || version != want {
			return nil, nil, fmt.Errorf("runtime app %q did not report pinned version %q", app.Name, want)
		}
		endpoints[app.Name], versions[app.Name] = base, version
	}
	return endpoints, versions, nil
}

func (a *App) inspectAppVersion(ctx context.Context, base *url.URL, name string) (string, error) {
	target := *base
	target.Path += "/mcp"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.runtimeHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var reply struct {
		Result struct {
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&reply) != nil ||
		reply.Result.ServerInfo.Name != name || reply.Result.ServerInfo.Version == "" {
		return "", errors.New("invalid runtime MCP identity")
	}
	return reply.Result.ServerInfo.Version, nil
}

func (s *releaseSession) verifyCandidate(ctx context.Context, record *ReleaseRecord) error {
	observed, err := s.probe(ctx, s.config.IdentityProbe)
	if err != nil || !digestPattern.MatchString(observed) {
		return errors.New("candidate identity probe failed")
	}
	record.ObservedDigest = observed
	if observed != record.CandidateDigest {
		return errors.New("runtime candidate digest does not match the requested candidate")
	}
	return nil
}

func (s *releaseSession) setCoverage(r *ReleaseRecord, covered map[string]bool) {
	for _, op := range s.contract {
		if covered[op.id] {
			r.Covered = append(r.Covered, op.id)
		} else {
			r.Missing = append(r.Missing, op.id)
		}
	}
	sort.Strings(r.Covered)
	sort.Strings(r.Missing)
}

func stopEnvironment(ctx context.Context, env environmentClient, id string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	var stopped struct {
		OK           bool   `json:"ok"`
		RuntimeState string `json:"runtime_state"`
	}
	err := env.call(ctx, "environment_run_stop", map[string]any{"id": id}, &stopped)
	if err != nil || !stopped.OK || stopped.RuntimeState != "stopped" {
		return errors.New("environment cleanup pending")
	}
	return nil
}

func (a *App) reconcileEnvironmentCleanup(ctx context.Context, app *sdk.AppCtx) {
	s := store{app.AppDB(), app.CurrentProject()}
	runs, err := s.cleanupCandidates()
	if err != nil {
		app.Logger().Warn("could not load environment cleanup queue", "error", err)
		return
	}
	env := environmentClient{app.PlatformAPI()}
	for _, r := range runs {
		if ctx.Err() != nil {
			return
		}
		if r.Release == nil {
			continue
		}
		// The maximum release-environment TTL is 15 minutes. Keep retrying
		// cleanup for 20 minutes, then leave expiration to the platform.
		finished, _ := time.Parse(time.RFC3339Nano, r.FinishedAt)
		if time.Since(finished) > 20*time.Minute || stopEnvironment(ctx, env, r.Release.EnvironmentRun) == nil {
			_, _ = s.db.Exec(`UPDATE test_runs SET cleanup_pending=0 WHERE project_id=? AND id=?`, s.project, r.ID)
		}
	}
}
