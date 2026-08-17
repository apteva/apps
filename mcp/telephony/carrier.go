package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type carrierPlaceRequest struct {
	CallID            string
	CallbackSecret    string
	ProjectID         string
	To                string
	From              string
	TimeoutSec        int
	MaxDurationSec    int
	AudioBridgeURL    string
	RecordingMode     string
	RecordingChannels string
}

type carrierPlaceResult struct {
	CarrierSID       string
	CarrierRequestID string
}

type carrierAdapter interface {
	Slug() string
	Place(ctx *sdk.AppCtx, req carrierPlaceRequest) (*carrierPlaceResult, error)
	Hangup(ctx *sdk.AppCtx, row *callRow) error
}

func (a *App) carrierFor(bound *sdk.BoundIntegration, credentialSlug string, fields map[string]string) (carrierAdapter, error) {
	slug := credentialSlug
	if slug == "" && bound != nil {
		slug = bound.AppSlug
	}
	slug = strings.ToLower(strings.TrimSpace(slug))
	connID := int64(0)
	if bound != nil {
		connID = bound.ConnectionID
	}
	return a.carrierForSlug(slug, connID, fields)
}

func (a *App) carrierForRow(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, row *callRow) (carrierAdapter, error) {
	connID := row.CarrierConnectionID
	if connID == 0 && bound != nil {
		connID = bound.ConnectionID
	}
	slug := row.CarrierSlug
	if slug == "" && bound != nil {
		slug = bound.AppSlug
	}
	return a.carrierForSlug(slug, connID, nil)
}

func (a *App) carrierForSlug(slug string, connID int64, fields map[string]string) (carrierAdapter, error) {
	switch slug {
	case "twilio":
		return &twilioCarrier{app: a, connID: connID}, nil
	case "signalwire":
		return &signalWireCarrier{app: a, connID: connID}, nil
	case "telnyx":
		return &telnyxCarrier{app: a, connID: connID, fields: fields}, nil
	case "plivo":
		return &plivoCarrier{app: a, connID: connID}, nil
	case "vonage":
		return &vonageCarrier{app: a, connID: connID}, nil
	default:
		return nil, fmt.Errorf("unsupported carrier %q", slug)
	}
}

func executeCarrierTool(ctx *sdk.AppCtx, connID int64, tool string, input map[string]any) (json.RawMessage, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("%s returned no response", tool)
	}
	if !res.Success {
		return nil, fmt.Errorf("%s failed: status=%d body=%s", tool, res.Status, string(res.Data))
	}
	return res.Data, nil
}

type twilioCarrier struct {
	app    *App
	connID int64
}

func (c *twilioCarrier) Slug() string { return "twilio" }

func (c *twilioCarrier) Place(ctx *sdk.AppCtx, req carrierPlaceRequest) (*carrierPlaceResult, error) {
	twiml := c.app.twilioStreamTwiML(&callRow{
		ID: req.CallID, CallbackSecret: req.CallbackSecret, ProjectID: req.ProjectID,
		RecordingMode: req.RecordingMode, RecordingChannels: req.RecordingChannels,
	})
	data, err := executeCarrierTool(ctx, c.connID, "make_call", map[string]any{
		"To":                   req.To,
		"From":                 req.From,
		"Twiml":                twiml,
		"StatusCallback":       c.app.statusCallbackURL(req.CallID, req.CallbackSecret, req.ProjectID),
		"StatusCallbackMethod": "POST",
		"StatusCallbackEvent":  []string{"initiated", "ringing", "answered", "completed"},
		"Timeout":              req.TimeoutSec,
	})
	if err != nil {
		return nil, fmt.Errorf("twilio make_call failed: %w", err)
	}
	var out struct {
		SID string `json:"sid"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode twilio make_call response: %w", err)
	}
	if out.SID == "" {
		return nil, errors.New("twilio make_call returned no call SID")
	}
	return &carrierPlaceResult{CarrierSID: out.SID}, nil
}

func (c *twilioCarrier) Hangup(ctx *sdk.AppCtx, row *callRow) error {
	_, err := executeCarrierTool(ctx, c.connID, "update_call", map[string]any{
		"CallSid": row.CarrierSID,
		"Status":  "completed",
	})
	return err
}

type signalWireCarrier struct {
	app    *App
	connID int64
}

func (c *signalWireCarrier) Slug() string { return "signalwire" }

func (c *signalWireCarrier) Place(ctx *sdk.AppCtx, req carrierPlaceRequest) (*carrierPlaceResult, error) {
	cxml := fmt.Sprintf(`<Response><Connect><Stream url="%s" codec="L16@24000h" realtime="true"/></Connect></Response>`, xmlEscape(c.app.publicWSStreamURL("signalwire", req.CallID, req.CallbackSecret)))
	data, err := executeCarrierTool(ctx, c.connID, "make_call", map[string]any{
		"To":                   req.To,
		"From":                 req.From,
		"Twiml":                cxml,
		"StatusCallback":       c.app.statusCallbackURL(req.CallID, req.CallbackSecret, req.ProjectID),
		"StatusCallbackMethod": "POST",
		"StatusCallbackEvent":  []string{"initiated", "ringing", "answered", "completed"},
		"Timeout":              req.TimeoutSec,
	})
	if err != nil {
		return nil, fmt.Errorf("signalwire make_call failed: %w", err)
	}
	var out struct {
		SID string `json:"sid"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode signalwire make_call response: %w", err)
	}
	if out.SID == "" {
		return nil, errors.New("signalwire make_call returned no call SID")
	}
	return &carrierPlaceResult{CarrierSID: out.SID}, nil
}

