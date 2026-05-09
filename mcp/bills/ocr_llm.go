package main

// LLM-based OCR path (v0.1.3+). Activates when ocr_provider="llm".
// Bills fetches the file bytes from storage, renders PDF pages to
// JPEG via embedded PDFium-WASM (no system deps, no CGO), and sends
// the image(s) to the bound vision_llm integration's chat-completion
// endpoint with a structured-output prompt. The reply is parsed into
// the same ExtractedInvoice shape the rest of the OCR pipeline uses.
//
// Storage cross-app shape: files_get for metadata (content_type),
// files_get_url for a signed URL we http.GET. We deliberately don't
// add a files_get_content tool to storage — the signed URL pattern
// already exists for the dashboard panel and Just Works here too.
//
// PDFium-WASM (Wazero runtime, BSD) lives bundled in the bills
// binary. ~10 MB binary delta, no external deps. Pool is lazy-init
// on first use so OnMount stays untouched.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// ─── Top-level orchestrator ─────────────────────────────────────────

func callOCRViaLLM(ctx *sdk.AppCtx, fileID int64) (*ExtractedInvoice, string, error) {
	bound := ctx.IntegrationFor("vision_llm")
	if bound == nil {
		return nil, "llm", errors.New("ocr_provider=llm but vision_llm integration not bound — bind opencode-go (or another compatible chat-completion provider) in the dashboard")
	}

	// 1. Storage metadata + bytes.
	contentType, err := storageGetContentType(ctx, fileID)
	if err != nil {
		return nil, "llm", err
	}
	rawBytes, err := storageFetchBytes(ctx, fileID)
	if err != nil {
		return nil, "llm", err
	}

	// 2. Materialise as JPEG image(s).
	dpi := configIntDefault(ctx, "render_dpi", 200)
	maxPages := configIntDefault(ctx, "max_pages", 3)
	images, err := materialiseImages(rawBytes, contentType, dpi, maxPages)
	if err != nil {
		return nil, "llm", err
	}
	if len(images) == 0 {
		return nil, "llm", fmt.Errorf("file %d (content_type=%q) produced no images — unsupported format?", fileID, contentType)
	}

	// 3. Build messages + call.
	model := configString(ctx, "ocr_llm_model", "kimi-k2.6")
	maxTokens := configIntDefault(ctx, "ocr_llm_max_tokens", 4000)
	messages := buildOCRMessages(images)

	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(
		bound.ConnectionID,
		bound.ToolFor("chat.complete"),
		map[string]any{
			"model":           model,
			"messages":        messages,
			"temperature":     0.1, // structured extraction wants determinism
			"max_tokens":      maxTokens,
			"response_format": map[string]any{"type": "json_object"},
		},
	)
	if err != nil {
		return nil, "llm/" + model, fmt.Errorf("vision_llm chat completion: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, "llm/" + model, fmt.Errorf("vision_llm non-2xx: %s", truncate(body, 500))
	}

	parsed, err := parseAssistantInvoice(res.Data)
	if err != nil {
		return nil, "llm/" + model, fmt.Errorf("parse llm extraction: %w", err)
	}
	if parsed.Provider == "" {
		parsed.Provider = "llm/" + model
	}
	return parsed, "llm/" + model, nil
}

// ─── Storage cross-app helpers ──────────────────────────────────────

func storageGetContentType(ctx *sdk.AppCtx, fileID int64) (string, error) {
	var meta struct {
		File struct {
			ContentType string `json:"content_type"`
		} `json:"file"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get",
		map[string]any{"id": fileID}, &meta); err != nil {
		// CallAppResult also accepts the unwrapped shape — try once
		// more in case storage returns the bare {content_type:…}.
		var bare struct {
			ContentType string `json:"content_type"`
		}
		if err2 := ctx.PlatformAPI().CallAppResult("storage", "files_get",
			map[string]any{"id": fileID}, &bare); err2 == nil && bare.ContentType != "" {
			return bare.ContentType, nil
		}
		return "", fmt.Errorf("storage.files_get(%d): %w", fileID, err)
	}
	return meta.File.ContentType, nil
}

// storageFetchBytes mints a signed URL via storage's files_get_url
// tool then http.GETs it. The signed URL is short-lived (~24h by
// default) and self-authenticates — no Authorization header needed.
func storageFetchBytes(ctx *sdk.AppCtx, fileID int64) ([]byte, error) {
	var sig struct {
		URL string `json:"url"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url",
		map[string]any{"id": fileID, "ttl_seconds": 600}, &sig); err != nil {
		return nil, fmt.Errorf("storage.files_get_url(%d): %w", fileID, err)
	}
	if sig.URL == "" {
		return nil, fmt.Errorf("storage.files_get_url(%d): empty URL", fileID)
	}

	httpCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, sig.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET signed URL: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GET signed URL: status %d: %s", resp.StatusCode, string(body))
	}
	// 25 MB ceiling — same as the multipart upload limit.
	return io.ReadAll(io.LimitReader(resp.Body, 25<<20))
}

