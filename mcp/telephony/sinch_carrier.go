package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// A Sinch session contains a named PSTN leg and a named STREAM leg. The
// session ID returned by create_call is persisted as CarrierSID, so hangup
// targets the original PSTN leg rather than creating another call.
type sinchCarrier struct {
	app    *App
	connID int64
	fields map[string]string
}

func (c *sinchCarrier) Slug() string { return "sinch" }

func (c *sinchCarrier) ControlFeatures() carrierControlFeatures {
	return carrierControlFeatures{}
}

func (c *sinchCarrier) Place(ctx *sdk.AppCtx, req carrierPlaceRequest) (*carrierPlaceResult, error) {
	if req.MachineDetection != "" && req.MachineDetection != machineDetectionOff {
		return nil, errors.New("answering machine detection is not implemented for provider sinch")
	}
	if req.RecordingMode == recordingModeAlways {
		return nil, errors.New("Sinch recording requires an external cloud destination and is not configured")
	}
	serviceID := strings.TrimSpace(c.fields["service_id"])
	if serviceID == "" || strings.TrimSpace(c.fields["service_secret"]) == "" {
		return nil, errors.New("Sinch service_id and service_secret are required for verified outbound callbacks")
	}
	secretBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.fields["service_secret"]))
	if err != nil || len(secretBytes) != 16 {
		return nil, errors.New("Sinch service_secret must be a base64-encoded 16-byte signing key")
	}
	input := map[string]any{
		"serviceId": serviceID,
		"commands":  sinchOutboundCommands(req, c.app.statusCallbackURL(req.CallID, req.CallbackSecret, req.ProjectID)),
	}
	raw, err := executeCarrierTool(ctx, c.connID, "create_call", input)
	if err != nil {
		return nil, fmt.Errorf("sinch create_call failed: %w", err)
	}
	var response struct {
		SessionID string `json:"sessionId"`
		ServiceID string `json:"serviceId"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode sinch create_call response: %w", err)
	}
	if response.SessionID == "" || response.ServiceID != serviceID {
		return nil, errors.New("Sinch create_call returned an invalid session or service ID")
	}
	return &carrierPlaceResult{CarrierSID: response.SessionID}, nil
}

func (c *sinchCarrier) Hangup(ctx *sdk.AppCtx, row *callRow) error {
	if row == nil || row.CarrierSID == "" {
		return errors.New("Sinch call has no session ID")
	}
	_, err := executeCarrierTool(ctx, c.connID, "patch_call_by_name", map[string]any{
		"sessionId": row.CarrierSID,
		"callName":  "customer",
		"commands":  []any{map[string]any{"command": "hangup"}},
	})
	return err
}

func sinchOutboundCommands(req carrierPlaceRequest, statusURL string) []any {
	phone := func(number string) map[string]any {
		return map[string]any{"type": "PHONE", "phone": map[string]any{"number": number}}
	}
	webhook := func(name string) map[string]any {
		return map[string]any{"command": "webhook", "webhookName": name, "url": statusURL}
	}
	return []any{map[string]any{
		"command": "dial", "callName": "customer",
		"to": phone(req.To), "from": phone(req.From),
		"dialTimeoutDurationSeconds": req.TimeoutSec,
		"maxCallDurationSeconds":     req.MaxDurationSec,
		"events": map[string]any{
			"onAnswer":  []any{webhook("answered")},
			"onBusy":    []any{webhook("busy")},
			"onReject":  []any{webhook("rejected")},
			"onTimeout": []any{webhook("no_answer")},
			"onFailure": []any{webhook("failed")},
			"onHangup":  []any{webhook("completed"), map[string]any{"command": "hangup", "callName": "operator"}},
		},
	}}
}

func sinchAnswerCommands(streamURL string) []any {
	return []any{
		map[string]any{
			"command": "dial", "callName": "operator",
			"to": map[string]any{"type": "STREAM", "stream": map[string]any{
				"endpoint":      streamURL,
				"streamOptions": map[string]any{"version": 1, "codec": "PCM", "sampleRate": 16000},
			}},
			"events": map[string]any{
				"onAnswer": []any{map[string]any{"command": "bridgeCall", "bridgeName": "apteva"}},
				"onHangup": []any{map[string]any{"command": "hangup", "callName": "customer"}},
			},
		},
		map[string]any{"command": "bridgeCall", "bridgeName": "apteva"},
	}
}
