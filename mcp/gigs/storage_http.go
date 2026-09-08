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
	"strings"
	"time"
)

// Only the platform's binding-gated proxy is used. Gigs never accepts a target
// URL or Storage credential from the worker; all paths are built server-side.
var storageHTTPClient = &http.Client{Timeout: 30 * time.Minute}

type storageHTTPError struct {
	Status  int
	Message string
}

func (e *storageHTTPError) Error() string {
	return fmt.Sprintf("Storage HTTP %d: %s", e.Status, e.Message)
}

func storageHTTP(ctx context.Context, pid, method, path string, body io.Reader, length int64, out any) error {
	gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if gateway == "" {
		return errors.New("Storage gateway is not configured")
	}
	token := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if token == "" {
		token = os.Getenv("APTEVA_APP_TOKEN")
	}
	target := gateway + "/api/apps/callback/apps/storage/proxy" + path + "?project_id=" + url.QueryEscape(pid)
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	req.ContentLength = length
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	res, err := storageHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("Storage connection interrupted: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("Storage response interrupted: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		message := e.Error
		if message == "" {
			message = http.StatusText(res.StatusCode)
		}
		if len(message) > 500 {
			message = message[:500]
		}
		return &storageHTTPError{res.StatusCode, message}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("Storage returned an invalid response")
		}
	}
	return nil
}
func storageHTTPJSON(ctx context.Context, pid, method, path string, input, out any) error {
	var data []byte
	if input != nil {
		var err error
		data, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	return storageHTTP(ctx, pid, method, path, bytes.NewReader(data), int64(len(data)), out)
}

type storageCompletion struct {
	File        storageFileMetadata `json:"file"`
	WasExisting bool                `json:"was_existing"`
}
type storagePart struct {
	N    int   `json:"n"`
	Size int64 `json:"size"`
}
type storageUploadStatus struct {
	Parts         []storagePart `json:"parts"`
	BytesUploaded int64         `json:"bytes_uploaded"`
	DeclaredSize  int64         `json:"declared_size"`
}
