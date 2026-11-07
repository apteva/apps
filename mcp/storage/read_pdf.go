package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
)

// PDFium's Kill method mutates Go instance fields concurrently with calls.
// Run rendering in a child of this same binary: cancellation kills and waits
// for the process instead, without racing library calls in the serving app.
var readPDFSlots = make(chan struct{}, 1)

func init() {
	if len(os.Args) == 3 && os.Args[1] == "--storage-read-pdf-worker" {
		os.Exit(runPDFWorker(os.Args[2]))
	}
}

type pdfWorkerResponse struct {
	Content []sdk.MCPContent `json:"content,omitempty"`
	Total   int              `json:"total_pages"`
	Error   string           `json:"error,omitempty"`
}

type pdfOutputBuffer struct {
	bytes.Buffer
	limit int
}

func (b *pdfOutputBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("PDF worker output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func readPDF(ctx context.Context, data []byte, pages []int) ([]sdk.MCPContent, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	select {
	case readPDFSlots <- struct{}{}:
		defer func() { <-readPDFSlots }()
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, 0, err
	}
	selection := make([]string, len(pages))
	for i, p := range pages {
		selection[i] = strconv.Itoa(p)
	}
	cmd := exec.CommandContext(ctx, executable, "--storage-read-pdf-worker", strings.Join(selection, ","))
	cmd.Stdin = bytes.NewReader(data)
	output := &pdfOutputBuffer{limit: 24 << 20}
	stderr := &pdfOutputBuffer{limit: 8192}
	cmd.Stdout, cmd.Stderr = output, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, fmt.Errorf("PDF worker: %w: %s", err, stderr.String())
	}
	var response pdfWorkerResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		return nil, 0, fmt.Errorf("decode PDF worker response: %w", err)
	}
	if response.Error != "" {
		return nil, 0, errors.New(response.Error)
	}
	return response.Content, response.Total, nil
}

func runPDFWorker(selection string) int {
	pages, err := parseReadPages(selection)
	var data []byte
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(os.Stdin, readMaxBytes+1))
	}
	if err == nil && len(data) > readMaxBytes {
		err = errors.New("PDF exceeds 25 MiB")
	}
	response := pdfWorkerResponse{}
	if err == nil {
		response.Content, response.Total, err = renderPDF(data, pages)
	}
	if err != nil {
		response.Error = err.Error()
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		return 1
	}
	return 0
}

func renderPDF(data []byte, pages []int) ([]sdk.MCPContent, int, error) {
	config := wazero.NewRuntimeConfig().WithMemoryLimitPages(4096)
	// The cache is optional; read-only hosts still work without it. Wazero keys
	// compiled code by module/version/platform and writes cache entries atomically.
	if base, err := os.UserCacheDir(); err == nil {
		if cache, err := wazero.NewCompilationCacheWithDir(filepath.Join(base, "apteva", "storage-pdf-wasm")); err == nil {
			defer cache.Close(context.Background())
			config = config.WithCompilationCache(cache)
		}
	}
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 0, MaxIdle: 1, MaxTotal: 1, RuntimeConfig: config})
	if err != nil {
		return nil, 0, fmt.Errorf("initialize PDF renderer: %w", err)
	}
	defer pool.Close()
	inst, err := pool.GetInstance(30 * time.Second)
	if err != nil {
		return nil, 0, err
	}
	defer inst.Close()
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, 0, fmt.Errorf("open PDF (encrypted or malformed files are unsupported): %w", err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return nil, 0, err
	}
	for _, p := range pages {
		if p > count.PageCount {
			return nil, 0, fmt.Errorf("page %d is out of range; PDF has %d pages", p, count.PageCount)
		}
	}
	var content []sdk.MCPContent
	for _, p := range pages {
		render, err := inst.RenderPageInPixels(&requests.RenderPageInPixels{
			Page:  requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: p - 1}},
			Width: readImageEdge, Height: readImageEdge, RenderForm: true, Document: &doc.Document,
		})
		if err != nil {
			return nil, 0, fmt.Errorf("render PDF page %d: %w", p, err)
		}
		block, err := imageContent(render.Result.Image)
		render.Cleanup()
		if err != nil {
			return nil, 0, err
		}
		content = append(content, sdk.MCPContent{Type: "text", Text: fmt.Sprintf("Page %d of %d", p, count.PageCount)}, block)
	}
	return content, count.PageCount, nil
}
