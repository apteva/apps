package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type directSIPProviderConfig struct {
	Provider               string `json:"provider"`
	PreviousVoiceURL       string `json:"previous_voice_url,omitempty"`
	PreviousStatusCallback string `json:"previous_status_callback,omitempty"`
	PreviousConnectionID   string `json:"previous_connection_id,omitempty"`
	TrunkID                string `json:"trunk_id,omitempty"`
	OriginationURLID       string `json:"origination_url_id,omitempty"`
	PhoneAssociationID     string `json:"phone_association_id,omitempty"`
	FQDNConnectionID       string `json:"fqdn_connection_id,omitempty"`
	FQDNID                 string `json:"fqdn_id,omitempty"`
	PreviousTrunkID        string `json:"previous_trunk_id,omitempty"`
	PreviousTrunkGroupID   string `json:"previous_trunk_group_id,omitempty"`
}

func (a *App) toolRoutesSetTransport(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	// A zero agent id is a platform principal (API key, panel session);
	// setRouteTransport then scopes ownership to the project only.
	agentID := callerAgentID(callerCtx)
	routeID := strings.TrimSpace(strArg(args, "route_id", ""))
	transport := strings.TrimSpace(strArg(args, "inbound_transport", ""))
	var route *routeRow
	var err error
	if boolArg(args, "configure", true) {
		route, err = a.applyRouteTransport(ctx, routeID, transport, agentID)
	} else {
		route, err = a.setRouteTransport(ctx, routeID, transport, agentID)
	}
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return map[string]any{
		"ok":                             true,
		"route":                          routePublic(a, *route),
		"carrier_configuration_required": !boolArg(args, "configure", true),
		"applies_to":                     "new calls",
		"active_calls_retained":          true,
	}, nil
}

func (a *App) setRouteTransport(ctx *sdk.AppCtx, routeID, transport string, agentID int64) (*routeRow, error) {
	if routeID == "" {
		return nil, errors.New("route_id required")
	}
	normalized, err := normalizeInboundTransport(transport)
	if err != nil {
		return nil, err
	}
	route, err := a.db().findRoute(routeID)
	if err != nil {
		return nil, fmt.Errorf("load route: %w", err)
	}
	if route == nil {
		return nil, errors.New("unknown route_id")
	}
	if route.ProjectID != currentProject(ctx) || (agentID != 0 && route.AgentID != agentID) {
		return nil, errors.New("route belongs to another agent or project")
	}
	if !supportsInboundTransport(route.CarrierSlug, normalized) {
		return nil, fmt.Errorf("%s inbound routing is not implemented for provider %s", normalized, route.CarrierSlug)
	}
	if normalized == inboundTransportSIPDirect {
		candidate := *route
		candidate.InboundTransport = normalized
		if err := a.validatePublishedFlowForInboundRoute(&candidate); err != nil {
			return nil, err
		}
		if err := a.ensureSIPGateway(ctx); err != nil {
			return nil, fmt.Errorf("prepare direct SIP: %w", err)
		}

	}
	active, err := a.db().countActiveCallsForRoute(route.ID)
	if err != nil {
		return nil, fmt.Errorf("check active calls: %w", err)
	}
	if active > 0 {
		return nil, errors.New("cannot change transport while this route has active calls")
	}
	if normalized == firstNonEmpty(route.InboundTransport, inboundTransportProgrammable) {
		return route, nil
	}
	if err := a.db().updateRouteTransport(route.ID, normalized, route.TransportConfig); err != nil {
		return nil, fmt.Errorf("persist route transport: %w", err)
	}
	route.InboundTransport = normalized
	return route, nil
}

func (a *App) callUsesDirectSIP(row *callRow) bool {
	if row == nil || row.Direction != "inbound" {
		return false
	}
	if gateway := a.directSIPGateway(); gateway != nil && gateway.sessionByCall(row.ID) != nil {
		return true
	}
	if row.IngressPath == inboundTransportSIPDirect {
		return true
	}
	if row.IngressPath != "" && row.IngressPath != "direct_or_unreported" {
		return false
	}
	if row.RouteID == "" {
		return false
	}
	route, err := a.db().findRoute(row.RouteID)
	return err == nil && route != nil && route.InboundTransport == inboundTransportSIPDirect
}

