package main

// Cross-app client for the storage app. All streaming HTTP goes
// through the platform's binding-gated callback proxy. Media keeps
// its own install token; the platform verifies platform.apps.call +
// the exact requires.apps binding, then swaps in Storage's token.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultStorageUploadPartSize int64 = 5 * 1024 * 1024
	boundStorageProxyPath              = "/api/apps/callback/apps/storage/proxy"
)

type storageClient struct {
	base       string
	token      string
	httpClient *http.Client
}

const (
	storageDeliveryApteva    = "apteva"
	storageDeliveryDirect    = "direct"
	storageDeliveryProxy     = "proxy"
	storageDispositionInline = "inline"
	storageDispositionAttach = "attachment"
)

// StorageSignedURL is Storage's confirmed response to files_get_url.
// Delivery and disposition are copied from Storage's effective result
// rather than inferred from Media's request.
type StorageSignedURL struct {
	URL         string `json:"url"`
	Delivery    string `json:"delivery,omitempty"`
	Disposition string `json:"disposition,omitempty"`
	ExpiresAt   int64  `json:"expires_at,omitempty"`
	FileID      int64  `json:"file_id,omitempty"`
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
	gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	proxyBase := ""
	if gateway != "" {
		proxyBase = gateway + boundStorageProxyPath
	}
	return &storageClient{
		base:       proxyBase,
		token:      tok,
		httpClient: &http.Client{Timeout: 30 * time.Minute},
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
	Source      string   `json:"source"`
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

// SearchFiles returns the first page of the active project's Storage
// inventory. Call SearchAllFiles for a complete sweep.
func (c *storageClient) SearchFiles(ctx context.Context, projectID string, limit int) ([]StorageFile, error) {
	return c.SearchFilesPage(ctx, projectID, limit, 0)
}

// SearchFilesPage fetches one stable Storage inventory page. Offset is
// supported by Storage v0.10.23+; Media's manifest pins that minimum so
// a catalog can grow beyond any single response window.
func (c *storageClient) SearchFilesPage(ctx context.Context, projectID string, limit, offset int) ([]StorageFile, error) {
	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
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

// SearchAllFiles walks the complete Storage inventory without a fixed
// catalog-size ceiling. IDs are de-duplicated defensively because uploads
// can move the offset window while a sweep is in progress; a later tick
// will pick up anything inserted concurrently.
func (c *storageClient) SearchAllFiles(ctx context.Context, projectID string, pageSize int) ([]StorageFile, error) {
	if pageSize <= 0 {
		pageSize = 5000
	}
	all := make([]StorageFile, 0, pageSize)
	seen := make(map[int64]struct{}, pageSize)
	for offset := 0; ; {
		page, err := c.SearchFilesPage(ctx, projectID, pageSize, offset)
		if err != nil {
			return nil, err
		}
		newRows := 0
		for _, f := range page {
			if _, ok := seen[f.ID]; ok {
				continue
			}
			seen[f.ID] = struct{}{}
			all = append(all, f)
			newRows++
		}
		if len(page) < pageSize {
			return all, nil
		}
		if newRows == 0 {
			return nil, errors.New("storage inventory pagination made no progress; Storage v0.10.23+ is required")
		}
		offset += len(page)
	}
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

// GetSignedURL asks Storage to mint a time-limited Apteva-delivered URL
// with inline disposition. It remains a string-only compatibility
// wrapper for internal callers; media_get uses GetSignedURLInfo to
// expose Storage's confirmed URL characteristics.
//
// Pre-v0.12.6 this read `os.Getenv("APTEVA_PUBLIC_URL")` directly,
// which captures the tunnel URL at sidecar SPAWN time. When ngrok
// flapped (new subdomain) operators had to restart the sidecar to
// get fresh URLs — and forgotten restarts manifested as remote
// renders failing with `curl: (22) The requested URL returned error:
// 404` because the script's STORAGE_BASE pointed at a dead subdomain.
// Now we route through resolvePublicURL, which prefers the SDK's
// hot-cached PlatformInfo() (60s freshness; picks up server-setting
// edits without restart) and falls back to the env for older
// platforms / unusual setups.
func (c *storageClient) GetSignedURL(ctx context.Context, projectID string, id int64, ttlSeconds int) (string, error) {
	info, err := c.GetSignedURLInfo(ctx, projectID, id, ttlSeconds)
	if err != nil {
		return "", err
	}
	return info.URL, nil
}

// GetSignedURLInfo is the structured form of GetSignedURL. Both the
// HTTP path and its MCP fallback explicitly request Apteva delivery so
// S3-backed Storage does not select legacy direct delivery.
func (c *storageClient) GetSignedURLInfo(ctx context.Context, projectID string, id int64, ttlSeconds int) (StorageSignedURL, error) {
	return c.GetSignedURLInfoWithOptions(ctx, projectID, id, ttlSeconds, storageDeliveryApteva, storageDispositionInline)
}

// GetSignedURLInfoWithOptions requests the caller-selected delivery and
// disposition while preserving Apteva/inline as the empty-value defaults.
func (c *storageClient) GetSignedURLInfoWithOptions(ctx context.Context, projectID string, id int64, ttlSeconds int, delivery, disposition string) (StorageSignedURL, error) {
	delivery, disposition, err := normalizeStorageURLRequest(delivery, disposition)
	if err != nil {
		return StorageSignedURL{}, err
	}
	publicURL, err := resolvePublicURL(globalCtx)
	if err != nil {
		return StorageSignedURL{}, fmt.Errorf("cannot mint a signed URL reachable from outside the cluster: %w", err)
	}
	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	body, err := c.do(ctx, http.MethodPost, "/files/"+strconv.FormatInt(id, 10)+"/url?"+q.Encode(),
		map[string]any{
			"project_id":  projectID,
			"ttl_seconds": ttlSeconds,
			"delivery":    delivery,
			"disposition": disposition,
		}, "application/json")
	if err != nil {
		// Fall back to the MCP tool via the same binding-gated gateway.
		return c.signedURLViaMCP(ctx, projectID, id, ttlSeconds, delivery, disposition, publicURL)
	}
	var resp StorageSignedURL
	if err := json.Unmarshal(body, &resp); err != nil {
		return StorageSignedURL{}, fmt.Errorf("parse get_url: %w (body=%s)", err, string(body))
	}
	if resp.URL == "" {
		return StorageSignedURL{}, errors.New("storage returned empty url")
	}
	resp.URL = absolutizeStorageURL(publicURL, resp.URL)
	return resp, nil
}

// signedURLViaMCP is the fallback when storage doesn't expose a
// dedicated HTTP route for url-minting. Hits files_get_url via the
// MCP endpoint — same gateway, JSON-RPC envelope.
func (c *storageClient) signedURLViaMCP(ctx context.Context, projectID string, id int64, ttlSeconds int, delivery, disposition, publicURL string) (StorageSignedURL, error) {
	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "files_get_url",
			"arguments": map[string]any{
				"_project_id": projectID,
				"id":          id,
				"ttl_seconds": ttlSeconds,
				"delivery":    delivery,
				"disposition": disposition,
			},
		},
	}
	if c.base == "" {
		return StorageSignedURL{}, errors.New("APTEVA_GATEWAY_URL not set")
	}
	raw, _ := json.Marshal(rpc)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/mcp?project_id="+url.QueryEscape(projectID), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return StorageSignedURL{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return StorageSignedURL{}, fmt.Errorf("files_get_url: %d: %s", resp.StatusCode, body)
	}
	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return StorageSignedURL{}, fmt.Errorf("decode mcp envelope: %w", err)
	}
	if len(env.Result.Content) == 0 {
		return StorageSignedURL{}, errors.New("files_get_url returned empty result")
	}
	var result StorageSignedURL
	if err := json.Unmarshal([]byte(env.Result.Content[0].Text), &result); err != nil {
		return StorageSignedURL{}, fmt.Errorf("decode inner: %w", err)
	}
	if result.URL == "" {
		return StorageSignedURL{}, errors.New("files_get_url returned no url")
	}
	result.URL = absolutizeStorageURL(publicURL, result.URL)
	return result, nil
}

