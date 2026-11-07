package main

import (
	"context"
	"encoding/json"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

var clientSchema = objectSchema(map[string]any{
	"name": textSchema, "email": textSchema, "crm_contact_id": map[string]any{"type": "integer", "minimum": 0},
}, "name")

var backlogSchema = objectSchema(map[string]any{
	"name": textSchema, "category": textSchema, "description": textSchema,
	"notes":        map[string]any{"type": "string", "description": "Private internal notes. Never copied into a proposal."},
	"amount_minor": map[string]any{"type": []string{"integer", "null"}, "minimum": 0},
	"currency":     textSchema, "required": map[string]any{"type": "boolean"}, "archived": map[string]any{"type": "boolean"},
}, "name", "category", "currency")

var backlogOperations = []operation{
	{name: "clients_list", description: "List private proposal clients. Supports name/email search and cursor pagination.", permission: "clients.read", readOnly: true,
		properties: map[string]any{"q": textSchema, "before": idSchema, "limit": idSchema}},
	{name: "clients_get", description: "Read a private proposal client.", permission: "clients.read", client: true, readOnly: true},
	{name: "clients_create", description: "Create a private client for backlog planning. Email and CRM contact reference are optional.", permission: "clients.write",
		properties: map[string]any{"client": clientSchema}, required: []string{"client"}},
	{name: "clients_update", description: "Update a client using its expected version. Existing proposal snapshots remain unchanged.", permission: "clients.write", client: true,
		properties: map[string]any{"client": clientSchema, "version": idSchema}, required: []string{"client", "version"}},
	{name: "clients_crm_search", description: "Search the linked CRM for contacts by name, email, or company. Returns linked=false when CRM is unbound.", permission: "clients.write", readOnly: true,
		properties: map[string]any{"q": textSchema, "limit": idSchema, "offset": map[string]any{"type": "integer", "minimum": 0}}},
	{name: "clients_crm_lookup", description: "Read CRM contact name and email to populate a private proposal client.", permission: "clients.write", readOnly: true,
		properties: map[string]any{"contact_id": idSchema}, required: []string{"contact_id"}},
	{name: "backlog_list", description: "Read a client's private backlog. Archived items are opt-in. Prices may be null until estimated.", permission: "clients.read", client: true, readOnly: true,
		properties: map[string]any{"archived": map[string]any{"type": "boolean"}, "before": idSchema, "limit": idSchema}},
	{name: "backlog_create", description: "Add a private item to a client's backlog without creating or changing a proposal.", permission: "clients.write", client: true,
		properties: map[string]any{"item": backlogSchema}, required: []string{"item"}},
	{name: "backlog_update", description: "Update or archive a backlog item using its version. Proposal copies remain unchanged.", permission: "clients.write", client: true,
		properties: map[string]any{"item_id": idSchema, "version": idSchema, "item": backlogSchema}, required: []string{"item_id", "version", "item"}},
	{name: "backlog_add_to_draft", description: "Copy selected, priced backlog items into the selected client's draft. Requires both draft edit and client read grants.", permission: "edit", record: true,
		properties: map[string]any{"client_id": idSchema, "revision_id": idSchema, "version": idSchema, "item_ids": arraySchema(idSchema)},
		required:   []string{"client_id", "revision_id", "version", "item_ids"}},
	{name: "backlog_create_proposal", description: "Create a private draft from selected backlog items. Supply a stable request_key to safely retry. Does not publish or send.", permission: "edit",
		properties: map[string]any{"client_id": idSchema, "item_ids": arraySchema(idSchema), "request_key": textSchema},
		required:   []string{"client_id", "item_ids", "request_key"}},
}

func (a *App) executeBacklog(ctx context.Context, op operation, in input) (any, error) {
	switch op.name {
	case "clients_list":
		limit := pageLimit(in.Limit)
		out := []client{}
		before := in.Before
		caller := sdk.CallerFrom(ctx)
		for len(out) < limit {
			rows, err := a.store.clientsList(ctx, a.project, strings.TrimSpace(in.Q), before, limit)
			if err != nil {
				return nil, err
			}
			for _, c := range rows {
				before = c.ID
				if caller.Allows("proposals.clients.read", clientResource(c.ID)) {
					out = append(out, c)
					if len(out) == limit {
						return map[string]any{"clients": out, "next_before": before}, nil
					}
				}
			}
			if len(rows) < limit {
				break
			}
		}
		return map[string]any{"clients": out}, nil
	case "clients_get":
		return a.store.clientGet(ctx, a.project, in.ClientID)
	case "clients_create", "clients_update":
		if in.Client == nil {
			return nil, invalid("client is required")
		}
		id := in.ClientID
		if op.name == "clients_create" {
			id = 0
		}
		return a.store.clientSave(ctx, a.project, id, in.Version, *in.Client)
	case "clients_crm_search":
		return a.searchCRMContacts(in)
	case "clients_crm_lookup":
		return a.crmContact(in.ContactID)
	case "backlog_list":
		rows, err := a.store.backlogList(ctx, a.project, in.ClientID, in.Archived, in.Before, pageLimit(in.Limit))
		if err != nil {
			return nil, err
		}
		out := map[string]any{"items": rows}
		if len(rows) == pageLimit(in.Limit) {
			out["next_before"] = rows[len(rows)-1].ID
		}
		return out, nil
	case "backlog_create", "backlog_update":
		if in.Item == nil {
			return nil, invalid("item is required")
		}
		id := in.ItemID
		if op.name == "backlog_create" {
			id = 0
		}
		return a.store.backlogSave(ctx, a.project, in.ClientID, id, in.Version, *in.Item)
	case "backlog_add_to_draft", "backlog_create_proposal":
		if !sdk.CallerFrom(ctx).Allows("proposals.clients.read", clientResource(in.ClientID)) {
			return nil, errForbidden
		}
		if op.name == "backlog_create_proposal" {
			in.ProposalID = 0
		}
		return a.store.importBacklog(ctx, a.project, in)
	}
	return nil, errNotFound
}

func pageLimit(limit int) int {
	if limit < 1 || limit > 100 {
		return 100
	}
	return limit
}

type crmContactRecord struct {
	ID           int64  `json:"id"`
	DisplayName  string `json:"display_name"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Company      string `json:"company"`
	PrimaryEmail string `json:"primary_email"`
}

func (c crmContactRecord) client() client {
	name := strings.TrimSpace(c.DisplayName)
	if name == "" {
		name = strings.TrimSpace(c.FirstName + " " + c.LastName)
	}
	if name == "" {
		name = strings.TrimSpace(c.Company)
	}
	if name == "" {
		name = c.PrimaryEmail
	}
	if name == "" {
		name = "Unnamed contact"
	}
	return client{Name: name, Email: c.PrimaryEmail, CRMContactID: c.ID}
}

type crmSearchContact struct {
	ContactID int64  `json:"crm_contact_id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Company   string `json:"company,omitempty"`
}

type crmSearchPage struct {
	Linked     bool               `json:"linked"`
	Contacts   []crmSearchContact `json:"contacts"`
	NextOffset int                `json:"next_offset,omitempty"`
}

func (a *App) searchCRMContacts(in input) (crmSearchPage, error) {
	page := crmSearchPage{Contacts: []crmSearchContact{}}
	query := strings.TrimSpace(in.Q)
	if len(query) > 200 || in.Offset < 0 || in.Offset > 1_000_000 {
		return page, invalid("use a search of at most 200 characters and a valid offset")
	}
	api := a.app.PlatformAPI()
	if api == nil {
		return page, errIntegration
	}
	identity, err := api.WhoAmI()
	if err != nil || identity == nil {
		return page, errIntegration
	}
	// requires.apps has one install binding per role.
	raw, err := json.Marshal(identity.Bindings["crm"])
	if err != nil {
		return page, errIntegration
	}
	var installID int64
	if json.Unmarshal(raw, &installID) != nil || installID <= 0 {
		return page, nil
	}
	page.Linked = true
	limit := in.Limit
	if limit < 1 || limit > 50 {
		limit = 20
	}
	var out struct {
		Contacts []crmContactRecord `json:"contacts"`
		Total    int                `json:"total"`
	}
	if err := a.call("crm", "contacts_search", map[string]any{"q": query, "limit": limit, "offset": in.Offset}, &out); err != nil {
		return page, err
	}
	for _, record := range out.Contacts {
		c := record.client()
		page.Contacts = append(page.Contacts, crmSearchContact{ContactID: c.CRMContactID, Name: c.Name, Email: c.Email, Company: record.Company})
	}
	if len(out.Contacts) > 0 && in.Offset+len(out.Contacts) < out.Total {
		page.NextOffset = in.Offset + len(out.Contacts)
	}
	return page, nil
}

func (a *App) crmContact(id int64) (*client, error) {
	if id <= 0 {
		return nil, invalid("contact_id is required")
	}
	var out struct {
		Contact *crmContactRecord `json:"contact"`
	}
	if err := a.call("crm", "contacts_get", map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	if out.Contact == nil {
		return nil, invalid("CRM contact was not found")
	}
	c := out.Contact.client()
	return &c, nil
}
