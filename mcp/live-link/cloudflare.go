// cloudflare.go — minimal Cloudflare API client for named tunnels.
//
// We only call the handful of endpoints v0.2 needs:
//
//   POST   /accounts/:id/cfd_tunnel                          create
//   GET    /accounts/:id/cfd_tunnel?name=<n>                 find by name
//   PUT    /accounts/:id/cfd_tunnel/:tid/configurations      set ingress
//   DELETE /accounts/:id/cfd_tunnel/:tid                     destroy
//   GET    /zones/:id/dns_records?type=CNAME&name=<host>     find existing
//   POST   /zones/:id/dns_records                            create CNAME
//   PUT    /zones/:id/dns_records/:rid                       update CNAME
//   DELETE /zones/:id/dns_records/:rid                       undo route
//
// Everything else (paging, token rotation, account lookup) is out of
// scope. The client has no mutable state — token + base URL + http
// client — so it's safe to share across goroutines.
//
// Why "minimal" and not cloudflare-go: we already pull modernc.org/sqlite
// and the app-sdk; adding the official SDK plus its dependency tree just
// for six calls would dwarf live-link's own code. The whole client is
// ~120 LOC and easy to mock in tests via baseURL.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const cfAPIBase = "https://api.cloudflare.com/client/v4"

type cfClient struct {
	apiToken string
	baseURL  string
	hc       *http.Client
}

func newCFClient(apiToken string) *cfClient {
	base := cfAPIBase
	// Test-only escape hatch — in tests we point at httptest. Reading
	// from env keeps the production code path single-line and avoids a
	// hook/var that would show up in IDE autocomplete.
	if v := os.Getenv("LIVE_LINK_CF_API_BASE"); v != "" {
		base = v
	}
	return &cfClient{
		apiToken: apiToken,
		baseURL:  base,
		hc:       &http.Client{Timeout: 30 * time.Second},
	}
}

type cfTunnel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token,omitempty"`
}

type cfDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// createTunnel creates a cfd_tunnel and returns its UUID + the
// connector token used to run it. CF only returns the token at create
// time; callers must persist it.
//
// We pin config_src=cloudflare so ingress is managed via the API
// (putTunnelConfig below) rather than a local config.yml — that's what
// lets the sidecar own the tunnel config end-to-end without writing
// files into ~/.cloudflared/.
func (c *cfClient) createTunnel(accountID, name string) (*cfTunnel, error) {
	body, _ := json.Marshal(map[string]any{
		"name":       name,
		"config_src": "cloudflare",
	})
	var resp struct {
		Result cfTunnel `json:"result"`
	}
	if err := c.do("POST", fmt.Sprintf("/accounts/%s/cfd_tunnel", accountID), body, &resp); err != nil {
		return nil, err
	}
	if resp.Result.ID == "" {
		return nil, fmt.Errorf("cloudflare returned empty tunnel id")
	}
	return &resp.Result, nil
}

// findTunnelByName returns a non-deleted tunnel with this name, or nil
// if none exists. Used for idempotent setup: if our DB row was lost
// but the tunnel survives in CF, we adopt it instead of duplicating.
func (c *cfClient) findTunnelByName(accountID, name string) (*cfTunnel, error) {
	q := url.Values{}
	q.Set("name", name)
	q.Set("is_deleted", "false")
	var resp struct {
		Result []cfTunnel `json:"result"`
	}
	if err := c.do("GET", fmt.Sprintf("/accounts/%s/cfd_tunnel?%s", accountID, q.Encode()), nil, &resp); err != nil {
		return nil, err
	}
	for i := range resp.Result {
		if resp.Result[i].Name == name {
			return &resp.Result[i], nil
		}
	}
	return nil, nil
}

// putTunnelConfig writes ingress: <hostname> → <service>, with a
// catch-all 404. We rewrite the whole config every call — live-link
// owns the tunnel, so there's no other ingress to preserve.
func (c *cfClient) putTunnelConfig(accountID, tunnelID, hostname, service string) error {
	body, _ := json.Marshal(map[string]any{
		"config": map[string]any{
			"ingress": []map[string]any{
				{"hostname": hostname, "service": service},
				{"service": "http_status:404"},
			},
		},
	})
	return c.do("PUT", fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", accountID, tunnelID), body, nil)
}

func (c *cfClient) deleteTunnel(accountID, tunnelID string) error {
	return c.do("DELETE", fmt.Sprintf("/accounts/%s/cfd_tunnel/%s", accountID, tunnelID), nil, nil)
}

// upsertDNSCNAME ensures a proxied CNAME points hostname at
// <tunnelID>.cfargotunnel.com. Returns the record id so we can clean
// up on uninstall. Uses GET-then-POST/PUT to avoid the
// duplicate-record error you get from a blind POST.
func (c *cfClient) upsertDNSCNAME(zoneID, hostname, tunnelID string) (string, error) {
	target := tunnelID + ".cfargotunnel.com"

	q := url.Values{}
	q.Set("type", "CNAME")
	q.Set("name", hostname)
	var listResp struct {
		Result []cfDNSRecord `json:"result"`
	}
	if err := c.do("GET", fmt.Sprintf("/zones/%s/dns_records?%s", zoneID, q.Encode()), nil, &listResp); err != nil {
		return "", err
	}

	body, _ := json.Marshal(map[string]any{
		"type":    "CNAME",
		"name":    hostname,
		"content": target,
		"proxied": true,
		"ttl":     1, // 1 = automatic; required when proxied
	})
	if len(listResp.Result) > 0 {
		rec := listResp.Result[0]
		var resp struct {
			Result cfDNSRecord `json:"result"`
		}
		if err := c.do("PUT", fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, rec.ID), body, &resp); err != nil {
			return "", err
		}
		return resp.Result.ID, nil
	}
	var resp struct {
		Result cfDNSRecord `json:"result"`
	}
	if err := c.do("POST", fmt.Sprintf("/zones/%s/dns_records", zoneID), body, &resp); err != nil {
		return "", err
	}
	return resp.Result.ID, nil
}

func (c *cfClient) deleteDNSRecord(zoneID, recordID string) error {
	return c.do("DELETE", fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, recordID), nil, nil)
}

func (c *cfClient) do(method, path string, body []byte, out any) error {
	var bodyR io.Reader
	if body != nil {
		bodyR = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.baseURL+path, bodyR)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		// CF returns {"errors":[{"code":..,"message":..}]} on failure.
		var ce struct {
			Errors []struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(raw, &ce)
		if len(ce.Errors) > 0 {
			return fmt.Errorf("cloudflare %s %s: %d %s (code %d)",
				method, path, resp.StatusCode, ce.Errors[0].Message, ce.Errors[0].Code)
		}
		return fmt.Errorf("cloudflare %s %s: %d %s",
			method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("cloudflare %s %s: decode: %w", method, path, err)
		}
	}
	return nil
}
