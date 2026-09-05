package environment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

var languageTag = regexp.MustCompile(`^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8})*$`)

// Parse decodes and validates the optional environment object. legacyUserAgent
// preserves the previously accepted, undocumented top-level user_agent field.
func Parse(raw any, legacyUserAgent string) (computer.EnvironmentOptions, error) {
	var out computer.EnvironmentOptions
	if raw != nil {
		data, err := json.Marshal(raw)
		if err != nil {
			return out, fmt.Errorf("environment must be an object: %w", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&out); err != nil {
			return out, fmt.Errorf("invalid environment: %w", err)
		}
	}
	legacyUserAgent = strings.TrimSpace(legacyUserAgent)
	if legacyUserAgent != "" {
		if out.UserAgent != "" && out.UserAgent != legacyUserAgent {
			return out, fmt.Errorf("environment.user_agent conflicts with deprecated top-level user_agent")
		}
		out.UserAgent = legacyUserAgent
	}
	return Normalize(out)
}

// Normalize validates settings and fills only explicit sub-setting defaults.
func Normalize(out computer.EnvironmentOptions) (computer.EnvironmentOptions, error) {
	out.UserAgent = strings.TrimSpace(out.UserAgent)
	out.Locale = strings.TrimSpace(out.Locale)
	out.Timezone = strings.TrimSpace(out.Timezone)
	if len(out.UserAgent) > 4096 || strings.ContainsAny(out.UserAgent, "\r\n") {
		return out, fmt.Errorf("environment.user_agent is invalid")
	}
	if out.Locale != "" && !languageTag.MatchString(out.Locale) {
		return out, fmt.Errorf("environment.locale must be a language tag such as fr-FR")
	}
	if len(out.Languages) > 10 {
		return out, fmt.Errorf("environment.languages supports at most 10 entries")
	}
	for i := range out.Languages {
		out.Languages[i] = strings.TrimSpace(out.Languages[i])
		if !languageTag.MatchString(out.Languages[i]) {
			return out, fmt.Errorf("environment.languages[%d] must be a language tag", i)
		}
	}
	if out.Locale != "" && len(out.Languages) == 0 {
		out.Languages = []string{out.Locale}
	}
	if out.Timezone != "" {
		if _, err := time.LoadLocation(out.Timezone); err != nil {
			return out, fmt.Errorf("environment.timezone must be an IANA timezone: %w", err)
		}
	}
	if out.Geolocation != nil {
		geo := out.Geolocation
		if geo.Latitude == nil || geo.Longitude == nil {
			return out, fmt.Errorf("environment.geolocation requires latitude and longitude")
		}
		if *geo.Latitude < -90 || *geo.Latitude > 90 {
			return out, fmt.Errorf("environment.geolocation.latitude must be between -90 and 90")
		}
		if *geo.Longitude < -180 || *geo.Longitude > 180 {
			return out, fmt.Errorf("environment.geolocation.longitude must be between -180 and 180")
		}
		if geo.Accuracy == nil {
			accuracy := 100.0
			geo.Accuracy = &accuracy
		} else if *geo.Accuracy < 0 || *geo.Accuracy > 100000 {
			return out, fmt.Errorf("environment.geolocation.accuracy must be between 0 and 100000 meters")
		}
		geo.Permission = strings.ToLower(strings.TrimSpace(geo.Permission))
		if geo.Permission == "" {
			geo.Permission = "grant"
		}
		if geo.Permission != "grant" && geo.Permission != "prompt" && geo.Permission != "deny" {
			return out, fmt.Errorf("environment.geolocation.permission must be grant, prompt, or deny")
		}
	}
	if out.DeviceScaleFactor != nil && (*out.DeviceScaleFactor < 0.5 || *out.DeviceScaleFactor > 4) {
		return out, fmt.Errorf("environment.device_scale_factor must be between 0.5 and 4; omit it for the normal scale of 1")
	}
	if out.MaxTouchPoints != nil {
		if *out.MaxTouchPoints < 1 || *out.MaxTouchPoints > 20 {
			return out, fmt.Errorf("environment.max_touch_points must be between 1 and 20")
		}
		if out.Touch == nil || !*out.Touch {
			return out, fmt.Errorf("environment.max_touch_points requires touch=true")
		}
	}
	return out, nil
}

// Apply sends only the requested overrides. An empty environment returns
// immediately, preserving the exact historical backend behavior.
func Apply(ctx context.Context, opts computer.EnvironmentOptions, display computer.DisplaySize) error {
	if opts.IsEmpty() {
		return nil
	}
	if opts.Geolocation != nil {
		setting := browser.PermissionSettingPrompt
		switch opts.Geolocation.Permission {
		case "grant":
			setting = browser.PermissionSettingGranted
		case "deny":
			setting = browser.PermissionSettingDenied
		}
		params := browser.SetPermission(&browser.PermissionDescriptor{Name: "geolocation"}, setting)
		if c := chromedp.FromContext(ctx); c != nil && c.Browser != nil {
			browserCtx := cdp.WithExecutor(ctx, c.Browser)
			if c.BrowserContextID != "" {
				params = params.WithBrowserContextID(c.BrowserContextID)
			}
			if err := params.Do(browserCtx); err != nil {
				return fmt.Errorf("apply geolocation permission: %w", err)
			}
		}
	}
	var actions []chromedp.Action
	if opts.UserAgent != "" || len(opts.Languages) > 0 {
		ua := opts.UserAgent
		if ua == "" {
			if err := cdputil.Run(ctx, chromedp.Evaluate(`navigator.userAgent`, &ua)); err != nil {
				return fmt.Errorf("read current user agent: %w", err)
			}
		}
		params := emulation.SetUserAgentOverride(ua)
		if len(opts.Languages) > 0 {
			params = params.WithAcceptLanguage(strings.Join(opts.Languages, ","))
		}
		actions = append(actions, params)
	}
	if opts.Locale != "" {
		actions = append(actions, emulation.SetLocaleOverride().WithLocale(opts.Locale))
	}
	if opts.Timezone != "" {
		actions = append(actions, emulation.SetTimezoneOverride(opts.Timezone))
	}
	if geo := opts.Geolocation; geo != nil {
		actions = append(actions, emulation.SetGeolocationOverride().
			WithLatitude(*geo.Latitude).WithLongitude(*geo.Longitude).WithAccuracy(*geo.Accuracy))
	}
	if opts.DeviceScaleFactor != nil || opts.Mobile != nil {
		if display.Width <= 0 || display.Height <= 0 {
			return fmt.Errorf("apply device emulation: invalid viewport %dx%d", display.Width, display.Height)
		}
		dsf := 1.0
		mobile := false
		if opts.DeviceScaleFactor != nil {
			dsf = *opts.DeviceScaleFactor
		}
		if opts.Mobile != nil {
			mobile = *opts.Mobile
		}
		actions = append(actions, emulation.SetDeviceMetricsOverride(int64(display.Width), int64(display.Height), dsf, mobile))
	}
	if opts.Touch != nil {
		params := emulation.SetTouchEmulationEnabled(*opts.Touch)
		if *opts.Touch && opts.MaxTouchPoints != nil {
			params = params.WithMaxTouchPoints(int64(*opts.MaxTouchPoints))
		}
		actions = append(actions, params)
	}
	if len(actions) == 0 {
		return nil
	}
	if err := cdputil.Run(ctx, actions...); err != nil {
		return fmt.Errorf("apply browser environment: %w", err)
	}
	return nil
}
