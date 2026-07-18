package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type service struct {
	ctx       *sdk.AppCtx
	db        store
	fixtureMu sync.Mutex
}

func (s *service) listDefinitions() ([]Definition, error) {
	defs, err := s.db.listDefinitions()
	if err != nil {
		return nil, err
	}
	live, _ := s.liveMap()
	for i := range defs {
		if defs[i].ActiveRun != nil {
			s.decorateRun(defs[i].ActiveRun)
			if rt := live[defs[i].ActiveRun.RuntimeID]; rt != nil {
				defs[i].Runtime = rt
			}
		}
	}
	return defs, nil
}

func (s *service) getDefinition(id string) (*Definition, error) {
	d, err := s.db.getDefinition(id)
	if err != nil || d == nil {
		return d, err
	}
	if d.ActiveRun != nil {
		s.decorateRun(d.ActiveRun)
		rt, err := s.runtime().GetRuntime(d.ActiveRun.RuntimeID)
		if err == nil {
			d.Runtime = rt
		}
	}
	return d, nil
}

func (s *service) saveDefinition(d *Definition) (*Definition, error) {
	d.ID = strings.TrimSpace(d.ID)
	d.Name = strings.TrimSpace(d.Name)
	if d.ID == "" {
		d.ID = "env_" + token(10)
	}
	if !validID(d.ID) {
		return nil, errors.New("id may contain only letters, numbers, dash, underscore, and dot")
	}
	if d.Name == "" {
		return nil, errors.New("name required")
	}
	if err := validateSpec(d.Spec); err != nil {
		return nil, err
	}
	if err := s.db.saveDefinition(d); err != nil {
		return nil, err
	}
	return s.getDefinition(d.ID)
}

func (s *service) startDefinition(id string) (*Run, error) {
	d, err := s.db.getDefinition(id)
	if err != nil || d == nil {
		if err == nil {
			err = errors.New("environment not found")
		}
		return nil, err
	}
	_ = s.db.setDesired(id, "running")
	return s.start(id, "interactive", d.Spec)
}

func (s *service) start(environmentID, kind string, spec EnvironmentSpec) (run *Run, err error) {
	if err = validateSpec(spec); err != nil {
		return nil, err
	}
	if s.runtime() == nil {
		return nil, errors.New("runtime API unavailable")
	}
	if environmentID != "" {
		if active, _ := s.db.activeRun(environmentID); active != nil {
			return active, nil
		}
	}
	runtimeID := "rt_env_" + token(12)
	run = &Run{ID: "run_" + token(12), EnvironmentID: environmentID, RuntimeID: runtimeID, Kind: kind, Status: "starting", StartedAt: time.Now().UTC()}
	if err = s.db.createRun(run); err != nil {
		return nil, err
	}
	if err = s.createWebFixtures(run, spec); err != nil {
		return run, fmt.Errorf("create web fixtures: %w", err)
	}
	created := false
	defer func() {
		if err != nil {
			if created {
				_ = s.runtime().DestroyRuntime(runtimeID)
			}
			run.Status = "failed"
			run.Error = err.Error()
			_ = s.db.updateRun(run.ID, "failed", err.Error())
			_ = s.db.setWebFixturesStatus(run.ID, "failed")
			s.ctx.Emit("environment.failed", map[string]any{"environment_id": environmentID, "run_id": run.ID, "error": err.Error()})
		}
	}()
	req := sdk.RuntimeCreateRequest{ID: runtimeID, ProjectID: s.ctx.CurrentProject(), TTLSeconds: spec.TTLSeconds, AppInstallIDs: spec.AppInstallIDs, ConnectionIDs: spec.ConnectionIDs, NetworkMode: spec.NetworkMode, IntegrationMode: spec.IntegrationMode, AllowHostSuffixes: spec.AllowHostSuffixes, HTTPMocks: spec.HTTPMocks, IntegrationFixtures: spec.IntegrationFixtures, IntegrationBindings: spec.IntegrationBindings, Subscriptions: spec.Subscriptions, SnapshotID: spec.SnapshotID}
	if _, err = s.runtime().CreateRuntime(req); err != nil {
		return run, fmt.Errorf("create runtime: %w", err)
	}
	created = true
	for i, seed := range spec.Seeds {
		if strings.TrimSpace(seed.App) == "" || strings.TrimSpace(seed.Tool) == "" {
			return run, fmt.Errorf("seed %d: app and tool required", i)
		}
		var result any
		if err = s.runtime().CallRuntimeAppResult(runtimeID, seed.App, seed.Tool, seed.Input, &result); err != nil {
			return run, fmt.Errorf("seed %d %s.%s: %w", i, seed.App, seed.Tool, err)
		}
	}
	for i, agent := range spec.Agents {
		_, err = s.runtime().SpawnRuntimeAgent(runtimeID, sdk.RuntimeAgentSpawnRequest{SourceAgentID: agent.SourceAgentID, Draft: agent.Draft, Directive: agent.Directive, Alias: agent.Alias, StartPaused: agent.StartPaused, Provider: agent.Provider, Model: agent.Model})
		if err != nil {
			return run, fmt.Errorf("agent %d: %w", i, err)
		}
	}
	run.Status = "running"
	if err = s.db.updateRun(run.ID, "running", ""); err != nil {
		return run, err
	}
	_ = s.db.setWebFixturesStatus(run.ID, "running")
	s.decorateRun(run)
	s.ctx.Emit("environment.started", map[string]any{"environment_id": environmentID, "run_id": run.ID, "runtime_id": runtimeID})
	return run, nil
}

