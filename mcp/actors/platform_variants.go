package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// A variant changes the implementation, never the operation's safety policy.
// Browser configuration replaces the shared configuration in full.
type actorPlatformVariant struct {
	PrivateScreen          bool              `json:"private_screen,omitempty"`
	ReadOnly               bool              `json:"read_only,omitempty"`
	Browser                *actorBrowser     `json:"browser,omitempty"`
	RequiredExplicitInputs []string          `json:"required_explicit_inputs,omitempty"`
	Steps                  []actorStep       `json:"steps"`
	OutputSchema           map[string]string `json:"output_schema,omitempty"`
}

func validActorPlatform(platform string) bool {
	return platform == "ios" || platform == "android" || platform == "web"
}

func actorVariantPlatforms(def actorDefinition) []string {
	platforms := make([]string, 0, len(def.Variants))
	for platform := range def.Variants {
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	return platforms
}

func selectActorVariant(def actorDefinition, platform string) (actorDefinition, error) {
	variant, ok := def.Variants[platform]
	if !ok {
		return def, fmt.Errorf("platform_variant_unavailable: %q; supported platforms: %s", platform, strings.Join(actorVariantPlatforms(def), ", "))
	}
	def.Variants = nil
	def.SelectedPlatform = platform
	def.PrivateScreen = def.PrivateScreen || variant.PrivateScreen
	def.ReadOnly = def.ReadOnly || variant.ReadOnly
	if variant.Browser != nil {
		def.Browser = *variant.Browser
	}
	def.Steps = variant.Steps
	def.RequiredExplicitInputs = append(append([]string{}, def.RequiredExplicitInputs...), variant.RequiredExplicitInputs...)
	if variant.OutputSchema != nil {
		def.OutputSchema = variant.OutputSchema
	}
	return def, nil
}

func validateActorVariants(def actorDefinition) error {
	if def.SchemaVersion != 1 || len(def.Steps) > 0 || def.SelectedPlatform != "" {
		return errors.New("platform variants require schema version 1 and cannot accompany steps or selected_platform")
	}
	if def.Recovery != nil || def.RecoveryOnly {
		return errors.New("platform variants do not support recovery policies; use a separate reviewed recovery operation")
	}
	for _, platform := range actorVariantPlatforms(def) {
		if !validActorPlatform(platform) {
			return fmt.Errorf("invalid platform variant %q: use ios, android or web", platform)
		}
		selected, _ := selectActorVariant(def, platform)
		if err := validateActorDefinition(selected); err != nil {
			return fmt.Errorf("platform variant %s: %w", platform, err)
		}
		// Static mismatches are rejected on save. Templates and saved contexts
		// are checked at admission, without opening any device session.
		browser := selected.Browser
		browserJSON, _ := json.Marshal(browser)
		if !strings.Contains(string(browserJSON), "{{") && browser.ContextID == "" {
			actual, err := actorBrowserPlatform(browser)
			if err != nil {
				return err
			}
			if actual != "" && actual != platform {
				return fmt.Errorf("platform variant %s conflicts with its %s browser target", platform, actual)
			}
		}
	}
	return nil
}

func actorVariantVariables(def actorDefinition, raw string) (map[string]any, error) {
	var input struct {
		Preset            string         `json:"preset"`
		ScheduleOverrides map[string]any `json:"schedule_overrides"`
		Input             map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return nil, err
	}
	vars := mergeActorMaps(nil, def.Defaults)
	if input.Preset != "" {
		preset, ok := def.Presets[input.Preset]
		if !ok {
			return nil, fmt.Errorf("preset %q not found", input.Preset)
		}
		vars = mergeActorMaps(vars, preset)
	}
	vars = mergeActorMaps(vars, input.ScheduleOverrides)
	return mergeActorMaps(vars, input.Input), nil
}

func actorBrowserPlatform(browser actorBrowser) (string, error) {
	if browser.Backend != "mobile" {
		return "web", nil
	}
	platform, device := "", browser.DeviceID
	if browser.Target != nil {
		platform = browser.Target.Platform
		device = firstNonEmpty(browser.Target.DeviceID, device)
	}
	inferred := ""
	for _, candidate := range []string{"ios", "android"} {
		if strings.HasPrefix(device, candidate+":") {
			inferred = candidate
		}
	}
	if platform != "" && platform != "ios" && platform != "android" {
		return "", errors.New("Mobile target.platform must be ios or android")
	}
	if platform != "" && inferred != "" && platform != inferred {
		return "", errors.New("Mobile target platform conflicts with device_id")
	}
	return firstNonEmpty(platform, inferred), nil
}

func renderVariantBrowser(ctx *sdk.AppCtx, browser actorBrowser, vars map[string]any) (actorBrowser, error) {
	raw, _ := json.Marshal(browser)
	var generic any
	_ = json.Unmarshal(raw, &generic)
	rendered, err := renderActorValue(generic, vars)
	if err != nil {
		return browser, err
	}
	raw, _ = json.Marshal(rendered)
	if err = json.Unmarshal(raw, &browser); err != nil {
		return browser, err
	}
	// Resolve only context metadata here, never open a session. Pin the exact
	// context device so a later context edit cannot redirect a queued run.
	probe := actorDefinition{Browser: browser, Steps: []actorStep{{Action: "wait"}}}
	if err = resolveMobileBrowser(context.Background(), ctx, &probe); err != nil {
		return browser, err
	}
	return probe.Browser, nil
}

func resolvePlatformSnapshot(ctx *sdk.AppCtx, def actorDefinition, inputJSON string) (actorDefinition, error) {
	if len(def.Variants) == 0 {
		return def, nil
	}
	vars, err := actorVariantVariables(def, inputJSON)
	if err != nil {
		return def, err
	}
	platform := ""
	if value, supplied := vars["platform"]; supplied {
		var ok bool
		platform, ok = value.(string)
		if !ok || !validActorPlatform(platform) {
			return def, errors.New("input.platform must be ios, android or web")
		}
	} else {
		browser, err := renderVariantBrowser(ctx, def.Browser, vars)
		if err != nil {
			return def, fmt.Errorf("resolve platform: %w", err)
		}
		platform, err = actorBrowserPlatform(browser)
		if err != nil {
			return def, err
		}
		if platform == "" {
			return def, errors.New("platform cannot be inferred; supply input.platform or a preset")
		}
	}
	selected, err := selectActorVariant(def, platform)
	if err != nil {
		return def, err
	}
	selected.Browser, err = renderVariantBrowser(ctx, selected.Browser, vars)
	if err != nil {
		return def, err
	}
	actual, err := actorBrowserPlatform(selected.Browser)
	if err != nil {
		return def, err
	}
	if actual != platform {
		return def, fmt.Errorf("platform variant %s does not match the resolved device/context platform %q", platform, actual)
	}
	if selected.Browser.Target != nil && selected.Browser.Target.Platform == "" && platform != "web" {
		selected.Browser.Target.Platform = platform
	}
	return selected, nil
}
