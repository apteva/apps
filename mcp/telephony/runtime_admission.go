package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var errOutboundDisabled = errors.New("outbound_disabled: outbound use is disabled")
var errInboundDisabled = errors.New("inbound_disabled: new inbound calls are disabled")

// These checks apply only at admission. Access grants, media leases, callbacks,
// and idempotent replay of existing calls remain independent.
func (a *App) checkOutboundAdmission(project, provider string, connection int64, number string) error {
	var count int
	err := a.db().db.QueryRow(`SELECT
 (SELECT COUNT(*) FROM outbound_admission_rules WHERE project_id=? AND
 ((scope='provider' AND value=?) OR (scope='connection' AND value=?) OR (scope='number' AND value=?))) +
 (SELECT COUNT(*) FROM carrier_binding_drains WHERE connection_id=?)`, project, strings.ToLower(provider), strconv.FormatInt(connection, 10), number, connection).Scan(&count)
	if err != nil {
		return fmt.Errorf("read outbound admission: %w", err)
	}
	if count > 0 {
		return errOutboundDisabled
	}
	return nil
}

func (a *App) checkInboundAdmission(route *routeRow) error {
	var enabled, draining int
	err := a.db().db.QueryRow(`SELECT enabled,(SELECT COUNT(*) FROM carrier_binding_drains WHERE connection_id=inbound_routes.carrier_connection_id) FROM inbound_routes WHERE id=?`, route.ID).Scan(&enabled, &draining)
	if err != nil {
		return err
	}
	if enabled == 0 || draining > 0 {
		return errInboundDisabled
	}
	return nil
}

