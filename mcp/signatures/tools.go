package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) MCPTools() []sdk.Tool {
	recipientItem := schemaObject(map[string]any{
		"name":          sString(),
		"email":         sString(),
		"role":          map[string]any{"type": "string", "enum": []string{"signer", "approver"}},
		"signing_order": sInteger(),
	}, "name")
	fieldItem := schemaObject(map[string]any{
		"recipient_id": sInteger(),
		"field_type":   map[string]any{"type": "string", "enum": []string{"signature", "initials", "date_signed", "text", "checkbox"}},
		"page":         sInteger(),
		"x":            sNumber(),
		"y":            sNumber(),
		"width":        sNumber(),
		"height":       sNumber(),
		"label":        sString(),
		"required":     sBool(),
	}, "recipient_id", "field_type", "page", "x", "y", "width", "height")
	return []sdk.Tool{
		{Name: "signatures_envelopes_create", Description: "Create a draft envelope from a PDF in Storage.", InputSchema: schemaObject(map[string]any{
			"source_file_id": sInteger(), "title": sString(), "sender_name": sString(), "message": sString(), "expires_at": sString(),
		}, "source_file_id", "title"), Handler: a.toolEnvelopeCreate},
		{Name: "signatures_envelopes_update", Description: "Update a draft envelope.", InputSchema: schemaObject(map[string]any{
			"envelope_id": sInteger(), "title": sString(), "sender_name": sString(), "message": sString(), "expires_at": sString(),
		}, "envelope_id"), Handler: a.toolEnvelopeUpdate},
		{Name: "signatures_envelopes_get", Description: "Get an envelope with recipients and fields.", InputSchema: schemaObject(map[string]any{"envelope_id": sInteger()}, "envelope_id"), Handler: a.toolEnvelopeGet},
		{Name: "signatures_envelopes_list", Description: "List envelopes.", InputSchema: schemaObject(map[string]any{"status": sString(), "limit": sInteger()}), Handler: a.toolEnvelopeList},
		{Name: "signatures_recipients_set", Description: "Replace a draft envelope's ordered recipients.", InputSchema: schemaObject(map[string]any{
			"envelope_id": sInteger(), "recipients": map[string]any{"type": "array", "items": recipientItem},
		}, "envelope_id", "recipients"), Handler: a.toolRecipientsSet},
		{Name: "signatures_fields_set", Description: "Replace a draft envelope's fields.", InputSchema: schemaObject(map[string]any{
			"envelope_id": sInteger(), "fields": map[string]any{"type": "array", "items": fieldItem},
		}, "envelope_id", "fields"), Handler: a.toolFieldsSet},
		{Name: "signatures_envelopes_validate", Description: "Validate that an envelope is ready to send.", InputSchema: schemaObject(map[string]any{
			"envelope_id": sInteger(), "delivery_mode": map[string]any{"type": "string", "enum": []string{"manual", "messaging"}},
		}, "envelope_id"), Handler: a.toolEnvelopeValidate},
		{Name: "signatures_envelopes_send", Description: "Freeze and activate a draft envelope.", InputSchema: schemaObject(map[string]any{
			"envelope_id": sInteger(), "delivery_mode": map[string]any{"type": "string", "enum": []string{"manual", "messaging"}}, "idempotency_key": sString(),
		}, "envelope_id", "delivery_mode"), Handler: a.toolEnvelopeSend},
		{Name: "signatures_recipient_link_create", Description: "Generate or rotate the current recipient's signing link.", InputSchema: schemaObject(map[string]any{
			"envelope_id": sInteger(), "recipient_id": sInteger(),
		}, "envelope_id", "recipient_id"), Handler: a.toolRecipientLinkCreate},
		{Name: "signatures_envelopes_remind", Description: "Send a reminder through optional Messaging.", InputSchema: schemaObject(map[string]any{
			"envelope_id": sInteger(), "recipient_id": sInteger(),
		}, "envelope_id", "recipient_id"), Handler: a.toolEnvelopeRemind},
		{Name: "signatures_envelopes_void", Description: "Void a draft or active envelope.", InputSchema: schemaObject(map[string]any{
			"envelope_id": sInteger(), "reason": sString(),
		}, "envelope_id"), Handler: a.toolEnvelopeVoid},
		{Name: "signatures_envelopes_finalize", Description: "Retry final document generation after every recipient has finished.", InputSchema: schemaObject(map[string]any{
			"envelope_id": sInteger(),
		}, "envelope_id"), Handler: a.toolEnvelopeFinalize},
		{Name: "signatures_audit_get", Description: "Return an envelope's append-only audit timeline.", InputSchema: schemaObject(map[string]any{"envelope_id": sInteger()}, "envelope_id"), Handler: a.toolAuditGet},
	}
}

