package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func webFixtureCatalog() []WebFixtureCatalogItem {
	return []WebFixtureCatalogItem{{
		ID:          "patreon",
		Name:        "Patreon",
		Description: "Memberships, creator posts and scheduling, payouts, and member conversations.",
		Version:     "1.0.0",
		Scenarios: []WebFixtureScenario{
			{ID: "new-visitor", Name: "New visitor", Description: "Signed in without an existing membership."},
			{ID: "signed-out", Name: "Signed out", Description: "Authentication is required before joining or messaging."},
			{ID: "existing-member", Name: "Existing member", Description: "Already subscribed to the Supporter tier."},
			{ID: "payment-failure", Name: "Payment failure", Description: "Checkout declines the simulated payment."},
		},
		SeedFields: []WebFixtureSeedField{
			{Name: "viewer_name", Label: "Viewer name", Default: "Alex Morgan"},
			{Name: "creator_slug", Label: "Creator handle", Default: "studio-north", Required: true},
			{Name: "creator_name", Label: "Creator name", Default: "Studio North", Required: true},
			{Name: "creator_tagline", Label: "Creator tagline", Default: "Independent films and stories from the edge of the map."},
		},
	}}
}

func validateWebFixtureSpec(spec WebFixtureSpec) error {
	if spec.Pack != "patreon" {
		return fmt.Errorf("unsupported pack %q", spec.Pack)
	}
	scenario := spec.Scenario
	if scenario == "" {
		scenario = "new-visitor"
	}
	for _, item := range webFixtureCatalog()[0].Scenarios {
		if item.ID == scenario {
			return nil
		}
	}
	return fmt.Errorf("unsupported scenario %q", scenario)
}

func (s *service) createWebFixtures(run *Run, spec EnvironmentSpec) error {
	for _, fixtureSpec := range spec.WebFixtures {
		if fixtureSpec.Version == "" {
			fixtureSpec.Version = "1.0.0"
		}
		if fixtureSpec.Scenario == "" {
			fixtureSpec.Scenario = "new-visitor"
		}
		state, err := initialWebFixtureState(fixtureSpec)
		if err != nil {
			return err
		}
		if spec.SnapshotID != "" {
			if restored, ok, err := s.db.webFixtureSnapshot(spec.SnapshotID, fixtureSpec.ID); err != nil {
				return err
			} else if ok {
				state = restored
			}
		}
		if fixtureSpec.Pack == "patreon" {
			state = normalizePatreonState(fixtureSpec, state)
		}
		x := &WebFixtureInstance{RunID: run.ID, ID: fixtureSpec.ID, Pack: fixtureSpec.Pack, Version: fixtureSpec.Version, Scenario: fixtureSpec.Scenario, Seed: fixtureSpec.Seed, State: cloneJSONMap(state), InitialState: cloneJSONMap(state), Status: "starting", Token: token(48)}
		if err := s.db.createWebFixture(x); err != nil {
			return err
		}
	}
	s.decorateRun(run)
	return nil
}

func initialWebFixtureState(spec WebFixtureSpec) (map[string]any, error) {
	switch spec.Pack {
	case "patreon":
		return initialPatreonState(spec), nil
	default:
		return nil, fmt.Errorf("unsupported web fixture pack %q", spec.Pack)
	}
}

func cloneJSONMap(value map[string]any) map[string]any {
	raw, _ := json.Marshal(value)
	var cloned map[string]any
	_ = json.Unmarshal(raw, &cloned)
	return cloned
}

func (s *service) decorateRun(run *Run) {
	if run == nil {
		return
	}
	fixtures, err := s.db.listWebFixtures(run.ID)
	if err != nil {
		return
	}
	for i := range fixtures {
		path := webFixturePath(fixtures[i])
		fixtures[i].PreviewPath = path
		if gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/"); gateway != "" {
			fixtures[i].TestURL = gateway + path
		} else {
			fixtures[i].TestURL = path
		}
	}
	run.WebFixtures = fixtures
	protocolFixtures, err := s.db.listProtocolFixtures(run.ID)
	if err == nil {
		run.ProtocolFixtures = protocolFixtures
	}
}

