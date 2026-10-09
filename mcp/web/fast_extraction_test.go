package main

import "testing"

func TestExtractionRetainsReusesAndReleasesSession(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	out, err := app.toolExtract(ctx, map[string]any{"url": "https://example.com/", "keep_session": true, "store": false, "cache": "bypass"})
	if err != nil {
		t.Fatal(err)
	}
	doc := out.(map[string]any)["page"].(pageDoc)
	if doc.Browser.SessionID == "" || countCalls(plat, "computer", "browser_close") != 0 || plat.lastCall("computer", "browser_open")["extraction_only"] != true {
		t.Fatal("session was not retained in extraction lifecycle")
	}
	out, err = app.toolExtract(ctx, map[string]any{"url": "https://example.com/contact", "keep_session": true, "session_id": doc.Browser.SessionID, "store": false})
	if err != nil {
		t.Fatal(err)
	}
	if countCalls(plat, "computer", "browser_open") != 1 || countCalls(plat, "computer", "computer_use") != 1 || out.(map[string]any)["page"].(pageDoc).FinalURL != "https://example.com/contact" {
		t.Fatal("reopened browser instead of reusing it")
	}
	for _, tool := range app.MCPTools() {
		if tool.Name == "web_session_close" {
			_, err = tool.Handler(ctx, map[string]any{"session_id": doc.Browser.SessionID})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if countCalls(plat, "computer", "browser_close") != 1 {
		t.Fatal("session was not released")
	}
}

func TestCacheRootNormalizationAndObservedRedirectAlias(t *testing.T) {
	_, a, _ := cacheKey("extract", map[string]any{"url": "https://EXAMPLE.com"})
	_, b, _ := cacheKey("extract", map[string]any{"url": "https://example.com/"})
	if a != b {
		t.Fatal("equivalent root URLs differ")
	}
	_, a, _ = cacheKey("extract", map[string]any{"url": "https://example.com/path"})
	_, b, _ = cacheKey("extract", map[string]any{"url": "https://example.com/path/"})
	if a == b {
		t.Fatal("unobserved path slash variants must not be assumed equivalent")
	}
	plat := newFakePlatform()
	ctx, _ := newTestCtx(t, plat)
	p, err := newCachePolicy("extract", map[string]any{"url": "http://example.com/contact", "source_only": true, "readability": false})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{"page": pageDoc{URL: "http://example.com/contact", FinalURL: "https://www.example.com/contact/", Text: "contact@example.com", Status: 200, ExtractionBackend: "http_source"}}
	applyCacheAfterFetch(ctx, p, out)
	alias, _ := newCachePolicy("extract", map[string]any{"url": "https://www.example.com/contact/", "source_only": true, "readability": false})
	cached, hit, err := loadCachedResponse(ctx, alias)
	if err != nil || !hit {
		t.Fatalf("redirect alias miss: %v %v", hit, err)
	}
	if cached["page"].(map[string]any)["text"] != "contact@example.com" {
		t.Fatal("cached content changed")
	}
	browser, _ := newCachePolicy("extract", map[string]any{"url": "https://www.example.com/contact/", "source_only": false, "readability": false})
	_, hit, _ = loadCachedResponse(ctx, browser)
	if hit {
		t.Fatal("source and browser caches collided")
	}
	live, _ := newCachePolicy("extract", map[string]any{"url": "https://example.com/", "session_id": "br_private"})
	if live.Read || live.Write {
		t.Fatal("private live session used public cache")
	}
}
