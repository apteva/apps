package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type plivoRouteConfig struct {
	NumberID              string `json:"number_id"`
	PreviousApplicationID string `json:"previous_application_id"`
	ApplicationID         string `json:"application_id"`
}

func (a *App) findPlivoPhoneNumber(ctx *sdk.AppCtx, route *routeRow) (string, string, error) {
	raw, err := executeCarrierTool(ctx, route.CarrierConnectionID, "list_owned_phone_numbers", map[string]any{
		"number_startswith": compactPhoneNumber(route.PhoneNumber), "limit": 20,
	})
	if err != nil {
		return "", "", err
	}
	var response struct {
		Objects []map[string]any `json:"objects"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", "", err
	}
	wanted := compactPhoneNumber(route.PhoneNumber)
	for _, number := range response.Objects {
		value := compactPhoneNumber(firstNonEmpty(stringValue(number["number"]), stringValue(number["phone_number"])))
		if value != wanted {
			continue
		}
		applicationID := plivoApplicationID(firstNonNil(number["application"], number["app_id"]))
		return value, applicationID, nil
	}
	return "", "", nil
}

func (a *App) configurePlivoRoute(ctx *sdk.AppCtx, route *routeRow) error {
	numberID, previousApplicationID, err := a.findPlivoPhoneNumber(ctx, route)
	if err != nil {
		return fmt.Errorf("find Plivo phone number: %w", err)
	}
	if numberID == "" {
		return fmt.Errorf("could not find an exact Plivo phone number match for %s", route.PhoneNumber)
	}
	if route.PhoneNumberSID != "" && compactPhoneNumber(route.PhoneNumberSID) != numberID {
		return errors.New("provided phone_number_id does not belong to " + route.PhoneNumber)
	}
	if route.PreviousVoiceURL != "" {
		var existing plivoRouteConfig
		if json.Unmarshal([]byte(route.PreviousVoiceURL), &existing) == nil && existing.NumberID == numberID && validProviderResourceID(existing.ApplicationID) {
			return nil
		}
		return errors.New("Plivo route has saved carrier configuration that does not match the number; disable or repair it before reconfiguring")
	}
	createdRaw, err := executeCarrierTool(ctx, route.CarrierConnectionID, "create_application", map[string]any{
		"app_name":      "Apteva-" + route.ID,
		"answer_url":    a.inboundRouteURL(*route),
		"answer_method": "POST",
		"hangup_url":    a.plivoRouteStatusURL(*route),
		"hangup_method": "POST",
	})
	if err != nil {
		return fmt.Errorf("create Plivo Voice Application: %w", err)
	}
	var created map[string]any
	if err := json.Unmarshal(createdRaw, &created); err != nil {
		return fmt.Errorf("decode Plivo Voice Application: %w", err)
	}
	applicationID := firstNonEmpty(stringValue(created["app_id"]), plivoApplicationID(created["application"]))
	if !validProviderResourceID(applicationID) {
		return errors.New("Plivo created the Voice Application but returned no usable app_id")
	}
	cleanupApplication := func() {
		_, _ = executeCarrierTool(ctx, route.CarrierConnectionID, "delete_application", map[string]any{"app_id": applicationID})
	}
	if _, err := executeCarrierTool(ctx, route.CarrierConnectionID, "update_owned_phone_number", map[string]any{
		"number": numberID, "app_id": applicationID,
	}); err != nil {
		cleanupApplication()
		return fmt.Errorf("assign Plivo phone number to Voice Application: %w", err)
	}
	config := plivoRouteConfig{NumberID: numberID, PreviousApplicationID: previousApplicationID, ApplicationID: applicationID}
	configJSON, err := json.Marshal(config)
	if err != nil {
		return err
	}
	rollback := func() {
		_, _ = executeCarrierTool(ctx, route.CarrierConnectionID, "update_owned_phone_number", map[string]any{
			"number": numberID, "app_id": previousApplicationID,
		})
		cleanupApplication()
	}
	if err := a.db().updateRoutePhoneSID(route.ID, numberID); err != nil {
		rollback()
		return fmt.Errorf("persist Plivo phone number: %w", err)
	}
	if err := a.db().updateRoutePreviousVoiceURL(route.ID, string(configJSON)); err != nil {
		rollback()
		_ = a.db().updateRoutePhoneSID(route.ID, "")
		return fmt.Errorf("persist prior Plivo routing configuration: %w", err)
	}
	route.PhoneNumberSID = numberID
	route.PreviousVoiceURL = string(configJSON)
	return nil
}

func (a *App) disablePlivoRoute(ctx *sdk.AppCtx, route *routeRow) error {
	var config plivoRouteConfig
	if route.PreviousVoiceURL == "" || json.Unmarshal([]byte(route.PreviousVoiceURL), &config) != nil ||
		compactPhoneNumber(config.NumberID) == "" || !validProviderResourceID(config.ApplicationID) {
		return errors.New("route cannot be safely disabled because its Plivo routing configuration is unavailable")
	}
	if _, err := executeCarrierTool(ctx, route.CarrierConnectionID, "update_owned_phone_number", map[string]any{
		"number": config.NumberID, "app_id": config.PreviousApplicationID,
	}); err != nil {
		return fmt.Errorf("restore Plivo phone number application: %w", err)
	}
	if _, err := executeCarrierTool(ctx, route.CarrierConnectionID, "delete_application", map[string]any{"app_id": config.ApplicationID}); err != nil {
		ctx.Logger().Warn("delete restored Plivo application", "application_id", config.ApplicationID, "err", err)
	}
	return nil
}

func (a *App) handlePlivoInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/inbound/plivo/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 {
		http.Error(w, "missing route_id", http.StatusBadRequest)
		return
	}
	route, err := a.authorizedPlivoRoute(r, parts[0])
	if err != nil {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "wait":
			a.handlePlivoInboundWait(w, r, route)
		case "status":
			a.handlePlivoInboundStatus(w, r, route)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
		return
	}
	callUUID := strings.TrimSpace(r.FormValue("CallUUID"))
	from := strings.TrimSpace(r.FormValue("From"))
	to := strings.TrimSpace(firstNonEmpty(r.FormValue("To"), route.PhoneNumber))
	if callUUID == "" {
		http.Error(w, "missing CallUUID", http.StatusBadRequest)
		return
	}
	if direction := strings.ToLower(strings.TrimSpace(r.FormValue("Direction"))); direction != "" && direction != "inbound" {
		http.Error(w, "call direction does not match route", http.StatusForbidden)
		return
	}
	if compactPhoneNumber(to) != compactPhoneNumber(route.PhoneNumber) {
		http.Error(w, "called number does not match route", http.StatusForbidden)
		return
	}
	stored, _, err := a.recordInboundCall(route, callUUID, from, route.PhoneNumber)
	if err != nil {
		http.Error(w, "persist call: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if route.AnswerMode == answerModeRealtimeImmediate {
		if globalCtx == nil {
			_ = a.db().updateStatus(stored.ID, "failed", "app context unavailable for immediate answer")
			writePlivoHangup(w)
			return
		}
		ctx := globalCtx.WithProject(route.ProjectID)
		if _, err := a.prepareInboundRealtime(ctx, stored, route.AutoDirective, route.AutoVoice, route.AutoGreeting); err != nil {
			_ = a.db().updateStatus(stored.ID, "failed", "prepare immediate answer: "+err.Error())
			writePlivoHangup(w)
			return
		}
		_ = a.db().updateStatus(stored.ID, "answered", "")
		stored, _ = a.db().findCall(stored.ID)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(a.plivoStreamXML(stored, xmlEscape(a.publicWSStreamURL("plivo", stored.ID, stored.CallbackSecret)))))
		return
	}
	writePlivoWait(w, a.plivoWaitURL(*route, stored.ID))
}

func (a *App) authorizedPlivoRoute(r *http.Request, routeID string) (*routeRow, error) {
	route, err := a.db().findRoute(routeID)
	if err != nil || route == nil || route.CarrierSlug != "plivo" || !route.Enabled || route.Secret == "" ||
		!secureEqual(r.URL.Query().Get("secret"), route.Secret) || r.URL.Query().Get("project_id") != route.ProjectID {
		return nil, errors.New("route unavailable")
	}
	if err := a.verifyPlivoRequest(r, route.CarrierConnectionID); err != nil {
		return nil, err
	}
	return route, nil
}

func (a *App) handlePlivoInboundWait(w http.ResponseWriter, r *http.Request, route *routeRow) {
	callID := strings.TrimSpace(r.URL.Query().Get("call_id"))
	row, err := a.db().findCall(callID)
	if err != nil || row == nil || row.RouteID != route.ID || row.ProjectID != route.ProjectID || row.CarrierSlug != "plivo" {
		http.Error(w, "call not found", http.StatusNotFound)
		return
	}
	if callUUID := strings.TrimSpace(r.FormValue("CallUUID")); callUUID != "" && callUUID != row.CarrierSID {
		http.Error(w, "call does not match route", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	switch row.Status {
	case "answered", "in-progress", "media-disconnected":
		_, _ = w.Write([]byte(a.plivoStreamXML(row, xmlEscape(a.publicWSStreamURL("plivo", row.ID, row.CallbackSecret)))))
	case "pending", "answering":
		writePlivoWait(w, a.plivoWaitURL(*route, row.ID))
	default:
		writePlivoHangup(w)
	}
}

func (a *App) handlePlivoInboundStatus(w http.ResponseWriter, r *http.Request, route *routeRow) {
	callUUID := strings.TrimSpace(r.FormValue("CallUUID"))
	if callUUID == "" {
		http.Error(w, "missing CallUUID", http.StatusBadRequest)
		return
	}
	row, err := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, callUUID)
	if err != nil {
		http.Error(w, "load call", http.StatusInternalServerError)
		return
	}
	if row == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	update := callbackUpdateFor("plivo", r)
	if update.Status != "" {
		created, err := a.db().updateStatusWithFacts(row.ID, update.Status, update.Error, update.Facts)
		if err != nil {
			http.Error(w, "persist status", http.StatusInternalServerError)
			return
		}
		if created && globalCtx != nil {
			_ = a.publishLifecycleEvents(globalCtx.WithProject(row.ProjectID), row.ID)
		}
	}
	if isTerminalStatus(update.Status) && globalCtx != nil {
		_ = a.killCallThread(globalCtx.WithProject(row.ProjectID), row)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) plivoRouteStatusURL(route routeRow) string {
	query := url.Values{"secret": {route.Secret}, "project_id": {route.ProjectID}}.Encode()
	return fmt.Sprintf("%s/inbound/plivo/%s/status?%s", a.publicAppURL(), route.ID, query)
}

func (a *App) plivoWaitURL(route routeRow, callID string) string {
	query := url.Values{"secret": {route.Secret}, "call_id": {callID}, "project_id": {route.ProjectID}}.Encode()
	return fmt.Sprintf("%s/inbound/plivo/%s/wait?%s", a.publicAppURL(), route.ID, query)
}

func writePlivoWait(w http.ResponseWriter, waitURL string) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = fmt.Fprintf(w, `<Response><Wait length="1" silence="true"/><Redirect method="POST">%s</Redirect></Response>`, xmlEscape(waitURL))
}

func writePlivoHangup(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(`<Response><Hangup/></Response>`))
}

func plivoApplicationID(value any) string {
	if object, ok := value.(map[string]any); ok {
		return firstNonEmpty(stringValue(object["app_id"]), stringValue(object["id"]))
	}
	raw := strings.Trim(stringValue(value), "/")
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "/")
	if len(parts) >= 2 && strings.EqualFold(parts[len(parts)-2], "Application") {
		return parts[len(parts)-1]
	}
	return raw
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
