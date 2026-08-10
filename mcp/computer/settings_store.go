package main

import (
	"database/sql"
	"fmt"
	"strings"
)

type ComputerSettings struct {
	DefaultBackend      string `json:"default_backend"`
	LockBackend         bool   `json:"lock_backend"`
	DefaultProxyMode    string `json:"default_proxy_mode"`
	DefaultProxyProfile string `json:"default_proxy_profile_id,omitempty"`
	LockProxyPolicy     bool   `json:"lock_proxy_policy"`
}

var errInvalidBackend = fmt.Errorf("invalid computer backend")

func defaultComputerSettings() ComputerSettings {
	return ComputerSettings{DefaultBackend: "local", DefaultProxyMode: "auto"}
}

func dbGetSettings(db *sql.DB) (ComputerSettings, error) {
	settings := defaultComputerSettings()
	if db == nil {
		return settings, nil
	}
	rows, err := db.Query(`SELECT key, value FROM computer_settings WHERE key IN (
		'default_backend', 'lock_backend', 'default_proxy_mode',
		'default_proxy_profile_id', 'lock_proxy_policy')`)
	if err != nil {
		return settings, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return settings, err
		}
		switch key {
		case "default_backend":
			if value != "" {
				settings.DefaultBackend = value
			}
		case "lock_backend":
			settings.LockBackend = parseSettingBool(value)
		case "default_proxy_mode":
			settings.DefaultProxyMode = normalizeProxyMode(value)
		case "default_proxy_profile_id":
			settings.DefaultProxyProfile = strings.TrimSpace(value)
		case "lock_proxy_policy":
			settings.LockProxyPolicy = parseSettingBool(value)
		}
	}
	if err := rows.Err(); err != nil {
		return settings, err
	}
	settings.DefaultBackend = normalizeBackend(settings.DefaultBackend)
	if !isSessionBackend(settings.DefaultBackend) {
		settings.DefaultBackend = defaultComputerSettings().DefaultBackend
	}
	if !validProxyMode(settings.DefaultProxyMode) {
		settings.DefaultProxyMode = "auto"
	}
	return settings, nil
}

func dbUpdateSettings(db *sql.DB, patch map[string]any) (ComputerSettings, error) {
	if db == nil {
		return defaultComputerSettings(), fmt.Errorf("computer settings unavailable")
	}
	current, err := dbGetSettings(db)
	if err != nil {
		return current, err
	}
	next := current
	if raw, ok := patch["default_backend"]; ok {
		backend, _ := raw.(string)
		backend = normalizeBackend(strings.TrimSpace(backend))
		if !isSessionBackend(backend) {
			return current, fmt.Errorf("%w %q", errInvalidBackend, backend)
		}
		next.DefaultBackend = backend
	}
	if raw, ok := patch["lock_backend"]; ok {
		switch v := raw.(type) {
		case bool:
			next.LockBackend = v
		case string:
			next.LockBackend = parseSettingBool(v)
		}
	}
	if raw, ok := patch["default_proxy_mode"]; ok {
		mode, _ := raw.(string)
		mode = normalizeProxyMode(mode)
		if !validProxyMode(mode) {
			return current, fmt.Errorf("invalid proxy mode %q", mode)
		}
		next.DefaultProxyMode = mode
	}
	if raw, ok := patch["default_proxy_profile_id"]; ok {
		next.DefaultProxyProfile, _ = raw.(string)
		next.DefaultProxyProfile = strings.TrimSpace(next.DefaultProxyProfile)
	}
	if raw, ok := patch["lock_proxy_policy"]; ok {
		switch v := raw.(type) {
		case bool:
			next.LockProxyPolicy = v
		case string:
			next.LockProxyPolicy = parseSettingBool(v)
		}
	}
	if next.DefaultProxyMode == "profile" && next.DefaultProxyProfile == "" {
		return current, fmt.Errorf("default_proxy_profile_id is required when default_proxy_mode=profile")
	}
	now := nowUTC()
	for key, value := range map[string]string{
		"default_backend":          next.DefaultBackend,
		"lock_backend":             formatSettingBool(next.LockBackend),
		"default_proxy_mode":       next.DefaultProxyMode,
		"default_proxy_profile_id": next.DefaultProxyProfile,
		"lock_proxy_policy":        formatSettingBool(next.LockProxyPolicy),
	} {
		if _, err := db.Exec(`
			INSERT INTO computer_settings (key, value, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, value, now,
		); err != nil {
			return current, err
		}
	}
	return next, nil
}

func normalizeProxyMode(mode string) string {
	return strings.ToLower(strings.TrimSpace(mode))
}

func validProxyMode(mode string) bool {
	switch normalizeProxyMode(mode) {
	case "auto", "direct", "managed", "profile":
		return true
	default:
		return false
	}
}

func isSessionBackend(backend string) bool {
	switch backend {
	case "local", "browserbase", "steel", "browser-engine", "service":
		return true
	default:
		return false
	}
}

func isContextBackend(backend string) bool {
	switch backend {
	case "local", "browserbase", "steel", "browser-engine":
		return true
	default:
		return false
	}
}

func parseSettingBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func formatSettingBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