// ─── PDFium-WASM render ─────────────────────────────────────────────

var (
	pdfiumPool      pdfium.Pool
	pdfiumPoolMu    sync.Mutex
	pdfiumInstanced sync.Once
	pdfiumInitErr   error
)

// initPDFRenderer brings up the WASM pool exactly once. We use a
// single-instance pool — pdf rendering is CPU-bound so concurrency
// here doesn't help, and a single instance keeps memory predictable.
func initPDFRenderer() error {
	pdfiumInstanced.Do(func() {
		pdfiumPoolMu.Lock()
		defer pdfiumPoolMu.Unlock()
		pool, err := webassembly.Init(webassembly.Config{
			MinIdle:  1,
			MaxIdle:  1,
			MaxTotal: 1,
		})
		if err != nil {
			pdfiumInitErr = fmt.Errorf("pdfium webassembly init: %w", err)
			return
		}
		pdfiumPool = pool
	})
	return pdfiumInitErr
}

// renderPDFToJPEGs decodes the PDF, renders up to maxPages pages at
// `dpi` DPI, and returns each page JPEG-encoded. Any one page failing
// short-circuits — invoices typically rest on page 1, so a partial
// render is more confusing than a clear error. Quality 80 keeps the
// JPEGs small enough to fit a vision LLM's input budget.
func renderPDFToJPEGs(pdfBytes []byte, dpi, maxPages int) ([][]byte, error) {
	if err := initPDFRenderer(); err != nil {
		return nil, err
	}
	inst, err := pdfiumPool.GetInstance(30 * time.Second)
	if err != nil {
		return nil, fmt.Errorf("pdfium GetInstance: %w", err)
	}
	defer inst.Close()

	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &pdfBytes})
	if err != nil {
		return nil, fmt.Errorf("pdfium OpenDocument: %w", err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})

	pageCount, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return nil, fmt.Errorf("pdfium FPDF_GetPageCount: %w", err)
	}
	pages := pageCount.PageCount
	if pages <= 0 {
		return nil, errors.New("pdfium: document has zero pages")
	}
	if maxPages > 0 && pages > maxPages {
		pages = maxPages
	}

	out := make([][]byte, 0, pages)
	for i := 0; i < pages; i++ {
		render, err := inst.RenderPageInDPI(&requests.RenderPageInDPI{
			Page: requests.Page{
				ByIndex: &requests.PageByIndex{
					Document: doc.Document,
					Index:    i,
				},
			},
			DPI: dpi,
		})
		if err != nil {
			return nil, fmt.Errorf("pdfium render page %d: %w", i, err)
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, render.Result.Image, &jpeg.Options{Quality: 80}); err != nil {
			return nil, fmt.Errorf("jpeg encode page %d: %w", i, err)
		}
		out = append(out, buf.Bytes())
	}
	return out, nil
}

// materialiseImages turns raw bytes + content_type into one or more
// JPEG byte slices ready to embed as data URIs. PDFs render via
// pdfium; image inputs (PNG/JPEG/WebP) pass through untouched (the
// LLM tolerates image_url with any common image type).
func materialiseImages(raw []byte, contentType string, dpi, maxPages int) ([][]byte, error) {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.HasPrefix(ct, "application/pdf"), strings.Contains(ct, "/pdf"):
		return renderPDFToJPEGs(raw, dpi, maxPages)
	case strings.HasPrefix(ct, "image/"):
		// Pass through. Vision LLMs accept image/* via image_url.
		return [][]byte{raw}, nil
	default:
		// Some uploads come in as application/octet-stream; sniff for
		// the PDF magic before giving up.
		if len(raw) >= 4 && string(raw[:4]) == "%PDF" {
			return renderPDFToJPEGs(raw, dpi, maxPages)
		}
		return nil, fmt.Errorf("unsupported content_type %q (need PDF or image/*)", contentType)
	}
}

// closePDFRenderer tears down the WASM pool. Called from OnUnmount
// (when wired) — until then it's a no-op, the pool gets reclaimed
// at process exit.
func closePDFRenderer() {
	pdfiumPoolMu.Lock()
	defer pdfiumPoolMu.Unlock()
	if pdfiumPool != nil {
		_ = pdfiumPool.Close()
		pdfiumPool = nil
	}
}

// ─── Prompt + response ──────────────────────────────────────────────

