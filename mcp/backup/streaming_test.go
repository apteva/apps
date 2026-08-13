package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type streamingProviderPlatform struct {
	tk.BasePlatformClient
	archiveURL string
	input      map[string]any
}

func (p *streamingProviderPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.input = input
	raw, _ := json.Marshal(map[string]any{
		"archive_url": p.archiveURL,
		"manifest":    json.RawMessage(`{"provider":"fleet"}`),
	})
	return json.Unmarshal(raw, out)
}

func TestStreamProviderSnapshotPrefersArchiveURL(t *testing.T) {
	payload := bytes.Repeat([]byte("streamed-backup"), 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	platform := &streamingProviderPlatform{archiveURL: server.URL}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, openTestDB(t), sdk.Config{}, platform, silentLogger{})
	var dst bytes.Buffer
	n, providerManifest, err := streamProviderSnapshot(context.Background(), ctx, &dst, Scope{Kind: "fleet_tenant", ID: "tenant-1", SourceApp: "fleet"})
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(payload)) || !bytes.Equal(dst.Bytes(), payload) {
		t.Fatalf("streamed bytes=%d buffer=%d", n, dst.Len())
	}
	if providerManifest != `{"provider":"fleet"}` {
		t.Fatalf("manifest=%q", providerManifest)
	}
	if supported, _ := platform.input["supports_streaming"].(bool); !supported {
		t.Fatalf("provider args=%#v", platform.input)
	}
}

func TestStreamProviderSnapshotRequiresStreamingProvider(t *testing.T) {
	platform := &streamingProviderPlatform{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, openTestDB(t), sdk.Config{}, platform, silentLogger{})
	_, _, err := streamProviderSnapshot(context.Background(), ctx, io.Discard, Scope{Kind: "fleet_tenant", ID: "tenant-1", SourceApp: "fleet"})
	if err == nil || !strings.Contains(err.Error(), "does not support streaming snapshots") {
		t.Fatalf("stream error = %v", err)
	}
}

func TestRestoreProviderRequiresStreamingProvider(t *testing.T) {
	platform := &streamingProviderPlatform{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, openTestDB(t), sdk.Config{}, platform, silentLogger{})
	run := &Run{Scope: Scope{Kind: "fleet_tenant", ID: "tenant-1", SourceApp: "fleet"}}
	_, err := restoreProviderRunStream(context.Background(), ctx, run, strings.NewReader("snapshot"))
	if err == nil || !strings.Contains(err.Error(), "does not support streaming restores") {
		t.Fatalf("restore error = %v", err)
	}
}