func (a *App) outboundPolicy(ctx *sdk.AppCtx, body map[string]any) (map[string]any, error) {
	project := currentProject(ctx)
	if project == "" {
		return nil, errors.New("project context required")
	}
	if scope := strArg(body, "scope", ""); scope != "" {
		a.inventoryReads.invalidate()
		defer a.inventoryReads.invalidate()
		enabled, ok := body["enabled"].(bool)
		if !ok {
			return nil, errors.New("enabled must be a boolean")
		}
		value := strings.TrimSpace(strArg(body, "value", ""))
		switch scope {
		case "provider":
			value = strings.ToLower(value)
			found := false
			for _, b := range ctx.IntegrationsFor("carrier") {
				if b != nil && b.AppSlug == value {
					found = true
				}
			}
			if !found {
				return nil, errors.New("provider is not bound")
			}
		case "connection":
			id, e := strconv.ParseInt(value, 10, 64)
			if e != nil || id <= 0 {
				return nil, errors.New("invalid connection")
			}
			found := false
			for _, b := range ctx.IntegrationsFor("carrier") {
				if b != nil && b.ConnectionID == id {
					found = true
				}
			}
			if !found {
				return nil, errors.New("connection is not bound")
			}
			value = strconv.FormatInt(id, 10)
		case "number":
			if !validE164(value) {
				return nil, errors.New("number must be E.164")
			}
			// Ownership and access stay authoritative, including on re-enable.
			if _, _, _, err := a.selectCarrierBinding(ctx, project, value); err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("scope must be provider, connection, or number")
		}
		a.admissionMu.Lock()
		var err error
		if enabled {
			_, err = a.db().db.Exec(`DELETE FROM outbound_admission_rules WHERE project_id=? AND scope=? AND value=?`, project, scope, value)
		} else {
			_, err = a.db().db.Exec(`INSERT INTO outbound_admission_rules(project_id,scope,value,updated_at) VALUES(?,?,?,?) ON CONFLICT(project_id,scope,value) DO UPDATE SET updated_at=excluded.updated_at`, project, scope, value, time.Now().UTC().Format(time.RFC3339Nano))
		}
		a.admissionMu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	rows, err := a.db().db.Query(`SELECT scope,value,updated_at FROM outbound_admission_rules WHERE project_id=? ORDER BY scope,value`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []map[string]any{}
	for rows.Next() {
		var scope, value, updated string
		if err = rows.Scan(&scope, &value, &updated); err != nil {
			return nil, err
		}
		rules = append(rules, map[string]any{"scope": scope, "value": value, "enabled": false, "updated_at": updated})
	}
	return map[string]any{"ok": true, "rules": rules}, rows.Err()
}

// Runtime route toggles retain carrier resources and route snapshots. Restoring
// or deleting external resources is a separate deconfiguration operation.
func (a *App) setInboundRouteEnabled(route *routeRow, enabled bool) (map[string]any, error) {
	a.inventoryReads.invalidate()
	defer a.inventoryReads.invalidate()
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	if enabled {
		var draining int
		if err := a.db().db.QueryRow(`SELECT COUNT(*) FROM carrier_binding_drains WHERE connection_id=?`, route.CarrierConnectionID).Scan(&draining); err != nil {
			return nil, err
		}
		if draining > 0 {
			return nil, errors.New("carrier binding is draining or removed; rebind it before enabling inbound")
		}
	}
	_, err := a.db().db.Exec(`UPDATE inbound_routes SET enabled=?,updated_at=? WHERE id=?`, enabled, time.Now().UTC().Format(time.RFC3339Nano), route.ID)
	if err != nil {
		return nil, err
	}
	route.Enabled = enabled
	return map[string]any{"ok": true, "route_id": route.ID, "carrier": route.CarrierSlug, "enabled": enabled, "carrier_configuration_retained": true}, nil
}

func (a *App) handleSoftphoneNumbers(w http.ResponseWriter, r *http.Request, project string) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	result, err := a.connectedNumbers(globalCtx.WithProject(project), inventoryFreshContext(r.Context(), r.URL.Query().Get("fresh") == "true"))
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	principal := phoneUserFrom(r)
	filtered := []connectedNumberView{}
	for _, n := range result["numbers"].([]connectedNumberView) {
		if !n.OutboundEnabled || n.CarrierStatus == "not_found" || !validE164(n.PhoneNumber) {
			continue
		}
		if principal != nil && !principal.Numbers[n.PhoneNumber] {
			continue
		}
		if len(n.Capabilities) > 0 && !containsString(n.Capabilities, "voice") {
			continue
		}
		filtered = append(filtered, n)
	}
	result["numbers"] = filtered
	result["count"] = len(filtered)
	// Only authorized caller IDs are exposed; operational admin policy is not.
	delete(result, "provider_statuses")
	if result["inventory_status"] == "unavailable" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	writeJSON(w, result)
}

// An external ring-group leg belongs to a previously admitted inbound call;
// stopping new outbound calls must not interrupt that call's routing.
func (a *App) checkOutboundPlacement(row *callRow) error {
	if row.IngressPath == "ring_group" {
		var continuing int
		err := a.db().db.QueryRow(`SELECT COUNT(*) FROM call_legs l JOIN calls parent ON parent.id=l.call_id WHERE l.id=? AND parent.carrier_connection_id=? AND parent.project_id=? AND parent.to_number=? AND parent.status NOT IN ('completed','failed','no-answer','busy','canceled')`, row.ID, row.CarrierConnectionID, row.ProjectID, row.FromNumber).Scan(&continuing)
		if err != nil {
			return err
		}
		if continuing > 0 {
			return nil
		}
	}
	return a.checkOutboundAdmission(row.ProjectID, row.CarrierSlug, row.CarrierConnectionID, row.FromNumber)
}

func (a *App) outboundScopeEnabled(project, scope, value string) (bool, error) {
	var count int
	err := a.db().db.QueryRow(`SELECT COUNT(*) FROM outbound_admission_rules WHERE project_id=? AND scope=? AND value=?`, project, scope, value).Scan(&count)
	return count == 0, err
}

func (a *App) toolRoutesSetEnabled(caller context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	enabled, ok := args["enabled"].(bool)
	if !ok {
		return mcpError("enabled must be a boolean"), nil
	}
	route, err := a.routeForCaller(ctx, strArg(args, "route_id", ""), callerAgentID(caller))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	result, err := a.setInboundRouteEnabled(route, enabled)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return result, nil
}
func (a *App) toolOutboundPolicy(caller context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if callerAgentID(caller) != 0 {
		return mcpError("outbound admission settings require a platform principal"), nil
	}
	result, err := a.outboundPolicy(ctx, args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return result, nil
}
