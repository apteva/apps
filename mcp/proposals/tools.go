package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type input struct {
	ClientID      int64        `json:"client_id,omitempty"`
	Client        *client      `json:"client,omitempty"`
	Item          *backlogItem `json:"item,omitempty"`
	ItemID        int64        `json:"item_id,omitempty"`
	ItemIDs       []int64      `json:"item_ids,omitempty"`
	ContactID     int64        `json:"contact_id,omitempty"`
	RequestKey    string       `json:"request_key,omitempty"`
	Q             string       `json:"q,omitempty"`
	Archived      bool         `json:"archived,omitempty"`
	ProposalID    int64        `json:"proposal_id,omitempty"`
	RevisionID    int64        `json:"revision_id,omitempty"`
	Version       int          `json:"version,omitempty"`
	Before        int64        `json:"before,omitempty"`
	Limit         int          `json:"limit,omitempty"`
	Content       *content     `json:"content,omitempty"`
	Selection     selection    `json:"selection,omitempty"`
	Rotate        bool         `json:"rotate,omitempty"`
	OpportunityID int64        `json:"opportunity_id,omitempty"`
	ReminderKey   string       `json:"reminder_key,omitempty"`
}

type operation struct {
	name        string
	description string
	permission  string
	properties  map[string]any
	required    []string
	client      bool
	record      bool
	readOnly    bool
}

var (
	idSchema      = map[string]any{"type": "integer", "minimum": 1}
	textSchema    = map[string]any{"type": "string"}
	contentSchema = objectSchema(map[string]any{
		"title": textSchema, "sender": textSchema, "client_name": textSchema, "client_email": textSchema,
		"overview": textSchema, "terms": textSchema, "currency": textSchema, "validity_days": idSchema,
		"client_id":      map[string]any{"type": "integer", "minimum": 0},
		"opportunity_id": map[string]any{"type": "integer", "minimum": 0},
		"categories": arraySchema(objectSchema(map[string]any{
			"name": textSchema, "rule": map[string]any{"type": "string", "enum": []string{"any", "one", "one_required"}},
			"items": arraySchema(objectSchema(map[string]any{
				"name": textSchema, "description": textSchema,
				"backlog_item_id": map[string]any{"type": "integer", "minimum": 0},
				"amount_minor":    map[string]any{"type": "integer", "minimum": 0, "description": "Price in minor currency units, e.g. 250000 for €2,500."},
				"required":        map[string]any{"type": "boolean"},
			}, "name", "amount_minor")),
		}, "name", "rule", "items")),
	}, "title", "currency", "validity_days")
)

func arraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

var operations = append([]operation{
	{name: "list", description: "List accessible proposals, newest first. Use before to page by proposal ID.", permission: "read", readOnly: true,
		properties: map[string]any{"limit": idSchema, "before": idSchema}},
	{name: "get", description: "Read a proposal and its revision history.", permission: "read", record: true, readOnly: true},
	{name: "create", description: "Create a draft. A previous proposal's content may be copied to reuse its structure.", permission: "edit",
		properties: map[string]any{"content": contentSchema}, required: []string{"content"}},
	{name: "update_draft", description: "Replace a draft's content using its revision ID and expected version.", permission: "edit", record: true,
		properties: map[string]any{"revision_id": idSchema, "version": idSchema, "content": contentSchema}, required: []string{"revision_id", "version", "content"}},
	{name: "revise", description: "Open a draft from the latest revision of an unaccepted proposal.", permission: "edit", record: true},
	{name: "preview", description: "Price a draft or published revision. Selection keys are zero-based category:item indices.", permission: "read", record: true, readOnly: true,
		properties: map[string]any{"revision_id": idSchema, "selection": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string", "enum": []string{"include", "later", "decline", "unselected"}}}}, required: []string{"revision_id"}},
	{name: "publish", description: "Freeze the exact draft version and activate its client page. Supersedes the previous open revision.", permission: "publish", record: true,
		properties: map[string]any{"revision_id": idSchema, "version": idSchema}, required: []string{"revision_id", "version"}},
	{name: "withdraw", description: "Close an open revision to client responses.", permission: "publish", record: true,
		properties: map[string]any{"revision_id": idSchema}, required: []string{"revision_id"}},
	{name: "link_create", description: "Return a private client URL. Rotate only to revoke the previous link.", permission: "share", record: true,
		properties: map[string]any{"revision_id": idSchema, "rotate": map[string]any{"type": "boolean"}}, required: []string{"revision_id"}},
	{name: "send", description: "REAL EXTERNAL SEND: email the published proposal to its saved recipient. Omit reminder_key for the invitation. Use a distinct stable key for each intentional reminder.", permission: "share", record: true,
		properties: map[string]any{"revision_id": idSchema, "reminder_key": textSchema}, required: []string{"revision_id"}},
	{name: "crm_lookup", description: "Read CRM opportunity title, client details, and currency for populating a draft.", permission: "edit", readOnly: true,
		properties: map[string]any{"opportunity_id": idSchema}, required: []string{"opportunity_id"}},
	{name: "crm_sync", description: "Explicitly write the accepted total and currency to the linked CRM opportunity.", permission: "sync_crm", record: true},
	{name: "acceptance_get", description: "Read the accepted revision, including frozen choices and total, for downstream apps.", permission: "read", record: true, readOnly: true},
}, backlogOperations...)

func (a *App) MCPTools() []sdk.Tool {
	out := make([]sdk.Tool, 0, len(operations))
	for _, op := range operations {
		properties := map[string]any{}
		for key, value := range op.properties {
			properties[key] = value
		}
		required := append([]string(nil), op.required...)
		if op.client {
			properties["client_id"] = idSchema
			required = append(required, "client_id")
		}
		if op.record {
			properties["proposal_id"] = idSchema
			required = append(required, "proposal_id")
		}
		out = append(out, sdk.Tool{
			Name: "proposals_" + op.name, Description: op.description,
			InputSchema: objectSchema(properties, required...),
			Annotations: map[string]any{"readOnlyHint": op.readOnly},
			HandlerCtx: func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
				if app.CurrentProject() != a.project {
					return nil, errNotFound
				}
				raw, err := json.Marshal(args)
				if err != nil || len(raw) > maxBody {
					return nil, invalid("invalid or oversized arguments")
				}
				var in input
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, invalid("invalid arguments")
				}
				return a.execute(ctx, op, in)
			},
		})
	}
	return out
}

