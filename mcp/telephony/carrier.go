package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type carrierPlaceRequest struct {
	CallID         string
	To             string
	From           string
	TimeoutSec     int
	AudioBridgeURL string
}

type carrierPlaceResult struct {
	CarrierSID string
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
	twiml := fmt.Sprintf(`<Response><Connect><Stream url="%s"/></Connect></Response>`, xmlEscape(c.app.publicWSStreamURL("twilio", req.CallID)))
	data, err := executeCarrierTool(ctx, c.connID, "make_call", map[string]any{
		"To":             req.To,
		"From":           req.From,
		"Twiml":          twiml,
		"StatusCallback": c.app.publicAppURL() + "/webhook/status/" + req.CallID,
		"Timeout":        req.TimeoutSec,
	})
	if err != nil {
		return nil, fmt.Errorf("twilio make_call failed: %w", err)
	}
	var out struct {
		SID string `json:"sid"`
	}
	_ = json.Unmarshal(data, &out)
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
	cxml := fmt.Sprintf(`<Response><Connect><Stream url="%s" codec="L16@24000h" realtime="true"/></Connect></Response>`, xmlEscape(c.app.publicWSStreamURL("signalwire", req.CallID)))
	data, err := executeCarrierTool(ctx, c.connID, "make_call", map[string]any{
		"To":                   req.To,
		"From":                 req.From,
		"Twiml":                cxml,
		"StatusCallback":       c.app.publicAppURL() + "/webhook/status/" + req.CallID,
		"StatusCallbackMethod": "POST",
		"Timeout":              req.TimeoutSec,
	})
	if err != nil {
		return nil, fmt.Errorf("signalwire make_call failed: %w", err)
	}
	var out struct {
		SID string `json:"sid"`
	}
	_ = json.Unmarshal(data, &out)
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
	data, err := executeCarrierTool(ctx, c.connID, "make_call", map[string]any{
		"connection_id":              connectionID,
		"to":                         req.To,
		"from":                       req.From,
		"stream_url":                 c.app.publicWSStreamURL("telnyx", req.CallID),
		"stream_track":               "inbound_track",
		"stream_codec":               "PCMU",
		"stream_bidirectional_mode":  "rtp",
		"stream_bidirectional_codec": "PCMU",
		"timeout_secs":               req.TimeoutSec,
		"command_id":                 req.CallID,
	})
	if err != nil {
		return nil, fmt.Errorf("telnyx make_call failed: %w", err)
	}
	var out struct {
		Data struct {
			CallControlID string `json:"call_control_id"`
			CallLegID     string `json:"call_leg_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(data, &out)
	sid := out.Data.CallControlID
	if sid == "" {
		sid = out.Data.CallLegID
	}
	return &carrierPlaceResult{CarrierSID: sid}, nil
}

func (c *telnyxCarrier) Hangup(ctx *sdk.AppCtx, row *callRow) error {
	_, err := executeCarrierTool(ctx, c.connID, "update_call", map[string]any{
		"call_control_id": row.CarrierSID,
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
		"answer_url":    c.app.publicAppURL() + "/xml/plivo/" + req.CallID,
		"answer_method": "POST",
		"hangup_url":    c.app.publicAppURL() + "/webhook/status/" + req.CallID,
		"hangup_method": "POST",
		"ring_timeout":  req.TimeoutSec,
	})
	if err != nil {
		return nil, fmt.Errorf("plivo make_call failed: %w", err)
	}
	var out struct {
		RequestUUID string `json:"request_uuid"`
		CallUUID    string `json:"call_uuid"`
	}
	_ = json.Unmarshal(data, &out)
	sid := out.CallUUID
	if sid == "" {
		sid = out.RequestUUID
	}
	return &carrierPlaceResult{CarrierSID: sid}, nil
}

func (c *plivoCarrier) Hangup(ctx *sdk.AppCtx, row *callRow) error {
	_, err := executeCarrierTool(ctx, c.connID, "hangup_call", map[string]any{
		"call_uuid": row.CarrierSID,
	})
	return err
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
			"uri":          c.app.publicWSStreamURL("vonage", req.CallID),
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
		"event_url": []string{c.app.publicAppURL() + "/webhook/status/" + req.CallID},
	})
	if err != nil {
		return nil, fmt.Errorf("vonage create_voice_call failed: %w", err)
	}
	var out struct {
		UUID string `json:"uuid"`
	}
	_ = json.Unmarshal(data, &out)
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
	streamURL := xmlEscape(a.publicWSStreamURL("plivo", callID))
	w.Header().Set("Content-Type", "application/xml")
	_, _ = fmt.Fprintf(w, `<Response><Stream bidirectional="true" keepCallAlive="true" contentType="audio/x-l16;rate=24000" audioTrack="inbound" statusCallbackUrl="%s" statusCallbackMethod="POST">%s</Stream></Response>`,
		xmlEscape(a.publicAppURL()+"/webhook/status/"+callID),
		streamURL,
	)
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
