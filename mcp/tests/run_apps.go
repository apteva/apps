package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// maxEnvironmentApps caps environment.apps and apps_by_name together.
// Environments and the server apply their own, lower runtime limit.
const maxEnvironmentApps = 24

// maxAppConfigKeys mirrors the Environments limit per app.
const maxAppConfigKeys = 128

// hasApp reports whether the run contains the app, by install or by name.
func (e *GeneratedEnvironment) hasApp(name string) bool {
	return containsString(e.Apps, name) || containsString(e.AppsByName, name)
}

// validateApps checks both app lists and returns every app in the run.
func (e *GeneratedEnvironment) validateApps() (map[string]bool, error) {
	total := len(e.Apps) + len(e.AppsByName)
	if total == 0 || total > maxEnvironmentApps {
		return nil, fmt.Errorf("environment.apps and environment.apps_by_name need 1–%d app names in all", maxEnvironmentApps)
	}
	seen := map[string]bool{}
	for _, app := range e.Apps {
		if !slugPattern.MatchString(app) || seen[app] {
			return nil, fmt.Errorf("invalid or duplicate environment app %q", app)
		}
		seen[app] = true
	}
	for _, app := range e.AppsByName {
		if containsString(e.Apps, app) {
			return nil, fmt.Errorf("app %q is in both environment.apps and environment.apps_by_name; list it once", app)
		}
		if !slugPattern.MatchString(app) || seen[app] {
			return nil, fmt.Errorf("invalid or duplicate environment app %q in apps_by_name", app)
		}
		seen[app] = true
	}
	return seen, nil
}

// validateAppConfig checks that settings name apps in the run. Environments
// validates the values when the suite is saved.
func (e *GeneratedEnvironment) validateAppConfig(apps map[string]bool) error {
	for app, values := range e.AppConfig {
		if !apps[app] {
			return fmt.Errorf("app_config sets %q, which is not in environment.apps or apps_by_name", app)
		}
		if len(values) > maxAppConfigKeys {
			return fmt.Errorf("app_config %s: at most %d settings", app, maxAppConfigKeys)
		}
		for key := range values {
			if strings.TrimSpace(key) == "" {
				return fmt.Errorf("app_config %s: setting name required", app)
			}
		}
	}
	return nil
}

// appsByNameSpec is the Environments `apps` list. An app_versions pin also
// selects the version the run fetches.
func (e *GeneratedEnvironment) appsByNameSpec() []map[string]any {
	out := make([]map[string]any, 0, len(e.AppsByName))
	for _, name := range e.AppsByName {
		entry := map[string]any{"name": name}
		if version := e.AppVersions[name]; version != "" {
			entry["version"] = version
		}
		out = append(out, entry)
	}
	return out
}

// checkSavedSpec confirms Environments kept the fields this suite relies on.
// An older Environments drops fields it does not know, and its runs would
// then start without the apps or settings.
func (e *GeneratedEnvironment) checkSavedSpec(raw json.RawMessage) error {
	if len(e.AppsByName) == 0 && len(e.AppConfig) == 0 {
		return nil
	}
	var saved struct {
		Apps []struct {
			Name string `json:"name"`
		} `json:"apps"`
		AppConfig map[string]map[string]string `json:"app_config"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &saved) != nil {
		return errors.New("Environments did not return the saved environment spec")
	}
	if len(e.AppsByName) > 0 {
		kept := map[string]bool{}
		for _, app := range saved.Apps {
			kept[app.Name] = true
		}
		for _, name := range e.AppsByName {
			if !kept[name] {
				return errors.New("environment.apps_by_name needs Environments 0.14.0 or newer; upgrade the bound Environments app, then save the suite again")
			}
		}
	}
	for app := range e.AppConfig {
		if _, ok := saved.AppConfig[app]; !ok {
			return errors.New("environment.app_config needs Environments 0.13.0 or newer; upgrade the bound Environments app, then save the suite again")
		}
	}
	return nil
}