func normalizeStorageURLRequest(delivery, disposition string) (string, string, error) {
	delivery = strings.ToLower(strings.TrimSpace(delivery))
	if delivery == "" {
		delivery = storageDeliveryApteva
	}
	if delivery != storageDeliveryApteva && delivery != storageDeliveryDirect && delivery != storageDeliveryProxy {
		return "", "", errors.New("delivery must be one of: proxy, apteva, direct")
	}
	disposition = strings.ToLower(strings.TrimSpace(disposition))
	if disposition == "" {
		disposition = storageDispositionInline
	}
	if disposition != storageDispositionInline && disposition != storageDispositionAttach {
		return "", "", errors.New("disposition must be one of: inline, attachment")
	}
	return delivery, disposition, nil
}

func absolutizeStorageURL(publicURL, urlStr string) string {
	if strings.HasPrefix(urlStr, "/") {
		if strings.HasPrefix(urlStr, "/api/apps/storage/") {
			return publicURL + urlStr
		}
		return publicURL + "/api/apps/storage" + urlStr
	}
	return urlStr
}

// DeleteFile hard-deletes a storage file by id. Used by the media
// cascade when a source file is removed — we follow up by deleting
// every derivation (thumbnail, waveform, keyframes) the indexer
// uploaded under /.media/. Without this, those bytes accumulate
// forever; the media DB rows get cleaned but the storage objects
// orphan.
//
// "Hard" matches our intent: the derivations are byproducts, not
// audit history; nothing else references them once the source is
// gone. Failures (storage temporarily down, row already deleted,
// etc.) are returned to the caller, which decides whether to log
// and continue or abort the cascade.
func (c *storageClient) DeleteFile(ctx context.Context, projectID string, id int64) error {
	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "files_delete",
			"arguments": map[string]any{
				"_project_id": projectID,
				"id":          id,
			},
		},
	}
	if c.base == "" {
		return errors.New("APTEVA_GATEWAY_URL not set")
	}
	raw, _ := json.Marshal(rpc)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/mcp?project_id="+url.QueryEscape(projectID), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("files_delete: %d: %s", resp.StatusCode, body)
	}
	return nil
}

