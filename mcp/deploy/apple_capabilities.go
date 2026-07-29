package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var appleCapabilityForMobileFeature = map[string]string{
	mobileFeatureIOSPushNotifications: "PUSH_NOTIFICATIONS",
}

func appleCertificateAvailable(bound *sdk.BoundIntegration, certificateID string) (bool, error) {
	certificateID = strings.TrimSpace(certificateID)
	if certificateID == "" {
		return false, nil
	}
	raw, err := executeIntegration(bound, "list_certificates", map[string]any{
		"certificate_type": "IOS_DISTRIBUTION",
		"limit":            200,
	})
	if err != nil {
		return false, fmt.Errorf("list Apple distribution certificates: %w", err)
	}
	var document struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				ExpirationDate string `json:"expirationDate"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &document) != nil {
		return false, errors.New("decode Apple distribution certificates")
	}
	for _, certificate := range document.Data {
		if certificate.ID != certificateID {
			continue
		}
		if certificate.Attributes.ExpirationDate == "" {
			return true, nil
		}
		expires, err := time.Parse(time.RFC3339, certificate.Attributes.ExpirationDate)
		if err != nil {
			return false, fmt.Errorf("parse Apple certificate expiration: %w", err)
		}
		return expires.After(time.Now().Add(24 * time.Hour)), nil
	}
	return false, nil
}

type appleCapabilityState struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type"`
}

type appleCapabilityReconcileResult struct {
	Provisioned []string
	Managed     []string
	StateJSON   string
	Changed     bool
}

func reconcileAppleCapabilities(
	bound *sdk.BoundIntegration,
	bundleResourceID string,
	required []string,
) (appleCapabilityReconcileResult, error) {
	for _, feature := range required {
		if _, ok := appleCapabilityForMobileFeature[feature]; !ok {
			return appleCapabilityReconcileResult{}, fmt.Errorf(
				"iOS feature %q has no Apple capability adapter; configure it manually or add a Deploy mapping",
				feature,
			)
		}
	}
	raw, err := executeIntegration(bound, "list_bundle_id_capabilities", map[string]any{
		"bundle_id":                    bundleResourceID,
		"fields[bundleIdCapabilities]": "capabilityType,settings",
	})
	if err != nil {
		return appleCapabilityReconcileResult{}, fmt.Errorf("list Apple bundle capabilities: %w", err)
	}
	states := parseAppleCapabilityStates(raw)
	byType := map[string]appleCapabilityState{}
	for _, state := range states {
		byType[state.Type] = state
	}
	var managed []string
	for _, feature := range required {
		capabilityType := appleCapabilityForMobileFeature[feature]
		if _, ok := byType[capabilityType]; ok {
			continue
		}
		created, err := executeIntegration(bound, "enable_bundle_id_capability", map[string]any{
			"bundle_id":      bundleResourceID,
			"capabilityType": capabilityType,
			"settings":       []any{},
		})
		if err != nil {
			return appleCapabilityReconcileResult{}, fmt.Errorf("enable Apple capability %s: %w", capabilityType, err)
		}
		state := appleCapabilityState{
			ID:   jsonStringAt(created, "data", "id"),
			Type: capabilityType,
		}
		states = append(states, state)
		byType[capabilityType] = state
		managed = append(managed, feature)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Type < states[j].Type })
	var provisioned []string
	for feature, capabilityType := range appleCapabilityForMobileFeature {
		if _, ok := byType[capabilityType]; ok {
			provisioned = append(provisioned, feature)
		}
	}
	sort.Strings(provisioned)
	sort.Strings(managed)
	body, _ := json.Marshal(map[string]any{
		"provider":     "app_store_connect",
		"bundle_id":    bundleResourceID,
		"capabilities": states,
	})
	return appleCapabilityReconcileResult{
		Provisioned: provisioned,
		Managed:     managed,
		StateJSON:   string(body),
		Changed:     len(managed) > 0,
	}, nil
}

func parseAppleCapabilityStates(raw json.RawMessage) []appleCapabilityState {
	var document struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				CapabilityType string `json:"capabilityType"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &document) != nil {
		return nil
	}
	out := make([]appleCapabilityState, 0, len(document.Data))
	for _, item := range document.Data {
		capabilityType := strings.ToUpper(strings.TrimSpace(item.Attributes.CapabilityType))
		if capabilityType != "" {
			out = append(out, appleCapabilityState{ID: item.ID, Type: capabilityType})
		}
	}
	return out
}

func mergeMobileFeatures(values ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, list := range values {
		for _, value := range list {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
