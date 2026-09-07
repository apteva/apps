package main

import (
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// One cache per project-scoped install; the lock also coalesces concurrent loads.
func (s *service) catalog() (map[string]any, error) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if s.catalogValue != nil && time.Now().Before(s.catalogUntil) {
		return s.catalogValue, nil
	}
	out := map[string]any{"assertion_types": assertionTypeCatalog(), "web_fixtures": webFixtureCatalog(), "protocol_fixtures": protocolFixtureCatalog()}
	var mu sync.Mutex
	set := func(key string, value any, err error) error {
		if err == nil {
			mu.Lock()
			out[key] = value
			mu.Unlock()
		}
		return err
	}
	tasks := []func() error{
		func() error {
			v, e := s.runtime().ListRuntimeCatalogApps(s.ctx.CurrentProject())
			return set("apps", v, e)
		},
		func() error {
			v, e := s.ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{ProjectID: s.ctx.CurrentProject()})
			return set("connections", v, e)
		},
		func() error { v, e := s.runtime().ListRuntimeCatalogIntegrations(); return set("integrations", v, e) },
		func() error {
			v, e := s.runtime().ListRuntimeCatalogManagedMCPServers(s.ctx.CurrentProject())
			return set("managed_mcps", v, e)
		},
		func() error {
			v, e := s.runtime().ListRuntimeCatalogAgents(s.ctx.CurrentProject())
			return set("agents", v, e)
		},
		func() error { v, e := s.runtime().ListRuntimeSnapshots(); return set("snapshots", v, e) },
		func() error {
			v, e := s.runtime().ListRuntimeRealtimeProviders(s.ctx.CurrentProject())
			return set("realtime_providers", v, e)
		},
	}
	results := make(chan error, len(tasks))
	for _, task := range tasks {
		go func(f func() error) { results <- f() }(task)
	}
	var first error
	for range tasks {
		if err := <-results; err != nil && first == nil {
			first = err
		}
	}
	if first != nil {
		return nil, first
	}
	s.catalogValue, s.catalogUntil = out, time.Now().Add(time.Minute)
	return out, nil
}
