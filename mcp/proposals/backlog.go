package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type client struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	CRMContactID int64  `json:"crm_contact_id"`
	Version      int    `json:"version"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

type backlogItem struct {
	ID          int64  `json:"id"`
	ClientID    int64  `json:"client_id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Notes       string `json:"notes"`
	AmountMinor *int64 `json:"amount_minor"`
	Currency    string `json:"currency"`
	Required    bool   `json:"required"`
	Archived    bool   `json:"archived"`
	Version     int    `json:"version"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

func (c *client) validate() error {
	c.Name, c.Email = strings.TrimSpace(c.Name), strings.TrimSpace(c.Email)
	if c.Name == "" || len(c.Name) > 160 || len(c.Email) > 254 || c.CRMContactID < 0 {
		return invalid("enter a client name and valid contact details")
	}
	if c.Email != "" {
		addr, err := mail.ParseAddress(c.Email)
		if err != nil || addr.Address != c.Email {
			return invalid("enter a valid client email address")
		}
	}
	return nil
}

func (b *backlogItem) validate() error {
	b.Name, b.Category = strings.TrimSpace(b.Name), strings.TrimSpace(b.Category)
	b.Currency = strings.ToUpper(strings.TrimSpace(b.Currency))
	if b.Name == "" || len(b.Name) > 160 || b.Category == "" || len(b.Category) > 120 ||
		len(b.Description) > 2000 || len(b.Notes) > 8000 ||
		(b.AmountMinor != nil && (*b.AmountMinor < 0 || *b.AmountMinor > maxItemMinor)) {
		return invalid("enter an item name, category, and valid price; keep descriptions within 2 KB and private notes within 8 KB")
	}
	c := content{Title: b.Name, Currency: b.Currency, ValidityDays: 14}
	if err := c.validate(false); err != nil {
		return invalid(err.Error())
	}
	return nil
}

const clientSelect = "SELECT id, name, email, crm_contact_id, version, created_at, updated_at FROM clients"

func scanClient(row interface{ Scan(...any) error }) (client, error) {
	var c client
	err := row.Scan(&c.ID, &c.Name, &c.Email, &c.CRMContactID, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = errNotFound
	}
	return c, err
}

func (s store) clientGet(ctx context.Context, project string, id int64) (client, error) {
	return scanClient(s.db.QueryRowContext(ctx, clientSelect+" WHERE project_id = ? AND id = ?", project, id))
}

func (s store) clientsList(ctx context.Context, project, q string, before int64, limit int) ([]client, error) {
	rows, err := s.db.QueryContext(ctx, clientSelect+`
