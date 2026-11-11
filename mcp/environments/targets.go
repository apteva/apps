package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type RuntimeTarget struct {
	App  string `json:"app"`
	Path string `json:"path"`
}

func validateTargets(targets map[string]RuntimeTarget) error {
	if len(targets) > 12 {
		return errors.New("at most 12 runtime targets are supported")
	}
	for name, target := range targets {
		if !validID(name) || len(name) > 128 || !validID(target.App) || len(target.App) > 128 || !strings.HasPrefix(target.Path, "/") || strings.HasPrefix(target.Path, "//") ||
			strings.ContainsAny(target.Path, "?#%\\\r\n") {
			return errors.New("named targets require an app and an explicit application path prefix")
		}
		for _, part := range strings.Split(target.Path, "/") {
			if part == "." || part == ".." {
				return errors.New("target path traversal is not allowed")
			}
		}
	}
	return nil
}
func (a *App) toolRunTargets(_ *sdk.AppCtx, args map[string]any) (any, error) {
	run, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	if run.Status != "running" {
		return nil, errors.New("environment run is not running")
	}
	var raw string
	if err = a.svc.db.db.QueryRow(`SELECT targets_json FROM environment_runs WHERE id=?`, run.ID).Scan(&raw); err != nil {
		return nil, err
	}
	var targets map[string]RuntimeTarget
	if err = json.Unmarshal([]byte(raw), &targets); err != nil {
		return nil, err
	}
	resolved, err := a.svc.resolveTargets(run.RuntimeID, targets)
	if err != nil {
		return nil, err
	}
	return map[string]any{"runtime_id": run.RuntimeID, "targets": resolved}, nil
}

func (s *service) resolveTargets(runtimeID string, targets map[string]RuntimeTarget) (map[string]string, error) {
	resolved := make(map[string]string, len(targets))
	for name, target := range targets {
		endpoint, err := s.runtime().GetRuntimeAppEndpoint(runtimeID, target.App)
		if err != nil {
			return nil, err
		}
		base, err := url.Parse(endpoint.AppURL)
		if err != nil {
			return nil, errors.New("invalid runtime endpoint")
		}
		base.Path += strings.TrimSuffix(target.Path, "/")
		resolved[name] = base.String()
	}
	return resolved, nil
}
