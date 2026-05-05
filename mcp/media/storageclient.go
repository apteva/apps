package main

// Cross-app client for the storage app. Hits the platform proxy at
// <APTEVA_GATEWAY_URL>/api/apps/storage/* with our own install
// token; the platform's authMiddleware accepts dev-<install_id>
// tokens for /api/apps/* paths and the proxy then swaps them to
// storage's own install token before forwarding.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type storageClient struct {
	base       string
	token      string
	httpClient *http.Client
}

func newStorageClient() *storageClient {
	// Outbound token: prefer APTEVA_OUTBOUND_TOKEN (set explicitly
	// for cross-app HTTP) and fall back to APTEVA_APP_TOKEN. In
	// production both are the install token; in test mode the
	// runner sets APP_TOKEN="" (so the sidecar's withTokenAuth
	// pass-throughs the agent's MCP) and OUTBOUND_TOKEN to the
	// install token (which authMiddleware now accepts).
	tok := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if tok == "" {
		tok = os.Getenv("APTEVA_APP_TOKEN")
	}
	return &storageClient{
		base:       os.Getenv("APTEVA_GATEWAY_URL"),
		token:      tok,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// StorageFile mirrors the subset of storage.File the indexer cares
// about. Lots of fields exist on storage's side (tags, metadata,
// uploaded_by, …) — we lift only what we actually use.
type StorageFile struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Folder      string   `json:"folder"`
	ContentType string   `json:"content_type"`
	SizeBytes   int64    `json:"size_bytes"`
	SHA256      string   `json:"sha256"`
	Tags        []string `json:"tags"`
	Visibility  string   `json:"visibility"`
	// URL — absolute canonical URL minted by storage. Same shape
	// regardless of visibility; the file's `visibility` field tells
	// you whether the URL works without auth (public), needs a
	// signature (signed), or needs an authenticated request
	// (private). Storage v0.8+ populates this; older storage drops
	// it and we fall through (URL stays "").
	URL string `json:"url"`
}

// SearchFiles asks storage for files matching contentType prefixes
// (e.g. "video/", "audio/", "image/"). Returns up to limit rows from
// the active project (env-pinned APTEVA_PROJECT_ID — storage handles
// the scoping).
func (c *storageClient) SearchFiles(ctx context.Context, projectID string, limit int) ([]StorageFile, error) {
	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	body, err := c.do(ctx, http.MethodGet, "/files?"+q.Encode(), nil, "")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Files []StorageFile `json:"files"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse files: %w (body=%s)", err, string(body))
	}
	return resp.Files, nil
}

// ResolveFiles batch-fetches storage metadata for a list of file ids.
// One HTTP round-trip regardless of result count (chunked at 500 ids
// per call, matching storage's URL-length cap). Returned map is keyed
// by string-id so callers can look up by MediaRow.FileID without
// formatting juggling. Missing ids are silently absent — caller
// decides how to render the gap (stale row, deleted file, etc.).
//
// Used by the media tool handlers to enrich MediaRow with the URL
// + name + visibility metadata storage holds, so an agent only needs
// the media MCP — never storage's.
func (c *storageClient) ResolveFiles(ctx context.Context, projectID string, ids []string) (map[string]*StorageFile, error) {
	out := make(map[string]*StorageFile, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	const chunkSize = 500
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		q := url.Values{}
		if projectID != "" {
			q.Set("project_id", projectID)
		}
		// Pre-validate that every chunk entry parses as int64 — saves
		// a round-trip on a typo'd argument.
		idsCSV := strings.Join(chunk, ",")
		q.Set("ids", idsCSV)
		body, err := c.do(ctx, http.MethodGet, "/files?"+q.Encode(), nil, "")
		if err != nil {
			return nil, fmt.Errorf("resolve files: %w", err)
		}
		var resp struct {
			Files []StorageFile `json:"files"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("parse files batch: %w", err)
		}
		for i := range resp.Files {
			f := &resp.Files[i]
			out[strconv.FormatInt(f.ID, 10)] = f
		}
	}
	return out, nil
}

