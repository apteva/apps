package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// Migration is transactional and idempotent. Unknown ownership stays empty and
// is never loaded into the broker. Legacy credentials are disabled even when
// ownership is known: their translated ACLs must be reviewed before enabling.
func (a *App) initializeOwnership() error {
	scope := a.ctx.CurrentProject()
	explicit := configString(a.ctx, "legacy_scope_id", "")
	if explicit != "" {
		if !validScope(explicit) || (scope != "" && scope != explicit) {
			return errors.New("legacy_scope_id conflicts with the install ownership scope")
		}
		scope = explicit
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if validScope(scope) {
		rows, err := tx.Query(`SELECT id, username, allow_publish_topics_json, allow_subscribe_topics_json FROM mqtt_users WHERE scope_id = ''`)
		if err != nil {
			return err
		}
		type legacyUser struct {
			id                 int64
			username, pub, sub string
		}
		var users []legacyUser
		for rows.Next() {
			var u legacyUser
			if err := rows.Scan(&u.id, &u.username, &u.pub, &u.sub); err != nil {
				rows.Close()
				return err
			}
			users = append(users, u)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, u := range users {
			username := base64.RawURLEncoding.EncodeToString([]byte(scope)) + ":" + u.username
			pub, err := migrateFilters(scope, u.pub)
			if err != nil {
				return err
			}
			sub, err := migrateFilters(scope, u.sub)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE mqtt_users SET scope_id=?, username=?, enabled=0,
                allow_publish_topics_json=?, allow_subscribe_topics_json=? WHERE id=? AND scope_id=''`, scope, username, pub, sub, u.id); err != nil {
				return err
			}
		}
		// Retained topic names are primary keys; a collision fails the entire
		// transaction rather than replacing any already-owned state.
		for _, table := range []string{"mqtt_retained", "mqtt_message_log", "mqtt_acl_log"} {
			where := ` WHERE scope_id=''`
			if table == "mqtt_acl_log" {
				where += ` AND id <= (SELECT value FROM mqtt_security_state WHERE name='legacy_acl_max_id')`
			}
			if _, err := tx.Exec(`UPDATE `+table+` SET scope_id=?, topic=CASE WHEN topic='' THEN '' ELSE ? || topic END`+where, scope, scopeTopicRoot(scope)); err != nil {
				return err
			}
		}
	}
	// These legacy records already carry an owner. The old "default" fallback
	// is ambiguous; only an explicit/known install scope can assign it.
	for _, table := range []string{"mqtt_subscriptions", "mqtt_devices"} {
		if err := migrateProjectRows(tx, table, scope); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func migrateFilters(scope, encoded string) (string, error) {
	var filters []string
	if err := json.Unmarshal([]byte(encoded), &filters); err != nil {
		return "", fmt.Errorf("invalid legacy MQTT ACL: %w", err)
	}
	out := []string{}
	for _, filter := range filters {
		// Legacy system/shared ACLs have no safe automatic interpretation.
		if filter == "" || filter[0] == '$' {
			continue
		}
		out = append(out, scopeTopicRoot(scope)+filter)
	}
	normalized, err := normalizeScopedFilters(scope, out)
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(normalized)
	return string(result), err
}

func migrateProjectRows(tx *sql.Tx, table, installScope string) error {
	rows, err := tx.Query(`SELECT id, project_id FROM ` + table + ` WHERE scope_id=''`)
	if err != nil {
		return err
	}
	type record struct {
		id    int64
		scope string
	}
	var records []record
	for rows.Next() {
		var row record
		if err := rows.Scan(&row.id, &row.scope); err != nil {
			rows.Close()
			return err
		}
		records = append(records, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, row := range records {
		if row.scope == "" || row.scope == "default" {
			row.scope = installScope
		}
		// A fixed install must not import historical rows for another owner.
		if !validScope(row.scope) || (installScope != "" && row.scope != installScope) {
			continue
		}
		if table == "mqtt_subscriptions" {
			if _, err := tx.Exec(`UPDATE mqtt_subscriptions SET scope_id=?, project_id=?, topic_pattern=? || topic_pattern WHERE id=? AND scope_id=''`, row.scope, row.scope, scopeTopicRoot(row.scope), row.id); err != nil {
				return err
			}
		} else {
			// Discovery is derived metadata. Scope its row, and prefix its
			// known topic fields (including the JSON returned to consumers).
			var encoded string
			if err := tx.QueryRow(`SELECT ha_config_json FROM mqtt_devices WHERE id=?`, row.id).Scan(&encoded); err != nil {
				return err
			}
			var config map[string]any
			if err := json.Unmarshal([]byte(encoded), &config); err != nil {
				return err
			}
			for _, key := range []string{"state_topic", "command_topic", "availability_topic", "json_attributes_topic"} {
				if topic, ok := config[key].(string); ok && topic != "" {
					config[key] = scopeTopicRoot(row.scope) + topic
				}
			}
			updated, err := json.Marshal(config)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE mqtt_devices SET scope_id=?, project_id=?, slug=? || slug,
                state_topic=CASE WHEN state_topic='' THEN '' ELSE ? || state_topic END,
                command_topic=CASE WHEN command_topic='' THEN '' ELSE ? || command_topic END,
                ha_config_json=? WHERE id=? AND scope_id=''`, row.scope, row.scope, scopeTopicRoot(row.scope), scopeTopicRoot(row.scope), scopeTopicRoot(row.scope), string(updated), row.id); err != nil {
				return err
			}
		}
	}
	return nil
}