WHERE project_id = ? AND (? = 0 OR id < ?)
AND (? = '' OR instr(lower(name || ' ' || email), lower(?)) > 0)
ORDER BY id DESC LIMIT ?`, project, before, before, q, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []client{}
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s store) clientSave(ctx context.Context, project string, id int64, version int, c client) (client, error) {
	if err := c.validate(); err != nil {
		return client{}, err
	}
	// Deduplicate explicit CRM references and non-empty email addresses.
	// The unique indexes also cover concurrent inserts.
	var duplicate bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM clients WHERE project_id = ? AND id <> ?
AND ((? <> '' AND lower(email) = lower(?)) OR (? > 0 AND crm_contact_id = ?)))`,
		project, id, c.Email, c.Email, c.CRMContactID, c.CRMContactID).Scan(&duplicate)
	if err != nil {
		return client{}, err
	}
	if duplicate {
		return client{}, invalid("a client with this email or CRM contact already exists")
	}
	now := time.Now().Unix()
	if id == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO clients(project_id, name, email, crm_contact_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`, project, c.Name, c.Email, c.CRMContactID, now, now)
		if err != nil {
			return client{}, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return client{}, err
		}
	} else {
		res, err := s.db.ExecContext(ctx, `UPDATE clients SET name = ?, email = ?, crm_contact_id = ?,
version = version + 1, updated_at = ? WHERE project_id = ? AND id = ? AND version = ?`,
			c.Name, c.Email, c.CRMContactID, now, project, id, version)
		if err := changed(res, err); err != nil {
			return client{}, err
		}
	}
	return s.clientGet(ctx, project, id)
}

const backlogSelect = `SELECT b.id, b.client_id, b.name, b.category, b.description, b.notes,
b.amount_minor, b.currency, b.required, b.archived, b.version, b.created_at, b.updated_at
FROM backlog_items b JOIN clients c ON c.id = b.client_id`

func scanBacklog(row interface{ Scan(...any) error }) (backlogItem, error) {
	var b backlogItem
	err := row.Scan(&b.ID, &b.ClientID, &b.Name, &b.Category, &b.Description, &b.Notes,
		&b.AmountMinor, &b.Currency, &b.Required, &b.Archived, &b.Version, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = errNotFound
	}
	return b, err
}

func (s store) backlogList(ctx context.Context, project string, clientID int64, archived bool, before int64, limit int) ([]backlogItem, error) {
	if _, err := s.clientGet(ctx, project, clientID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, backlogSelect+`
WHERE c.project_id = ? AND b.client_id = ? AND b.archived = ? AND (? = 0 OR b.id < ?)
ORDER BY b.id DESC LIMIT ?`, project, clientID, archived, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []backlogItem{}
	for rows.Next() {
		b, err := scanBacklog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s store) backlogSave(ctx context.Context, project string, clientID, id int64, version int, b backlogItem) (backlogItem, error) {
	if err := b.validate(); err != nil {
		return backlogItem{}, err
	}
	if _, err := s.clientGet(ctx, project, clientID); err != nil {
		return backlogItem{}, err
	}
	now := time.Now().Unix()
	if id == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO backlog_items(client_id, name, category, description, notes,
amount_minor, currency, required, archived, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			clientID, b.Name, b.Category, b.Description, b.Notes, b.AmountMinor, b.Currency, b.Required, b.Archived, now, now)
		if err != nil {
			return backlogItem{}, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return backlogItem{}, err
		}
	} else {
		res, err := s.db.ExecContext(ctx, `UPDATE backlog_items SET name = ?, category = ?, description = ?, notes = ?,
amount_minor = ?, currency = ?, required = ?, archived = ?, version = version + 1, updated_at = ?
WHERE id = ? AND client_id = ? AND version = ?`,
			b.Name, b.Category, b.Description, b.Notes, b.AmountMinor, b.Currency, b.Required, b.Archived, now, id, clientID, version)
		if err := changed(res, err); err != nil {
			return backlogItem{}, err
		}
	}
	return scanBacklog(s.db.QueryRowContext(ctx, backlogSelect+" WHERE c.project_id = ? AND b.id = ?", project, id))
}

func clientResource(id int64) string { return fmt.Sprintf("client/%d", id) }

func (a *App) validateClientReferences(ctx context.Context, c content) error {
	caller := sdk.CallerFrom(ctx)
	if c.ClientID != 0 {
		if !caller.Allows("proposals.clients.read", clientResource(c.ClientID)) {
			return errForbidden
		}
		if _, err := a.store.clientGet(ctx, a.project, c.ClientID); err != nil {
			return err
		}
	}
	for _, group := range c.Categories {
		for _, line := range group.Items {
			if line.BacklogItemID == 0 {
				continue
			}
			var belongs bool
			err := a.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM backlog_items b
JOIN clients c ON c.id = b.client_id WHERE c.project_id = ? AND c.id = ? AND b.id = ?)`,
				a.project, c.ClientID, line.BacklogItemID).Scan(&belongs)
			if err != nil {
				return err
			}
			if !belongs {
				return invalid("backlog items must belong to the proposal’s selected client")
			}
		}
	}
	return nil
}