func (a *App) configureDirectSIPCarrierRoute(ctx *sdk.AppCtx, route *routeRow) error {
	gateway := a.directSIPGateway()
	if gateway == nil {
		return errors.New("direct SIP gateway is not running")
	}
	if route.TransportConfig != "" {
		var existing directSIPProviderConfig
		if json.Unmarshal([]byte(route.TransportConfig), &existing) == nil && existing.Provider == route.CarrierSlug {
			return nil
		}
		return errors.New("route has an unreadable or mismatched direct SIP carrier configuration")
	}
	switch route.CarrierSlug {
	case "twilio":
		return a.configureTwilioDirectSIP(ctx, route, gateway.cfg)
	case "telnyx":
		return a.configureTelnyxDirectSIP(ctx, route, gateway.cfg)
	case "didww":
		return a.configureDIDWWDirectSIP(ctx, route, gateway.cfg)
	default:
		return fmt.Errorf("direct SIP carrier configuration is not implemented for provider %s", route.CarrierSlug)
	}
}

func (a *App) configureTwilioDirectSIP(ctx *sdk.AppCtx, route *routeRow, cfg sipGatewayConfig) error {
	secure := cfg.Transport == "tls" && cfg.SRTPMode != sipSRTPDisabled
	if cfg.SRTPMode == sipSRTPRequired && !secure {
		return errors.New("Twilio direct SIP with required SRTP also requires TLS signaling")
	}
	phoneID, previousVoiceURL, previousStatusCallback, err := a.findTwilioPhoneNumber(ctx, route)
	if err != nil {
		return fmt.Errorf("find Twilio phone number: %w", err)
	}
	if phoneID == "" {
		return fmt.Errorf("could not find Twilio phone number %s", route.PhoneNumber)
	}
	raw, err := executeCarrierTool(ctx, route.CarrierConnectionID, "create_elastic_sip_trunk", map[string]any{
		"FriendlyName": "Apteva " + route.PhoneNumber,
		"Secure":       secure,
	})
	if err != nil {
		return fmt.Errorf("create Twilio SIP trunk: %w", err)
	}
	trunkID := providerResourceID(raw)
	if trunkID == "" {
		return errors.New("Twilio SIP trunk response contained no SID")
	}
	rollback := func() {
		_, _ = executeCarrierTool(ctx, route.CarrierConnectionID, "delete_elastic_sip_trunk", map[string]any{"TrunkSid": trunkID})
	}
	raw, err = executeCarrierTool(ctx, route.CarrierConnectionID, "create_sip_trunk_origination_url", map[string]any{
		"TrunkSid": trunkID, "SipUrl": cfg.endpointURI(), "FriendlyName": "Apteva primary",
		"Priority": 10, "Weight": 10, "Enabled": true,
	})
	if err != nil {
		rollback()
		return fmt.Errorf("create Twilio SIP origination URL: %w", err)
	}
	originationID := providerResourceID(raw)
	if originationID == "" {
		rollback()
		return errors.New("Twilio SIP origination response contained no SID")
	}
	raw, err = executeCarrierTool(ctx, route.CarrierConnectionID, "associate_sip_trunk_phone_number", map[string]any{
		"TrunkSid": trunkID, "PhoneNumberSid": phoneID,
	})
	if err != nil {
		rollback()
		return fmt.Errorf("associate Twilio number with SIP trunk: %w", err)
	}
	state := directSIPProviderConfig{
		Provider: route.CarrierSlug, PreviousVoiceURL: previousVoiceURL,
		PreviousStatusCallback: previousStatusCallback, TrunkID: trunkID,
		OriginationURLID: originationID, PhoneAssociationID: providerResourceID(raw),
	}
	encoded, _ := json.Marshal(state)
	if err := a.db().updateRoutePhoneSID(route.ID, phoneID); err != nil {
		rollback()
		return err
	}
	if err := a.db().updateRouteTransport(route.ID, inboundTransportSIPDirect, string(encoded)); err != nil {
		rollback()
		return err
	}
	route.PhoneNumberSID = phoneID
	route.TransportConfig = string(encoded)
	return nil
}

