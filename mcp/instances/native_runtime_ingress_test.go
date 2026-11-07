package main

import (
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
)

func TestNativeLegacyIngressMetadataDoesNotPublishRoutes(t *testing.T) {
	b, _, r := newNativeTest(t)
	r.Manifest.Runtime.Ingress = &sdk.RuntimeIngress{Routes: []sdk.RuntimeIngressRoute{{Path: "/browser/*"}}, Endpoints: map[string]string{"browser": ""}, BrowserPath: "/browser"}
	r.Options["ingress"] = map[string]any{"hostname": "voice.example.com"}
	r.Execution = prepareNativeTest(t, b, r)
	if r.Execution.BrowserEndpoint != "https://voice.example.com/browser" {
		t.Fatal("missing managed endpoint")
	}
	record, _ := b.record(r.DeploymentID)
	cfg := nativeIngressConfig(record, true)
	if !strings.Contains(cfg, "path /browser/*") || strings.Contains(cfg, "path /*") || strings.Contains(cfg, "runtime/app") {
		t.Fatal("unsafe public surface config")
	}
	start := startNativeRequest(r)
	if _, w := nativeCall(t, b, "start", start); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}

	if _, w := nativeCall(t, b, "publish-ingress", r); w.Code != 404 {
		t.Fatal("native backend still orchestrates public ingress", w.Code)
	}

	if _, w := nativeCall(t, b, "stop", r); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, w := nativeCall(t, b, "publish-ingress", r); w.Code == 200 {
		t.Fatal("stopped deployment published")
	}
}
