package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	webhookConfigured    = "configured"
	webhookMissing       = "missing"
	webhookMismatch      = "mismatch"
	webhookNotConfigured = "not_configured"
	webhookDisabled      = "disabled"
	webhookUnsupported   = "unsupported"
	webhookUnknown       = "unknown"
)

type ownedNumber struct {
	ProviderNumberID string
	PhoneNumber      string
	FriendlyName     string
	Capabilities     []string
	CarrierStatus    string
	VoiceURL         string
	VoiceMethod      string
	StatusCallback   string
	StatusMethod     string
	ApplicationID    string
	ConnectionID     string
}

type connectedRouteView struct {
	ID            string `json:"id"`
	AgentID       int64  `json:"agent_id"`
	AgentName     string `json:"agent_name,omitempty"`
	Enabled       bool   `json:"enabled"`
	AnswerMode    string `json:"answer_mode"`
	Voice         string `json:"voice,omitempty"`
	RecordingMode string `json:"recording_mode"`
}

type connectedNumberView struct {
	PhoneNumber         string              `json:"phone_number"`
	Provider            string              `json:"provider"`
	ProviderNumberID    string              `json:"provider_number_id,omitempty"`
	FriendlyName        string              `json:"friendly_name,omitempty"`
	Capabilities        []string            `json:"capabilities"`
	CarrierStatus       string              `json:"carrier_status,omitempty"`
	RouteStatus         string              `json:"route_status"`
	Route               *connectedRouteView `json:"route,omitempty"`
	VoiceWebhookStatus  string              `json:"voice_webhook_status"`
	StatusCallbackState string              `json:"status_callback_status"`
	RoutingHealth       string              `json:"routing_health"`
	HealthMessage       string              `json:"health_message,omitempty"`
}