// Telnyx provisioning uses G711U/G711A; SIP SDP and our shared settings use
// PCMU/PCMA. Translate only at this provider boundary, preserving preference.
func telnyxProvisioningCodecs(codecs []string) ([]string, error) {
	if err := validateLocalMediaSettings(localMediaSettings{Codecs: codecs}); err != nil {
		return nil, err
	}
	if len(codecs) == 0 {
		codecs = []string{"G722", "PCMU", "PCMA"}
	}
	result := make([]string, len(codecs))
	for i, codec := range codecs {
		switch codec {
		case "PCMU":
			result[i] = "G711U"
		case "PCMA":
			result[i] = "G711A"
		default:
			result[i] = codec
		}
	}
	return result, nil
}

func (a *App) configureTelnyxDirectSIP(ctx *sdk.AppCtx, route *routeRow, cfg sipGatewayConfig) error {
	phoneID, previousConnectionID, err := a.findTelnyxPhoneNumber(ctx, route)
	if err != nil {
		return fmt.Errorf("find Telnyx phone number: %w", err)
	}
	if phoneID == "" {
		return fmt.Errorf("could not find Telnyx phone number %s", route.PhoneNumber)
	}
	if previousConnectionID == "" {
		return errors.New("Telnyx number has no previous connection to restore; assign it to the bound connection before enabling direct SIP")
	}
	settings, err := a.localMediaSettings(route.ProjectID, route.ID)
	if err != nil {
		return err
	}
	codecs, err := telnyxProvisioningCodecs(settings.Codecs)
	if err != nil {
		return fmt.Errorf("Telnyx SIP codecs: %w", err)
	}
	connectionInput := map[string]any{
		"connection_name":    "Apteva " + route.PhoneNumber,
		"active":             true,
		"transport_protocol": strings.ToUpper(cfg.Transport),
		"dtmf_type":          "RFC 2833",
		"inbound": map[string]any{
			"ani_number_format": "+E.164", "dnis_number_format": "+e164",
			"codecs": codecs, "generate_ringback_tone": true,
		},
		"noise_suppression": "disabled",
		"jitter_buffer":     map[string]any{"enable_jitter_buffer": false},
	}
	// The FQDN API forbids encrypted_media with TLS. TLS carrier policy must
	// offer SRTP; required mode still rejects plaintext SDP, never downgrades.
	if cfg.SRTPMode != sipSRTPDisabled && cfg.Transport != "tls" {
		connectionInput["encrypted_media"] = "SRTP"
	}
	raw, err := executeCarrierTool(ctx, route.CarrierConnectionID, "create_fqdn_connection", connectionInput)
	if err != nil {
		return fmt.Errorf("create Telnyx FQDN connection: %w", err)
	}
	connectionID := providerResourceID(raw)
	if connectionID == "" {
		return errors.New("Telnyx FQDN connection response contained no ID")
	}
	rollbackConnection := func() {
		_, _ = executeCarrierTool(ctx, route.CarrierConnectionID, "delete_fqdn_connection", map[string]any{"id": connectionID})
	}
	_, portValue, _ := net.SplitHostPort(cfg.ListenAddress)
	port, _ := strconv.Atoi(portValue)
	raw, err = executeCarrierTool(ctx, route.CarrierConnectionID, "create_fqdn", map[string]any{
		"connection_id": connectionID, "fqdn": cfg.PublicHost, "dns_record_type": "a", "port": port,
	})
	if err != nil {
		rollbackConnection()
		return fmt.Errorf("attach Telnyx FQDN: %w", err)
	}
	fqdnID := providerResourceID(raw)
	if fqdnID == "" {
		rollbackConnection()
		return errors.New("Telnyx FQDN response contained no ID")
	}
	rollback := func() {
		_, _ = executeCarrierTool(ctx, route.CarrierConnectionID, "update_phone_number", map[string]any{
			"id": phoneID, "connection_id": previousConnectionID,
		})
		_, _ = executeCarrierTool(ctx, route.CarrierConnectionID, "delete_fqdn", map[string]any{"id": fqdnID})
		rollbackConnection()
	}
	if _, err := executeCarrierTool(ctx, route.CarrierConnectionID, "update_phone_number", map[string]any{
		"id": phoneID, "connection_id": connectionID,
	}); err != nil {
		if fqdnID != "" {
			_, _ = executeCarrierTool(ctx, route.CarrierConnectionID, "delete_fqdn", map[string]any{"id": fqdnID})
		}
		rollbackConnection()
		return fmt.Errorf("assign Telnyx number to direct SIP connection: %w", err)
	}
	state := directSIPProviderConfig{
		Provider: route.CarrierSlug, PreviousConnectionID: previousConnectionID,
		FQDNConnectionID: connectionID, FQDNID: fqdnID,
	}
	encoded, _ := json.Marshal(state)
	if err := a.db().updateRoutePhoneSID(route.ID, phoneID); err != nil {
		rollback()
		return err
	}
	if err := a.db().updateRouteTransport(route.ID, inboundTransportSIPDirect, string(encoded)); err != nil {
		rollback()
		return err
	}
	route.PhoneNumberSID = phoneID
	route.TransportConfig = string(encoded)
	return nil
}