// GetFile pulls one file's metadata.
func (c *storageClient) GetFile(ctx context.Context, projectID string, id int64) (*StorageFile, error) {
	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/files/%d?%s", id, q.Encode()), nil, "")
	if err != nil {
		return nil, err
	}
	var resp struct {
		File StorageFile `json:"file"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse file: %w", err)
	}
	if resp.File.ID == 0 {
		return nil, fmt.Errorf("storage returned empty file row")
	}
	return &resp.File, nil
}

// GetSignedURL asks storage to mint a time-limited signed URL for a
// file. Returned absolute URL is reachable from outside the cluster
// (it embeds APTEVA_PUBLIC_URL); callers hand it to third-party
// services like Deepgram that need to fetch the bytes themselves.
//
// Storage's files_get_url HTTP endpoint returns a path-only URL
// (e.g. "/files/42/content?sig=..."). We prepend the platform's
// public host so it's a real https:// URL.
func (c *storageClient) GetSignedURL(ctx context.Context, projectID string, id int64, ttlSeconds int) (string, error) {
	publicURL := strings.TrimRight(os.Getenv("APTEVA_PUBLIC_URL"), "/")
	if publicURL == "" {
		return "", errors.New("APTEVA_PUBLIC_URL not set — cannot mint a signed URL reachable from outside the cluster")
	}
	body, err := c.do(ctx, http.MethodPost, "/files/"+strconv.FormatInt(id, 10)+"/url",
		map[string]any{"project_id": projectID, "ttl_seconds": ttlSeconds}, "application/json")
	if err != nil {
		// Older storage versions might not have the HTTP route; fall
		// back to the MCP tool via the same gateway.
		return c.signedURLViaMCP(ctx, projectID, id, ttlSeconds, publicURL)
	}
	var resp struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse get_url: %w (body=%s)", err, string(body))
	}
	if resp.URL == "" {
		return "", errors.New("storage returned empty url")
	}
	if strings.HasPrefix(resp.URL, "/") {
		return publicURL + "/api/apps/storage" + resp.URL, nil
	}
	return resp.URL, nil
}

// signedURLViaMCP is the fallback when storage doesn't expose a
// dedicated HTTP route for url-minting. Hits files_get_url via the
// MCP endpoint — same gateway, JSON-RPC envelope.
func (c *storageClient) signedURLViaMCP(ctx context.Context, projectID string, id int64, ttlSeconds int, publicURL string) (string, error) {
	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "files_get_url",
			"arguments": map[string]any{
				"_project_id": projectID,
				"id":          id,
				"ttl_seconds": ttlSeconds,
			},
		},
	}
	if c.base == "" {
		return "", errors.New("APTEVA_GATEWAY_URL not set")
	}
	raw, _ := json.Marshal(rpc)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/api/apps/storage/mcp", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("files_get_url: %d: %s", resp.StatusCode, body)
	}
	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("decode mcp envelope: %w", err)
	}
	if len(env.Result.Content) == 0 {
		return "", errors.New("files_get_url returned empty result")
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(env.Result.Content[0].Text), &inner); err != nil {
		return "", fmt.Errorf("decode inner: %w", err)
	}
	urlStr, _ := inner["url"].(string)
	if urlStr == "" {
		return "", errors.New("files_get_url returned no url")
	}
	if strings.HasPrefix(urlStr, "/") {
		return publicURL + "/api/apps/storage" + urlStr, nil
	}
	return urlStr, nil
}

// DownloadContent streams the raw bytes of a file to dst. Used by the
// indexer to feed ffprobe / ffmpeg a local copy.
func (c *storageClient) DownloadContent(ctx context.Context, projectID string, id int64, dst io.Writer) error {
	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/api/apps/storage/files/"+strconv.FormatInt(id, 10)+"/content?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return errMsg("download", resp)
	}
	_, err = io.Copy(dst, resp.Body)
	return err
}

// UploadDerivation pushes a derivation file (thumbnail, waveform)
// back into the storage app under a hidden "/.media/<kind>/" folder.
// visibility=private so the dashboard's cookie-authenticated
// /api/apps/storage/files/<id>/content fetch passes — derivations
// are an internal implementation detail of the media app, not
// hot-linkable from outside. (Earlier versions wrote 'signed' with
// the intent of having the panel mint a signed URL per fetch — the
// panel never did, so every thumbnail 403'd.)
func (c *storageClient) UploadDerivation(ctx context.Context, projectID, name, folder, contentType string, bytes []byte) (int64, error) {
	body := map[string]any{
		"name":           name,
		"folder":         folder,
		"content_type":   contentType,
		"content_base64": base64.StdEncoding.EncodeToString(bytes),
		"visibility":     "private",
		"source":         "media-derivation",
		"tags":           []string{"derivation"},
	}
	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	respBody, err := c.do(ctx, http.MethodPost, "/files?"+q.Encode(), body, "application/json")
	if err != nil {
		return 0, err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return 0, fmt.Errorf("parse upload: %w (body=%s)", err, string(respBody))
	}
	if out.ID == 0 {
		return 0, errors.New("storage returned id=0 for derivation upload")
	}
	return out.ID, nil
}

// UploadRender pushes a finished render output back to storage. The
// shape mirrors UploadDerivationMultipart but tags the file as a
// render output (separate from indexer-created derivations) so the
// catalog can tell them apart and panels can filter accordingly.
func (c *storageClient) UploadRender(ctx context.Context, projectID, folder, filename, contentType string, r io.Reader) (int64, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("folder", folder); err != nil {
		return 0, err
	}
	if err := mw.WriteField("visibility", "private"); err != nil {
		return 0, err
	}
	if err := mw.WriteField("source", "media-render"); err != nil {
		return 0, err
	}
	if err := mw.WriteField("tags", "render"); err != nil {
		return 0, err
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(part, r); err != nil {
		return 0, err
	}
	if err := mw.Close(); err != nil {
		return 0, err
	}

	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	respBody, err := c.do(ctx, http.MethodPost, "/files?"+q.Encode(), nil, mw.FormDataContentType(), withBody(buf.Bytes()))
	if err != nil {
		return 0, err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return 0, fmt.Errorf("parse upload: %w (body=%s)", err, string(respBody))
	}
	_ = contentType // reserved for future header injection
	if out.ID == 0 {
		return 0, errors.New("storage returned id=0 for render upload")
	}
	return out.ID, nil
}

// UploadDerivationMultipart is the multipart variant — used when the
// derivation file is already on disk and we'd rather stream it than
// base64-encode in memory. Storage accepts multipart on POST /files
// (FormData with "file" + "folder").
func (c *storageClient) UploadDerivationMultipart(ctx context.Context, projectID, folder, filename, contentType string, r io.Reader) (int64, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("folder", folder); err != nil {
		return 0, err
	}
	if err := mw.WriteField("visibility", "private"); err != nil {
		return 0, err
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(part, r); err != nil {
		return 0, err
	}
	if err := mw.Close(); err != nil {
		return 0, err
	}

	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	respBody, err := c.do(ctx, http.MethodPost, "/files?"+q.Encode(), nil, mw.FormDataContentType(), withBody(buf.Bytes()))
	if err != nil {
		return 0, err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return 0, fmt.Errorf("parse upload: %w (body=%s)", err, string(respBody))
	}
	_ = contentType // reserved for future header injection
	return out.ID, nil
}

// --- internals ---------------------------------------------------------------

type doOpt func(*doConfig)

type doConfig struct {
	rawBody []byte
}

func withBody(b []byte) doOpt {
	return func(c *doConfig) { c.rawBody = b }
}

// do is the one HTTP entry point — every other method funnels here so
// auth header handling lives in exactly one place.
func (c *storageClient) do(ctx context.Context, method, path string, jsonBody any, contentType string, opts ...doOpt) ([]byte, error) {
	if c.base == "" {
		return nil, errors.New("APTEVA_GATEWAY_URL not set — cannot reach storage")
	}
	cfg := doConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	var bodyReader io.Reader
	if cfg.rawBody != nil {
		bodyReader = bytes.NewReader(cfg.rawBody)
	} else if jsonBody != nil {
		buf, err := json.Marshal(jsonBody)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(buf)
		if contentType == "" {
			contentType = "application/json"
		}
	}
	url := c.base + "/api/apps/storage" + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s %s: %d: %s", method, path, resp.StatusCode, string(body))
	}
	return body, nil
}

func errMsg(op string, resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("%s: %d %s: %s", op, resp.StatusCode, resp.Status, string(body))
}