func (a *App) connectedNumbers(ctx *sdk.AppCtx) (map[string]any, error) {
	projectID := currentProject(ctx)
	if projectID == "" {
		return nil, errors.New("project context required for connected numbers")
	}
	provider, err := a.numberProviderFor(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := listOwnedCarrierNumbers(ctx, provider)
	if err != nil {
		return nil, err
	}
	owned, err := parseOwnedCarrierNumbers(provider.Slug, raw)
	if err != nil {
		return nil, fmt.Errorf("decode %s owned phone numbers: %w", provider.Slug, err)
	}
	routes, err := a.db().listRoutesForProjectConnection(projectID, provider.ConnID)
	if err != nil {
		return nil, fmt.Errorf("list project routes: %w", err)
	}
	routes = currentRoutesByNumber(routes)

	routesByID := make(map[string]*routeRow, len(routes))
	routesByPhone := make(map[string]*routeRow, len(routes))
	for i := range routes {
		route := &routes[i]
		if id := strings.TrimSpace(route.PhoneNumberSID); id != "" {
			routesByID[id] = route
		}
		if phone := compactPhoneNumber(route.PhoneNumber); phone != "" {
			routesByPhone[phone] = route
		}
	}

	agentNames := make(map[int64]string)
	resolveAgentName := func(agentID int64) string {
		if name, ok := agentNames[agentID]; ok {
			return name
		}
		agentNames[agentID] = ""
		if agentID <= 0 {
			return ""
		}
		agent, getErr := ctx.GetAgent(agentID)
		if getErr == nil && agent != nil && (agent.ProjectID == "" || agent.ProjectID == projectID) {
			agentNames[agentID] = strings.TrimSpace(agent.Name)
		}
		return agentNames[agentID]
	}

	usedRoutes := make(map[string]bool, len(routes))
	numbers := make([]connectedNumberView, 0, len(owned)+len(routes))
	for _, number := range owned {
		route := routesByID[strings.TrimSpace(number.ProviderNumberID)]
		if route == nil {
			route = routesByPhone[compactPhoneNumber(number.PhoneNumber)]
		}
		view := a.connectedNumberView(ctx, provider.Slug, number, route, resolveAgentName)
		if route != nil {
			usedRoutes[route.ID] = true
		}
		numbers = append(numbers, view)
	}

	// A route should remain visible even if the carrier no longer returns the
	// number. That is an important operational warning, not an empty state.
	for i := range routes {
		route := &routes[i]
		if usedRoutes[route.ID] {
			continue
		}
		number := ownedNumber{
			ProviderNumberID: route.PhoneNumberSID,
			PhoneNumber:      route.PhoneNumber,
			Capabilities:     []string{},
			CarrierStatus:    "not_found",
		}
		numbers = append(numbers, a.connectedNumberView(ctx, provider.Slug, number, route, resolveAgentName))
	}

	sort.SliceStable(numbers, func(i, j int) bool {
		leftRouted := numbers[i].Route != nil && numbers[i].Route.Enabled
		rightRouted := numbers[j].Route != nil && numbers[j].Route.Enabled
		if leftRouted != rightRouted {
			return leftRouted
		}
		return numbers[i].PhoneNumber < numbers[j].PhoneNumber
	})
	return map[string]any{
		"provider": provider.Slug,
		"count":    len(numbers),
		"numbers":  numbers,
	}, nil
}

func listOwnedCarrierNumbers(ctx *sdk.AppCtx, provider *numberProvider) (json.RawMessage, error) {
	var tool string
	var input map[string]any
	switch provider.Slug {
	case "twilio", "signalwire":
		tool = "list_phone_numbers"
		input = map[string]any{"PageSize": 1000}
	case "telnyx":
		tool = "list_phone_numbers"
		input = map[string]any{"page[size]": 250}
	case "plivo":
		tool = "list_owned_phone_numbers"
		input = map[string]any{"limit": 20, "offset": 0}
	case "vonage":
		tool = "numbers_list"
		input = map[string]any{"size": 100}
	default:
		return nil, fmt.Errorf("owned phone-number listing is unsupported for provider %s", provider.Slug)
	}
	return executeCarrierTool(ctx, provider.ConnID, tool, input)
}

func parseOwnedCarrierNumbers(provider string, raw json.RawMessage) ([]ownedNumber, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	var values []any
	switch provider {
	case "twilio", "signalwire":
		values = anyList(root["incoming_phone_numbers"])
	case "telnyx":
		response, err := telnyxResponse(raw)
		if err != nil {
			return nil, err
		}
		values = telnyxDataList(response)
	case "plivo":
		values = anyList(root["objects"])
	case "vonage":
		values = anyList(root["numbers"])
	default:
		return nil, fmt.Errorf("unsupported provider %s", provider)
	}

	out := make([]ownedNumber, 0, len(values))
	for _, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		number := ownedNumber{Capabilities: []string{}}
		switch provider {
		case "twilio", "signalwire":
			number.ProviderNumberID = firstNonEmpty(stringValue(item["sid"]), stringValue(item["id"]))
			number.PhoneNumber = normalizedOwnedPhone(firstNonEmpty(stringValue(item["phone_number"]), stringValue(item["number"])))
			number.FriendlyName = stringValue(item["friendly_name"])
			number.Capabilities = normalizedCapabilities(item["capabilities"])
			number.CarrierStatus = stringValue(item["status"])
			number.VoiceURL = stringValue(item["voice_url"])
			number.VoiceMethod = stringValue(item["voice_method"])
			number.StatusCallback = stringValue(item["status_callback"])
			number.StatusMethod = stringValue(item["status_callback_method"])
		case "telnyx":
			number.ProviderNumberID = stringValue(item["id"])
			number.PhoneNumber = normalizedOwnedPhone(stringValue(item["phone_number"]))
			number.FriendlyName = firstNonEmpty(stringValue(item["customer_reference"]), stringValue(item["name"]))
			number.Capabilities = normalizedCapabilities(firstNonNil(item["features"], item["capabilities"]))
			number.CarrierStatus = stringValue(item["status"])
			number.ConnectionID = firstNonEmpty(stringValue(item["connection_id"]), nestedString(item["connection"], "id"))
		case "plivo":
			number.PhoneNumber = normalizedOwnedPhone(firstNonEmpty(stringValue(item["number"]), stringValue(item["phone_number"])))
			number.ProviderNumberID = firstNonEmpty(stringValue(item["id"]), compactPhoneNumber(number.PhoneNumber))
			number.FriendlyName = firstNonEmpty(stringValue(item["alias"]), stringValue(item["name"]))
			number.Capabilities = normalizedCapabilities(firstNonNil(item["services"], item["capabilities"]))
			if boolValue(item["voice_enabled"]) {
				number.Capabilities = appendUnique(number.Capabilities, "voice")
			}
			if boolValue(item["sms_enabled"]) {
				number.Capabilities = appendUnique(number.Capabilities, "sms")
			}
			number.CarrierStatus = stringValue(item["status"])
			number.ApplicationID = plivoApplicationID(firstNonNil(item["application"], item["app_id"]))
		case "vonage":
			number.PhoneNumber = normalizedOwnedPhone(firstNonEmpty(stringValue(item["msisdn"]), stringValue(item["phone_number"])))
			number.ProviderNumberID = firstNonEmpty(stringValue(item["id"]), compactPhoneNumber(number.PhoneNumber))
			number.FriendlyName = firstNonEmpty(stringValue(item["name"]), stringValue(item["label"]))
			number.Capabilities = normalizedCapabilities(item["features"])
			number.CarrierStatus = stringValue(item["status"])
			number.ApplicationID = stringValue(item["application_id"])
		}
		if number.PhoneNumber == "" {
			continue
		}
		sort.Strings(number.Capabilities)
		out = append(out, number)
	}
	return out, nil
}

