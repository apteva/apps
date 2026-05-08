package main

import (
	sdk "github.com/apteva/app-sdk"
)

// messagingCaller is the abstraction over CallAppResult("messaging", …).
// nil-safe: when messaging isn't bound, Send returns "not bound"
// signaled via SkipReason so the reminder scheduler can mark the row
// as 'skipped' rather than 'failed'.

type messagingCaller interface {
	SendMessage(req MsgSendReq) (MsgSendResp, error)
	Bound() bool
}

type MsgSendReq struct {
	Channel        string `json:"channel"` // email | sms | whatsapp
	To             string `json:"to"`
	From           string `json:"from,omitempty"`
	Subject        string `json:"subject,omitempty"`
	Body           string `json:"body"`
	BodyHTML       string `json:"body_html,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	ProjectID      string `json:"_project_id,omitempty"`
}

type MsgSendResp struct {
	ID                int64  `json:"id"`
	ProviderMessageID string `json:"provider_message_id,omitempty"`
}

// ─── Production wiring ────────────────────────────────────────────

type platformMessagingCaller struct {
	ctx *sdk.AppCtx
}

func newPlatformMessagingCaller(ctx *sdk.AppCtx) messagingCaller {
	return &platformMessagingCaller{ctx: ctx}
}

func (p *platformMessagingCaller) Bound() bool {
	if p == nil || p.ctx == nil {
		return false
	}
	return p.ctx.IntegrationFor("messaging") != nil
}

func (p *platformMessagingCaller) SendMessage(req MsgSendReq) (MsgSendResp, error) {
	if !p.Bound() {
		return MsgSendResp{}, errMessagingNotBound
	}
	args := map[string]any{
		"channel":  req.Channel,
		"to":       req.To,
		"from":     req.From,
		"subject":  req.Subject,
		"body":     req.Body,
		"body_html": req.BodyHTML,
	}
	if req.IdempotencyKey != "" {
		args["idempotency_key"] = req.IdempotencyKey
	}
	if req.ProjectID != "" {
		args["_project_id"] = req.ProjectID
	}
	var out MsgSendResp
	err := p.ctx.PlatformAPI().CallAppResult("messaging", "send_message", args, &out)
	return out, err
}

// errMessagingNotBound — sentinel the reminder scheduler checks to
// distinguish "operator hasn't bound the dep" (mark skipped) from
// "send failed" (mark failed + retry).
var errMessagingNotBound = simpleErr("messaging not bound")

type simpleErr string

func (e simpleErr) Error() string { return string(e) }
