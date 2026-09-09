package main

// The published SDK provides streaming but not recovery-passphrase headers.
// This adapter uses the same permissioned callbacks and outbound install token;
// it can be removed once that SDK extension is released.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const recoveryResponseLimit = 64 << 10

type recoveryHTTPClient struct {
	baseURL, token string
	client         *http.Client
}

func platformRecoveryAPI(api sdk.PlatformBackupClient) (platformRecoveryClient, error) {
	if recovery, ok := api.(platformRecoveryClient); ok {
		return recovery, nil
	}
	token := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if token == "" {
		token = os.Getenv("APTEVA_APP_TOKEN")
	}
	if token == "" {
		return nil, errors.New("platform recovery requires an outbound app token")
	}
	baseURL := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:5280"
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid platform gateway URL")
	}
	client := *platformTransferClient
	// Never forward the recovery passphrase or install credentials on redirects.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &recoveryHTTPClient{baseURL: baseURL, token: token, client: &client}, nil
}

func (c *recoveryHTTPClient) addAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Apteva-App-Install-ID", os.Getenv("APTEVA_INSTALL_ID"))
	if id := os.Getenv("APTEVA_ENVIRONMENT_ID"); id != "" {
		req.Header.Set("X-Apteva-Environment-Id", id)
	}
}

func recoveryResponseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, recoveryResponseLimit))
	return fmt.Errorf("platform %s: http %d: %s", resp.Request.URL.Path, resp.StatusCode, body)
}

func (c *recoveryHTTPClient) OpenPlatformSnapshotWithPassphrase(ctx context.Context, passphrase string) (io.ReadCloser, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/apps/callback/platform/snapshot", nil)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(passphrase, "\r\n") {
		return nil, errors.New("platform recovery passphrase must contain no line breaks")
	}
	if passphrase != "" {
		req.Header.Set("X-Backup-Passphrase", passphrase)
	}
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, recoveryResponseError(resp)
	}
	return resp.Body, nil
}

func (c *recoveryHTTPClient) RestorePlatformSnapshotWithPassphrase(ctx context.Context, body io.Reader, size int64, passphrase string) (map[string]any, error) {
	if body == nil {
		return nil, errors.New("platform restore body is required")
	}
	if size < -1 {
		return nil, errors.New("platform restore size must be -1 or non-negative")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/apps/callback/platform/restore", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("X-Confirm-Restore", "yes")
	if size >= 0 {
		req.ContentLength = size
	} else {
		// Override net/http's reader-size inference so -1 has the promised
		// chunked-transfer meaning even for bytes.Buffer/bytes.Reader inputs.
		req.ContentLength = -1
	}
	if strings.ContainsAny(passphrase, "\r\n") {
		return nil, errors.New("platform recovery passphrase must contain no line breaks")
	}
	if passphrase != "" {
		req.Header.Set("X-Backup-Passphrase", passphrase)
	}
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, recoveryResponseError(resp)
	}
	limited := io.LimitReader(resp.Body, recoveryResponseLimit+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read platform restore response: %w", err)
	}
	if len(raw) > recoveryResponseLimit {
		return nil, errors.New("platform restore response exceeds limit")
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("decode platform restore response: %w", err)
	}
	return report, nil
}