func (a *App) deconfigureDirectSIPCarrierRoute(ctx *sdk.AppCtx, route *routeRow) error {
	if route.TransportConfig == "" {
		return nil
	}
	var state directSIPProviderConfig
	if err := json.Unmarshal([]byte(route.TransportConfig), &state); err != nil || state.Provider != route.CarrierSlug {
		return errors.New("route contains invalid direct SIP carrier configuration")
	}
	active, err := a.db().countActiveCallsForRoute(route.ID)
	if err != nil {
		return err
	}
	if gateway := a.directSIPGateway(); gateway != nil && gateway.hasRouteDialogs(route.ID) {
		active = max(active, 1)
	}
	if active > 0 {
		if err := a.retireSIPResources(ctx, route, state); err != nil {
			return err
		}
	}
	// Restore number assignment now, retain dialog resources until drain.
	switch state.Provider {
	case "twilio":
		if state.TrunkID == "" {
			return errors.New("saved Twilio SIP trunk ID is missing")
		}
		if active > 0 {
			if err := detachTwilioSIPNumber(ctx, route.CarrierConnectionID, state.TrunkID, route.PhoneNumberSID); err != nil {
				return err
			}
		}
	case "telnyx":
		if route.PhoneNumberSID == "" || state.PreviousConnectionID == "" {
			return errors.New("saved Telnyx routing state incomplete")
		}
		if _, err := executeCarrierTool(ctx, route.CarrierConnectionID, "update_phone_number", map[string]any{"id": route.PhoneNumberSID, "connection_id": state.PreviousConnectionID}); err != nil {
			return err
		}
	case "didww":
		if err := restoreDIDWWDIDTrunk(ctx, route.CarrierConnectionID, route.PhoneNumberSID, state); err != nil {
			return err
		}
	default:
		return errors.New("unsupported SIP provider")
	}
	if active == 0 {
		if err := cleanupSIPResources(ctx, route.CarrierConnectionID, state); err != nil {
			return err
		}
	}

	if err := a.db().updateRouteTransport(route.ID, route.InboundTransport, ""); err != nil {
		return err
	}
	route.TransportConfig = ""
	return nil
}

func providerResourceID(raw json.RawMessage) string {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return ""
	}
	for _, value := range []any{root["sid"], root["id"]} {
		if id := stringValue(value); id != "" {
			return id
		}
	}
	if data, ok := root["data"].(map[string]any); ok {
		return firstNonEmpty(stringValue(data["id"]), stringValue(data["sid"]))
	}
	return ""
}

func (c *callsDB) countActiveCallsForRoute(routeID string) (int, error) {
	var count int
	err := c.db.QueryRow(`SELECT COUNT(*) FROM calls WHERE route_id = ?
		AND status NOT IN ('completed','failed','no-answer','busy','canceled')`, routeID).Scan(&count)
	return count, err
}
