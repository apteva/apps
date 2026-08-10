package main

import (
	"errors"
	"fmt"
	"html"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func sendInvitation(ctx *sdk.AppCtx, env *Envelope, recipient *Recipient, token string, reminder bool) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("messaging is unavailable")
	}
	if recipient == nil || strings.TrimSpace(recipient.Email) == "" {
		return errors.New("recipient email required for messaging delivery")
	}
	url, err := signingURL(ctx, token)
	if err != nil {
		return err
	}
	sender := strings.TrimSpace(env.SenderName)
	if sender == "" {
		sender = "A document sender"
	}
	subject := "Signature requested: " + env.Title
	intro := sender + " has sent you a document to review and sign."
	if reminder {
		subject = "Reminder: " + subject
		intro = sender + " is reminding you to review and sign a document."
	}
	body := intro + "\n\n" + env.Title + "\n\nOpen the secure signing page:\n" + url + "\n\nThis link expires at " + env.ExpiresAt + "."
	bodyHTML := "<p>" + html.EscapeString(intro) + "</p><p><strong>" + html.EscapeString(env.Title) + "</strong></p><p><a href=\"" + html.EscapeString(url) + "\">Review and sign</a></p><p>This link expires at " + html.EscapeString(env.ExpiresAt) + ".</p>"
	var out map[string]any
	err = ctx.WithProject(env.ProjectID).PlatformAPI().CallAppResult("messaging", "send_message", map[string]any{
		"to":              "mailto:" + recipient.Email,
		"subject":         subject,
		"body":            body,
		"body_html":       bodyHTML,
		"idempotency_key": fmt.Sprintf("signatures:%d:%d:%t:%s", env.ID, recipient.ID, reminder, tokenHash(token)[:16]),
	}, &out)
	event := "notification.sent"
	detail := map[string]any{"kind": "invitation", "recipient_email": recipient.Email}
	if reminder {
		detail["kind"] = "reminder"
	}
	if err != nil {
		event = "notification.failed"
		detail["error"] = err.Error()
		_ = addAudit(ctx.AppDB(), env.ID, env.ProjectID, recipient.ID, event, detail)
		return fmt.Errorf("messaging.send_message: %w", err)
	}
	_ = addAudit(ctx.AppDB(), env.ID, env.ProjectID, recipient.ID, event, detail)
	return nil
}

func sendCompletionNotices(ctx *sdk.AppCtx, env *Envelope) {
	if ctx == nil || env == nil || env.DeliveryMode != "messaging" {
		return
	}
	recipients, err := listRecipients(ctx.AppDB(), env.ProjectID, env.ID)
	if err != nil {
		return
	}
	for _, recipient := range recipients {
		if recipient.Email == "" {
			continue
		}
		var out map[string]any
		err := ctx.WithProject(env.ProjectID).PlatformAPI().CallAppResult("messaging", "send_message", map[string]any{
			"to":              "mailto:" + recipient.Email,
			"subject":         "Completed: " + env.Title,
			"body":            "All recipients have completed \"" + env.Title + "\". The completed document is available from the sender.",
			"idempotency_key": fmt.Sprintf("signatures:%d:completed:%d", env.ID, recipient.ID),
		}, &out)
		if err != nil {
			_ = addAudit(ctx.AppDB(), env.ID, env.ProjectID, recipient.ID, "notification.failed", map[string]any{"kind": "completion", "error": err.Error()})
		} else {
			_ = addAudit(ctx.AppDB(), env.ID, env.ProjectID, recipient.ID, "notification.sent", map[string]any{"kind": "completion"})
		}
	}
}
