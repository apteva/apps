package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Individual destinations opt into capacity. The resource is the verified
// identity, not the destination: a person can have several routing addresses.
var errPhoneCapacity = errors.New("destination capacity unavailable")

type destinationCapacity struct {
	Identity phoneIdentity `json:"identity"`
	Limit    int           `json:"concurrent_call_limit"`
}

func readDestinationCapacity(raw string) (destinationCapacity, error) {
	var config struct {
		Capacity *destinationCapacity `json:"capacity"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return destinationCapacity{}, err
	}
	if config.Capacity == nil {
		return destinationCapacity{}, nil
	}
	c := *config.Capacity
	if !c.Identity.valid() || c.Limit < 1 || c.Limit > 10 {
		return c, errors.New("individual capacity requires a verified identity and concurrent_call_limit between 1 and 10")
	}
	return c, nil
}

func cleanupCapacityTx(tx *sql.Tx, now time.Time) error {
	_, err := tx.Exec(`DELETE FROM phone_capacity WHERE call_id IN (SELECT id FROM calls WHERE status IN ('completed','failed','busy','no-answer','canceled')) OR (expires_at<>'' AND call_id IN (SELECT id FROM calls WHERE status='pending') AND NOT EXISTS(SELECT 1 FROM call_offers o WHERE o.call_id=phone_capacity.call_id AND o.capacity_principal=phone_capacity.principal AND o.status='offered' AND o.expires_at>?) AND (expires_at<=? OR EXISTS(SELECT 1 FROM call_offers o WHERE o.call_id=phone_capacity.call_id AND o.capacity_principal=phone_capacity.principal AND o.status IN ('failed','expired','canceled'))))`, ringTime(now), ringTime(now))
	return err
}

func reserveCapacityTx(tx *sql.Tx, callID, project, dest string, c destinationCapacity, expires string) error {
	if c.Limit == 0 {
		return nil
	}
	// This first write serializes concurrent acquisition before counting. Existing
	// nonterminal owners include outbound calls created before capacity was enabled.
	if err := cleanupCapacityTx(tx, time.Now()); err != nil {
		return err
	}
	// Apply the strictest configured limit across aliases inside the write lock.
	live, err := capacityForIdentityQuery(tx, project, c.Identity)
	if err != nil {
		return err
	}
	if live.Limit > 0 {
		c.Limit = min(c.Limit, live.Limit)
	}
	principal := c.Identity.key()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM (SELECT call_id FROM phone_capacity WHERE project_id=? AND principal=? AND call_id<>? UNION SELECT o.call_id FROM telephony_call_owners o JOIN calls c ON c.id=o.call_id WHERE o.project_id=? AND o.principal=? AND o.call_id<>? AND c.status NOT IN ('completed','failed','busy','no-answer','canceled'))`, project, principal, callID, project, principal, callID).Scan(&count); err != nil {
		return err
	}
	if count >= c.Limit {
		return errPhoneCapacity
	}
	_, err = tx.Exec(`INSERT INTO phone_capacity(call_id,project_id,principal,destination_id,expires_at) VALUES(?,?,?,?,?) ON CONFLICT(call_id,principal) DO UPDATE SET destination_id=excluded.destination_id,expires_at=CASE WHEN phone_capacity.expires_at='' OR excluded.expires_at='' THEN '' ELSE MAX(phone_capacity.expires_at,excluded.expires_at) END`, callID, project, principal, dest, expires)
	return err
}

func (a *App) capacityForIdentity(project string, identity phoneIdentity) (destinationCapacity, error) {
	return capacityForIdentityQuery(a.db().db, project, identity)
}
func capacityForIdentityQuery(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, project string, identity phoneIdentity) (destinationCapacity, error) {
	rows, err := q.Query(`SELECT config_json FROM routing_destinations WHERE project_id=? AND kind='browser'`, project)
	if err != nil {
		return destinationCapacity{}, err
	}
	defer rows.Close()
	result := destinationCapacity{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return result, err
		}
		c, e := readDestinationCapacity(raw)
		if e != nil {
			return result, e
		}
		if c.Limit > 0 && c.Identity == identity && (result.Limit == 0 || c.Limit < result.Limit) {
			result = c
		}
	}
	return result, rows.Err()
}

func (a *App) validateDecisionDestination(project string, dest *routingDestinationRow) error {
	if dest == nil || !dest.Enabled || dest.ProjectID != project || dest.Kind != "browser" {
		return errors.New("decision targets must be enabled browser destinations in this project")
	}
	c, err := readDestinationCapacity(dest.ConfigJSON)
	if err != nil {
		return err
	}
	if c.Limit == 0 {
		return errors.New("decision targets require individual capacity configuration")
	}
	p, err := a.phonePrincipal(project, c.Identity)
	if err != nil || !p.Destinations[dest.ID] {
		return fmt.Errorf("destination %s identity must have enabled Telephony access", dest.ID)
	}
	return nil
}

func (a *App) destinationAllowsIdentity(project, dest string, identity phoneIdentity) bool {
	row, err := a.findRoutingDestination(project, dest)
	if err != nil || row == nil {
		return false
	}
	c, err := readDestinationCapacity(row.ConfigJSON)
	return err == nil && (c.Limit == 0 || c.Identity == identity)
}

// Recheck mutable permissions under the same lock that commits the reservation.
func validateDecisionTargetTx(tx *sql.Tx, project, dest string, identity phoneIdentity) error {
	var raw, kind string
	var enabled bool
	if e := tx.QueryRow(`SELECT config_json,kind,enabled FROM routing_destinations WHERE project_id=? AND id=?`, project, dest).Scan(&raw, &kind, &enabled); e != nil {
		return e
	}
	c, e := readDestinationCapacity(raw)
	if e != nil {
		return e
	}
	if !enabled || kind != "browser" || c.Limit == 0 || c.Identity != identity {
		return errors.New("destination unavailable")
	}
	if e = tx.QueryRow(`SELECT policy_json FROM telephony_access_policies WHERE project_id=?`, project).Scan(&raw); e != nil {
		return e
	}
	var policy phonePolicy
	if e = json.Unmarshal([]byte(raw), &policy); e != nil {
		return e
	}
	allowed := func(g phoneGrant) bool {
		for _, id := range g.Destinations {
			if id == dest {
				return true
			}
		}
		return false
	}
	for _, u := range policy.Users {
		if u.Identity != identity || !u.Enabled {
			continue
		}
		if allowed(u.phoneGrant) {
			return nil
		}
		for _, group := range policy.Groups {
			for _, id := range u.Groups {
				if id == group.ID && allowed(group.phoneGrant) {
					return nil
				}
			}
		}
	}
	return errors.New("destination access revoked")
}