func (s *service) stopDefinition(id string) error {
	_ = s.db.setDesired(id, "stopped")
	run, err := s.db.activeRun(id)
	if err != nil || run == nil {
		return err
	}
	return s.stopRun(run)
}
func (s *service) stopRun(run *Run) error {
	_ = s.db.updateRun(run.ID, "stopping", "")
	_ = s.db.setWebFixturesStatus(run.ID, "stopping")
	err := s.runtime().DestroyRuntime(run.RuntimeID)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "not found") {
		_ = s.db.updateRun(run.ID, "failed", err.Error())
		return err
	}
	_ = s.db.updateRun(run.ID, "stopped", "")
	_ = s.db.setWebFixturesStatus(run.ID, "stopped")
	s.ctx.Emit("environment.stopped", map[string]any{"environment_id": run.EnvironmentID, "run_id": run.ID, "runtime_id": run.RuntimeID})
	return nil
}

func (s *service) reconcile(context.Context) error {
	live, err := s.liveMap()
	if err != nil {
		return err
	}
	runs, err := s.db.listRuns()
	if err != nil {
		return err
	}
	for i := range runs {
		r := &runs[i]
		if (r.Status == "starting" || r.Status == "running" || r.Status == "stopping") && live[r.RuntimeID] == nil {
			_ = s.db.updateRun(r.ID, "expired", "runtime no longer exists")
			_ = s.db.setWebFixturesStatus(r.ID, "expired")
			s.ctx.Emit("environment.expired", map[string]any{"environment_id": r.EnvironmentID, "run_id": r.ID, "runtime_id": r.RuntimeID})
		}
	}
	defs, err := s.db.listDefinitions()
	if err != nil {
		return err
	}
	for i := range defs {
		d := defs[i]
		if d.DesiredState == "running" {
			active, _ := s.db.activeRun(d.ID)
			if active == nil {
				if _, err := s.start(d.ID, "reconcile", d.Spec); err != nil {
					s.ctx.Logger().Error("environment reconcile start failed", "id", d.ID, "err", err)
				}
			}
		}
	}
	return nil
}

func (s *service) snapshot(environmentID, description string) (*Snapshot, error) {
	run, err := s.db.activeRun(environmentID)
	if err != nil || run == nil {
		if err == nil {
			err = errors.New("environment is not running")
		}
		return nil, err
	}
	x, err := s.runtime().SnapshotRuntime(run.RuntimeID, sdk.RuntimeSnapshotRequest{ID: "snap_" + token(12), Description: description})
	if err != nil {
		return nil, err
	}
	out := &Snapshot{ID: x.ID, EnvironmentID: environmentID, Description: x.Description, CreatedAt: x.CreatedAt}
	if err = s.db.saveSnapshot(*out); err != nil {
		return nil, err
	}
	if err = s.db.saveWebFixtureSnapshots(out.ID, run.ID); err != nil {
		return nil, err
	}
	s.ctx.Emit("snapshot.created", out)
	return out, nil
}