const ocrSystemPrompt = `You are a precision invoice data extractor. From the image(s) you receive, extract structured invoice fields and return ONLY a single JSON object — no commentary, no markdown fences, no explanation.

The JSON must follow this shape EXACTLY (omit fields you cannot determine):

{
  "vendor": {
    "name": "...",                 // company billing us, as printed
    "email": "...",                // accounts-payable email if visible
    "phone": "...",
    "address": {                   // the vendor's address, NOT ours
      "line1": "...", "line2": "...",
      "city": "...", "state": "...",
      "postal_code": "...", "country": "..."
    },
    "tax_id": "..."                // VAT/EIN/GST if visible
  },
  "invoice_number": "...",         // the vendor's invoice number, as printed
  "issue_date": "YYYY-MM-DD",      // ISO 8601 date only
  "due_date": "YYYY-MM-DD",
  "currency": "USD",               // ISO 4217 (USD/EUR/GBP/JPY/...)
  "subtotal_cents": 12345,         // INTEGER cents — e.g. $123.45 → 12345
  "tax_cents": 246,
  "total_cents": 12591,
  "payment_terms_days": 30,
  "line_items": [
    {
      "description": "...",
      "quantity": 1.0,
      "unit_price_cents": 12345,
      "amount_cents": 12345,
      "tax_rate_bps": 1000          // basis points; 1000 = 10.00%
    }
  ]
}

Critical rules:
- All money fields are INTEGER CENTS, never decimals.
- All dates are YYYY-MM-DD.
- The vendor block describes the company billing US, not the customer (us).
- If a field isn't on the document or you can't read it confidently, OMIT IT — do not invent.
- Return only the JSON object. No surrounding prose, no fences.`

// buildOCRMessages assembles the OpenAI chat-completion messages
// array. System prompt + a single user turn carrying the prompt
// string and one image_url part per page (data URI base64).
func buildOCRMessages(images [][]byte) []any {
	parts := make([]any, 0, len(images)+1)
	parts = append(parts, map[string]any{
		"type": "text",
		"text": "Extract the invoice fields from the page(s) below. Reply with ONLY the JSON object described in the system prompt — no other text.",
	})
	for _, img := range images {
		// JPEG by default (PDFs) or whatever pass-through mime we got
		// (PNG/JPEG/WebP). The data URI mime affects nothing semantically
		// for the model so we always tag image/jpeg — saves carrying the
		// original MIME through the call chain.
		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img),
			},
		})
	}
	return []any{
		map[string]any{
			"role":    "system",
			"content": ocrSystemPrompt,
		},
		map[string]any{
			"role":    "user",
			"content": parts,
		},
	}
}

// parseAssistantInvoice digs the assistant's content out of the
// chat-completion response and unmarshals it as ExtractedInvoice.
// The OpenCode Go gateway returns OpenAI-shape responses
// ({choices:[{message:{content:"<json string>"}}]}). We tolerate the
// content being either a raw JSON string or a single content-part
// array (defensive against gateway shape drift).
func parseAssistantInvoice(raw json.RawMessage) (*ExtractedInvoice, error) {
	var envelope struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode chat-completion envelope: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return nil, errors.New("chat-completion: no choices in response")
	}
	contentRaw := envelope.Choices[0].Message.Content

	// Two shapes: string (the common OpenAI shape) or array of parts
	// ([{type:'text', text:'...'}]). Try string first.
	var contentStr string
	if err := json.Unmarshal(contentRaw, &contentStr); err == nil && contentStr != "" {
		return parseInvoiceJSON(contentStr)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(contentRaw, &parts); err == nil {
		for _, p := range parts {
			if p.Type == "text" && p.Text != "" {
				if inv, err := parseInvoiceJSON(p.Text); err == nil {
					return inv, nil
				}
			}
		}
	}
	return nil, errors.New("chat-completion: could not extract assistant content")
}

// parseInvoiceJSON handles the small annoying real-world cases: the
// model wrapped its JSON in ```json fences, prepended a sentence,
// or used "json" as a key. We strip fences/prose and try once.
func parseInvoiceJSON(s string) (*ExtractedInvoice, error) {
	s = strings.TrimSpace(s)
	// Strip ```json … ``` fences if present.
	if strings.HasPrefix(s, "```") {
		// Drop opening fence (with optional language tag).
		if idx := strings.Index(s, "\n"); idx > 0 {
			s = s[idx+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	// Find the first '{' if there's leading prose.
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	var inv ExtractedInvoice
	if err := json.Unmarshal([]byte(s), &inv); err != nil {
		return nil, fmt.Errorf("invoice JSON parse: %w", err)
	}
	return &inv, nil
}

// configIntDefault — local to ocr_llm.go; main.go's configIntBps is
// for basis points (rejects values > 100000). DPI / page count have
// different ranges, so a less-opinionated reader.
func configIntDefault(ctx *sdk.AppCtx, key string, def int) int {
	if ctx == nil || ctx.Config() == nil {
		return def
	}
	v := strings.TrimSpace(ctx.Config().Get(key))
	if v == "" {
		return def
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return def
	}
	return n
}

// truncate keeps log output bounded.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
