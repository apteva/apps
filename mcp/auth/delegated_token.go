package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

type aptevaDelegatedToken struct {
	AccessToken   string `json:"access_token"`
	TokenType     string `json:"token_type"`
	ExpiresIn     int    `json:"expires_in"`
	ExpiresAt     string `json:"expires_at"`
	KeyPrefix     string `json:"key_prefix"`
	ProjectID     string `json:"project_id"`
	OAuthClientID string `json:"oauth_client_id"`
}

func mintAptevaDelegatedToken(projectID string, org *Organization, user *User, oauthClient *Client) (*aptevaDelegatedToken, error) {
	appToken := strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN"))
	if appToken == "" {
		return nil, nil
	}
	// Auth supplies identity, OAuth client identity, and trusted browser
	// origins only. The platform owns the delegated-access policy and returns
	// no token when that OAuth client has no configured policy.
	if oauthClient == nil || len(oauthClient.AllowedOrigins) == 0 {
		return nil, nil
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("APTEVA_GATEWAY_URL")), "/")
	if base == "" {
		base = "http://127.0.0.1:5280"
	}
	body := map[string]any{
		"project_id":        projectID,
		"oauth_client_id":   oauthClient.ClientID,
		"subject_type":      "user",
		"subject_id":        uintToStr(user.ID),
		"subject_email":     user.Email,
		"organization_id":   uintToStr(org.ID),
		"organization_slug": org.Slug,
		"allowed_origins":   append([]string(nil), oauthClient.AllowedOrigins...),
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, base+"/api/apps/callback/delegated-keys/mint", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+appToken)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var detail map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&detail)
		if msg, _ := detail["error"].(string); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, errors.New("delegated token mint failed")
	}
	var out aptevaDelegatedToken
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, errors.New("delegated token response missing access_token")
	}
	if out.OAuthClientID != oauthClient.ClientID {
		return nil, errors.New("delegated token response did not apply the requested OAuth client policy")
	}
	return &out, nil
}
