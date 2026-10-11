package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// A transport change is serialized per route, never on a media/answer lock.
// Provider resource retirement is durable and waits for existing calls to end.
func (a *App) applyRouteTransport(ctx *sdk.AppCtx, id, transport string, agentID int64) (*routeRow, error) {
	unlock := lockRoutingCall("transport:" + id)
	defer unlock()
	before, err := a.routeForCaller(ctx, id, agentID)
	if err != nil {
		return nil, err
	}
	desired, err := normalizeInboundTransport(transport)
	if err != nil {
		return nil, err
	}
	if !supportsInboundTransport(before.CarrierSlug, desired) {
		return nil, errors.New("provider does not support selected inbound transport")
	}
	next := *before
	next.InboundTransport = desired
	if err = a.validatePublishedFlowForInboundRoute(&next); err != nil {
		return nil, err
	}
	if desired == inboundTransportSIPDirect {
		if err = a.ensureSIPGateway(ctx); err != nil {
			return nil, err
		}
	}
	if desired == before.InboundTransport && (desired != inboundTransportSIPDirect || before.TransportConfig != "") {
		return before, nil
	}
	a.inventoryReads.invalidate()
	defer a.inventoryReads.invalidate()
	if desired == inboundTransportProgrammable && before.InboundTransport == inboundTransportSIPDirect && before.TransportConfig != "" {
		err = a.switchSIPToProgrammable(ctx, before, &next)
	} else {
		err = a.configureRouteCarrier(ctx, &next)
	}
	if err != nil {
		// Restore the committed view; retain provider resources if the external
		// operation was uncertain. Never falsely publish a successful switch.
		_ = a.db().updateRouteTransport(before.ID, before.InboundTransport, before.TransportConfig)
		return nil, fmt.Errorf("carrier transport change failed: %w", err)
	}
	if err = a.db().updateRouteTransport(next.ID, desired, next.TransportConfig); err != nil {
		rollback := a.restoreRouteTransport(ctx, before, &next)
		if rollback != nil {
			return nil, fmt.Errorf("transport save failed; carrier rollback requires reconciliation: %w", errors.Join(err, rollback))
		}
		return nil, fmt.Errorf("transport save failed; previous carrier assignment restored: %w", err)
	}
	return a.db().findRoute(next.ID)
}
func (a *App) switchSIPToProgrammable(ctx *sdk.AppCtx, before, next *routeRow) error {
	if err := a.validatePublicEndpoint(); err != nil {
		return err
	}
	var state directSIPProviderConfig
	if json.Unmarshal([]byte(before.TransportConfig), &state) != nil || state.Provider != before.CarrierSlug {
		return errors.New("invalid saved SIP configuration")
	}
	// Persist retirement before any external mutation. Cleanup cannot run while
	// the committed route still references this resource.
	if err := a.retireSIPResources(ctx, before, state); err != nil {
		return err
	}
	switch before.CarrierSlug {
	case "twilio":
		if err := a.configureTwilioRoute(ctx, next); err != nil {
			return err
		}
		if err := detachTwilioSIPNumber(ctx, before.CarrierConnectionID, state.TrunkID, before.PhoneNumberSID); err != nil {
			return err
		}
	case "telnyx":
		if state.PreviousConnectionID == "" {
			return errors.New("previous Telnyx connection missing")
		}
		if _, err := executeCarrierTool(ctx, before.CarrierConnectionID, "update_phone_number", map[string]any{"id": before.PhoneNumberSID, "connection_id": state.PreviousConnectionID}); err != nil {
			return err
		}
		if err := a.configureTelnyxRoute(ctx, next); err != nil {
			_, rollbackErr := executeCarrierTool(ctx, before.CarrierConnectionID, "update_phone_number", map[string]any{"id": before.PhoneNumberSID, "connection_id": state.FQDNConnectionID})
			if rollbackErr != nil {
				return fmt.Errorf("configuration failed; carrier rollback requires reconciliation: %w", err)
			}
			return err
		}
	default:
		return errors.New("provider has no programmable inbound transport")
	}
	next.TransportConfig = ""
	return nil
}
func (a *App) retireSIPResources(ctx *sdk.AppCtx, route *routeRow, state directSIPProviderConfig) error {
	raw, _ := json.Marshal(state)
	id := fmt.Sprintf("%s:%s:%s:%s", route.ID, state.TrunkID, state.FQDNConnectionID, state.FQDNID)
	_, err := a.db().db.Exec(`INSERT OR IGNORE INTO retired_sip_resources(id,route_id,project_id,connection_id,provider,state_json,updated_at) VALUES(?,?,?,?,?,?,?)`, id, route.ID, route.ProjectID, route.CarrierConnectionID, state.Provider, string(raw), ringTime(time.Now()))
	return err
}
func cleanupSIPResources(ctx *sdk.AppCtx, connection int64, state directSIPProviderConfig, requests ...context.Context) error {
	parent := context.Background()
	if len(requests) > 0 && requests[0] != nil {
		parent = requests[0]
	}
	request, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	call := func(tool string, input map[string]any) error {
		result, err := sdk.ExecuteIntegrationToolContext(request, ctx.PlatformAPI(), connection, tool, input)
		if err != nil {
			return err
		}
		if result == nil {
			return errors.New("provider cleanup returned no response")
		}
		if result.Success || result.Status == 404 {
			return nil
		}
		return errors.New("provider cleanup failed")
	}
	switch state.Provider {
	case "twilio":
		if state.TrunkID != "" {
			return call("delete_elastic_sip_trunk", map[string]any{"TrunkSid": state.TrunkID})
		}
	case "telnyx":
		if state.FQDNID != "" {
			if err := call("delete_fqdn", map[string]any{"id": state.FQDNID}); err != nil {
				return err
			}
		}
		if state.FQDNConnectionID != "" {
			return call("delete_fqdn_connection", map[string]any{"id": state.FQDNConnectionID})
		}
	case "didww":
		if state.TrunkID != "" {
			return call("delete_inbound_trunk", map[string]any{"id": state.TrunkID})
		}
	}
	return nil
}
func (a *App) runRetiredSIPResources(worker context.Context, ctx *sdk.AppCtx) error {
	rows, err := a.db().db.Query(`SELECT id,route_id,connection_id,state_json FROM retired_sip_resources WHERE project_id=? AND NOT EXISTS(SELECT 1 FROM calls c WHERE c.route_id=retired_sip_resources.route_id AND c.ingress_path='sip_direct' AND c.status NOT IN ('completed','failed','no-answer','busy','canceled')) LIMIT 20`, ctx.CurrentProject())
	if err != nil {
		return err
	}
	type resource struct {
		id, route, raw string
		connection     int64
	}
	var items []resource
	for rows.Next() {
		var r resource
		if err = rows.Scan(&r.id, &r.route, &r.connection, &r.raw); err != nil {
			rows.Close()
			return err
		}
		items = append(items, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range items {
		if worker.Err() != nil {
			return worker.Err()
		}
		unlock := lockRoutingCall("transport:" + r.route)
		func() {
			defer unlock()
			// A re-enabled route must not be using the retired resource being deleted.
			if gateway := a.directSIPGateway(); gateway != nil && gateway.hasRouteDialogs(r.route) {
				return
			}
			var live int
			if err = a.db().db.QueryRow(`SELECT COUNT(*) FROM calls WHERE route_id=? AND ingress_path='sip_direct' AND status NOT IN ('completed','failed','no-answer','busy','canceled')`, r.route).Scan(&live); err != nil || live > 0 {
				return
			}
			var current string
			err = a.db().db.QueryRow(`SELECT transport_config FROM inbound_routes WHERE id=?`, r.route).Scan(&current)
			if err != nil {
				return
			}

			var state directSIPProviderConfig
			if json.Unmarshal([]byte(r.raw), &state) != nil {
				return
			}
			var active directSIPProviderConfig
			if current != "" && (json.Unmarshal([]byte(current), &active) != nil || sipResourcesOverlap(active, state)) {
				return
			}
			if err = cleanupSIPResources(ctx, r.connection, state, worker); err != nil {
				_, _ = a.db().db.Exec(`UPDATE retired_sip_resources SET last_error='provider cleanup failed',updated_at=? WHERE id=?`, ringTime(time.Now()), r.id)
				return
			}
			_, err = a.db().db.Exec(`DELETE FROM retired_sip_resources WHERE id=?`, r.id)
			if err != nil {
				return
			}
		}()
	}
	return err
}

func (g *sipGateway) hasRouteDialogs(route string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.reserved) > 0 {
		return true
	}
	for _, s := range g.byCall {
		if s.route != nil && s.route.ID == route {
			return true
		}
	}
	return false
}

func sipResourcesOverlap(a, b directSIPProviderConfig) bool {
	if a.Provider != b.Provider {
		return false
	}
	return a.TrunkID != "" && a.TrunkID == b.TrunkID || a.FQDNConnectionID != "" && a.FQDNConnectionID == b.FQDNConnectionID || a.FQDNID != "" && a.FQDNID == b.FQDNID
}

func (a *App) restoreRouteTransport(ctx *sdk.AppCtx, before, next *routeRow) error {
	if before.InboundTransport == inboundTransportSIPDirect {
		var state directSIPProviderConfig
		if json.Unmarshal([]byte(before.TransportConfig), &state) != nil {
			return errors.New("invalid saved SIP assignment")
		}
		switch before.CarrierSlug {
		case "telnyx":
			_, err := executeCarrierTool(ctx, before.CarrierConnectionID, "update_phone_number", map[string]any{"id": before.PhoneNumberSID, "connection_id": state.FQDNConnectionID})
			return err
		case "twilio":
			_, err := executeCarrierTool(ctx, before.CarrierConnectionID, "associate_sip_trunk_phone_number", map[string]any{"TrunkSid": state.TrunkID, "PhoneNumberSid": before.PhoneNumberSID})
			return err
		}
		return errors.New("SIP assignment rollback unavailable")
	}
	// Initial SIP setup saves internally and rolls back its own save failures.
	// If the final save failed after setup, restore programmable assignment and
	// retain resources for any dialogs admitted during the transition.
	restored := *before
	restored.TransportConfig = next.TransportConfig
	return a.configureRouteCarrier(ctx, &restored)
}
