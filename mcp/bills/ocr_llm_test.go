package main

// Tier 1 — pure-logic tests for the v0.1.3 LLM-OCR path. Tests the
// prompt builder, response parser, materialiseImages routing, and
// the callOCR dispatcher's "llm without binding" guard. The actual
// PDFium render + ExecuteIntegrationTool round-trip needs a live
// vision_llm binding and is exercised by tier 3 scenarios.

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// withOCRConfig wraps tk.WithConfig to set ocr_provider for the
// dispatcher tests. Local helper; bills' main test ctx is set up
// separately and this file only needs the OCR config knob.
func withOCRConfig(provider string) tk.Option {
	return tk.WithConfig(map[string]string{
		"ocr_provider": provider,
	})
}

// ─── Prompt builder ────────────────────────────────────────────────

func TestBuildOCRMessages_ShapeAndImageParts(t *testing.T) {
	images := [][]byte{[]byte("page-1-jpeg-bytes"), []byte("page-2-jpeg-bytes")}
	messages := buildOCRMessages(images)
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(messages))
	}
	sys := messages[0].(map[string]any)
	if sys["role"] != "system" {
		t.Errorf("first message role=%q, want system", sys["role"])
	}
	if !strings.Contains(sys["content"].(string), "INTEGER CENTS") {
		t.Error("system prompt should enforce integer cents")
	}
	user := messages[1].(map[string]any)
	if user["role"] != "user" {
		t.Errorf("second message role=%q, want user", user["role"])
	}
	parts := user["content"].([]any)
	// 1 text part + N image parts
	if len(parts) != 1+len(images) {
		t.Fatalf("expected %d content parts, got %d", 1+len(images), len(parts))
	}
	textPart := parts[0].(map[string]any)
	if textPart["type"] != "text" {
		t.Errorf("first part type=%q, want text", textPart["type"])
	}
	for i, img := range images {
		p := parts[1+i].(map[string]any)
		if p["type"] != "image_url" {
			t.Errorf("part %d type=%q, want image_url", 1+i, p["type"])
		}
		url := p["image_url"].(map[string]any)["url"].(string)
		if !strings.HasPrefix(url, "data:image/jpeg;base64,") {
			t.Errorf("part %d url should be data URI, got %q", 1+i, truncate(url, 60))
		}
		want := base64.StdEncoding.EncodeToString(img)
		if !strings.HasSuffix(url, want) {
			t.Errorf("part %d data URI doesn't end with the expected base64", 1+i)
		}
	}
}

// ─── Response parser ───────────────────────────────────────────────

func TestParseAssistantInvoice_StringContent(t *testing.T) {
	// The common OpenAI shape: choices[0].message.content is a JSON
	// string the model produced.
	innerJSON := `{"vendor":{"name":"AWS","email":"billing@aws.amazon.com"},"invoice_number":"AWS-2026-04-001","total_cents":48000,"currency":"USD","line_items":[{"description":"EC2","amount_cents":48000}]}`
	envelope := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{"content": innerJSON},
			},
		},
	}
	raw, _ := json.Marshal(envelope)
	got, err := parseAssistantInvoice(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Vendor.Name != "AWS" || got.InvoiceNumber != "AWS-2026-04-001" || got.TotalCents != 48000 {
		t.Errorf("got %+v", got)
	}
}

func TestParseAssistantInvoice_ContentInArrayParts(t *testing.T) {
	// Defensive shape: content is an array of typed parts. Some
	// gateways send this even for chat-completion replies.
	envelope := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"content": []any{
						map[string]any{
							"type": "text",
							"text": `{"vendor":{"name":"Globex","email":"ap@globex.com"},"total_cents":12000}`,
						},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(envelope)
	got, err := parseAssistantInvoice(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Vendor.Email != "ap@globex.com" || got.TotalCents != 12000 {
		t.Errorf("got %+v", got)
	}
}

func TestParseAssistantInvoice_StripsMarkdownFences(t *testing.T) {
	// Real-world: even with json_object response_format, models
	// sometimes wrap output in ```json … ``` fences. The parser
	// must strip them.
	innerJSON := "```json\n{\"vendor\":{\"name\":\"Acme\"},\"total_cents\":5000}\n```"
	envelope := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{"content": innerJSON},
			},
		},
	}
	raw, _ := json.Marshal(envelope)
	got, err := parseAssistantInvoice(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Vendor.Name != "Acme" || got.TotalCents != 5000 {
		t.Errorf("got %+v", got)
	}
}

