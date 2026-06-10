package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type storageHTTPClient struct {
	base       string
	token      string
	httpClient *http.Client
}

func newStorageHTTPClient() *storageHTTPClient {
	token := strings.TrimSpace(os.Getenv("APTEVA_OUTBOUND_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN"))
	}
	return &storageHTTPClient{
		base:       strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 10 * time.Minute},
	}
}

func (c *storageHTTPClient) uploadMultipart(ctx context.Context, projectID string, r io.Reader, name, contentType, folder, visibility string, tags []string, size int64, sha256Hex string) (storageUploadResult, error) {
	init, err := c.initUpload(ctx, projectID, map[string]any{
		"filename":     name,
		"size":         size,
		"content_type": contentType,
		"folder":       folder,
		"visibility":   visibility,
		"tags":         tags,
		"sha256":       sha256Hex,
	})
	if err != nil {
		return storageUploadResult{}, err
	}
	if init.File != nil {
		return storageUploadResult{ID: init.File.ID, URL: init.File.URL}, nil
	}
	if init.UploadID == "" || init.PartSize <= 0 {
		return storageUploadResult{}, errors.New("storage upload init returned no upload_id")
	}
	partSize := init.PartSize
	if partSize <= 0 {
		partSize = 5 * 1024 * 1024
	}
	buf := make([]byte, partSize)
	part := 1
	for {
		n, readErr := io.ReadFull(r, buf)
		if readErr == io.EOF {
			break
		}
		if readErr == io.ErrUnexpectedEOF {
			readErr = nil
		}
		if readErr != nil {
			_ = c.abortUpload(context.Background(), projectID, init.UploadID)
			return storageUploadResult{}, readErr
		}
		if n > 0 {
			if err := c.uploadPart(ctx, projectID, init.UploadID, part, buf[:n]); err != nil {
				_ = c.abortUpload(context.Background(), projectID, init.UploadID)
				return storageUploadResult{}, err
			}
			part++
		}
		if n < len(buf) {
			break
		}
	}
	file, err := c.completeUpload(ctx, projectID, init.UploadID, sha256Hex)
	if err != nil {
		_ = c.abortUpload(context.Background(), projectID, init.UploadID)
		return storageUploadResult{}, err
	}
	return storageUploadResult{ID: file.ID, URL: file.URL}, nil
}

type storageUploadInitResponse struct {
	UploadID string                  `json:"upload_id"`
	PartSize int                     `json:"part_size"`
	File     *storageHTTPFileSummary `json:"file,omitempty"`
}

type storageHTTPFileSummary struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

func (c *storageHTTPClient) initUpload(ctx context.Context, projectID string, body map[string]any) (storageUploadInitResponse, error) {
	var out storageUploadInitResponse
	err := c.doJSON(ctx, http.MethodPost, "/uploads", projectID, body, &out)
	return out, err
}

func (c *storageHTTPClient) uploadPart(ctx context.Context, projectID, uploadID string, part int, body []byte) error {
	_, err := c.do(ctx, http.MethodPut, "/uploads/"+url.PathEscape(uploadID)+"/parts/"+strconv.Itoa(part), projectID, bytes.NewReader(body), "application/octet-stream")
	return err
}

func (c *storageHTTPClient) completeUpload(ctx context.Context, projectID, uploadID, sha256Hex string) (storageHTTPFileSummary, error) {
	var out struct {
		File storageHTTPFileSummary `json:"file"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/uploads/"+url.PathEscape(uploadID)+"/complete", projectID, map[string]any{"sha256": sha256Hex}, &out)
	return out.File, err
}

func (c *storageHTTPClient) abortUpload(ctx context.Context, projectID, uploadID string) error {
	_, err := c.do(ctx, http.MethodDelete, "/uploads/"+url.PathEscape(uploadID), projectID, nil, "")
	return err
}

func (c *storageHTTPClient) doJSON(ctx context.Context, method, path, projectID string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, method, path, projectID, bytes.NewReader(raw), "application/json")
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp, out); err != nil {
		return fmt.Errorf("parse storage response: %w (body=%s)", err, string(resp))
	}
	return nil
}

func (c *storageHTTPClient) do(ctx context.Context, method, path, projectID string, body io.Reader, contentType string) ([]byte, error) {
	if c.base == "" {
		return nil, errors.New("APTEVA_GATEWAY_URL not set")
	}
	u := c.base + "/api/apps/storage" + path
	if projectID != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		u += sep + "project_id=" + url.QueryEscape(projectID)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("storage %s %s: http %d: %s", method, path, resp.StatusCode, string(raw))
	}
	return raw, nil
}