// DownloadContent streams the raw bytes of a file to dst. Used by the
// indexer to feed ffprobe / ffmpeg a local copy.
func (c *storageClient) DownloadContent(ctx context.Context, projectID string, id int64, dst io.Writer) error {
	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/files/"+strconv.FormatInt(id, 10)+"/content?"+q.Encode(), nil)
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
	tmp, err := os.CreateTemp("", "apteva-media-render-upload-*")
	if err != nil {
		return 0, fmt.Errorf("create upload temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return 0, fmt.Errorf("spool render output: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("close upload temp: %w", err)
	}
	return c.UploadRenderFile(ctx, projectID, folder, filename, contentType, tmpPath)
}

// UploadRenderFile pushes a finished render output back to storage via
// storage's resumable /uploads protocol. That path validates declared
// size + sha256 before creating the final storage row, so media cannot
// mark a render ok after a truncated single-shot upload.
func (c *storageClient) UploadRenderFile(ctx context.Context, projectID, folder, filename, contentType, path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat render output: %w", err)
	}
	if info.Size() <= 0 {
		return 0, errors.New("render output is empty")
	}
	sha, err := sha256File(path)
	if err != nil {
		return 0, err
	}
	file, err := c.uploadFileChunked(ctx, projectID, folder, filename, contentType, path, info.Size(), sha)
	if err != nil {
		return 0, err
	}
	if file.ID == 0 {
		return 0, errors.New("storage returned id=0 for render upload")
	}
	if file.SizeBytes != info.Size() {
		return 0, fmt.Errorf("storage render upload size mismatch: local=%d stored=%d file_id=%d",
			info.Size(), file.SizeBytes, file.ID)
	}
	if !strings.EqualFold(file.SHA256, sha) {
		return 0, fmt.Errorf("storage render upload sha mismatch: local=%s stored=%s file_id=%d",
			sha, file.SHA256, file.ID)
	}
	if err := validateRenderUploadDestination(file, folder, filename); err != nil {
		return 0, err
	}
	return file.ID, nil
}

