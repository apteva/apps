package main

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

const maxItemMinor = 100_000_000_000

var (
	errNotFound = errors.New("proposal not found")
	errConflict = errors.New("the proposal changed or is no longer open; refresh and try again")
)

type item struct {
	BacklogItemID int64  `json:"backlog_item_id,omitempty"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	AmountMinor   int64  `json:"amount_minor"`
	Required      bool   `json:"required,omitempty"`
}

type category struct {
	Name  string `json:"name"`
	Rule  string `json:"rule"`
	Items []item `json:"items"`
}

type content struct {
	ClientID      int64      `json:"client_id,omitempty"`
	Title         string     `json:"title"`
	Sender        string     `json:"sender"`
	ClientName    string     `json:"client_name"`
	ClientEmail   string     `json:"client_email,omitempty"`
	Overview      string     `json:"overview,omitempty"`
	Terms         string     `json:"terms,omitempty"`
	Currency      string     `json:"currency"`
	ValidityDays  int        `json:"validity_days"`
	OpportunityID int64      `json:"opportunity_id,omitempty"`
	Categories    []category `json:"categories"`
}

type revision struct {
	ID          int64     `json:"id"`
	ProposalID  int64     `json:"proposal_id"`
	Number      int       `json:"number"`
	Version     int       `json:"version"`
	State       string    `json:"state"`
	Content     content   `json:"content"`
	ExpiresAt   int64     `json:"expires_at,omitempty"`
	PublishedAt int64     `json:"published_at,omitempty"`
	Response    *response `json:"response,omitempty"`
	nonce       string
}

type selection map[string]string

type response struct {
	Outcome    string    `json:"outcome"`
	Name       string    `json:"name"`
	Note       string    `json:"note"`
	Selection  selection `json:"selection"`
	TotalMinor int64     `json:"total_minor"`
	CreatedAt  int64     `json:"created_at"`
}

type detail struct {
	ID        int64      `json:"id"`
	Revisions []revision `json:"revisions"`
}

func (r revision) stateAt(now int64) string {
	if r.State == "published" && r.ExpiresAt <= now {
		return "expired"
	}
	return r.State
}

func (c *content) validate(publish bool) error {
	c.Title = strings.TrimSpace(c.Title)
	c.Sender = strings.TrimSpace(c.Sender)
	c.ClientName = strings.TrimSpace(c.ClientName)
	c.ClientEmail = strings.TrimSpace(c.ClientEmail)
	c.Currency = strings.ToUpper(strings.TrimSpace(c.Currency))
	if c.Title == "" || len(c.Title) > 200 || len(c.Sender) > 160 || len(c.ClientName) > 160 || len(c.ClientEmail) > 254 {
		return errors.New("enter a title and keep names and contact details within their length limits")
	}
	if publish && c.Sender == "" {
		return errors.New("enter a sender or business name before publishing")
	}
	if c.ClientEmail != "" {
		addr, err := mail.ParseAddress(c.ClientEmail)
		if err != nil || addr.Address != c.ClientEmail {
			return errors.New("enter a valid client email address")
		}
	}
	if len(c.Overview) > 16000 || len(c.Terms) > 16000 {
		return errors.New("overview and terms each have a 16 KB limit")
	}
	switch c.Currency {
	case "EUR", "USD", "GBP", "CAD", "AUD", "CHF", "NZD", "SEK", "NOK", "DKK":
	default:
		return errors.New("choose a supported currency")
	}
	if c.ValidityDays < 1 || c.ValidityDays > 365 || c.OpportunityID < 0 || c.ClientID < 0 {
		return errors.New("validity must be between 1 and 365 days and the opportunity ID cannot be negative")
	}
	if len(c.Categories) > 20 {
		return errors.New("use at most 20 categories")
	}
	totalItems := 0
	for i := range c.Categories {
		group := &c.Categories[i]
		group.Name = strings.TrimSpace(group.Name)
		if len(group.Name) > 120 || (publish && (group.Name == "" || len(group.Items) == 0)) {
			return fmt.Errorf("category %d needs a name and at least one item", i+1)
		}
		switch group.Rule {
		case "any", "one", "one_required":
		default:
			return fmt.Errorf("invalid selection rule in category %d", i+1)
		}
		totalItems += len(group.Items)
		if totalItems > 100 {
			return errors.New("use at most 100 items")
		}
		for j := range group.Items {
			line := &group.Items[j]
			line.Name = strings.TrimSpace(line.Name)
			if len(line.Name) > 160 || (publish && line.Name == "") || len(line.Description) > 2000 {
				return fmt.Errorf("check the name and description for item %d in category %d", j+1, i+1)
			}
			if line.BacklogItemID < 0 || line.AmountMinor < 0 || line.AmountMinor > maxItemMinor {
				return fmt.Errorf("invalid amount for item %d in category %d", j+1, i+1)
			}
			if line.Required && group.Rule != "any" {
				return fmt.Errorf("required items belong in a multi-select category, or use the pick-one-required rule for category %d", i+1)
			}
		}
	}
	if publish && totalItems == 0 {
		return errors.New("add at least one item before publishing")
	}
	return nil
}

// Indices identify items within an immutable revision.
func (c content) price(choices selection, accepting bool) (selection, int64, error) {
	normalized := make(selection)
	var total int64
	known := 0
	for i, group := range c.Categories {
		included := 0
		for j, line := range group.Items {
			key := fmt.Sprintf("%d:%d", i, j)
			choice, provided := choices[key]
			if provided {
				known++
				switch choice {
				case "include", "later", "decline", "unselected":
				default:
					return nil, 0, errors.New("invalid item decision")
				}
			}
			if line.Required {
				if provided && choice != "include" {
					return nil, 0, errors.New("required items cannot be removed; request a change instead")
				}
				choice = "include"
			}
			if !provided && !line.Required {
				choice = "unselected"
			}
			normalized[key] = choice
			if choice == "include" {
				included++
				total += line.AmountMinor
			}
		}
		if group.Rule != "any" && (included > 1 || (accepting && group.Rule == "one_required" && included != 1)) {
			return nil, 0, fmt.Errorf("check your selection in %q", group.Name)
		}
	}
	if known != len(choices) {
		return nil, 0, errors.New("selection contains an unknown item")
	}
	return normalized, total, nil
}

func newResponse(c content, outcome, name, note string, choices selection, now time.Time) (response, error) {
	name, note = strings.TrimSpace(name), strings.TrimSpace(note)
	if len(name) > 160 || len(note) > 4000 {
		return response{}, errors.New("name or message is too long")
	}
	switch outcome {
	case "accepted":
		if name == "" {
			return response{}, errors.New("enter your name to accept")
		}
	case "changes_requested":
		if note == "" {
			return response{}, errors.New("describe what you would like changed")
		}
	case "declined":
	default:
		return response{}, errors.New("invalid proposal response")
	}
	normalized, total, err := c.price(choices, outcome == "accepted")
	return response{Outcome: outcome, Name: name, Note: note, Selection: normalized, TotalMinor: total, CreatedAt: now.Unix()}, err
}