type inputError struct{ message string }

func (e inputError) Error() string { return e.message }
func invalid(message string) error { return inputError{message} }

var errForbidden = errors.New("proposal permission denied")
var errIntegration = errors.New("optional integration is unavailable or rejected the request")

func (a *App) execute(ctx context.Context, op operation, in input) (any, error) {
	caller := sdk.CallerFrom(ctx)
	permission := "proposals." + op.permission
	switch {
	case op.client:
		if in.ClientID <= 0 {
			return nil, invalid("client_id is required")
		}
		if !caller.Allows(permission, clientResource(in.ClientID)) {
			return nil, errForbidden
		}
	case op.record:
		if in.ProposalID <= 0 {
			return nil, invalid("proposal_id is required")
		}
		if !caller.Allows(permission, resource(in.ProposalID)) {
			return nil, errForbidden
		}
	case op.name == "list" || op.name == "clients_list":
		if !caller.Has(permission) {
			return nil, errForbidden
		}
	default:
		if !caller.Allows(permission, "") {
			return nil, errForbidden
		}
	}
	s := a.store
	switch op.name {
	case "list":
		limit := in.Limit
		if limit < 1 || limit > 100 {
			limit = 50
		}
		// Apply grants before determining the page boundary.
		results := []revision{}
		before := in.Before
		for len(results) < limit {
			page, err := s.list(ctx, a.project, before, limit)
			if err != nil {
				return nil, err
			}
			for _, r := range page {
				before = r.ProposalID
				if caller.Allows(permission, resource(r.ProposalID)) {
					results = append(results, r)
					if len(results) == limit {
						return map[string]any{"proposals": results, "next_before": before}, nil
					}
				}
			}
			if len(page) < limit {
				break
			}
		}
		return map[string]any{"proposals": results}, nil
	case "get":
		return s.get(ctx, a.project, in.ProposalID)
	case "create", "update_draft":
		if in.Content == nil {
			return nil, invalid("content is required")
		}
		if err := in.Content.validate(false); err != nil {
			return nil, invalid(err.Error())
		}
		if err := a.validateClientReferences(ctx, *in.Content); err != nil {
			return nil, err
		}
		if op.name == "create" {
			return s.create(ctx, a.project, *in.Content)
		}
		return s.update(ctx, a.project, in.ProposalID, in.RevisionID, in.Version, *in.Content)
	case "revise":
		return s.revise(ctx, a.project, in.ProposalID)
	case "publish":
		return s.publish(ctx, a.project, in.ProposalID, in.RevisionID, in.Version)
	case "withdraw":
		return s.withdraw(ctx, a.project, in.ProposalID, in.RevisionID)
	case "link_create", "send":
		return a.share(ctx, op.name, in)
	case "crm_lookup":
		return a.crmOpportunity(in.OpportunityID)
	case "acceptance_get", "crm_sync":
		r, err := a.accepted(ctx, in.ProposalID)
		if err != nil || op.name == "acceptance_get" {
			return r, err
		}
		return a.syncCRM(r)
	case "preview":
		p, err := s.get(ctx, a.project, in.ProposalID)
		if err != nil {
			return nil, err
		}
		for _, r := range p.Revisions {
			if r.ID == in.RevisionID {
				choices, total, err := r.Content.price(in.Selection, false)
				if err != nil {
					return nil, invalid(err.Error())
				}
				return map[string]any{"selection": choices, "total_minor": total, "currency": r.Content.Currency}, nil
			}
		}
	}
	return a.executeBacklog(ctx, op, in)
}