// validateRenderUploadDestination prevents a SHA-only Storage dedupe result
// from masquerading as a newly-created render output. Render callers asked
// for a specific filename and folder; returning an older record elsewhere is
// not success even when the bytes are identical.
func validateRenderUploadDestination(file *StorageFile, folder, filename string) error {
	if file == nil {
		return errors.New("storage returned no file for render upload")
	}
	expectedFolder := normalizeFolderFilter(folder)
	if expectedFolder == "" {
		expectedFolder = "/"
	}
	actualFolder := normalizeFolderFilter(file.Folder)
	if actualFolder == "" {
		actualFolder = "/"
	}
	if file.Name != filename || actualFolder != expectedFolder {
		return fmt.Errorf(
			"storage returned render file_id=%d at %s%s; expected %s%s; refusing to mark render complete",
			file.ID, actualFolder, file.Name, expectedFolder, filename,
		)
	}
	return nil
}

func (c *storageClient) uploadFileChunked(ctx context.Context, projectID, folder, filename, contentType, path string, size int64, sha string) (*StorageFile, error) {
	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	initBody := map[string]any{
		"filename":     filename,
		"size":         size,
		"content_type": contentType,
		"folder":       folder,
		"visibility":   "private",
		"source":       "media-render",
		"tags":         []string{"render"},
		"sha256":       sha,
	}
	respBody, err := c.do(ctx, http.MethodPost, "/uploads?"+q.Encode(), initBody, "application/json")
	if err != nil {
		return nil, fmt.Errorf("init upload: %w", err)
	}
	var initResp struct {
		UploadID    string       `json:"upload_id"`
		PartSize    int64        `json:"part_size"`
		MaxParts    int          `json:"max_parts"`
		WasExisting bool         `json:"was_existing"`
		File        *StorageFile `json:"file"`
	}
	if err := json.Unmarshal(respBody, &initResp); err != nil {
		return nil, fmt.Errorf("parse upload init: %w (body=%s)", err, string(respBody))
	}
	if initResp.WasExisting && initResp.File != nil {
		return initResp.File, nil
	}
	if initResp.UploadID == "" {
		return nil, fmt.Errorf("storage upload init returned no upload_id (body=%s)", string(respBody))
	}
	uploadID := initResp.UploadID
	defer func() {
		if err != nil {
			_ = c.abortUpload(context.Background(), projectID, uploadID)
		}
	}()

	partSize := initResp.PartSize
	if partSize <= 0 {
		partSize = defaultStorageUploadPartSize
	}
	totalParts := int((size + partSize - 1) / partSize)
	if initResp.MaxParts > 0 && totalParts > initResp.MaxParts {
		err = fmt.Errorf("render output too large for storage upload: needs %d parts, max %d", totalParts, initResp.MaxParts)
		return nil, err
	}

	f, openErr := os.Open(path)
	if openErr != nil {
		err = fmt.Errorf("open render output: %w", openErr)
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, int(partSize))
	for part := 1; ; part++ {
		n, readErr := io.ReadFull(f, buf)
		if readErr == io.EOF {
			break
		}
		if readErr == io.ErrUnexpectedEOF {
			readErr = nil
		}
		if readErr != nil {
			err = fmt.Errorf("read render output part %d: %w", part, readErr)
			return nil, err
		}
		if n == 0 {
			break
		}
		if _, err = c.do(ctx, http.MethodPut,
			fmt.Sprintf("/uploads/%s/parts/%d?%s", url.PathEscape(uploadID), part, q.Encode()),
			nil, "application/octet-stream", withBody(buf[:n])); err != nil {
			err = fmt.Errorf("upload part %d/%d: %w", part, totalParts, err)
			return nil, err
		}
		if n < len(buf) {
			break
		}
	}

	respBody, err = c.do(ctx, http.MethodPost,
		fmt.Sprintf("/uploads/%s/complete?%s", url.PathEscape(uploadID), q.Encode()),
		map[string]any{"sha256": sha}, "application/json")
	if err != nil {
		return nil, fmt.Errorf("complete upload: %w", err)
	}
	var completeResp struct {
		File        *StorageFile `json:"file"`
		WasExisting bool         `json:"was_existing"`
	}
	if err = json.Unmarshal(respBody, &completeResp); err != nil {
		return nil, fmt.Errorf("parse upload complete: %w (body=%s)", err, string(respBody))
	}
	if completeResp.File == nil || completeResp.File.ID == 0 {
		return nil, fmt.Errorf("storage upload complete returned no file (body=%s)", string(respBody))
	}
	return completeResp.File, nil
}

