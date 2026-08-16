package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

var outreachChannels = map[string]bool{"email": true, "sms": true, "whatsapp": true}

func candidateOutreach(ctx *sdk.AppCtx, id int64) (map[string]any, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("prospecting context unavailable")
	}
	if err := requireOptionalApp(ctx, "crm"); err != nil {
		return nil, err
	}
	pid := ctx.CurrentProject()
	candidate, err := getCandidate(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, sql.ErrNoRows
	}
	handoff, err := getHandoff(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if handoff == nil {
		return map[string]any{"linked": false, "candidate_id": id}, nil
	}

	var crmContext map[string]any
	if err := ctx.PlatformAPI().CallAppResult("crm", "contacts_get_context", map[string]any{
		"id": handoff.CRMContactID, "activity_limit": 30, "conversation_limit": 20, "opportunity_limit": 10,
	}, &crmContext); err != nil {
		return nil, fmt.Errorf("CRM outreach context: %w", err)
	}

	messaging := map[string]any{"available": false}
	var senders map[string]any
	if err := ctx.PlatformAPI().CallAppResult("crm", "messaging_senders_list", map[string]any{
		"verified_only": true,
	}, &senders); err != nil {
		messaging["error"] = err.Error()
	} else {
		messaging["available"] = true
		messaging["senders"] = senders
		if candidate.Phone != "" {
			var session map[string]any
			if err := ctx.PlatformAPI().CallAppResult("crm", "messaging_whatsapp_session_check", map[string]any{
				"id": handoff.CRMContactID,
			}, &session); err != nil {
				messaging["whatsapp_error"] = err.Error()
			} else {
				messaging["whatsapp_session"] = session
			}
			var templates map[string]any
			if err := ctx.PlatformAPI().CallAppResult("crm", "messaging_templates_list", map[string]any{
				"channel": "whatsapp", "approved_only": true, "limit": 100,
			}, &templates); err == nil {
				messaging["whatsapp_templates"] = templates
			}
		}
	}

	return map[string]any{
		"linked":         true,
		"candidate_id":   id,
		"crm_contact_id": handoff.CRMContactID,
		"context":        crmContext,
		"messaging":      messaging,
	}, nil
}

func sendCandidateOutreach(ctx *sdk.AppCtx, id int64, args map[string]any) (map[string]any, error) {
	if !boolArg(args, "confirm", false) {
		return nil, errors.New("confirm=true is required before sending a real external message")
	}
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("prospecting context unavailable")
	}
	if err := requireOptionalApp(ctx, "crm"); err != nil {
		return nil, err
	}
	pid := ctx.CurrentProject()
	candidate, err := getCandidate(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, sql.ErrNoRows
	}
	if candidate.Status == "rejected" {
		return nil, errors.New("rejected candidates cannot be contacted")
	}
	handoff, err := getHandoff(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if handoff == nil || handoff.CRMContactID <= 0 {
		return nil, errors.New("start outreach first to create or link the CRM contact")
	}

	channel := strings.ToLower(stringArg(args, "channel"))
	if !outreachChannels[channel] {
		return nil, errors.New("channel must be email, sms, or whatsapp")
	}
	body := stringArg(args, "body")
	templateID := int64Arg(args, "template_id")
	if body == "" && templateID <= 0 {
		return nil, errors.New("body or template_id required")
	}
	conversationID := int64Arg(args, "conversation_id")
	if channel == "email" && conversationID == 0 && templateID == 0 && stringArg(args, "subject") == "" {
		return nil, errors.New("subject required for a new email conversation")
	}

	input := map[string]any{"id": handoff.CRMContactID, "body": body}
	if conversationID > 0 {
		input["conversation_id"] = conversationID
	} else {
		input["channel"] = channel
	}
	for _, key := range []string{"subject", "from", "idempotency_key"} {
		if value := stringArg(args, key); value != "" {
			input[key] = value
		}
	}
	if templateID > 0 {
		input["template_id"] = templateID
	}
	if vars, ok := args["template_vars"].(map[string]any); ok && len(vars) > 0 {
		input["template_vars"] = vars
	}

	tool := "contacts_send_message"
	if conversationID > 0 {
		tool = "contacts_reply"
	}
	var sent map[string]any
	if err := ctx.PlatformAPI().CallAppResult("crm", tool, input, &sent); err != nil {
		return nil, fmt.Errorf("CRM message send: %w", err)
	}
	ctx.EmitWithProject("prospecting.candidate.contacted", pid, map[string]any{
		"candidate_id": id, "crm_contact_id": handoff.CRMContactID, "channel": channel,
	})
	return map[string]any{
		"sent":           sent,
		"candidate_id":   id,
		"crm_contact_id": handoff.CRMContactID,
		"channel":        channel,
		"reply":          conversationID > 0,
	}, nil
}
