package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

func loadPhoneResourceGrants(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, project string) ([]phoneResourceGrant, error) {
	rows, err := q.Query(`SELECT id,principal_json,resource_kind,resource_id,actions_json FROM telephony_resource_grants WHERE project_id=? AND enabled=1 AND deleted=0`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grants []phoneResourceGrant
	for rows.Next() {
		var g phoneResourceGrant
		var principal, actions string
		if err = rows.Scan(&g.ID, &principal, &g.Resource.Kind, &g.Resource.ID, &actions); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(principal), &g.Principal); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(actions), &g.Actions); err != nil {
			return nil, err
		}
		g.Enabled = true
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// Legacy policy remains additive, except that an explicit disabled user is
// rejected before independent grants are considered. New grants never confer
// supervision or replace the identity and ownership checks.
func (p *phonePrincipal) forAction(action string) *phonePrincipal {
	if p == nil {
		return nil
	}
	copy := *p
	if p.resourceActions == nil {
		return &copy
	}
	copy.Destinations = make(map[string]bool, len(p.legacyDestinations)+len(p.resourceActions[action]))
	for id, allowed := range p.legacyDestinations {
		copy.Destinations[id] = allowed
	}
	for id, allowed := range p.resourceActions[action] {
		copy.Destinations[id] = allowed
	}
	return &copy
}
func (p *phonePrincipal) outboundDestination(requested, from string) (string, error) {
	if p == nil {
		return "", nil
	}
	if requested == "" && p.legacyNumbers[from] {
		return "", nil
	}
	allowed := p.forAction("call.dial").Destinations
	if requested != "" {
		if !allowed[requested] {
			return "", errors.New("outbound destination not permitted")
		}
		return requested, nil
	}
	var ids []string
	for id, ok := range allowed {
		if ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) != 1 {
		return "", errors.New("select one permitted outbound destination")
	}
	return ids[0], nil
}
