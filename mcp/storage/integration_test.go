//go:build integration

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_HealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 || got["ok"] != true {
		t.Fatalf("/health status=%d body=%v", resp.Status, got)
	}
}

func TestSidecar_UploadDownloadRoundtrip(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	body := []byte("hello apteva")
	hash := sha256.Sum256(body)
	hashHex := hex.EncodeToString(hash[:])

	// Upload via MCP.
	out := sc.MCP("files_upload", map[string]any{
		"name":           "hello.txt",
		"folder":         "/notes/",
		"content_base64": base64.StdEncoding.EncodeToString(body),
		"content_type":   "text/plain",
	})
	if out["sha256"] != hashHex {
		t.Errorf("sha256 mismatch: got %v want %s", out["sha256"], hashHex)
	}
	id := int64(out["id"].(float64))

	// Fetch metadata via REST.
	var meta map[string]any
	sc.GET("/files/"+itoa(id), &meta)
	f := meta["file"].(map[string]any)
	if f["name"] != "hello.txt" || f["folder"] != "/notes/" {
		t.Errorf("metadata: %v", f)
	}

	// Fetch content via REST. Tier 2 talks to the sidecar directly
	// (no platform proxy in front), so we must send the same Bearer
	// the sidecar's withTokenAuth expects.
	req, _ := http.NewRequest("GET", sc.URL()+"/files/"+itoa(id)+"/content", nil)
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("content status=%d", resp.StatusCode)
	}
	got := make([]byte, 32)
	n, _ := resp.Body.Read(got)
	if string(got[:n]) != string(body) {
		t.Errorf("content mismatch: %q", string(got[:n]))
	}
}

func TestSidecar_FoldersAndList(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	for _, f := range []struct{ name, folder string }{
		{"a", "/reports/2025/"},
		{"b", "/reports/2026/"},
		{"c", "/reports/2026/q1/"},
		{"d", "/notes/"},
	} {
		sc.MCP("files_upload", map[string]any{
			"name": f.name, "folder": f.folder,
			"content_base64": base64.StdEncoding.EncodeToString([]byte(f.name)),
		})
	}
	// Listing /reports/ non-recursive — should be empty (files are
	// under /reports/2025/ etc., not directly in /reports/).
	out := sc.MCP("files_list", map[string]any{"folder": "/reports/"})
	if out["count"].(float64) != 0 {
		t.Errorf("non-recursive /reports/ count=%v, want 0", out["count"])
	}
	out = sc.MCP("files_list", map[string]any{"folder": "/reports/", "recursive": true})
	if out["count"].(float64) != 3 {
		t.Errorf("recursive /reports/ count=%v, want 3", out["count"])
	}
	out = sc.MCP("files_list_folders", map[string]any{"parent": "/reports/"})
	folders := out["folders"].([]any)
	if len(folders) != 2 {
		t.Errorf("/reports/ child folders=%v, want 2", folders)
	}
}

func TestSidecar_SignedURL(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	out := sc.MCP("files_upload", map[string]any{
		"name": "secret.txt", "content_base64": base64.StdEncoding.EncodeToString([]byte("shh")),
		"visibility": "signed",
	})
	id := int64(out["id"].(float64))

	urlOut := sc.MCP("files_get_url", map[string]any{"id": id, "ttl_seconds": 60})
	signedPath := urlOut["url"].(string)
	if !strings.Contains(signedPath, "sig=") {
		t.Fatalf("url missing sig: %s", signedPath)
	}

	// Anonymous fetch (no Authorization) — should succeed via signature.
	resp, err := http.Get(sc.URL() + signedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("signed URL fetch status=%d", resp.StatusCode)
	}

	// Tamper with the sig — should fail.
	tampered := strings.Replace(signedPath, "sig=", "sig=00", 1)
	resp2, _ := http.Get(sc.URL() + tampered)
	if resp2 != nil {
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusForbidden {
			t.Errorf("tampered sig status=%d, want 403", resp2.StatusCode)
		}
	}
}

func TestSidecar_DedupeMCP(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	body := []byte("dedup-target")
	sc.MCP("files_upload", map[string]any{
		"name": "x.txt", "content_base64": base64.StdEncoding.EncodeToString(body),
	})
	hash := sha256.Sum256(body)
	out := sc.MCP("files_dedupe_check", map[string]any{
		"sha256": hex.EncodeToString(hash[:]),
	})
	if out["found"] != true {
		t.Errorf("dedupe didn't find existing")
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
