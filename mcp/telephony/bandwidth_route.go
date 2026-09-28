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

// Bandwidth assigns a Voice Application to a Location, not to an individual
// number. Telephony never changes that shared assignment. The operator must
// point a dedicated Application at the returned callback URL and keep its
// disconnect callback on the same route.
type bandwidthRouteConfig struct {
	AccountID     string `json:"account_id"`
	ApplicationID string `json:"application_id"`
}

func (a *App) configureBandwidthRoute(ctx *sdk.AppCtx, route *routeRow) error {
	creds, err := ctx.PlatformAPI().GetConnectionCredentials(route.CarrierConnectionID)
	if err != nil {
		return fmt.Errorf("read Bandwidth credentials: %w", err)
	}
	if creds == nil {
		return errors.New("Bandwidth credentials are unavailable")
	}
	config := bandwidthRouteConfig{
		AccountID:     strings.TrimSpace(creds.Fields["account_id"]),
		ApplicationID: strings.TrimSpace(creds.Fields["application_id"]),
	}
	if !validProviderResourceID(config.AccountID) || !validProviderResourceID(config.ApplicationID) {
		return errors.New("Bandwidth account_id and application_id are required for manual inbound setup")
	}
	if route.PreviousVoiceURL != "" {
		var saved bandwidthRouteConfig
		if json.Unmarshal([]byte(route.PreviousVoiceURL), &saved) != nil || saved != config {
			return errors.New("Bandwidth route binding changed; disable the route before binding another Application")
		}
		return nil
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	if err := a.db().updateRoutePreviousVoiceURL(route.ID, string(encoded)); err != nil {
		return err
	}
	route.PreviousVoiceURL = string(encoded)
	return nil
}

func (a *App) bandwidthWaitURL(route routeRow, callID string) string {
	query := url.Values{"secret": {route.Secret}, "project_id": {route.ProjectID}, "call_id": {callID}}.Encode()
	return a.publicAppURL() + "/inbound/bandwidth/" + url.PathEscape(route.ID) + "/wait?" + query
}

func (a *App) bandwidthRouteStatusURL(route routeRow) string {
	query := url.Values{"secret": {route.Secret}, "project_id": {route.ProjectID}}.Encode()
	return a.publicAppURL() + "/inbound/bandwidth/" + url.PathEscape(route.ID) + "/status?" + query
}

func writeBandwidthHold(w http.ResponseWriter, waitURL, secret, initialPrompt string) {
	w.Header().Set("Content-Type", "application/xml")
	if initialPrompt != "" {
		_, _ = fmt.Fprintf(w, `<Response><SpeakSentence>%s</SpeakSentence><Pause duration="1"/><Redirect redirectUrl="%s" username="apteva" password="%s"/></Response>`, xmlEscape(initialPrompt), xmlEscape(waitURL), xmlEscape(secret))
		return
	}
	_, _ = fmt.Fprintf(w, `<Response><Pause duration="1"/><Redirect redirectUrl="%s" username="apteva" password="%s"/></Response>`, xmlEscape(waitURL), xmlEscape(secret))
}

func writeBandwidthEnd(w http.ResponseWriter, prompt string) {
	w.Header().Set("Content-Type", "application/xml")
	if prompt != "" {
		_, _ = fmt.Fprintf(w, `<Response><SpeakSentence>%s</SpeakSentence><Hangup/></Response>`, xmlEscape(prompt))
		return
	}
	_, _ = w.Write([]byte(`<Response><Hangup/></Response>`))
}

func (a *App) handleBandwidthInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/inbound/bandwidth/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || len(parts) > 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	route, err := a.db().findRoute(parts[0])
	if err != nil {
		http.Error(w, "load route", http.StatusInternalServerError)
		return
	}
	if route == nil || !route.Enabled || route.CarrierSlug != "bandwidth" || route.InboundTransport != inboundTransportProgrammable ||
		!secureEqual(r.URL.Query().Get("secret"), route.Secret) || r.URL.Query().Get("project_id") != route.ProjectID {
		http.NotFound(w, r)
		return
	}
	username, password, ok := r.BasicAuth()
	if !ok || username != "apteva" || !secureEqual(password, route.Secret) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var binding bandwidthRouteConfig
	if json.Unmarshal([]byte(route.PreviousVoiceURL), &binding) != nil || !validProviderResourceID(binding.AccountID) || !validProviderResourceID(binding.ApplicationID) {
		http.Error(w, "Bandwidth route is not bound", http.StatusServiceUnavailable)
		return
	}
	var event struct {
		EventType     string `json:"eventType"`
		EventTime     string `json:"eventTime"`
		AccountID     string `json:"accountId"`
		ApplicationID string `json:"applicationId"`
		Direction     string `json:"direction"`
		CallID        string `json:"callId"`
		To            string `json:"to"`
		From          string `json:"from"`
		Cause         string `json:"cause"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&event); err != nil || event.CallID == "" ||
		event.AccountID != binding.AccountID || event.ApplicationID != binding.ApplicationID || event.To != route.PhoneNumber || event.Direction != "inbound" {
		http.Error(w, "invalid Bandwidth callback", http.StatusForbidden)
		return
	}
	phase := "initiate"
	if len(parts) == 2 {
		phase = parts[1]
	}
	switch phase {
	case "initiate":
		if event.EventType != "initiate" {
			http.Error(w, "invalid Bandwidth initiate event", http.StatusBadRequest)
			return
		}
		call, _, err := a.recordInboundCall(route, event.CallID, event.From, event.To)
		if err != nil {
			http.Error(w, "persist inbound call", http.StatusServiceUnavailable)
			return
		}
		if isSuppressedHandlingReason(call.HandlingReason) {
			_ = a.db().updateStatus(call.ID, "canceled", call.ErrorMessage)
			writeBandwidthEnd(w, "")
			return
		}
		if call.AnnouncementState != "" {
			writeBandwidthEnd(w, call.AnnouncementText)
			return
		}
		if route.RoutingTerminalType == "hangup" || route.RoutingTerminalType == "reject" {
			writeBandwidthEnd(w, "")
			return
		}
		writeBandwidthHold(w, a.bandwidthWaitURL(*route, call.ID), route.Secret, a.holdText(*route))
		a.enqueueImmediateAnswer(route, call.ID)
	case "wait":
		if event.EventType != "redirect" {
			http.Error(w, "invalid Bandwidth redirect event", http.StatusBadRequest)
			return
		}
		call, err := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, event.CallID)
		if err != nil || call == nil || call.ID != r.URL.Query().Get("call_id") {
			http.NotFound(w, r)
			return
		}
		if isTerminalStatus(call.Status) {
			writeBandwidthEnd(w, "")
			return
		}
		if call.AnnouncementState != "" {
			writeBandwidthEnd(w, call.AnnouncementText)
			return
		}
		writeBandwidthHold(w, a.bandwidthWaitURL(*route, call.ID), route.Secret, "")
	case "status":
		if event.EventType != "disconnect" {
			http.Error(w, "invalid Bandwidth disconnect event", http.StatusBadRequest)
			return
		}
		call, err := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, event.CallID)
		if err != nil {
			http.Error(w, "load call", http.StatusInternalServerError)
			return
		}
		if call != nil {
			created, err := a.db().updateStatusWithFacts(call.ID, "completed", "", lifecycleFacts{
				OccurredAt: event.EventTime, Source: "provider", ProviderEventID: event.CallID + ":disconnect:" + event.EventTime,
				TerminationCause: event.Cause,
			})
			if err != nil {
				http.Error(w, "persist disconnect", http.StatusInternalServerError)
				return
			}
			if globalCtx != nil {
				projectCtx := globalCtx.WithProject(route.ProjectID)
				if created {
					_ = a.publishLifecycleEvents(projectCtx, call.ID)
				}
				_ = a.killCallThread(projectCtx, call)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}
