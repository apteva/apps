package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const storageRecordingProxyPath = "/api/apps/callback/apps/storage/proxy"

type storedRecordingFile struct {
	ID        int64  `json:"id"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type recordingStorageClient struct {
	base  string
	token string
	http  *http.Client
}

type storageProxyError struct {
	StatusCode int
	Body       string
}

func (e *storageProxyError) Error() string {
	return fmt.Sprintf("Storage returned %d: %s", e.StatusCode, e.Body)
}

func newRecordingStorageClient() *recordingStorageClient {
	token := strings.TrimSpace(os.Getenv("APTEVA_OUTBOUND_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN"))
	}
	return &recordingStorageClient{
		base:  strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/"),
		token: token,
		http:  &http.Client{Timeout: 2 * time.Minute},
	}
}

func (c *recordingStorageClient) bindingAvailable(ctx context.Context, projectID string) (bool, error) {
	query := url.Values{"project_id": {projectID}}
	_, err := c.do(ctx, http.MethodGet, "/health?"+query.Encode(), nil, "", nil)
	if err == nil {
		return true, nil
	}
	var proxyErr *storageProxyError
	if errors.As(err, &proxyErr) && proxyErr.StatusCode == http.StatusForbidden &&
		strings.Contains(strings.ToLower(proxyErr.Body), "not bound") {
		return false, nil
	}
	return false, err
}

func (c *recordingStorageClient) upload(ctx context.Context, projectID string, row *recordingRow, path string) (*storedRecordingFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return nil, errors.New("provider recording is empty")
	}
	sha, err := fileSHA256(path)
	if err != nil {
		return nil, err
	}
	created := time.Now().UTC()
	if parsed, parseErr := time.Parse(time.RFC3339, row.CreatedAt); parseErr == nil {
		created = parsed
	}
	folder := fmt.Sprintf("/.telephony/recordings/%04d/%02d/", created.Year(), int(created.Month()))
	filename := fmt.Sprintf("call-%s-%s.%s", row.CallID, row.ProviderRecordingID, recordingExtension(row.Format))
	query := url.Values{"project_id": {projectID}}
	initBody := map[string]any{
		"filename":     filename,
		"size":         info.Size(),
		"content_type": recordingContentType(row.Format),
		"folder":       folder,
		"visibility":   "private",
		"source":       "telephony:" + row.Provider,
		"tags":         []string{"telephony", "call:" + row.CallID, "provider:" + row.Provider},
		"sha256":       sha,
	}
	body, err := c.do(ctx, http.MethodPost, "/uploads?"+query.Encode(), initBody, "application/json", nil)
	if err != nil {
		return nil, fmt.Errorf("initialize Storage upload: %w", err)
	}
	var initialized struct {
		UploadID    string               `json:"upload_id"`
		PartSize    int64                `json:"part_size"`
		MaxParts    int                  `json:"max_parts"`
		WasExisting bool                 `json:"was_existing"`
		File        *storedRecordingFile `json:"file"`
	}
	if err := json.Unmarshal(body, &initialized); err != nil {
		return nil, fmt.Errorf("decode Storage upload initialization: %w", err)
	}
	if initialized.WasExisting && initialized.File != nil {
		return initialized.File, verifyStoredRecording(initialized.File, info.Size(), sha)
	}
	if initialized.UploadID == "" {
		return nil, errors.New("Storage returned no upload id")
	}
	completed := false
	defer func() {
		if !completed {
			abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = c.do(abortCtx, http.MethodDelete, "/uploads/"+url.PathEscape(initialized.UploadID)+"?"+query.Encode(), nil, "", nil)
		}
	}()
	partSize := initialized.PartSize
	if partSize <= 0 {
		partSize = 5 * 1024 * 1024
	}
	parts := int((info.Size() + partSize - 1) / partSize)
	if initialized.MaxParts > 0 && parts > initialized.MaxParts {
		return nil, fmt.Errorf("recording needs %d upload parts; Storage permits %d", parts, initialized.MaxParts)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	buffer := make([]byte, int(partSize))
	for part := 1; ; part++ {
		n, readErr := io.ReadFull(file, buffer)
		if readErr == io.EOF {
			break
		}
		if readErr == io.ErrUnexpectedEOF {
			readErr = nil
		}
		if readErr != nil {
			return nil, fmt.Errorf("read recording part %d: %w", part, readErr)
		}
		partPath := fmt.Sprintf("/uploads/%s/parts/%d?%s", url.PathEscape(initialized.UploadID), part, query.Encode())
		if _, err := c.do(ctx, http.MethodPut, partPath, nil, "application/octet-stream", bytes.NewReader(buffer[:n])); err != nil {
			return nil, fmt.Errorf("upload recording part %d/%d: %w", part, parts, err)
		}
		if n < len(buffer) {
			break
		}
	}
	body, err = c.do(ctx, http.MethodPost, "/uploads/"+url.PathEscape(initialized.UploadID)+"/complete?"+query.Encode(), map[string]any{"sha256": sha}, "application/json", nil)
	if err != nil {
		return nil, fmt.Errorf("complete Storage upload: %w", err)
	}
	var result struct {
		File *storedRecordingFile `json:"file"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.File == nil {
		return nil, fmt.Errorf("decode Storage upload completion: %w", err)
	}
	if err := verifyStoredRecording(result.File, info.Size(), sha); err != nil {
		return nil, err
	}
	completed = true
	return result.File, nil
}