func (c *storageClient) abortUpload(ctx context.Context, projectID, uploadID string) error {
	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	_, err := c.do(ctx, http.MethodDelete,
		fmt.Sprintf("/uploads/%s?%s", url.PathEscape(uploadID), q.Encode()), nil, "")
	return err
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open for sha256: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash render output: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c *storageClient) uploadRenderSingleShot(ctx context.Context, projectID, folder, filename, contentType string, r io.Reader) (int64, error) {
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
	if err := writeFilePartWithType(mw, filename, contentType, r); err != nil {
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
	if out.ID == 0 {
		return 0, errors.New("storage returned id=0 for render upload")
	}
	return out.ID, nil
}

// writeFilePartWithType writes a multipart "file" part with an
// explicit Content-Type header. Go's standard CreateFormFile ALWAYS
// sets the part header to "application/octet-stream" — the parameter
// is not exposed — and storage reads exactly that header to populate
// files.content_type. So a render output named frame.png landed in
// storage with content_type="application/octet-stream", which made
// the panel say "No preview available" and broke download MIME-
// sniffing for any client that respects the content_type column.
//
// The textproto.MIMEHeader form lets us drop in the real MIME type
// (image/png for extract_frame, video/mp4 for transcodes, etc.) so
// storage saves the right value and previews work end-to-end.
func writeFilePartWithType(mw *multipart.Writer, filename, contentType string, r io.Reader) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="file"; filename=%q`, escapeQuotes(filename)))
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, r); err != nil {
		return err
	}
	return nil
}

// escapeQuotes mirrors mime/multipart's internal quoteEscaper, which
// is unexported. Avoids breaking Content-Disposition on filenames
// with embedded quotes or backslashes.
func escapeQuotes(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// UploadDerivationMultipart is the multipart variant — used when the
// derivation file is already on disk and we'd rather stream it than
// base64-encode in memory. Storage accepts multipart on POST /files
// (FormData with "file" + "folder").
func (c *storageClient) UploadDerivationMultipart(ctx context.Context, projectID, folder, filename, contentType string, r io.Reader) (int64, error) {
	return c.UploadInternalFile(ctx, projectID, folder, filename, contentType, r, "", "")
}

// UploadInternalFile pushes a media-owned byproduct into storage.
// Used for derivations and the hidden transcript-audio proxy. The
// file remains private and lives under a hidden folder, so the media
// catalog will not index it as user content.
func (c *storageClient) UploadInternalFile(ctx context.Context, projectID, folder, filename, contentType string, r io.Reader, source, tags string) (int64, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("folder", folder); err != nil {
		return 0, err
	}
	if err := mw.WriteField("visibility", "private"); err != nil {
		return 0, err
	}
	if source != "" {
		if err := mw.WriteField("source", source); err != nil {
			return 0, err
		}
	}
	if tags != "" {
		if err := mw.WriteField("tags", tags); err != nil {
			return 0, err
		}
	}
	if err := writeFilePartWithType(mw, filename, contentType, r); err != nil {
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
	url := c.base + path
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
