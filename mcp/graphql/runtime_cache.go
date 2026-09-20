package main

import (
	"strings"
)

func apiRuntimeKey(project, api string) string {
	return project + "\x00" + normalizeAPISlug(api)
}

func schemaRuntimeKey(project, api, environment string) string {
	return apiRuntimeKey(project, api) + "\x00" + normalizeEnvironment(environment)
}

func (a *App) cachedAPI(project, api string) (*graphqlAPI, error) {
	key := apiRuntimeKey(project, api)
	a.cacheMu.RLock()
	row := a.apiCache[key]
	generation := a.cacheGeneration[key]
	a.cacheMu.RUnlock()
	if row != nil {
		return row, nil
	}
	row, err := resolveGraphQLAPI(a.ctx.AppDB(), project, api)
	if err != nil {
		return nil, err
	}
	a.cacheMu.Lock()
	if a.apiCache == nil {
		a.apiCache = make(map[string]*graphqlAPI)
	}
	if a.cacheGeneration[key] == generation {
		a.apiCache[key] = row
	}
	a.cacheMu.Unlock()
	return row, nil
}

func (a *App) cachedSecurity(project, api string) (securityPolicy, error) {
	key := apiRuntimeKey(project, api)
	a.cacheMu.RLock()
	policy, found := a.securityCache[key]
	generation := a.cacheGeneration[key]
	a.cacheMu.RUnlock()
	if found {
		return policy, nil
	}
	policy, err := getSecurity(a.ctx.AppReadDB(), project, api)
	if err != nil {
		return securityPolicy{}, err
	}
	a.cacheMu.Lock()
	if a.securityCache == nil {
		a.securityCache = make(map[string]securityPolicy)
	}
	if a.cacheGeneration[key] == generation {
		a.securityCache[key] = policy
	}
	a.cacheMu.Unlock()
	return policy, nil
}

func (a *App) cachedPublishedSchema(project, api, environment string) (*schemaRecord, error) {
	key := schemaRuntimeKey(project, api, environment)
	apiKey := apiRuntimeKey(project, api)
	a.cacheMu.RLock()
	row, found := a.schemaRowCache[key]
	generation := a.cacheGeneration[apiKey]
	a.cacheMu.RUnlock()
	if found {
		return row, nil
	}
	row, err := getSchemaForAPI(a.ctx.AppReadDB(), project, api, environment, 0, true)
	if err != nil {
		return nil, err
	}
	a.cacheMu.Lock()
	if a.schemaRowCache == nil {
		a.schemaRowCache = make(map[string]*schemaRecord)
	}
	// A nil entry is useful too: publishing invalidates it immediately.
	if a.cacheGeneration[apiKey] == generation {
		a.schemaRowCache[key] = row
	}
	a.cacheMu.Unlock()
	return row, nil
}

func (a *App) cachedActiveRelease(project, api, environment string) (*apiRelease, error) {
	key := schemaRuntimeKey(project, api, environment)
	apiKey := apiRuntimeKey(project, api)
	a.cacheMu.RLock()
	row, found := a.releaseCache[key]
	generation := a.cacheGeneration[apiKey]
	a.cacheMu.RUnlock()
	if found {
		return row, nil
	}
	row, err := getAPIRelease(a.ctx.AppReadDB(), project, api, environment, 0, true)
	if err != nil {
		return nil, err
	}
	a.cacheMu.Lock()
	if a.releaseCache == nil {
		a.releaseCache = make(map[string]*apiRelease)
	}
	if a.cacheGeneration[apiKey] == generation {
		a.releaseCache[key] = row
	}
	a.cacheMu.Unlock()
	return row, nil
}

// invalidateRuntime is called after every successful configuration write.
// Published execution state is immutable between these explicit invalidations,
// so authenticated requests do not need TTL-based database refreshes.
func (a *App) invalidateRuntime(project, api string) {
	prefix := apiRuntimeKey(project, api)
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if a.cacheGeneration == nil {
		a.cacheGeneration = make(map[string]uint64)
	}
	a.cacheGeneration[prefix]++
	delete(a.apiCache, prefix)
	delete(a.securityCache, prefix)
	delete(a.planCache, prefix)
	for key := range a.releaseCache {
		if key == prefix || strings.HasPrefix(key, prefix+"\x00") {
			delete(a.releaseCache, key)
		}
	}
	for key := range a.schemaRowCache {
		if key == prefix || strings.HasPrefix(key, prefix+"\x00") {
			delete(a.schemaRowCache, key)
		}
	}
	for key := range a.schemaCache {
		if strings.HasPrefix(key, prefix+"\x00") {
			delete(a.schemaCache, key)
		}
	}
	for key := range a.queryCache {
		if strings.HasPrefix(key, prefix+"\x00") {
			delete(a.queryCache, key)
		}
	}
	for key := range a.operationCache {
		if strings.HasPrefix(key, prefix+"\x00") {
			delete(a.operationCache, key)
		}
	}
	for key := range a.runtimeCache {
		if strings.HasPrefix(key, prefix+"\x00") {
			delete(a.runtimeCache, key)
		}
	}
}