func webFixturePath(x WebFixtureInstance) string {
	prefix := "/api/apps/environments"
	if installID, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("APTEVA_INSTALL_ID")), 10, 64); err == nil && installID > 0 {
		prefix += "/_install/" + strconv.FormatInt(installID, 10)
	}
	return fmt.Sprintf("%s/fixtures/%s/%s/%s/", prefix, x.RunID, x.ID, x.Token)
}

func (s *service) fixtureDetail(runID, fixtureID string) (map[string]any, error) {
	x, err := s.db.getWebFixture(runID, fixtureID)
	if err != nil || x == nil {
		if err == nil {
			err = errors.New("web fixture not found")
		}
		return nil, err
	}
	s.decorateFixture(x)
	events, err := s.db.listWebFixtureEvents(runID, fixtureID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"fixture": x, "state": x.State, "events": events}, nil
}

func (s *service) decorateFixture(x *WebFixtureInstance) {
	path := webFixturePath(*x)
	x.PreviewPath = path
	if gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/"); gateway != "" {
		x.TestURL = gateway + path
	} else {
		x.TestURL = path
	}
}

func (s *service) resetFixture(runID, fixtureID string) error {
	s.fixtureMu.Lock()
	defer s.fixtureMu.Unlock()
	return s.db.resetWebFixture(runID, fixtureID)
}

func (a *App) handleFixture(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/fixtures/"), "/"), "/")
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	runID, fixtureID, supplied := parts[0], parts[1], parts[2]
	run, runErr := a.svc.db.getRun(runID)
	if runErr != nil || run == nil || (run.Status != "starting" && run.Status != "running") {
		http.NotFound(w, r)
		return
	}
	x, err := a.svc.db.getWebFixture(runID, fixtureID)
	if err != nil || x == nil || (x.Status != "starting" && x.Status != "running") || subtle.ConstantTimeCompare([]byte(x.Token), []byte(supplied)) != 1 {
		http.NotFound(w, r)
		return
	}
	tail := strings.Join(parts[3:], "/")
	if tail == "api/state" {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"fixture": map[string]any{"id": x.ID, "pack": x.Pack, "scenario": x.Scenario}, "state": x.State})
		return
	}
	if tail == "api/action" {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var input struct {
			Action string         `json:"action"`
			Input  map[string]any `json:"input"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			httpError(w, http.StatusBadRequest, errors.New("invalid JSON"))
			return
		}
		state, event, err := a.svc.applyFixtureAction(runID, fixtureID, input.Action, input.Input)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"state": state, "event": event})
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if x.Pack != "patreon" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; frame-ancestors 'self'")
	_, _ = w.Write([]byte(patreonFixtureHTML))
}

func (s *service) applyFixtureAction(runID, fixtureID, action string, input map[string]any) (map[string]any, *WebFixtureEvent, error) {
	s.fixtureMu.Lock()
	defer s.fixtureMu.Unlock()
	run, err := s.db.getRun(runID)
	if err != nil || run == nil || (run.Status != "starting" && run.Status != "running") {
		if err == nil {
			err = errors.New("environment run is not active")
		}
		return nil, nil, err
	}
	x, err := s.db.getWebFixture(runID, fixtureID)
	if err != nil || x == nil {
		if err == nil {
			err = errors.New("web fixture not found")
		}
		return nil, nil, err
	}
	if x.Status != "starting" && x.Status != "running" {
		return nil, nil, errors.New("web fixture is not active")
	}
	var eventType string
	var data map[string]any
	switch x.Pack {
	case "patreon":
		eventType, data, err = applyPatreonAction(x.State, action, input)
	default:
		err = fmt.Errorf("unsupported web fixture pack %q", x.Pack)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := s.db.updateWebFixtureState(runID, fixtureID, x.State); err != nil {
		return nil, nil, err
	}
	var event *WebFixtureEvent
	if eventType != "" {
		event = &WebFixtureEvent{RunID: runID, FixtureID: fixtureID, Type: eventType, Data: data, CreatedAt: time.Now().UTC()}
		if err := s.db.appendWebFixtureEvent(event); err != nil {
			return nil, nil, err
		}
	}
	return x.State, event, nil
}
