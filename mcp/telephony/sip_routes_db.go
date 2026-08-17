package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func normalizeInboundTransport(value string) (string, error) {
	switch value {
	case "", inboundTransportProgrammable:
		return inboundTransportProgrammable, nil
	case inboundTransportSIPDirect:
		return inboundTransportSIPDirect, nil
	default:
		return "", fmt.Errorf("inbound_transport must be %s or %s", inboundTransportProgrammable, inboundTransportSIPDirect)
	}
}

func (c *callsDB) updateRouteTransport(id, transport, config string) error {
	normalized, err := normalizeInboundTransport(transport)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(`UPDATE inbound_routes
		SET inbound_transport = ?, transport_config = ?, updated_at = ?
		WHERE id = ?`, normalized, config, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (c *callsDB) findDirectSIPRouteByNumber(phone string) (*routeRow, error) {
	rows, err := c.db.Query(`SELECT id FROM inbound_routes
		WHERE inbound_transport = ? AND enabled = 1 AND phone_number = ?
		ORDER BY updated_at DESC LIMIT 2`, inboundTransportSIPDirect, phone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch len(ids) {
	case 0:
		return nil, nil
	case 1:
		return c.findRoute(ids[0])
	default:
		return nil, fmt.Errorf("multiple enabled direct SIP routes match %s", phone)
	}
}

func (c *callsDB) directSIPRouteCount() (int, error) {
	var count int
	err := c.db.QueryRow(`SELECT COUNT(*) FROM inbound_routes
		WHERE inbound_transport = ? AND enabled = 1`, inboundTransportSIPDirect).Scan(&count)
	return count, err
}

func (c *callsDB) findInboundCallByProviderID(providerID string) (*callRow, error) {
	row, err := scanCall(c.db.QueryRow(`SELECT `+callSelectColumns+` FROM calls
		WHERE direction = 'inbound' AND carrier_sid = ?
		ORDER BY placed_at DESC LIMIT 1`, providerID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}
