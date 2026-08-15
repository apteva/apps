package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type aptevaDelegatedToken struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	ExpiresAt   string `json:"expires_at"`
	KeyPrefix   string `json:"key_prefix"`
	ProjectID   string `json:"project_id"`
}

var delegatedChatActions = []string{
	"chat.create",
	"chat.list",
	"chat.read",
	"chat.update",
	"chat.seen",
	"chat.presence",
	"message.read",
	"message.send",
	"stream.read",
}

func configuredDelegatedChatAgentIDs(ctx *sdk.AppCtx) ([]int64, error) {
	raw := strings.TrimSpace(cfgStr(ctx, "apteva_chat_agent_ids", ""))
	if raw == "" {
		return nil, nil
	}
	seen := make(map[int64]struct{})
	ids := make([]int64, 0)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		id, err := strconv.ParseInt(item, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid apteva_chat_agent_ids entry %q: expected a positive integer", item)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func mintAptevaDelegatedToken(ctx *sdk.AppCtx, projectID string, org *Organization, user *User, oauthClient *Client) (*aptevaDelegatedToken, error) {
	appToken := strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN"))
	if appToken == "" {
		return nil, nil
	}
	// Delegated access is opt-in and fail-closed. Identity sessions remain
	// available when an install has no Chat policy, while Auth never falls
	// back to the historical app=* / actions=* grant.
	if oauthClient == nil || len(oauthClient.AllowedOrigins) == 0 {
		return nil, nil
	}
	agentIDs, err := configuredDelegatedChatAgentIDs(ctx)
	if err != nil {
		return nil, err
	}
	if len(agentIDs) == 0 {
		return nil, nil
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("APTEVA_GATEWAY_URL")), "/")
	if base == "" {
		base = "http://127.0.0.1:5280"
	}
	body := map[string]any{
		"project_id":        projectID,
		"subject_type":      "user",
		"subject_id":        uintToStr(user.ID),
		"subject_email":     user.Email,
		"organization_id":   uintToStr(org.ID),
		"organization_slug": org.Slug,
		"expires_in":        cfgInt(ctx, "apteva_token_ttl_seconds", 3600),
		"allowed_origins":   append([]string(nil), oauthClient.AllowedOrigins...),
		"scopes": []map[string]any{
			{
				"type":      "app_user",
				"app":       "channel-chat",
				"actions":   append([]string(nil), delegatedChatActions...),
				"agent_ids": agentIDs,
			},
		},
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
	return &out, nil
}
