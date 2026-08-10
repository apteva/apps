package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	protocolPackTelephonyCarrier = "telephony-carrier"
	protocolTwilio               = "twilio"
)

func protocolFixtureCatalog() []ProtocolFixtureCatalogItem {
	return []ProtocolFixtureCatalogItem{{
		ID:          protocolPackTelephonyCarrier,
		Name:        "Telephony carrier",
		Description: "A local carrier that exercises Telephony webhooks, signed callbacks, media WebSockets, codecs, and call lifecycle.",
		Version:     "1.0.0",
		Protocol:    "Twilio Voice and Media Streams",
		TargetApp:   "telephony",
	}}
}

func validateProtocolFixtureSpec(spec ProtocolFixtureSpec) error {
	if !validID(spec.ID) {
		return errors.New("valid id required")
	}
	if spec.Pack != protocolPackTelephonyCarrier {
		return fmt.Errorf("unsupported pack %q", spec.Pack)
	}
	target := strings.TrimSpace(spec.TargetApp)
	if target != "" && target != "telephony" {
		return fmt.Errorf("telephony carrier target_app must be telephony")
	}
	return nil
}

func normalizeProtocolFixtureSpec(spec ProtocolFixtureSpec) ProtocolFixtureSpec {
	if spec.Version == "" {
		spec.Version = "1.0.0"
	}
	if spec.TargetApp == "" {
		spec.TargetApp = "telephony"
	}
	if spec.Config == nil {
		spec.Config = map[string]any{}
	}
	if stringConfig(spec.Config, "provider", "") == "" {
		spec.Config["provider"] = protocolTwilio
	}
	if stringConfig(spec.Config, "account_sid", "") == "" {
		spec.Config["account_sid"] = "AC00000000000000000000000000000001"
	}
	if stringConfig(spec.Config, "auth_token", "") == "" {
		spec.Config["auth_token"] = "environment-carrier-" + token(24)
	}
	if stringConfig(spec.Config, "phone_number", "") == "" {
		spec.Config["phone_number"] = "+15550100001"
	}
	if stringConfig(spec.Config, "caller_number", "") == "" {
		spec.Config["caller_number"] = "+15550100002"
	}
	return spec
}

func normalizeProtocolFixtureSpecs(spec *EnvironmentSpec) {
	for i := range spec.ProtocolFixtures {
		spec.ProtocolFixtures[i] = normalizeProtocolFixtureSpec(spec.ProtocolFixtures[i])
	}
}

func protocolFixtureBindings(spec EnvironmentSpec) []sdk.RuntimeIntegrationBinding {
	out := append([]sdk.RuntimeIntegrationBinding(nil), spec.IntegrationBindings...)
	for _, raw := range spec.ProtocolFixtures {
		fixture := normalizeProtocolFixtureSpec(raw)
		if fixture.Pack != protocolPackTelephonyCarrier {
			continue
		}
		exists := false
		for i, binding := range out {
			if binding.App == fixture.TargetApp && binding.Role == "carrier" {
				out[i].Slug = protocolTwilio
				out[i].Name = "Simulated Twilio carrier"
				out[i].AuthType = "api_key"
				out[i].Credentials = map[string]string{
					"account_sid":  stringConfig(fixture.Config, "account_sid", ""),
					"auth_token":   stringConfig(fixture.Config, "auth_token", ""),
					"phone_number": stringConfig(fixture.Config, "phone_number", ""),
				}
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		out = append(out, sdk.RuntimeIntegrationBinding{
			App: fixture.TargetApp, Role: "carrier", Slug: protocolTwilio,
			Name: "Simulated Twilio carrier", AuthType: "api_key",
			Credentials: map[string]string{
				"account_sid":  stringConfig(fixture.Config, "account_sid", ""),
				"auth_token":   stringConfig(fixture.Config, "auth_token", ""),
				"phone_number": stringConfig(fixture.Config, "phone_number", ""),
			},
		})
	}
	return out
}

func (s *service) createProtocolFixtures(run *Run, spec EnvironmentSpec) error {
	for _, raw := range spec.ProtocolFixtures {
		fixture := normalizeProtocolFixtureSpec(raw)
		x := &ProtocolFixtureInstance{
			RunID: run.ID, ID: fixture.ID, Pack: fixture.Pack, Version: fixture.Version,
			Protocol: "twilio", TargetApp: fixture.TargetApp, Status: "starting",
			Config: fixture.Config, CreatedAt: time.Now().UTC(),
		}
		if err := s.db.createProtocolFixture(x); err != nil {
			return err
		}
	}
	return nil
}

func stringConfig(config map[string]any, key, fallback string) string {
	value, _ := config[key].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