func (a *App) connectedNumberView(ctx *sdk.AppCtx, provider string, number ownedNumber, route *routeRow, agentName func(int64) string) connectedNumberView {
	view := connectedNumberView{
		PhoneNumber:         number.PhoneNumber,
		Provider:            provider,
		ProviderNumberID:    number.ProviderNumberID,
		FriendlyName:        number.FriendlyName,
		Capabilities:        number.Capabilities,
		CarrierStatus:       number.CarrierStatus,
		RouteStatus:         "not_configured",
		VoiceWebhookStatus:  webhookNotConfigured,
		StatusCallbackState: webhookNotConfigured,
		RoutingHealth:       "not_configured",
	}
	if route == nil {
		return view
	}
	view.Route = &connectedRouteView{
		ID:            route.ID,
		AgentID:       route.AgentID,
		AgentName:     agentName(route.AgentID),
		Enabled:       route.Enabled,
		AnswerMode:    firstNonEmpty(route.AnswerMode, answerModeAgent),
		Voice:         route.AutoVoice,
		RecordingMode: firstNonEmpty(route.RecordingMode, recordingModeInherit),
	}
	if !route.Enabled {
		view.RouteStatus = "disabled"
		view.VoiceWebhookStatus = webhookDisabled
		view.StatusCallbackState = webhookDisabled
		view.RoutingHealth = "disabled"
		return view
	}
	view.RouteStatus = "enabled"
	view.VoiceWebhookStatus, view.StatusCallbackState, view.HealthMessage = a.routeWebhookHealth(ctx, provider, number, *route)
	switch {
	case view.VoiceWebhookStatus == webhookConfigured && view.StatusCallbackState == webhookConfigured:
		view.RoutingHealth = "healthy"
	case view.VoiceWebhookStatus == webhookUnknown || view.StatusCallbackState == webhookUnknown:
		view.RoutingHealth = "unverified"
	default:
		view.RoutingHealth = "degraded"
	}
	return view
}

func (a *App) routeWebhookHealth(ctx *sdk.AppCtx, provider string, number ownedNumber, route routeRow) (string, string, string) {
	switch provider {
	case "twilio":
		return webhookURLState(number.VoiceURL, a.inboundRouteURL(route), number.VoiceMethod),
			webhookURLState(number.StatusCallback, a.twilioRouteStatusURL(route), number.StatusMethod), ""
	case "telnyx":
		var config telnyxRouteConfig
		if json.Unmarshal([]byte(route.PreviousVoiceURL), &config) != nil || config.ApplicationID == "" {
			return webhookMissing, webhookMissing, "Telephony has no saved Telnyx application for this route."
		}
		if number.ConnectionID == "" {
			return webhookMissing, webhookUnknown, "The Telnyx number is not assigned to a voice application."
		}
		if number.ConnectionID != config.ApplicationID {
			return webhookMismatch, webhookMismatch, "The Telnyx number is assigned to a different voice application."
		}
		raw, err := executeCarrierTool(ctx, route.CarrierConnectionID, "get_call_control_application", map[string]any{"id": config.ApplicationID})
		if err != nil {
			return webhookUnknown, webhookUnknown, "Could not verify the Telnyx application."
		}
		response, err := telnyxResponse(raw)
		if err != nil {
			return webhookUnknown, webhookUnknown, "Could not decode the Telnyx application."
		}
		webhook := stringValue(telnyxDataMap(response)["webhook_event_url"])
		state := webhookURLState(webhook, a.inboundRouteURL(route), "")
		return state, state, ""
	case "plivo":
		var config plivoRouteConfig
		if json.Unmarshal([]byte(route.PreviousVoiceURL), &config) != nil || config.ApplicationID == "" {
			return webhookMissing, webhookMissing, "Telephony has no saved Plivo application for this route."
		}
		if number.ApplicationID == "" {
			return webhookMissing, webhookMissing, "The Plivo number is not assigned to a voice application."
		}
		if number.ApplicationID != config.ApplicationID {
			return webhookMismatch, webhookMismatch, "The Plivo number is assigned to a different voice application."
		}
		raw, err := executeCarrierTool(ctx, route.CarrierConnectionID, "get_application", map[string]any{"app_id": config.ApplicationID})
		if err != nil {
			return webhookUnknown, webhookUnknown, "Could not verify the Plivo application."
		}
		var application map[string]any
		if json.Unmarshal(raw, &application) != nil {
			return webhookUnknown, webhookUnknown, "Could not decode the Plivo application."
		}
		return webhookURLState(stringValue(application["answer_url"]), a.inboundRouteURL(route), stringValue(application["answer_method"])),
			webhookURLState(stringValue(application["hangup_url"]), a.plivoRouteStatusURL(route), stringValue(application["hangup_method"])), ""
	default:
		return webhookUnsupported, webhookUnsupported, "Inbound route verification is not supported for this provider."
	}
}

