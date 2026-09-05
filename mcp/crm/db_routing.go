package main

// db_routing.go — inbound routing rules (migration 006).
//
// A rule matches inbound on the recipient address (which of our
// addresses it hit) and/or the sender address, and applies additive
// actions: add the contact to a list and/or tag it. Evaluated in
// handleInbound after the contact is resolved.

import (
	"database/sql"
	"errors"
	"strings"
)

type routingRule struct {
	ID             int64  `json:"id"`
	Name           string `json:"name,omitempty"`
	MatchRecipient string `json:"match_recipient,omitempty"`
	MatchSender    string `json:"match_sender,omitempty"`
	AddListID      *int64 `json:"add_list_id,omitempty"`
	AddTag         string `json:"add_tag,omitempty"`
	Priority       int    `json:"priority"`
	Enabled        bool   `json:"enabled"`
}

func dbCreateRoutingRule(db *sql.DB, pid string, r *routingRule) (int64, error) {
	if r.AddListID != nil {
		l, err := dbListGet(db, pid, *r.AddListID)
		if err != nil {
			return 0, err
		}
		if l == nil || l.ArchivedAt != "" {
			return 0, errors.New("list not found in this project")
		}
	}
	if r.MatchRecipient == "" && r.MatchSender == "" {
		// Allowed (a catch-all), but it must DO something.
	}
	if r.AddListID == nil && strings.TrimSpace(r.AddTag) == "" {
		return 0, errors.New("rule must set at least one action (add_list_id or add_tag)")
	}
	res, err := db.Exec(
		`INSERT INTO routing_rules
			(project_id, name, match_recipient, match_sender, add_list_id, add_tag, priority, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, nullStr(r.Name), nullStr(r.MatchRecipient), nullStr(r.MatchSender),
		r.AddListID, nullStr(r.AddTag), r.Priority, boolToInt(r.Enabled),
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func dbListRoutingRules(db *sql.DB, pid string) ([]*routingRule, error) {
	rows, err := db.Query(
		`SELECT id, COALESCE(name,''), COALESCE(match_recipient,''),
				COALESCE(match_sender,''), add_list_id, COALESCE(add_tag,''),
				priority, enabled
		 FROM routing_rules
		 WHERE project_id = ? AND archived_at IS NULL
		 ORDER BY priority ASC, id ASC`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*routingRule{}
	for rows.Next() {
		r := &routingRule{}
		var listID sql.NullInt64
		var enabled int
		if err := rows.Scan(&r.ID, &r.Name, &r.MatchRecipient, &r.MatchSender,
			&listID, &r.AddTag, &r.Priority, &enabled); err != nil {
			return nil, err
		}
		if listID.Valid {
			v := listID.Int64
			r.AddListID = &v
		}
		r.Enabled = enabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func dbDeleteRoutingRule(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(
		`UPDATE routing_rules SET archived_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND id = ? AND archived_at IS NULL`,
		pid, id,
	)
	return err
}

// routingActions is what a rule pass produced — returned so the caller
// can emit events / report what happened.
type routingActions struct {
	Lists []int64
	Tags  []string
}

// applyRoutingRules evaluates every enabled rule for the project and
// applies the actions of those whose recipient + sender patterns match.
// All matches apply (additive). Idempotent: list add + tag add both
// no-op on repeat. recipients is the set of addresses the inbound hit
// (matched recipient + To); sender is the from address.
func applyRoutingRules(db *sql.DB, pid string, contactID int64, recipients []string, sender string) (routingActions, error) {
	var acted routingActions
	rules, err := dbListRoutingRules(db, pid)
	if err != nil {
		return acted, err
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if !ruleRecipientMatches(r, recipients) || !ruleSenderMatches(r, sender) {
			continue
		}
		if r.AddListID != nil {
			if changed, err := dbListAddContactChanged(db, pid, *r.AddListID, contactID, "routing_rule"); err == nil && changed {
				acted.Lists = append(acted.Lists, *r.AddListID)
			}
		}
		if tag := strings.TrimSpace(r.AddTag); tag != "" {
			if err := dbAddTag(db, pid, contactID, tag); err == nil {
				acted.Tags = append(acted.Tags, tag)
			}
		}
	}
	return acted, nil
}

func ruleRecipientMatches(r *routingRule, recipients []string) bool {
	if strings.TrimSpace(r.MatchRecipient) == "" {
		return true
	}
	for _, addr := range recipients {
		if addressMatchesPattern(r.MatchRecipient, addr) {
			return true
		}
	}
	return false
}

func ruleSenderMatches(r *routingRule, sender string) bool {
	if strings.TrimSpace(r.MatchSender) == "" {
		return true
	}
	return addressMatchesPattern(r.MatchSender, sender)
}

// addressMatchesPattern matches an email address against a small
// pattern grammar (case-insensitive):
//
//	""  or  "*"  or  "*@*"   → any
//	"@acme.com" or "*@acme.com" → any local-part at that domain
//	"support@*"                 → that local-part at any domain
//	"alice@acme.com"            → exact
func addressMatchesPattern(pattern, addr string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return false
	}
	switch {
	case pattern == "" || pattern == "*" || pattern == "*@*":
		return true
	case strings.HasPrefix(pattern, "@"):
		return strings.HasSuffix(addr, pattern)
	case strings.HasPrefix(pattern, "*@"):
		return strings.HasSuffix(addr, pattern[1:]) // drop the '*', keep "@domain"
	case strings.HasSuffix(pattern, "@*"):
		local := strings.TrimSuffix(pattern, "@*")
		at := strings.IndexByte(addr, '@')
		return at > 0 && addr[:at] == local
	default:
		return pattern == addr
	}
}