func (a *App) toolEnvelopeCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	fileID := int64Arg(args, "source_file_id")
	file, body, _, err := sourcePDF(ctx, fileID)
	if err != nil {
		return nil, err
	}
	expiresAt := defaultExpiry(ctx)
	if value := stringArg(args, "expires_at"); value != "" {
		expiresAt, err = parseRFC3339(value)
		if err != nil {
			return nil, err
		}
	}
	if expiresAt.Before(time.Now().UTC().Add(15 * time.Minute)) {
		return nil, errors.New("expires_at must be at least 15 minutes in the future")
	}
	env, err := createEnvelope(ctx.AppDB(), ctx.CurrentProject(), file, bytesHash(body), stringArg(args, "title"), stringArg(args, "sender_name"), stringArg(args, "message"), expiresAt)
	if err != nil {
		return nil, err
	}
	return map[string]any{"envelope": env}, nil
}

func (a *App) toolEnvelopeUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	env, err := updateEnvelope(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "envelope_id"), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"envelope": env}, nil
}

func (a *App) toolEnvelopeGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	detail, err := getEnvelopeDetail(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "envelope_id"), false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"envelope": detail}, nil
}

func (a *App) toolEnvelopeList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	envelopes, err := listEnvelopes(ctx.AppDB(), ctx.CurrentProject(), strings.ToLower(stringArg(args, "status")), intArg(args, "limit", 50))
	if err != nil {
		return nil, err
	}
	return map[string]any{"envelopes": envelopes}, nil
}

func (a *App) toolRecipientsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	specs, err := mapSliceArg(args, "recipients")
	if err != nil {
		return nil, err
	}
	recipients, err := setRecipients(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "envelope_id"), specs)
	if err != nil {
		return nil, err
	}
	return map[string]any{"recipients": recipients}, nil
}

func (a *App) toolFieldsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	specs, err := mapSliceArg(args, "fields")
	if err != nil {
		return nil, err
	}
	fields, err := setFields(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "envelope_id"), specs)
	if err != nil {
		return nil, err
	}
	return map[string]any{"fields": fields}, nil
}

func (a *App) toolEnvelopeValidate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	envelopeID := int64Arg(args, "envelope_id")
	env, err := getEnvelopeRequired(ctx.AppDB(), ctx.CurrentProject(), envelopeID)
	if err != nil {
		return nil, err
	}
	_, body, pageCount, err := sourcePDF(ctx, env.SourceFileID)
	if err != nil {
		return map[string]any{"valid": false, "errors": []string{err.Error()}}, nil
	}
	if bytesHash(body) != env.SourceSHA256 {
		return map[string]any{"valid": false, "errors": []string{"source PDF changed after envelope creation"}}, nil
	}
	deliveryMode := strings.ToLower(stringArg(args, "delivery_mode"))
	if deliveryMode == "" {
		deliveryMode = "manual"
	}
	errs := validateEnvelope(ctx.AppDB(), ctx.CurrentProject(), envelopeID, pageCount, deliveryMode)
	return map[string]any{"valid": len(errs) == 0, "errors": errs, "page_count": pageCount}, nil
}

