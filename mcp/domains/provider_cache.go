package main

import (
	"fmt"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type zoneCacheEntry struct {
	id      string
	expires time.Time
}

var zoneIDs = struct {
	sync.Mutex
	entries map[string]zoneCacheEntry
}{entries: map[string]zoneCacheEntry{}}

func zoneCacheKey(ctx *sdk.AppCtx, conn int64, domain string) string {
	return fmt.Sprintf("%p:%s:%d:%s", ctx.AppDB(), ctx.CurrentProject(), conn, domain)
}
func cachedZoneID(ctx *sdk.AppCtx, conn int64, domain string) string {
	zoneIDs.Lock()
	defer zoneIDs.Unlock()
	key := zoneCacheKey(ctx, conn, domain)
	e := zoneIDs.entries[key]
	if time.Now().Before(e.expires) {
		return e.id
	}
	delete(zoneIDs.entries, key)
	return ""
}
func cacheZoneID(ctx *sdk.AppCtx, conn int64, domain, id string) {
	zoneIDs.Lock()
	defer zoneIDs.Unlock()
	key := zoneCacheKey(ctx, conn, domain)
	if id == "" {
		delete(zoneIDs.entries, key)
		return
	}
	if len(zoneIDs.entries) >= 512 {
		for k, e := range zoneIDs.entries {
			if time.Now().After(e.expires) {
				delete(zoneIDs.entries, k)
			}
		}
		if len(zoneIDs.entries) >= 512 {
			for k := range zoneIDs.entries {
				delete(zoneIDs.entries, k)
				break
			}
		}
	}
	zoneIDs.entries[key] = zoneCacheEntry{id, time.Now().Add(5 * time.Minute)}
}
