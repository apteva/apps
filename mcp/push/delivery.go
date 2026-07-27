package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type providerResult struct {
	ID               string
	PermanentFailure bool
}

type pushProvider interface {
	send(ctx *sdk.AppCtx, token string, device *device, d *delivery) (*providerResult, error)
}

type apnsProvider struct{}

func (apnsProvider) send(ctx *sdk.AppCtx, token string, device *device, d *delivery) (*providerResult, error) {
	bound := ctx.IntegrationFor("ios_provider")
	if bound == nil || bound.ConnectionID == 0 {
		return nil, errors.New("Apple Push Notifications integration is not connected")
	}
	title, body := notificationCopy(d.Type)
	aps := map[string]any{
		"alert": map[string]string{"title": title, "body": body},
		"sound": "default",
	}
	if d.Badge != nil {
		aps["badge"] = *d.Badge
	}
	tool := bound.ToolFor("push.ios.send")
	if tool == "" || tool == "push.ios.send" {
		tool = "send_notification"
	}
	data := map[string]any{
		"type":    d.Type,
		"item_id": d.ItemID,
	}
	if d.ProjectID != "" {
		data["project_id"] = d.ProjectID
	}
	result, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, map[string]any{
		"device_token": token,
		"topic":        device.BundleID,
		"environment":  device.Environment,
		"push_type":    "alert",
		"priority":     10,
		"aps":          aps,
		"data":         data,
	})
	if err != nil {
		return nil, fmt.Errorf("call APNs integration: %w", err)
	}
	if result == nil {
		return nil, errors.New("APNs integration returned no result")
	}
	providerID := ""
	for key, value := range result.Headers {
		if strings.EqualFold(key, "apns-id") {
			providerID = value
		}
	}
	if result.Success && result.Status >= 200 && result.Status < 300 {
		return &providerResult{ID: providerID}, nil
	}
	reason := apnsReason(result.Data)
	permanent := result.Status == 410 ||
		reason == "BadDeviceToken" ||
		reason == "DeviceTokenNotForTopic" ||
		reason == "Unregistered"
	if reason == "" {
		reason = fmt.Sprintf("APNs returned HTTP %d", result.Status)
	}
	return &providerResult{ID: providerID, PermanentFailure: permanent}, errors.New(reason)
}

func notificationCopy(typ string) (string, string) {
	switch typ {
	case "approval":
		return "Approval required", "Open Apteva to review."
	case "alert":
		return "New alert", "Open Apteva to review."
	case "report":
		return "New report", "Open Apteva to review."
	default:
		return "Push is connected", "Notifications are working."
	}
}

func apnsReason(data json.RawMessage) string {
	var body struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(data, &body) == nil {
		return body.Reason
	}
	return ""
}
