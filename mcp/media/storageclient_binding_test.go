package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression for the production failure where Media's app token was
// sent to /api/apps/storage and the hardened app proxy rejected it as
// belonging to a different install. Every byte-bearing request must
// now enter through the binding-gated callback proxy.
func TestStorageClientUsesBoundStreamingProxy(t *testing.T) {
	var paths []string
	var urlRequest map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer media-install-token" {
			t.Errorf("Authorization=%q", got)
		}
		if got := r.URL.Query().Get("project_id"); got != "prod-project" {
			t.Errorf("project_id=%q", got)
		}
		switch r.URL.Path {
		case boundStorageProxyPath + "/files/5300":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file": map[string]any{"id": 5300, "name": "zombie.mp4", "content_type": "video/mp4"},
			})
		case boundStorageProxyPath + "/files/5300/content":
			_, _ = w.Write([]byte("video-bytes"))
		case boundStorageProxyPath + "/files/5300/url":
			if err := json.NewDecoder(r.Body).Decode(&urlRequest); err != nil {
				t.Errorf("decode url request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"url":         "/api/apps/storage/public/files/5300/content/zombie.mp4?sig=test&exp=999",
				"delivery":    "apteva",
				"disposition": "inline",
				"expires_at":  int64(999),
				"file_id":     int64(5300),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "media-install-token")
	c := newStorageClient()
	f, err := c.GetFile(context.Background(), "prod-project", 5300)
	if err != nil || f.ID != 5300 {
		t.Fatalf("GetFile = %+v, %v", f, err)
	}
	var content bytes.Buffer
	if err := c.DownloadContent(context.Background(), "prod-project", 5300, &content); err != nil {
		t.Fatal(err)
	}
	if content.String() != "video-bytes" {
		t.Fatalf("content=%q", content.String())
	}
	t.Setenv("APTEVA_PUBLIC_URL", srv.URL)
	signed, err := c.GetSignedURLInfoWithOptions(context.Background(), "prod-project", 5300, 60, "direct", "attachment")
	wantSigned := srv.URL + "/api/apps/storage/public/files/5300/content/zombie.mp4?sig=test&exp=999"
	if err != nil || signed.URL != wantSigned {
		t.Fatalf("GetSignedURLInfoWithOptions = %+v, %v", signed, err)
	}
	if signed.Delivery != "apteva" || signed.Disposition != "inline" || signed.ExpiresAt != 999 || signed.FileID != 5300 {
		t.Fatalf("confirmed URL characteristics = %+v", signed)
	}
	if urlRequest["delivery"] != "direct" || urlRequest["disposition"] != "attachment" || urlRequest["ttl_seconds"] != float64(60) {
		t.Fatalf("url request = %#v", urlRequest)
	}
	for _, path := range paths {
		if path == "/api/apps/storage" || strings.HasPrefix(path, "/api/apps/storage/") {
			t.Fatalf("legacy cross-install route used: %s", path)
		}
	}
}

func TestStorageClientMCPFallbackPreservesCallerDelivery(t *testing.T) {
	var arguments map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case boundStorageProxyPath + "/files/42/url":
			http.Error(w, "HTTP route unavailable", http.StatusNotFound)
		case boundStorageProxyPath + "/mcp":
			var rpc struct {
				Params struct {
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
				t.Fatalf("decode MCP request: %v", err)
			}
			arguments = rpc.Params.Arguments
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]any{"content": []map[string]any{{
					"type": "text",
					"text": `{"url":"/files/42/content?sig=mcp&exp=1234","expires_at":1234,"file_id":42,"delivery":"apteva","disposition":"inline"}`,
				}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "media-token")
	t.Setenv("APTEVA_PUBLIC_URL", srv.URL)
	globalCtx = nil
	info, err := newStorageClient().GetSignedURLInfoWithOptions(context.Background(), "prod-project", 42, 86400, "direct", "attachment")
	if err != nil {
		t.Fatal(err)
	}
	if info.URL != srv.URL+"/api/apps/storage/files/42/content?sig=mcp&exp=1234" || info.Delivery != "apteva" || info.Disposition != "inline" || info.ExpiresAt != 1234 {
		t.Fatalf("fallback info = %+v", info)
	}
	if arguments["delivery"] != "direct" || arguments["disposition"] != "attachment" || arguments["ttl_seconds"] != float64(86400) {
		t.Fatalf("fallback arguments = %#v", arguments)
	}
}