func (a *App) toolEnvelopeSend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	envelopeID := int64Arg(args, "envelope_id")
	deliveryMode := strings.ToLower(stringArg(args, "delivery_mode"))
	idempotencyKey := stringArg(args, "idempotency_key")
	existing, err := getEnvelopeRequired(ctx.AppDB(), ctx.CurrentProject(), envelopeID)
	if err != nil {
		return nil, err
	}
	if existing.Status != "draft" {
		matched, err := sendIdempotencyMatches(ctx.AppDB(), ctx.CurrentProject(), envelopeID, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if !matched {
			return nil, errors.New("only draft envelopes can be sent")
		}
		detail, err := getEnvelopeDetail(ctx.AppDB(), ctx.CurrentProject(), envelopeID, false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"envelope": detail, "idempotent_replay": true}, nil
	}
	validation, err := a.toolEnvelopeValidate(ctx, map[string]any{"envelope_id": envelopeID, "delivery_mode": deliveryMode})
	if err != nil {
		return nil, err
	}
	result := validation.(map[string]any)
	if valid, _ := result["valid"].(bool); !valid {
		return nil, fmt.Errorf("envelope is not ready: %v", result["errors"])
	}
	env, recipient, err := activateEnvelope(ctx.AppDB(), ctx.CurrentProject(), envelopeID, deliveryMode, idempotencyKey)
	if err != nil {
		return nil, err
	}
	emit(ctx, "envelope.sent", env, 0)
	if deliveryMode == "messaging" && recipient != nil {
		_, _, token, err := createRecipientToken(ctx.AppDB(), env.ProjectID, env.ID, recipient.ID)
		if err != nil {
			return nil, fmt.Errorf("envelope activated but signing link creation failed: %w", err)
		}
		if err := sendInvitation(ctx, env, recipient, token, false); err != nil {
			return nil, fmt.Errorf("envelope activated but invitation delivery failed: %w", err)
		}
	}
	detail, err := getEnvelopeDetail(ctx.AppDB(), ctx.CurrentProject(), envelopeID, false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"envelope": detail}, nil
}

func (a *App) toolRecipientLinkCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	env, recipient, token, err := createRecipientToken(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "envelope_id"), int64Arg(args, "recipient_id"))
	if err != nil {
		return nil, err
	}
	url, err := signingURL(ctx.WithProject(env.ProjectID), token)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"envelope_id": env.ID, "recipient_id": recipient.ID, "url": url,
		"expires_at": env.ExpiresAt, "warning": "Treat this URL as a secret; generating another link revokes it.",
	}, nil
}

func (a *App) toolEnvelopeRemind(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	env, recipient, token, err := createRecipientToken(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "envelope_id"), int64Arg(args, "recipient_id"))
	if err != nil {
		return nil, err
	}
	if recipient.Email == "" {
		return nil, errors.New("recipient has no email; create a manual link instead")
	}
	if err := sendInvitation(ctx, env, recipient, token, true); err != nil {
		return nil, err
	}
	return map[string]any{"sent": true, "envelope_id": env.ID, "recipient_id": recipient.ID}, nil
}

func (a *App) toolEnvelopeVoid(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	env, err := voidEnvelope(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "envelope_id"), stringArg(args, "reason"))
	if err != nil {
		return nil, err
	}
	emit(ctx, "envelope.voided", env, 0)
	return map[string]any{"envelope": env}, nil
}

func (a *App) toolEnvelopeFinalize(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	env, err := getEnvelopeRequired(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "envelope_id"))
	if err != nil {
		return nil, err
	}
	if env.Status == "completed" {
		return map[string]any{"envelope": env, "idempotent_replay": true}, nil
	}
	if env.Status != "sent" {
		return nil, errors.New("only an active envelope can be finalized")
	}
	completed, err := finalizeEnvelope(ctx, env)
	if err != nil {
		return nil, err
	}
	emit(ctx, "envelope.completed", completed, 0)
	sendCompletionNotices(ctx, completed)
	return map[string]any{"envelope": completed}, nil
}

func (a *App) toolAuditGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	envelopeID := int64Arg(args, "envelope_id")
	if _, err := getEnvelopeRequired(ctx.AppDB(), ctx.CurrentProject(), envelopeID); err != nil {
		return nil, err
	}
	events, err := listAudit(ctx.AppDB(), ctx.CurrentProject(), envelopeID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"events": events}, nil
}
