package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/url"
	"strings"
)

func (a *App) xAppCard(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (string, map[string]any) {
	app, _ := args["_mobile_app"].(*adResource)
	if app == nil {
		return "", mcpError("mobile app required for app card")
	}
	country := strings.ToUpper(firstString(args, "app_country_code"))
	if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
		return "", mcpError("app_country_code must be a two-letter country code")
	}
	keys, ok := args["media_keys"].([]any)
	if !ok || len(keys) < 1 || len(keys) > 6 {
		return "", mcpError("X app creatives require 1–6 uploaded media_keys")
	}
	for _, raw := range keys {
		key := toString(raw)
		parts := strings.Split(key, "_")
		if len(parts) != 2 || !asciiDigits(parts[0]) || !asciiDigits(parts[1]) {
			return "", mcpError("X app card requires uploaded media keys, such as 3_<image id> or 13_<video id>")
		}
	}
	media := map[string]any{"type": "MEDIA", "media_key": keys[0]}
	if len(keys) > 1 {
		media = map[string]any{"type": "SWIPEABLE_MEDIA", "media_keys": keys}
	}
	destination := map[string]any{"type": "APP", "country_code": country}
	field := "googleplay_app_id"
	if firstString(app.Metadata, "os") == "ios" {
		field = "iphone_app_id"
	}
	destination[field] = firstString(app.Metadata, "store_id")
	button := map[string]any{"type": "BUTTON", "label": map[string]any{"type": "ENUM", "value": "INSTALL"}, "destination": destination}
	parsed, out := a.execIntegrationTool(ctx, acct, "create_card", map[string]any{"account_id": acct.NativeAccountID, "name": firstString(args, "name", "headline"), "components": []any{media, button}})
	if out != nil {
		return "", out
	}
	v := asMap(parsed)
	card := firstString(v, "card_uri")
	if card == "" {
		card = firstString(asMap(v["data"]), "card_uri")
	}
	if card == "" {
		return "", mcpError("X app card creation returned no card_uri; inspect provider cards before retrying")
	}
	return card, nil
}
func googleYouTubeID(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return "", fmt.Errorf("Google video assets require an HTTPS YouTube URL")
	}
	id := ""
	switch u.Hostname() {
	case "youtu.be":
		id = strings.TrimPrefix(u.Path, "/")
	case "www.youtube.com", "youtube.com":
		id = u.Query().Get("v")
	}
	if len(id) != 11 || !safeProviderID(id) {
		return "", fmt.Errorf("source_url must identify a YouTube video")
	}
	return id, nil
}