func (c *recordingStorageClient) do(ctx context.Context, method, path string, jsonBody any, contentType string, raw io.Reader) ([]byte, error) {
	if c.base == "" || c.token == "" {
		return nil, errors.New("Storage app transport is unavailable")
	}
	var body io.Reader = raw
	if jsonBody != nil {
		encoded, err := json.Marshal(jsonBody)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+storageRecordingProxyPath+"/"+strings.TrimLeft(path, "/"), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &storageProxyError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(responseBody))}
	}
	return responseBody, nil
}

func downloadTwilioRecording(ctx context.Context, creds map[string]string, recordingSID, format string) (string, int64, error) {
	return downloadTwilioRecordingChannels(ctx, creds, recordingSID, format, 0)
}

func downloadTwilioRecordingChannels(ctx context.Context, creds map[string]string, recordingSID, format string, requestedChannels int) (string, int64, error) {
	accountSID := strings.TrimSpace(creds["account_sid"])
	authToken := strings.TrimSpace(creds["auth_token"])
	if accountSID == "" || authToken == "" {
		return "", 0, errors.New("Twilio account_sid and auth_token are required to download recordings")
	}
	if !strings.HasPrefix(recordingSID, "RE") || len(recordingSID) > 64 {
		return "", 0, errors.New("invalid Twilio recording SID")
	}
	ext := recordingExtension(format)
	downloadURL := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Recordings/%s.%s", url.PathEscape(accountSID), url.PathEscape(recordingSID), ext)
	if requestedChannels == 1 || requestedChannels == 2 {
		downloadURL += "?RequestedChannels=" + strconv.Itoa(requestedChannels)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", 0, err
	}
	req.SetBasicAuth(accountSID, authToken)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", 0, fmt.Errorf("Twilio recording download returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	tmp, err := os.CreateTemp("", "apteva-telephony-recording-*."+ext)
	if err != nil {
		return "", 0, err
	}
	path := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	written, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	if written <= 0 {
		return "", 0, errors.New("Twilio recording download was empty")
	}
	ok = true
	return path, written, nil
}

func (c *recordingStorageClient) download(ctx context.Context, projectID string, fileID int64, format string) (string, int64, error) {
	if c.base == "" || c.token == "" || fileID <= 0 {
		return "", 0, errors.New("Storage app transport is unavailable")
	}
	query := url.Values{"project_id": {projectID}}
	downloadURL := c.base + storageRecordingProxyPath + "/files/" + strconv.FormatInt(fileID, 10) + "/content?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	client := *c.http
	if client.Timeout < 10*time.Minute {
		client.Timeout = 10 * time.Minute
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", 0, &storageProxyError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	ext := recordingExtension(format)
	tmp, err := os.CreateTemp("", "apteva-telephony-stored-recording-*."+ext)
	if err != nil {
		return "", 0, err
	}
	path := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, recordingDownloadLimit(ctx)+1))
	if err != nil {
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	if written <= 0 || written > recordingDownloadLimit(ctx) {
		return "", 0, errors.New("stored recording download empty or exceeds size limit")
	}
	ok = true
	return path, written, nil
}

func verifyStoredRecording(file *storedRecordingFile, size int64, sha string) error {
	if file == nil {
		return errors.New("Storage verification failed: no file metadata returned")
	}
	if file.ID <= 0 || file.SizeBytes != size || !strings.EqualFold(file.SHA256, sha) {
		return fmt.Errorf("Storage verification failed: id=%d size=%d expected_size=%d", file.ID, file.SizeBytes, size)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func recordingExtension(format string) string {
	if strings.EqualFold(format, "mp3") {
		return "mp3"
	}
	return "wav"
}

func recordingContentType(format string) string {
	if strings.EqualFold(format, "mp3") {
		return "audio/mpeg"
	}
	return "audio/wav"
}

func storagePlaybackURL(projectID string, fileID int64) string {
	return "/api/apps/storage/files/" + strconv.FormatInt(fileID, 10) + "/content?project_id=" + url.QueryEscape(projectID)
}

func providerPlaybackURL(projectID, recordingID string) string {
	return "/api/apps/telephony/recordings/" + url.PathEscape(recordingID) + "/content?project_id=" + url.QueryEscape(projectID)
}

func recordingVariantPlaybackURL(projectID, recordingID, variant string) string {
	return providerPlaybackURL(projectID, recordingID) + "&variant=" + url.QueryEscape(variant)
}
