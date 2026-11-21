package main

import (
	"fmt"
	"strings"
)

// App settings for a run's app copies (0.13.0). Environments checks the
// shape; the platform checks each key against the app's config_schema and
// that the app is in the run when the runtime starts.

const (
	maxAppConfigApps     = 32
	maxAppConfigKeys     = 128
	maxAppConfigValueLen = 16 << 10
)

func validateAppConfig(cfg map[string]map[string]string) error {
	if len(cfg) > maxAppConfigApps {
		return fmt.Errorf("app_config may set at most %d apps", maxAppConfigApps)
	}
	for app, values := range cfg {
		if strings.TrimSpace(app) == "" {
			return fmt.Errorf("app_config: app name required")
		}
		if len(values) > maxAppConfigKeys {
			return fmt.Errorf("app_config %s: at most %d settings", app, maxAppConfigKeys)
		}
		for key, value := range values {
			if strings.TrimSpace(key) == "" {
				return fmt.Errorf("app_config %s: setting name required", app)
			}
			if len(value) > maxAppConfigValueLen {
				return fmt.Errorf("app_config %s.%s: value longer than %d bytes", app, key, maxAppConfigValueLen)
			}
		}
	}
	return nil
}