func (c *signalWireCarrier) Hangup(ctx *sdk.AppCtx, row *callRow) error {
	_, err := executeCarrierTool(ctx, c.connID, "update_call", map[string]any{
		"Sid":    row.CarrierSID,
		"Status": "completed",
	})
	return err
}

type telnyxCarrier struct {
	app    *App
	connID int64
	fields map[string]string
}

func (c *telnyxCarrier) Slug() string { return "telnyx" }

func (c *telnyxCarrier) Place(ctx *sdk.AppCtx, req carrierPlaceRequest) (*carrierPlaceResult, error) {
	connectionID := ""
	if c.fields != nil {
		connectionID = c.fields["connection_id"]
	}
	if connectionID == "" {
		return nil, fmt.Errorf("telnyx connection has no connection_id configured")
	}
	input := map[string]any{
		"connection_id":              connectionID,
		"to":                         req.To,
		"from":                       req.From,
		"stream_url":                 c.app.publicWSStreamURL("telnyx", req.CallID, req.CallbackSecret),
		"stream_track":               "inbound_track",
		"stream_codec":               "PCMU",
		"stream_bidirectional_mode":  "rtp",
		"stream_bidirectional_codec": "PCMU",
		"timeout_secs":               req.TimeoutSec,
		"time_limit_secs":            req.MaxDurationSec,
		"command_id":                 telnyxCommandID(req.CallID, "place"),
		"webhook_url":                c.app.statusCallbackURL(req.CallID, req.CallbackSecret, req.ProjectID),
		"webhook_url_method":         "POST",
	}
	if req.RecordingMode == recordingModeAlways {
		input["record"] = "record-from-answer"
		input["record_channels"] = telnyxRecordingChannels(req.RecordingChannels)
		input["record_format"] = "wav"
		input["record_track"] = "both"
	}
	data, err := executeCarrierTool(ctx, c.connID, "make_call", input)
	if err != nil {
		return nil, fmt.Errorf("telnyx make_call failed: %w", err)
	}
	var out struct {
		Data struct {
			CallControlID string `json:"call_control_id"`
			CallLegID     string `json:"call_leg_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode telnyx make_call response: %w", err)
	}
	sid := out.Data.CallControlID
	if sid == "" {
		sid = out.Data.CallLegID
	}
	if sid == "" {
		return nil, errors.New("telnyx make_call returned no call control id")
	}
	return &carrierPlaceResult{CarrierSID: sid}, nil
}

func (c *telnyxCarrier) Hangup(ctx *sdk.AppCtx, row *callRow) error {
	_, err := executeCarrierTool(ctx, c.connID, "hangup_call", map[string]any{
		"call_control_id": row.CarrierSID,
		"command_id":      telnyxCommandID(row.ID, "hangup"),
	})
	return err
}

type plivoCarrier struct {
	app    *App
	connID int64
}

func (c *plivoCarrier) Slug() string { return "plivo" }

func (c *plivoCarrier) Place(ctx *sdk.AppCtx, req carrierPlaceRequest) (*carrierPlaceResult, error) {
	data, err := executeCarrierTool(ctx, c.connID, "make_call", map[string]any{
		"from":          req.From,
		"to":            req.To,
		"answer_url":    plivoReliableCallbackURL(c.app.plivoXMLURL(req.CallID, req.CallbackSecret, req.ProjectID)),
		"answer_method": "POST",
		"ring_url":      plivoReliableCallbackURL(c.app.statusCallbackURL(req.CallID, req.CallbackSecret, req.ProjectID)),
		"ring_method":   "POST",
		"hangup_url":    plivoReliableCallbackURL(c.app.statusCallbackURL(req.CallID, req.CallbackSecret, req.ProjectID)),
		"hangup_method": "POST",
		"ring_timeout":  req.TimeoutSec,
		"time_limit":    req.MaxDurationSec,
	})
	if err != nil {
		return nil, fmt.Errorf("plivo make_call failed: %w", err)
	}
	var out struct {
		RequestUUID string `json:"request_uuid"`
		CallUUID    string `json:"call_uuid"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode plivo make_call response: %w", err)
	}
	if out.CallUUID == "" && out.RequestUUID == "" {
		return nil, errors.New("plivo make_call returned no request or call UUID")
	}
	return &carrierPlaceResult{CarrierSID: out.CallUUID, CarrierRequestID: out.RequestUUID}, nil
}

func (c *plivoCarrier) Hangup(ctx *sdk.AppCtx, row *callRow) error {
	_, err := executeCarrierTool(ctx, c.connID, "hangup_call", map[string]any{
		"call_uuid": row.CarrierSID,
	})
	return err
}

func telnyxCommandID(callID, action string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(callID) + "\x00" + strings.TrimSpace(action)))
	return hex.EncodeToString(sum[:16])
}

