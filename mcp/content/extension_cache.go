package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"sync"
	"time"
)

func extensionPatternsOverlap(a, b string) bool {
	x, y := splitRoutePath(a), splitRoutePath(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] && !strings.HasPrefix(x[i], ":") && !strings.HasPrefix(y[i], ":") {
			return false
		}
	}
	return true
}

type extensionCacheKey struct {
	db     *sql.DB
	pid    string
	siteID int64
}
type extensionCacheEntry struct {
	extensions []Extension
	bytes      int
	at         time.Time
}

var extensionCache = struct {
	sync.Mutex
	entries map[extensionCacheKey]extensionCacheEntry
	bytes   int
}{entries: map[extensionCacheKey]extensionCacheEntry{}}

const extensionCacheByteLimit = 16 << 20

func invalidateExtensionCache(db *sql.DB, pid string, siteID int64) {
	extensionCache.Lock()
	defer extensionCache.Unlock()
	key := extensionCacheKey{db, pid, siteID}
	extensionCache.bytes -= extensionCache.entries[key].bytes
	delete(extensionCache.entries, key)
}

// Cached manifests contain only published data. They are immutable; request
// settings, sessions and provider results are never stored here.
func cachedPublishedExtensions(db *sql.DB, pid string, siteID int64) ([]Extension, error) {
	extensionCache.Lock()
	defer extensionCache.Unlock()
	key := extensionCacheKey{db, pid, siteID}
	if entry, ok := extensionCache.entries[key]; ok && time.Since(entry.at) < time.Minute {
		return entry.extensions, nil
	}
	if entry, ok := extensionCache.entries[key]; ok {
		extensionCache.bytes -= entry.bytes
		delete(extensionCache.entries, key)
	}
	rows, err := db.Query(`SELECT extension_key,provider_app,display_name,published_manifest FROM content_extensions WHERE project_id=? AND site_id=? AND status='published' ORDER BY display_name,extension_key`, pid, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Extension
	size := 0
	for rows.Next() {
		ext := Extension{ProjectID: pid, SiteID: siteID, Status: "published"}
		var body string
		if err := rows.Scan(&ext.Key, &ext.ProviderApp, &ext.DisplayName, &body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &ext.PublishedManifest); err != nil {
			return nil, err
		}
		size += len(body)
		out = append(out, ext)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if size <= extensionCacheByteLimit {
		for len(extensionCache.entries) >= 64 || extensionCache.bytes+size > extensionCacheByteLimit {
			var oldest extensionCacheKey
			var at time.Time
			for k, v := range extensionCache.entries {
				if at.IsZero() || v.at.Before(at) {
					oldest, at = k, v.at
				}
			}
			extensionCache.bytes -= extensionCache.entries[oldest].bytes
			delete(extensionCache.entries, oldest)
		}
		extensionCache.entries[key] = extensionCacheEntry{out, size, time.Now()}
		extensionCache.bytes += size
	}
	return out, nil
}

type extensionTemplateEntry struct {
	tpl  *template.Template
	size int
	at   time.Time
}

var extensionTemplates = struct {
	sync.Mutex
	entries map[string]extensionTemplateEntry
	bytes   int
}{entries: map[string]extensionTemplateEntry{}}

func cachedExtensionTemplate(ext Extension, name string) (*template.Template, error) {
	source, ok := ext.PublishedManifest.Templates[name]
	if !ok {
		return nil, fmt.Errorf("missing extension template %q", name)
	}
	key := ext.Key + ":" + name + ":" + extensionAssetRevision(source)
	extensionTemplates.Lock()
	defer extensionTemplates.Unlock()
	if entry, ok := extensionTemplates.entries[key]; ok {
		return entry.tpl, nil
	}
	tpl, err := template.New(ext.Key + ":" + name).Funcs(extensionTemplateFuncs(Extension{}, extensionPageData{})).Parse(source)
	if err != nil {
		return nil, err
	}
	if len(source) <= extensionCacheByteLimit {
		for len(extensionTemplates.entries) >= 128 || extensionTemplates.bytes+len(source) > extensionCacheByteLimit {
			var oldest string
			var at time.Time
			for k, v := range extensionTemplates.entries {
				if at.IsZero() || v.at.Before(at) {
					oldest, at = k, v.at
				}
			}
			extensionTemplates.bytes -= extensionTemplates.entries[oldest].size
			delete(extensionTemplates.entries, oldest)
		}
		extensionTemplates.entries[key] = extensionTemplateEntry{tpl, len(source), time.Now()}
		extensionTemplates.bytes += len(source)
	}
	return tpl, nil
}
