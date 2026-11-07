//go:build integration

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// Tier 2 exercises the real sidecar and SDK serializer. A nested JSON string
// containing base64 must never stand in for an MCP image content block.
func TestSidecar_FilesReadNativeContentAndRawCompatibility(t *testing.T) {
	sc := spawnStorageSidecar(t, ".", tk.WithProjectID("test-proj"))
	for _, tc := range []struct {
		name, mime, format string
		image              bool
	}{
		{"picture.png", "image/png", "image", true},
		{"report.pdf", "application/pdf", "pdf", true},
		{"note.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "text", false},
		{"note.txt", "text/plain", "text", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := readFixture(t, tc.name)
			upload := sc.MCP("files_upload", map[string]any{"name": tc.name, "content_type": tc.mime, "content_base64": base64.StdEncoding.EncodeToString(data)})
			id := upload["id"]
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "files_read", "arguments": map[string]any{"id": id}}})
			req, _ := http.NewRequest(http.MethodPost, sc.URL()+"/mcp", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+sc.Token())
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var wire struct {
				Result sdk.MCPResult `json:"result"`
				Error  any           `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 || wire.Error != nil || wire.Result.StructuredContent["format"] != tc.format {
				t.Fatalf("wrong wire result: status=%d %+v", resp.StatusCode, wire)
			}
			images := 0
			for _, block := range wire.Result.Content {
				if block.Type == "image" {
					images++
					if block.Data == "" || block.MIMEType != "image/jpeg" {
						t.Fatal("image bytes lost")
					}
				}
			}
			if (images > 0) != tc.image {
				t.Fatalf("expected image=%v, got %d blocks", tc.image, images)
			}
			raw := sc.MCP("files_get_content", map[string]any{"id": id})
			if raw["content_base64"] != base64.StdEncoding.EncodeToString(data) {
				t.Fatal("existing byte response changed")
			}
		})
	}
}
