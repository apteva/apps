package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type catalogOptions struct {
	ConnectionID  int64
	Zone          string
	ResourceClass string
}

var catalogScopeMu sync.RWMutex
var catalogScopes = map[*sdk.AppCtx]catalogOptions{}

// A private immutable context copy scopes nested provider helpers without
// changing SDK state or mutating the shared request/project context.
func scopedCatalog(ctx *sdk.AppCtx, options catalogOptions) (*sdk.AppCtx, func()) {
	if ctx == nil {
		return nil, func() {}
	}
	cp := *ctx
	catalogScopeMu.Lock()
	catalogScopes[&cp] = options
	catalogScopeMu.Unlock()
	return &cp, func() { catalogScopeMu.Lock(); delete(catalogScopes, &cp); catalogScopeMu.Unlock() }
}
func catalogScope(ctx *sdk.AppCtx) catalogOptions {
	catalogScopeMu.RLock()
	defer catalogScopeMu.RUnlock()
	return catalogScopes[ctx]
}
func catalogZone(ctx *sdk.AppCtx) string {
	if z := catalogScope(ctx).Zone; z != "" {
		return z
	}
	return "fr-par-1"
}
func catalogRequest(ctx *sdk.AppCtx, r *http.Request) (*sdk.AppCtx, func(), error) {
	id := int64(0)
	if raw := r.URL.Query().Get("provider_connection_id"); raw != "" {
		var err error
		id, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return nil, nil, fmt.Errorf("invalid provider_connection_id")
		}
	}
	scoped, release := scopedCatalog(ctx, catalogOptions{id, r.URL.Query().Get("region"), r.URL.Query().Get("resource_class")})
	return scoped, release, nil
}

type catalogCacheKey struct {
	DB                   *sql.DB
	Generation           uint64
	Connection           int64
	Provider, Tool, Args string
}
type catalogCacheEntry struct {
	done    chan struct{}
	data    json.RawMessage
	err     error
	expires time.Time
}

var catalogCacheMu sync.Mutex
var catalogCache = map[catalogCacheKey]*catalogCacheEntry{}

func isCatalogTool(tool string) bool {
	switch tool {
	case "locations_list", "images_list", "list_sizes", "server_type_list", "location_list", "list_plans", "list_instance_types", "server_types_list", "list_flavors", "list_types", "list_regions", "list_availability_zones", "image_list", "list_images", "list_os", "elastic_metal_offers_list", "elastic_metal_os_list", "dedibox_offers_list", "apple_products_list", "apple_os_list":
		return true
	}
	return false
}
func cachedCatalog(ctx *sdk.AppCtx, connection int64, provider, tool string, args map[string]any, load func() (json.RawMessage, error)) (json.RawMessage, error) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	key := catalogCacheKey{ctx.AppDB(), ctx.AppDBGeneration(), connection, provider, tool, string(encoded)}
	catalogCacheMu.Lock()
	if entry := catalogCache[key]; entry != nil && (entry.expires.IsZero() || time.Now().Before(entry.expires)) {
		catalogCacheMu.Unlock()
		<-entry.done
		return append(json.RawMessage(nil), entry.data...), entry.err
	}
	if len(catalogCache) > 256 {
		for k, v := range catalogCache {
			if !v.expires.IsZero() {
				delete(catalogCache, k)
			}
		}
	}
	entry := &catalogCacheEntry{done: make(chan struct{})}
	catalogCache[key] = entry
	catalogCacheMu.Unlock()
	data, err := load()
	catalogCacheMu.Lock()
	entry.data = append(json.RawMessage(nil), data...)
	entry.err = err
	entry.expires = time.Now().Add(5 * time.Minute)
	if err != nil {
		delete(catalogCache, key)
	}
	close(entry.done)
	catalogCacheMu.Unlock()
	return data, err
}