func (s *service) assert(runtimeID string, a Assertion) (AssertionResult, error) {
	switch a.Type {
	case "app_state":
		var actual any
		if err := s.runtime().CallRuntimeAppResult(runtimeID, a.App, a.Tool, a.Input, &actual); err != nil {
			return AssertionResult{}, err
		}
		got := jsonPath(actual, a.Path)
		return AssertionResult{Passed: reflect.DeepEqual(got, a.Equals), Actual: got}, nil
	case "edge_call":
		calls, err := s.runtime().ListRuntimeEdgeCalls(runtimeID)
		if err != nil {
			return AssertionResult{}, err
		}
		n := 0
		for _, c := range calls {
			if (a.Method == "" || strings.EqualFold(c.Method, a.Method)) && (a.Host == "" || strings.EqualFold(c.Host, a.Host)) {
				n++
			}
		}
		min := a.MinCalls
		if min == 0 {
			min = 1
		}
		return AssertionResult{Passed: n >= min, Actual: n}, nil
	case "telemetry":
		events, err := s.runtime().ListRuntimeAgentTelemetry(runtimeID, a.AgentAlias, time.Time{}, 1000)
		if err != nil {
			return AssertionResult{}, err
		}
		n := 0
		for _, e := range events {
			if a.EventType == "" || e.Type == a.EventType {
				n++
			}
		}
		min := a.MinCalls
		if min == 0 {
			min = 1
		}
		return AssertionResult{Passed: n >= min, Actual: n}, nil
	case "web_state":
		run, err := s.db.getRun(runtimeID)
		if err != nil || run == nil {
			if err == nil {
				err = errors.New("run not found")
			}
			return AssertionResult{}, err
		}
		x, err := s.db.getWebFixture(run.ID, a.Fixture)
		if err != nil || x == nil {
			if err == nil {
				err = errors.New("web fixture not found")
			}
			return AssertionResult{}, err
		}
		got := jsonPath(x.State, a.Path)
		return AssertionResult{Passed: reflect.DeepEqual(got, a.Equals), Actual: got}, nil
	case "web_event":
		run, err := s.db.getRun(runtimeID)
		if err != nil || run == nil {
			if err == nil {
				err = errors.New("run not found")
			}
			return AssertionResult{}, err
		}
		events, err := s.db.listWebFixtureEvents(run.ID, a.Fixture)
		if err != nil {
			return AssertionResult{}, err
		}
		n := 0
		for _, event := range events {
			if a.EventType != "" && event.Type != a.EventType {
				continue
			}
			if a.Path != "" && !reflect.DeepEqual(jsonPath(event.Data, a.Path), a.Equals) {
				continue
			}
			n++
		}
		min := a.MinCalls
		if min == 0 {
			min = 1
		}
		return AssertionResult{Passed: n >= min, Actual: n}, nil
	default:
		return AssertionResult{}, fmt.Errorf("unsupported assertion type %q", a.Type)
	}
}

func (s *service) runtime() sdk.RuntimeClient { return s.ctx.RuntimeAPI() }
func (s *service) liveMap() (map[string]*sdk.RuntimeSummary, error) {
	list, err := s.runtime().ListRuntimes()
	if err != nil {
		return nil, err
	}
	out := map[string]*sdk.RuntimeSummary{}
	for i := range list {
		rt := list[i]
		out[rt.ID] = &rt
	}
	return out, nil
}
func validateSpec(spec EnvironmentSpec) error {
	if spec.TTLSeconds != 0 && (spec.TTLSeconds < 60 || spec.TTLSeconds > 86400) {
		return errors.New("ttl_seconds must be between 60 and 86400")
	}
	if spec.NetworkMode == "" {
		spec.NetworkMode = sdk.RuntimeNetworkBlock
	}
	seen := map[string]bool{}
	for i, fixture := range spec.WebFixtures {
		if !validID(fixture.ID) {
			return fmt.Errorf("web fixture %d: valid id required", i)
		}
		if seen[fixture.ID] {
			return fmt.Errorf("web fixture %d: duplicate id %q", i, fixture.ID)
		}
		seen[fixture.ID] = true
		if err := validateWebFixtureSpec(fixture); err != nil {
			return fmt.Errorf("web fixture %s: %w", fixture.ID, err)
		}
	}
	return nil
}
func validID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
func token(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}
func jsonPath(v any, path string) any {
	cur := v
	for _, p := range strings.Split(strings.Trim(path, "."), ".") {
		if p == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}
