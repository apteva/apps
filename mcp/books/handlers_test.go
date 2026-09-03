package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssetJSONLimitAccommodatesBase64Expansion(t *testing.T) {
	payload := `{"content_base64":"` + strings.Repeat("A", 3<<20) + `"}`
	request := httptest.NewRequest("POST", "/books/1/assets", strings.NewReader(payload))
	var body struct {
		ContentBase64 string `json:"content_base64"`
	}
	if err := decodeJSONLimit(request, &body, maxAssetUploadRequestBytes); err != nil {
		t.Fatalf("decode asset request: %v", err)
	}
	if len(body.ContentBase64) != 3<<20 {
		t.Fatalf("content length = %d, want %d", len(body.ContentBase64), 3<<20)
	}
}

func TestDefaultJSONLimitRemainsSmall(t *testing.T) {
	payload := `{"value":"` + strings.Repeat("A", 3<<20) + `"}`
	request := httptest.NewRequest("POST", "/books", strings.NewReader(payload))
	var body map[string]any
	if err := decodeJSON(request, &body); err == nil || !strings.Contains(err.Error(), "request body exceeds 2 MB limit") {
		t.Fatalf("decode error = %v, want request-size error", err)
	}
}
