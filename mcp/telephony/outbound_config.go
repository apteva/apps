package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	outboundReady             = "ready"
	outboundAutoConfigurable  = "auto_configurable"
	outboundSelectionRequired = "selection_required"
	outboundSetupRequired     = "setup_required"
	outboundConfigError       = "error"
)

// outboundProfileOption is the provider-neutral representation consumed by
// the dialer. Telnyx calls these Outbound Voice Profiles; another carrier can
// expose the same readiness contract without leaking its API vocabulary into
// the UI or call-placement flow.
type outboundProfileOption struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	Enabled                bool     `json:"enabled"`
	Destinations           []string `json:"destinations"`
	TrafficType            string   `json:"traffic_type,omitempty"`
	ServicePlan            string   `json:"service_plan,omitempty"`
	DailySpendLimitEnabled bool     `json:"daily_spend_limit_enabled"`
	DailySpendLimit        string   `json:"daily_spend_limit,omitempty"`
}

type outboundReadinessView struct {
	Required             bool                    `json:"required"`
	Status               string                  `json:"status"`
	ApplicationID        string                  `json:"application_id,omitempty"`
	ProfileID            string                  `json:"profile_id,omitempty"`
	RecommendedProfileID string                  `json:"recommended_profile_id,omitempty"`
	Profiles             []outboundProfileOption `json:"profiles"`
	Message              string                  `json:"message,omitempty"`
}

func carrierOutboundReady() outboundReadinessView {
	return outboundReadinessView{Status: outboundReady, Profiles: []outboundProfileOption{}}
}

func (a *App) outboundReadiness(ctx *sdk.AppCtx, provider string, connectionID int64, applicationID string) (outboundReadinessView, error) {
	if strings.ToLower(strings.TrimSpace(provider)) != "telnyx" {
		return carrierOutboundReady(), nil
	}
	profiles, err := listTelnyxOutboundProfiles(ctx, connectionID)
	if err != nil {
		return outboundReadinessView{Required: true, Status: outboundConfigError, ApplicationID: applicationID, Profiles: []outboundProfileOption{}}, err
	}
	return a.telnyxOutboundReadiness(ctx, connectionID, applicationID, profiles)
}

func (a *App) telnyxOutboundReadiness(ctx *sdk.AppCtx, connectionID int64, applicationID string, profiles []outboundProfileOption) (outboundReadinessView, error) {
	view := outboundReadinessView{
		Required: true, ApplicationID: strings.TrimSpace(applicationID), Profiles: profiles,
	}
	if !validProviderResourceID(view.ApplicationID) {
		view.Status = outboundSetupRequired
		view.Message = "The selected number has no Call Control application for outbound calls. Configure carrier routing, then refresh."
		return view, nil
	}

	raw, err := executeCarrierTool(ctx, connectionID, "get_call_control_application", map[string]any{"id": view.ApplicationID})
	if err != nil {
		view.Status = outboundConfigError
		return view, fmt.Errorf("inspect Telnyx Call Control application: %w", err)
	}
	response, err := telnyxResponse(raw)
	if err != nil {
		view.Status = outboundConfigError
		return view, fmt.Errorf("decode Telnyx Call Control application: %w", err)
	}
	application := telnyxDataMap(response)
	if !boolValue(application["active"]) {
		view.Status = outboundSetupRequired
		view.Message = "The selected number's Call Control application is disabled. Enable it in the carrier, then refresh."
		return view, nil
	}
	outbound, _ := application["outbound"].(map[string]any)
	view.ProfileID = strings.TrimSpace(stringValue(outbound["outbound_voice_profile_id"]))

	enabled := enabledOutboundProfiles(profiles)
	for _, profile := range enabled {
		if profile.ID == view.ProfileID {
			view.Status = outboundReady
			return view, nil
		}
	}
	switch len(enabled) {
	case 0:
		view.Status = outboundSetupRequired
		view.Message = "No enabled outbound calling profile exists. Configure destinations and spend policy in the carrier, then refresh."
	case 1:
		view.Status = outboundAutoConfigurable
		view.RecommendedProfileID = enabled[0].ID
		view.Message = fmt.Sprintf("%s will be attached automatically before the call is placed.", enabled[0].Name)
	default:
		view.Status = outboundSelectionRequired
		view.Message = "Choose which outbound calling profile this number should use."
	}
	return view, nil
}

