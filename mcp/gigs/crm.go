package main

import (
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// Thin wrappers around the CRM app's MCP tools. Every call goes
// through ctx.WithProject(pid).PlatformAPI() so the SDK auto-injects
// `_project_id` for global-scope CRM installs.

type crmContact struct {
	ID           int64  `json:"id"`
	ProjectID    string `json:"project_id"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	DisplayName  string `json:"display_name"`
	PrimaryEmail string `json:"primary_email"`
	PrimaryPhone string `json:"primary_phone"`
	Company      string `json:"company"`
	JobTitle     string `json:"job_title"`
	Status       string `json:"status"`
}

// crmUpsertByChannel finds or creates a contact by email/phone.
// `kind` is "email" or "phone". `defaults` is applied only on create.
func crmUpsertByChannel(ctx *sdk.AppCtx, pid, kind, value string, defaults map[string]any, source string) (*crmContact, bool, error) {
	if kind == "" || value == "" {
		return nil, false, errors.New("crm upsert: kind and value required")
	}
	args := map[string]any{
		"kind":  kind,
		"value": value,
	}
	if len(defaults) > 0 {
		args["defaults"] = defaults
	}
	if source != "" {
		args["source"] = source
	}
	var got struct {
		Contact     *crmContact `json:"contact"`
		WasCreated  bool        `json:"was_created"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("crm", "contacts_upsert_by_channel", args, &got); err != nil {
		return nil, false, fmt.Errorf("crm.contacts_upsert_by_channel: %w", err)
	}
	if got.Contact == nil {
		return nil, false, errors.New("crm.contacts_upsert_by_channel returned no contact")
	}
	return got.Contact, got.WasCreated, nil
}

// crmGetContact fetches a contact snapshot. Returns (nil, nil) when
// the contact does not exist — distinct from an error.
func crmGetContact(ctx *sdk.AppCtx, pid string, contactID int64) (*crmContact, error) {
	if contactID == 0 {
		return nil, errors.New("contact_id required")
	}
	var got struct {
		Contact *crmContact `json:"contact"`
		Found   bool        `json:"found"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("crm", "contacts_get", map[string]any{
		"id": contactID,
	}, &got); err != nil {
		return nil, fmt.Errorf("crm.contacts_get(%d): %w", contactID, err)
	}
	if !got.Found || got.Contact == nil {
		return nil, nil
	}
	return got.Contact, nil
}

// crmSendMessage delivers a message to a contact via the CRM's
// messaging-bound channel preferences. Returns the resulting
// conversation_id so we can correlate inbound replies.
func crmSendMessage(ctx *sdk.AppCtx, pid string, contactID int64, body, channel, subject string) (conversationID int64, err error) {
	if contactID == 0 || body == "" {
		return 0, errors.New("contact_id and body required")
	}
	args := map[string]any{
		"id":   contactID,
		"body": body,
	}
	if channel != "" {
		args["channel"] = channel
	}
	if subject != "" {
		args["subject"] = subject
	}
	var got struct {
		ConversationID int64 `json:"conversation_id"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("crm", "contacts_send_message", args, &got); err != nil {
		// Soft-fail: CRM returns an error when messaging isn't bound.
		// The caller decides whether to surface this to the operator
		// or proceed (e.g. a fully manual gig that doesn't need
		// notifications).
		return 0, fmt.Errorf("crm.contacts_send_message: %w", err)
	}
	return got.ConversationID, nil
}

// crmLogActivity appends a row to the contact's timeline. `kind` is
// one of CRM's activity kinds — for gigs we use "note" or "system".
func crmLogActivity(ctx *sdk.AppCtx, pid string, contactID int64, kind, body, source string) error {
	if contactID == 0 || kind == "" || body == "" {
		return errors.New("contact_id, kind, body required")
	}
	args := map[string]any{
		"contact_id": contactID,
		"kind":       kind,
		"body":       body,
	}
	if source != "" {
		args["source"] = source
	}
	var got map[string]any
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("crm", "contacts_log_activity", args, &got); err != nil {
		return fmt.Errorf("crm.contacts_log_activity: %w", err)
	}
	return nil
}
