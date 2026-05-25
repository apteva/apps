package main

import (
	"database/sql"
	"fmt"
	"strings"
)

type ComputerSettings struct {
	DefaultBackend string `json:"default_backend"`
	LockBackend    bool   `json:"lock_backend"`
}

var errInvalidBackend = fmt.Errorf("invalid computer backend")

func defaultComputerSettings() ComputerSettings {
	return ComputerSettings{DefaultBackend: "local", LockBackend: false}
}

func dbGetSettings(db *sql.DB) (ComputerSettings, error) {
	settings := defaultComputerSettings()
	if db == nil {
		return settings, nil
	}
	rows, err := db.Query(`SELECT key, value FROM computer_settings WHERE key IN ('default_backend', 'lock_backend')`)
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
		}
	}
	if err := rows.Err(); err != nil {
		return settings, err
	}
	settings.DefaultBackend = normalizeBackend(settings.DefaultBackend)
	if !isSessionBackend(settings.DefaultBackend) {
		settings.DefaultBackend = defaultComputerSettings().DefaultBackend
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
	now := nowUTC()
	for key, value := range map[string]string{
		"default_backend": next.DefaultBackend,
		"lock_backend":    formatSettingBool(next.LockBackend),
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