func listTelnyxOutboundProfiles(ctx *sdk.AppCtx, connectionID int64) ([]outboundProfileOption, error) {
	const pageSize = 100
	profiles := make([]outboundProfileOption, 0)
	for page := 1; page <= maxOwnedNumberPages; page++ {
		raw, err := executeCarrierTool(ctx, connectionID, "list_outbound_voice_profiles", map[string]any{
			"page[number]": page,
			"page[size]":   pageSize,
			"sort":         "name",
		})
		if err != nil {
			return nil, fmt.Errorf("list Telnyx outbound voice profiles: %w", err)
		}
		response, err := telnyxResponse(raw)
		if err != nil {
			return nil, fmt.Errorf("decode Telnyx outbound voice profiles page %d: %w", page, err)
		}
		values := telnyxDataList(response)
		for _, value := range values {
			item, _ := value.(map[string]any)
			id := strings.TrimSpace(stringValue(item["id"]))
			if !validProviderResourceID(id) {
				continue
			}
			destinations := make([]string, 0)
			for _, rawDestination := range anyList(item["whitelisted_destinations"]) {
				if destination := strings.ToUpper(strings.TrimSpace(stringValue(rawDestination))); destination != "" {
					destinations = append(destinations, destination)
				}
			}
			profiles = append(profiles, outboundProfileOption{
				ID: id, Name: firstNonEmpty(strings.TrimSpace(stringValue(item["name"])), id),
				Enabled: boolValue(item["enabled"]), Destinations: destinations,
				TrafficType: stringValue(item["traffic_type"]), ServicePlan: stringValue(item["service_plan"]),
				DailySpendLimitEnabled: boolValue(item["daily_spend_limit_enabled"]),
				DailySpendLimit:        stringValue(item["daily_spend_limit"]),
			})
		}
		meta, _ := response["meta"].(map[string]any)
		totalPages := intValue(meta["total_pages"])
		hasMore := len(values) == pageSize
		if totalPages > 0 {
			hasMore = page < totalPages
		}
		if hasMore && len(values) == 0 {
			return nil, fmt.Errorf("Telnyx outbound voice profile listing made no progress on page %d", page)
		}
		if !hasMore {
			return profiles, nil
		}
	}
	return nil, fmt.Errorf("Telnyx outbound voice profile listing exceeded %d pages", maxOwnedNumberPages)
}

func enabledOutboundProfiles(profiles []outboundProfileOption) []outboundProfileOption {
	enabled := make([]outboundProfileOption, 0, len(profiles))
	for _, profile := range profiles {
		if profile.Enabled {
			enabled = append(enabled, profile)
		}
	}
	return enabled
}

func soleEnabledOutboundProfileID(profiles []outboundProfileOption) string {
	enabled := enabledOutboundProfiles(profiles)
	if len(enabled) == 1 {
		return enabled[0].ID
	}
	return ""
}

func (a *App) applyOutboundProfile(ctx *sdk.AppCtx, provider string, connectionID int64, applicationID, profileID string) (outboundReadinessView, error) {
	if strings.ToLower(strings.TrimSpace(provider)) != "telnyx" {
		return outboundReadinessView{}, fmt.Errorf("outbound profile selection is not required for provider %s", provider)
	}
	profiles, err := listTelnyxOutboundProfiles(ctx, connectionID)
	if err != nil {
		return outboundReadinessView{}, err
	}
	profileID = strings.TrimSpace(profileID)
	valid := false
	for _, profile := range enabledOutboundProfiles(profiles) {
		if profile.ID == profileID {
			valid = true
			break
		}
	}
	if !valid {
		return outboundReadinessView{}, errors.New("selected outbound profile is unavailable or disabled")
	}
	if !validProviderResourceID(applicationID) {
		return outboundReadinessView{}, errors.New("selected number has no configurable outbound application")
	}
	if _, err := executeCarrierTool(ctx, connectionID, "update_call_control_application", map[string]any{
		"id":       applicationID,
		"outbound": map[string]any{"outbound_voice_profile_id": profileID},
	}); err != nil {
		return outboundReadinessView{}, fmt.Errorf("assign outbound profile: %w", err)
	}
	view, err := a.telnyxOutboundReadiness(ctx, connectionID, applicationID, profiles)
	if err != nil {
		return view, err
	}
	if view.Status != outboundReady || view.ProfileID != profileID {
		return view, errors.New("carrier did not retain the selected outbound profile")
	}
	return view, nil
}

func (a *App) ensureOutboundReady(ctx *sdk.AppCtx, provider string, connectionID int64, applicationID string) error {
	view, err := a.outboundReadiness(ctx, provider, connectionID, applicationID)
	if err != nil {
		return err
	}
	switch view.Status {
	case outboundReady:
		return nil
	case outboundAutoConfigurable:
		_, err := a.applyOutboundProfile(ctx, provider, connectionID, applicationID, view.RecommendedProfileID)
		return err
	case outboundSelectionRequired, outboundSetupRequired:
		return errors.New(view.Message)
	default:
		return errors.New(firstNonEmpty(view.Message, "outbound carrier configuration is unavailable"))
	}
}

func telnyxRouteApplicationID(route *routeRow, fallback string) string {
	if route != nil {
		var config telnyxRouteConfig
		if json.Unmarshal([]byte(route.PreviousVoiceURL), &config) == nil && validProviderResourceID(config.ApplicationID) {
			return config.ApplicationID
		}
	}
	if validProviderResourceID(fallback) {
		return fallback
	}
	return ""
}