type vonageCarrier struct {
	app    *App
	connID int64
}

func (c *vonageCarrier) Slug() string { return "vonage" }

func (c *vonageCarrier) Place(ctx *sdk.AppCtx, req carrierPlaceRequest) (*carrierPlaceResult, error) {
	ncco := []map[string]any{{
		"action": "connect",
		"endpoint": []map[string]any{{
			"type":         "websocket",
			"uri":          c.app.publicWSStreamURL("vonage", req.CallID, req.CallbackSecret),
			"content-type": "audio/l16;rate=16000",
			"headers": map[string]string{
				"call_id": req.CallID,
			},
		}},
	}}
	data, err := executeCarrierTool(ctx, c.connID, "create_voice_call", map[string]any{
		"to": []map[string]string{{
			"type":   "phone",
			"number": vonageNumber(req.To),
		}},
		"from": map[string]string{
			"type":   "phone",
			"number": vonageNumber(req.From),
		},
		"ncco":      ncco,
		"event_url": []string{c.app.statusCallbackURL(req.CallID, req.CallbackSecret, req.ProjectID)},
	})
	if err != nil {
		return nil, fmt.Errorf("vonage create_voice_call failed: %w", err)
	}
	var out struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode vonage create_voice_call response: %w", err)
	}
	if out.UUID == "" {
		return nil, errors.New("vonage create_voice_call returned no UUID")
	}
	return &carrierPlaceResult{CarrierSID: out.UUID}, nil
}

func (c *vonageCarrier) Hangup(ctx *sdk.AppCtx, row *callRow) error {
	_, err := executeCarrierTool(ctx, c.connID, "hangup_voice_call", map[string]any{
		"uuid":   row.CarrierSID,
		"action": "hangup",
	})
	return err
}

func (a *App) handlePlivoXML(w http.ResponseWriter, r *http.Request) {
	callID := strings.TrimPrefix(r.URL.Path, "/xml/plivo/")
	callID = strings.TrimSuffix(callID, "/")
	if callID == "" {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	row, err := a.db().findCall(callID)
	if err != nil || row == nil || row.CarrierSlug != "plivo" {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if err := a.authorizeCallRequest(r, row); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if callUUID := firstNonEmpty(r.FormValue("CallUUID"), r.URL.Query().Get("CallUUID")); callUUID != "" {
		if err := a.db().updateCarrierIdentity(callID, callUUID, row.CarrierRequestID); err != nil {
			http.Error(w, "persist Plivo call id", http.StatusInternalServerError)
			return
		}
	}
	streamURL := xmlEscape(a.publicWSStreamURL("plivo", callID, row.CallbackSecret))
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(a.plivoStreamXML(row, streamURL)))
}

func telnyxRecordingChannels(channels string) string {
	if channels == "dual" {
		return "dual"
	}
	return "single"
}

func (a *App) plivoStreamXML(row *callRow, escapedStreamURL string) string {
	record := ""
	if row != nil && row.RecordingMode == recordingModeAlways {
		channelType := "mono"
		if row.RecordingChannels == "dual" {
			channelType = "stereo"
		}
		record = fmt.Sprintf(`<Record recordSession="true" redirect="false" fileFormat="wav" recordChannelType="%s" callbackUrl="%s" callbackMethod="POST"/>`,
			channelType, xmlEscape(a.plivoRecordingStatusURL(row.ID, row.CallbackSecret, row.ProjectID)))
	}
	return fmt.Sprintf(`<Response>%s<Stream bidirectional="true" keepCallAlive="true" contentType="audio/x-mulaw;rate=8000" audioTrack="inbound" statusCallbackUrl="%s" statusCallbackMethod="POST">%s</Stream></Response>`,
		record, xmlEscape(a.statusCallbackURL(row.ID, row.CallbackSecret, row.ProjectID)), escapedStreamURL)
}

func xmlEscape(s string) string {
	return strings.NewReplacer(
		`&`, `&amp;`,
		`<`, `&lt;`,
		`>`, `&gt;`,
		`"`, `&quot;`,
		`'`, `&apos;`,
	).Replace(s)
}

func vonageNumber(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "+")
}