func TestParseAssistantInvoice_StripsLeadingProse(t *testing.T) {
	// The model occasionally prepends "Here is the JSON:" before the
	// object. The parser locates the first '{' and tries from there.
	innerJSON := `Here is the extracted invoice:

{"vendor":{"name":"Initech"},"total_cents":900}`
	envelope := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{"content": innerJSON},
			},
		},
	}
	raw, _ := json.Marshal(envelope)
	got, err := parseAssistantInvoice(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Vendor.Name != "Initech" {
		t.Errorf("got %+v", got)
	}
}

func TestParseAssistantInvoice_NoChoicesErrors(t *testing.T) {
	envelope := map[string]any{"choices": []any{}}
	raw, _ := json.Marshal(envelope)
	if _, err := parseAssistantInvoice(raw); err == nil {
		t.Fatal("expected error on empty choices")
	}
}

func TestParseAssistantInvoice_BogusEnvelope(t *testing.T) {
	if _, err := parseAssistantInvoice([]byte(`not-json`)); err == nil {
		t.Fatal("expected parse error")
	}
}

// ─── materialiseImages routing ─────────────────────────────────────

func TestMaterialiseImages_PassThroughForImage(t *testing.T) {
	raw := []byte("fake-png-bytes")
	imgs, err := materialiseImages(raw, "image/png", 200, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || string(imgs[0]) != string(raw) {
		t.Errorf("expected image bytes passed through unchanged")
	}
}

func TestMaterialiseImages_RejectsUnknownType(t *testing.T) {
	_, err := materialiseImages([]byte("garbage"), "application/zip", 200, 3)
	if err == nil {
		t.Fatal("expected error on unsupported content type")
	}
	if !strings.Contains(err.Error(), "PDF or image/*") {
		t.Errorf("error %q should mention PDF / image hint", err.Error())
	}
}

func TestMaterialiseImages_PDFMagicSniffOctetStream(t *testing.T) {
	// Some uploads come through as application/octet-stream; the
	// helper should sniff the %PDF magic and route to the PDF path.
	// We can't actually render without a real PDF, so just verify
	// the routing kicks in by expecting a pdfium-shaped error rather
	// than the "unsupported content_type" error.
	pdfish := []byte("%PDF-1.4\n%not really a pdf")
	_, err := materialiseImages(pdfish, "application/octet-stream", 200, 3)
	if err == nil {
		t.Fatal("expected pdfium error on bogus PDF bytes")
	}
	if strings.Contains(err.Error(), "unsupported content_type") {
		t.Errorf("expected magic-sniff to route to PDF path, got %q", err.Error())
	}
}

// ─── callOCR dispatcher ────────────────────────────────────────────

func TestCallOCR_LLMWithoutBindingErrors(t *testing.T) {
	// ocr_provider="llm" with no platform API → the upstream guard in
	// callOCR ("platform API unavailable") trips before we get to the
	// vision_llm binding check. The user-facing message is the same
	// flavor either way: ocr_provider is set but not usable.
	ctx := newTestCtx(t, withOCRConfig("llm"))
	_, providerLabel, err := callOCR(ctx, 42)
	if err == nil {
		t.Fatal("expected error when ocr_provider=llm with no platform")
	}
	if providerLabel != "llm" {
		t.Errorf("provider label=%q, want llm", providerLabel)
	}
	if !strings.Contains(err.Error(), "platform API unavailable") {
		t.Errorf("error %q should mention platform API", err.Error())
	}
}

func TestCallOCR_DisabledByDefault(t *testing.T) {
	// Empty ocr_provider → (nil, "", nil) — disabled, not an error.
	ctx := newTestCtx(t)
	parsed, providerLabel, err := callOCR(ctx, 42)
	if err != nil {
		t.Fatalf("expected no error when disabled, got %v", err)
	}
	if parsed != nil || providerLabel != "" {
		t.Errorf("expected nil/empty when disabled, got %+v / %q", parsed, providerLabel)
	}
}

func TestParseInvoiceJSON_BareJSON(t *testing.T) {
	got, err := parseInvoiceJSON(`{"vendor":{"name":"X"},"total_cents":100}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Vendor.Name != "X" || got.TotalCents != 100 {
		t.Errorf("got %+v", got)
	}
}