func webhookURLState(actual, expected, method string) string {
	actual = strings.TrimSpace(actual)
	if actual == "" {
		return webhookMissing
	}
	if actual != strings.TrimSpace(expected) {
		return webhookMismatch
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method != "" && method != "POST" {
		return webhookMismatch
	}
	return webhookConfigured
}

func currentRoutesByNumber(routes []routeRow) []routeRow {
	out := make([]routeRow, 0, len(routes))
	seen := make(map[string]bool)
	for _, route := range routes {
		key := compactPhoneNumber(route.PhoneNumber)
		if key == "" {
			key = strings.TrimSpace(route.PhoneNumberSID)
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, route)
	}
	return out
}

func normalizedOwnedPhone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "+") {
		return value
	}
	digits := compactPhoneNumber(value)
	if digits == "" {
		return value
	}
	return "+" + digits
}

func normalizedCapabilities(value any) []string {
	var out []string
	switch typed := value.(type) {
	case map[string]any:
		for name, enabled := range typed {
			if boolValue(enabled) {
				out = appendUnique(out, strings.ToLower(strings.TrimSpace(name)))
			}
		}
	case []any:
		for _, item := range typed {
			if object, ok := item.(map[string]any); ok {
				out = appendUnique(out, strings.ToLower(firstNonEmpty(stringValue(object["name"]), stringValue(object["type"]))))
				continue
			}
			out = appendUnique(out, strings.ToLower(strings.TrimSpace(stringValue(item))))
		}
	case []string:
		for _, item := range typed {
			out = appendUnique(out, strings.ToLower(strings.TrimSpace(item)))
		}
	case string:
		for _, item := range strings.Split(typed, ",") {
			out = appendUnique(out, strings.ToLower(strings.TrimSpace(item)))
		}
	}
	sort.Strings(out)
	return out
}

func appendUnique(values []string, value string) []string {
	if value == "" || containsString(values, value) {
		return values
	}
	return append(values, value)
}

func anyList(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func nestedString(value any, key string) string {
	object, _ := value.(map[string]any)
	return stringValue(object[key])
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	case float64:
		return typed != 0
	case int:
		return typed != 0
	default:
		return false
	}
}

func (c *callsDB) listRoutesForProjectConnection(project string, connectionID int64) ([]routeRow, error) {
	rows, err := c.db.Query(`SELECT id, project_id, carrier_slug, carrier_connection_id, phone_number,
	        phone_number_sid, agent_id, enabled, hold_prompt, timeout_sec,
	        COALESCE(answer_mode,'agent'), COALESCE(auto_directive,''), COALESCE(auto_voice,''),
	        COALESCE(auto_greeting,''), secret,
	        COALESCE(previous_voice_url,''), COALESCE(previous_status_callback,''), created_at, updated_at,
	        COALESCE(recording_mode,'inherit')
	        FROM inbound_routes
	        WHERE project_id = ? AND carrier_connection_id = ?
	        ORDER BY enabled DESC, updated_at DESC`, project, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []routeRow
	for rows.Next() {
		var route routeRow
		var enabled int
		if err := rows.Scan(&route.ID, &route.ProjectID, &route.CarrierSlug, &route.CarrierConnectionID, &route.PhoneNumber,
			&route.PhoneNumberSID, &route.AgentID, &enabled, &route.HoldPrompt, &route.TimeoutSec,
			&route.AnswerMode, &route.AutoDirective, &route.AutoVoice, &route.AutoGreeting, &route.Secret,
			&route.PreviousVoiceURL, &route.PreviousStatusCallback, &route.CreatedAt, &route.UpdatedAt, &route.RecordingMode); err != nil {
			return nil, err
		}
		route.Enabled = enabled != 0
		out = append(out, route)
	}
	return out, rows.Err()
}