// Import selected items in one transaction. Existing draft items keep their
// snapshots, and a retry never adds the same backlog item twice.
func (s store) importBacklog(ctx context.Context, project string, in input) (detail, error) {
	if len(in.ItemIDs) < 1 || len(in.ItemIDs) > 100 {
		return detail{}, invalid("select between 1 and 100 backlog items")
	}
	ids := slices.Clone(in.ItemIDs)
	slices.Sort(ids)
	if ids[0] <= 0 || len(slices.Compact(slices.Clone(ids))) != len(ids) {
		return detail{}, invalid("backlog item IDs must be positive and unique")
	}
	creating := in.ProposalID == 0
	if creating && (len(in.RequestKey) < 8 || len(in.RequestKey) > 100) {
		return detail{}, invalid("supply a stable request_key between 8 and 100 characters")
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint(in.ClientID, ids))))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return detail{}, err
	}
	defer tx.Rollback()
	var draft revision
	if creating {
		var previousID int64
		var previousFingerprint string
		err := tx.QueryRowContext(ctx, `SELECT proposal_id, fingerprint FROM backlog_proposal_requests WHERE project_id = ? AND request_key = ?`,
			project, in.RequestKey).Scan(&previousID, &previousFingerprint)
		if err == nil {
			if fingerprint != previousFingerprint {
				return detail{}, errConflict
			}
			if err := tx.Commit(); err != nil {
				return detail{}, err
			}
			return s.get(ctx, project, previousID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return detail{}, err
		}
	} else {
		draft, err = scanRevision(tx.QueryRowContext(ctx, revisionSelect+" WHERE p.project_id = ? AND p.id = ? AND r.id = ? AND r.state = 'draft'",
			project, in.ProposalID, in.RevisionID))
		if err != nil {
			return detail{}, err
		}
		if draft.Version != in.Version {
			return detail{}, errConflict
		}
		if draft.Content.ClientID != in.ClientID {
			return detail{}, invalid("select this client in the proposal before importing their backlog")
		}
	}
	owner, err := scanClient(tx.QueryRowContext(ctx, clientSelect+" WHERE project_id = ? AND id = ?", project, in.ClientID))
	if err != nil {
		return detail{}, err
	}
	imported := map[int64]bool{}
	for _, group := range draft.Content.Categories {
		for _, line := range group.Items {
			if line.BacklogItemID > 0 {
				imported[line.BacklogItemID] = true
			}
		}
	}
	if creating {
		draft.Content = content{Title: "Proposal for " + owner.Name, ClientID: owner.ID,
			ClientName: owner.Name, ClientEmail: owner.Email, Currency: "EUR", ValidityDays: 14}
	}
	for index, id := range ids {
		b, err := scanBacklog(tx.QueryRowContext(ctx, backlogSelect+" WHERE c.project_id = ? AND b.client_id = ? AND b.id = ? AND b.archived = 0",
			project, owner.ID, id))
		if err != nil {
			return detail{}, err
		}
		if b.AmountMinor == nil {
			return detail{}, invalid("set a price for " + b.Name + " before adding it to a proposal")
		}
		if creating && index == 0 {
			draft.Content.Currency = b.Currency
		}
		if b.Currency != draft.Content.Currency {
			return detail{}, invalid("selected items must use the proposal’s currency")
		}
		if imported[id] {
			continue
		}
		groupIndex := -1
		for i, group := range draft.Content.Categories {
			if group.Name == b.Category {
				if group.Rule != "any" {
					return detail{}, invalid("the matching category must allow any items before importing a backlog item")
				}
				groupIndex = i
				break
			}
		}
		if groupIndex == -1 {
			draft.Content.Categories = append(draft.Content.Categories, category{Name: b.Category, Rule: "any"})
			groupIndex = len(draft.Content.Categories) - 1
		}
		draft.Content.Categories[groupIndex].Items = append(draft.Content.Categories[groupIndex].Items,
			item{Name: b.Name, Description: b.Description, AmountMinor: *b.AmountMinor, Required: b.Required, BacklogItemID: b.ID})
	}
	if err := draft.Content.validate(false); err != nil {
		return detail{}, invalid(err.Error())
	}
	raw, err := json.Marshal(draft.Content)
	if err != nil {
		return detail{}, err
	}
	id := in.ProposalID
	if creating {
		result, err := tx.ExecContext(ctx, "INSERT INTO proposals(project_id) VALUES (?)", project)
		if err != nil {
			return detail{}, err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return detail{}, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO revisions(proposal_id, number, state, content) VALUES (?, 1, 'draft', ?)", id, string(raw)); err != nil {
			return detail{}, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO backlog_proposal_requests(project_id, request_key, fingerprint, proposal_id) VALUES (?, ?, ?, ?)",
			project, in.RequestKey, fingerprint, id); err != nil {
			return detail{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, "UPDATE revisions SET content = ?, version = version + 1 WHERE id = ?", string(raw), draft.ID); err != nil {
			return detail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return detail{}, err
	}
	return s.get(ctx, project, id)
}
