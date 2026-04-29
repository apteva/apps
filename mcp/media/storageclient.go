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
	"time"
)

type storageClient struct {
	base       string
	token      string
	httpClient *http.Client
}

func newStorageClient() *storageClient {
	return &storageClient{
		base:       os.Getenv("APTEVA_GATEWAY_URL"),
		token:      os.Getenv("APTEVA_APP_TOKEN"),
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
// back into the storage app under a hidden "/.media/<kind>/" folder,
// visibility=signed so the dashboard can render via signed URL. The
// agent calling files_upload uses a base64 body — same shape here so
// the Content-Type negotiation is consistent.
func (c *storageClient) UploadDerivation(ctx context.Context, projectID, name, folder, contentType string, bytes []byte) (int64, error) {
	body := map[string]any{
		"name":           name,
		"folder":         folder,
		"content_type":   contentType,
		"content_base64": base64.StdEncoding.EncodeToString(bytes),
		"visibility":     "signed",
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
	if err := mw.WriteField("visibility", "signed"); err != nil {
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