func resource(id int64) string { return fmt.Sprintf("proposal/%d", id) }

func (a *App) accepted(ctx context.Context, id int64) (revision, error) {
	p, err := a.store.get(ctx, a.project, id)
	if err != nil {
		return revision{}, err
	}
	for _, r := range p.Revisions {
		if r.State == "accepted" {
			return r, nil
		}
	}
	return revision{}, invalid("this proposal has no accepted revision")
}

func (a *App) publicBase() (*url.URL, error) {
	raw := strings.TrimSpace(a.app.Config().Get("public_base_url"))
	if raw == "" {
		info, err := a.app.PlatformInfo()
		if err != nil || info == nil || info.PublicURL == "" {
			return nil, invalid("configure the platform public URL before sharing")
		}
		raw = strings.TrimRight(info.PublicURL, "/") + "/api/apps/proposals"
	}
	base, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" ||
		(base.Scheme != "https" && !(base.Scheme == "http" && (base.Hostname() == "localhost" || base.Hostname() == "127.0.0.1" || base.Hostname() == "::1"))) {
		return nil, invalid("the public app URL must be HTTPS")
	}
	return base, nil
}

func (a *App) share(ctx context.Context, operation string, in input) (any, error) {
	base, err := a.publicBase()
	if err != nil {
		return nil, err
	}
	r, token, err := a.store.link(ctx, a.project, in.ProposalID, in.RevisionID, operation == "link_create" && in.Rotate)
	if err != nil {
		return nil, err
	}
	base.Path += "/offer/"
	base.RawQuery = url.Values{"project_id": {a.project}}.Encode()
	base.Fragment = token
	if operation == "link_create" {
		return map[string]any{"url": base.String(), "expires_at": r.ExpiresAt}, nil
	}
	if r.Content.ClientEmail == "" {
		return nil, invalid("the published proposal has no client email")
	}
	reminderKey := strings.TrimSpace(in.ReminderKey)
	if len(reminderKey) > 80 {
		return nil, invalid("reminder_key must be at most 80 characters")
	}
	subject := "Proposal: " + strings.ReplaceAll(strings.ReplaceAll(r.Content.Title, "\r", " "), "\n", " ")
	if reminderKey != "" {
		subject = "Reminder: " + subject
	}
	body := fmt.Sprintf("%s has shared a proposal with you. Review the scope and choose what to include:\n\n%s\n\nThis proposal is open until %s.",
		r.Content.Sender, base.String(), time.Unix(r.ExpiresAt, 0).UTC().Format("2 January 2006, 15:04 UTC"))
	var result struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	err = a.call("messaging", "send_message", map[string]any{
		"channel": "email", "to": r.Content.ClientEmail, "subject": subject, "body": body,
		"body_html": "<p>" + html.EscapeString(r.Content.Sender) + " has shared a proposal with you.</p><p><a href=\"" +
			html.EscapeString(base.String()) + "\">Review proposal</a></p>",
		"idempotency_key": fmt.Sprintf("proposals:%d:%s:%s", r.ID, r.nonce, reminderKey),
	}, &result)
	if err != nil {
		return nil, err
	}
	return map[string]any{"message_id": result.ID, "status": result.Status}, nil
}

type opportunity struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	ClientName  string `json:"contact_name"`
	ClientEmail string `json:"contact_email"`
	Currency    string `json:"currency"`
}

func (a *App) crmOpportunity(id int64) (*opportunity, error) {
	if id <= 0 {
		return nil, invalid("opportunity_id is required")
	}
	var out struct {
		Opportunity *opportunity `json:"opportunity"`
	}
	if err := a.call("crm", "opportunities_get", map[string]any{"opportunity_id": id, "activity_limit": 1, "conversation_limit": 1}, &out); err != nil {
		return nil, err
	}
	if out.Opportunity == nil {
		return nil, invalid("CRM opportunity was not found")
	}
	return out.Opportunity, nil
}

func (a *App) syncCRM(r revision) (any, error) {
	if r.Content.OpportunityID <= 0 {
		return nil, invalid("the accepted revision has no linked CRM opportunity")
	}
	var out map[string]any
	if err := a.call("crm", "opportunities_update", map[string]any{"opportunity_id": r.Content.OpportunityID, "patch": map[string]any{
		"value": float64(r.Response.TotalMinor) / 100, "currency": r.Content.Currency,
	}}, &out); err != nil {
		return nil, err
	}
	return map[string]any{"opportunity_id": r.Content.OpportunityID, "synced": true}, nil
}

func (a *App) call(app, tool string, args map[string]any, out any) error {
	api := a.app.WithProject(a.project).PlatformAPI()
	if api == nil {
		return errIntegration
	}
	if err := api.CallAppResult(app, tool, args, out); err != nil {
		// The downstream error may include a private URL or recipient details.
		return fmt.Errorf("%s: %w", app, errIntegration)
	}
	return nil
}
