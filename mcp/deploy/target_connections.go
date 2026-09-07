package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

type genericTargetConfig struct {
	Connections   map[string]int64      `json:"connections,omitempty"`
	Pipeline      *commandPipeline      `json:"pipeline,omitempty"`
	Publisher     *integrationPublisher `json:"publisher,omitempty"`
	ReleasePolicy *releasePolicy        `json:"release_policy,omitempty"`
}

func genericTarget(raw string) (genericTargetConfig, error) {
	var t genericTargetConfig
	e := json.Unmarshal([]byte(defaultStr(raw, "{}")), &t)
	if e != nil {
		return t, e
	}
	return t, validateGenericTarget(t)
}
func selectedIntegration(role, raw string) (*sdk.BoundIntegration, error) {
	t, e := genericTarget(raw)
	if e != nil {
		return nil, e
	}
	id, explicit := t.Connections[role]
	if !explicit {
		return boundIntegration(role)
	}
	if id <= 0 || globalCtx == nil {
		return nil, errors.New("invalid target connection")
	}
	for _, bound := range globalCtx.IntegrationsFor(role) {
		if bound != nil && bound.Kind == "integration" && bound.ConnectionID == id {
			return bound, nil
		}
	}
	return nil, fmt.Errorf("connection %d is not an authorized binding for role %s", id, role)
}
func freezeTargetConnections(raw string) (string, error) {
	t, e := genericTarget(raw)
	if e != nil {
		return "", e
	}
	if t.Connections == nil {
		t.Connections = map[string]int64{}
	}
	for role := range t.Connections {
		if _, e = selectedIntegration(role, raw); e != nil {
			return "", e
		}
	}
	for _, role := range []string{"app_store", "play_store", "publisher"} {
		if _, ok := t.Connections[role]; !ok {
			if b, e := boundIntegration(role); e == nil {
				t.Connections[role] = b.ConnectionID
			}
		}
	}
	if len(t.Connections) == 0 {
		return defaultStr(raw, "{}"), nil
	}
	var all map[string]any
	if e = json.Unmarshal([]byte(defaultStr(raw, "{}")), &all); e != nil {
		return "", e
	}
	all["connections"] = t.Connections
	return mustJSON(all), nil
}
func selectedCredentials(role, raw string) (*sdk.ConnectionCredentials, error) {
	b, e := selectedIntegration(role, raw)
	if e != nil {
		return nil, e
	}
	return globalCtx.PlatformAPI().GetConnectionCredentials(b.ConnectionID)
}
func releaseBindingConfig(meta *mobileReleaseMeta) string {
	return mustJSON(map[string]any{"connections": meta.Connections})
}

func validateBuildDestination(d *Deployment, b *Build) error {
	old, e := genericTarget(b.TargetConfigJSON)
	if e != nil {
		return e
	}
	if len(old.Connections) == 0 {
		return nil
	}
	frozen, e := freezeTargetConnections(d.TargetConfigJSON)
	if e != nil {
		return e
	}
	next, e := genericTarget(frozen)
	if e != nil {
		return e
	}
	roles := []string{"app_store", "play_store"}
	if old.Publisher != nil {
		roles = []string{old.Publisher.Role}
		if publisherIdentity(old.Publisher) != publisherIdentity(next.Publisher) {
			return errors.New("publisher application changed since build")
		}
	}
	for _, role := range roles {
		if id, ok := old.Connections[role]; ok && id != next.Connections[role] {
			return errors.New("publisher account changed since build; create a new build for the selected account")
		}
	}
	return nil
}

func validateGenericTarget(t genericTargetConfig) error {
	if t.Pipeline != nil {
		if len(t.Pipeline.Outputs) == 0 {
			return errors.New("pipeline requires artifact outputs")
		}
		if _, err := pipelineConfig(mustJSON(map[string]any{"pipeline": t.Pipeline})); err != nil {
			return err
		}
	}
	if p := t.Publisher; p != nil {
		if p.Role == "" || p.Provider == "" || len(p.Identity) == 0 || p.Publish.Tool == "" {
			return errors.New("publisher requires role, provider, identity and publish tool")
		}
		if _, err := pipelineConfig(mustJSON(map[string]any{"pipeline": commandPipeline{Prepare: p.Upload}})); err != nil {
			return err
		}
		if p.Observe != nil && p.Observe.Action.Tool == "" {
			return errors.New("availability observer requires an integration tool")
		}
	}
	if p := t.ReleasePolicy; p != nil {
		if p.Version == "" || len(p.Channels) == 0 {
			return errors.New("release policy requires version and channels")
		}
		if p.AutoChannel != "" {
			rule, ok := p.Channels[p.AutoChannel]
			if !ok || !rule.Automatic {
				return errors.New("auto_channel requires an explicit automatic channel rule")
			}
		}
		for channel, rule := range p.Channels {
			if channel == "" || rule.MinimumAgeSeconds < 0 || rule.MaximumObservationAgeSeconds < 0 {
				return errors.New("release policy contains an invalid channel or negative duration")
			}
			for _, name := range rule.RequiredTests {
				found := false
				if t.Pipeline != nil {
					for _, test := range t.Pipeline.Tests {
						if name == test.Name {
							found = true
						}
					}
				}
				if !found {
					return fmt.Errorf("policy test %s is not declared in the pipeline", name)
				}
			}
		}
	}
	return nil
}
