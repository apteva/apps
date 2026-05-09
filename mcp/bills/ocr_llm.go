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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
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
	log := ctx.Logger()
	bound := ctx.IntegrationFor("vision_llm")
	if bound == nil {
		log.Warn("ocr/llm: vision_llm integration not bound", "file_id", fileID)
		return nil, "llm", errors.New("ocr_provider=llm but vision_llm integration not bound — bind opencode-go (or another compatible chat-completion provider) in the dashboard")
	}
	log.Info("ocr/llm: starting",
		"file_id", fileID,
		"connection_id", bound.ConnectionID,
		"chat_tool", bound.ToolFor("chat.complete"))

	// 1. Storage bytes + content type in one call (storage v0.9.5+).
	t1 := time.Now()
	contentType, rawBytes, err := storageFetchBytes(ctx, fileID)
	if err != nil {
		log.Warn("ocr/llm: storage fetch failed",
			"file_id", fileID, "err", err, "elapsed_ms", time.Since(t1).Milliseconds())
		return nil, "llm", err
	}
	log.Info("ocr/llm: storage fetch ok",
		"file_id", fileID, "bytes", len(rawBytes), "content_type", contentType,
		"elapsed_ms", time.Since(t1).Milliseconds())

	// 2. Materialise as JPEG image(s).
	dpi := configIntDefault(ctx, "render_dpi", 200)
	maxPages := configIntDefault(ctx, "max_pages", 3)
	t2 := time.Now()
	images, err := materialiseImages(rawBytes, contentType, dpi, maxPages)
	if err != nil {
		log.Warn("ocr/llm: render failed",
			"file_id", fileID, "content_type", contentType, "err", err,
			"elapsed_ms", time.Since(t2).Milliseconds())
		return nil, "llm", err
	}
	if len(images) == 0 {
		return nil, "llm", fmt.Errorf("file %d (content_type=%q) produced no images — unsupported format?", fileID, contentType)
	}
	totalImgBytes := 0
	for _, img := range images {
		totalImgBytes += len(img)
	}
	log.Info("ocr/llm: render ok",
		"file_id", fileID, "pages", len(images), "total_bytes", totalImgBytes,
		"dpi", dpi, "elapsed_ms", time.Since(t2).Milliseconds())

	// 3. Build messages + call.
	model := configString(ctx, "ocr_llm_model", "kimi-k2.6")
	maxTokens := configIntDefault(ctx, "ocr_llm_max_tokens", 4000)
	messages := buildOCRMessages(images)

	log.Info("ocr/llm: calling vision_llm",
		"file_id", fileID, "model", model, "max_tokens", maxTokens, "pages", len(images))
	t3 := time.Now()
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
	llmElapsed := time.Since(t3).Milliseconds()
	if err != nil {
		log.Warn("ocr/llm: chat completion call errored",
			"file_id", fileID, "model", model, "err", err, "elapsed_ms", llmElapsed)
		return nil, "llm/" + model, fmt.Errorf("vision_llm chat completion: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		log.Warn("ocr/llm: chat completion non-2xx",
			"file_id", fileID, "model", model,
			"body", truncate(body, 500), "elapsed_ms", llmElapsed)
		return nil, "llm/" + model, fmt.Errorf("vision_llm non-2xx: %s", truncate(body, 500))
	}
	log.Info("ocr/llm: chat completion ok",
		"file_id", fileID, "model", model, "response_bytes", len(res.Data),
		"elapsed_ms", llmElapsed)

	parsed, err := parseAssistantInvoice(res.Data)
	if err != nil {
		log.Warn("ocr/llm: response parse failed",
			"file_id", fileID, "model", model, "err", err)
		return nil, "llm/" + model, fmt.Errorf("parse llm extraction: %w", err)
	}
	if parsed.Provider == "" {
		parsed.Provider = "llm/" + model
	}
	log.Info("ocr/llm: extraction complete",
		"file_id", fileID, "model", model,
		"vendor_name", parsed.Vendor.Name, "vendor_email", parsed.Vendor.Email,
		"invoice_number", parsed.InvoiceNumber, "total_cents", parsed.TotalCents,
		"line_items", len(parsed.LineItems))
	return parsed, "llm/" + model, nil
}

// ─── Storage cross-app helpers ──────────────────────────────────────

// storageFetchBytes pulls the file's bytes via storage's
// files_get_content tool (storage v0.9.5+). Returns content_type +
// bytes in one call. Routes through the binding via CallAppResult so
// it always hits the storage install bound to bills — distinct from
// the v0.1.3 path that minted a signed URL and http.GET'd it, which
// failed when the platform's HTTP routing for /api/apps/storage/*
// landed at a different storage install than the one the bound
// CallAppResult uses (multi-storage-install case).
func storageFetchBytes(ctx *sdk.AppCtx, fileID int64) (contentType string, data []byte, err error) {
	var got struct {
		ID            int64  `json:"id"`
		Name          string `json:"name"`
		ContentType   string `json:"content_type"`
		SizeBytes     int64  `json:"size_bytes"`
		ContentBase64 string `json:"content_base64"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_content",
		map[string]any{"id": fileID}, &got); err != nil {
		return "", nil, fmt.Errorf("storage.files_get_content(%d): %w", fileID, err)
	}
	if got.ContentBase64 == "" {
		return "", nil, fmt.Errorf("storage.files_get_content(%d): empty content_base64 (storage may be older than v0.9.5)", fileID)
	}
	bytes, err := base64.StdEncoding.DecodeString(got.ContentBase64)
	if err != nil {
		return "", nil, fmt.Errorf("decode content_base64: %w", err)
	}
	return got.ContentType, bytes, nil
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
