package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Upgrade only aggregates whose identity can be reconstructed. Historical
// arithmetic errors cannot be inferred from the aggregate and are never guessed.
func upgradeLegacyPolicyKeys(db sqlDatabase) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT e.id,e.ts,e.app,e.topic,COALESCE(e.project_id,''),e.upsert_key,e.props,e.source FROM events e WHERE e.upsert_key IS NOT NULL AND e.upsert_key NOT LIKE 'v2:%' AND (e.source='rollup' OR EXISTS(SELECT 1 FROM event_specs s WHERE s.project_id=e.project_id AND s.app=e.app AND s.topic=e.topic AND s.ingest_mode='upsert'))`)
	if err != nil {
		return err
	}
	type legacy struct {
		id int64
		ev EventInsert
	}
	events := []legacy{}
	for rows.Next() {
		var item legacy
		if err = rows.Scan(&item.id, &item.ev.TS, &item.ev.App, &item.ev.Topic, &item.ev.ProjectID, &item.ev.UpsertKey, &item.ev.Props, &item.ev.Source); err != nil {
			rows.Close()
			return err
		}
		events = append(events, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range events {
		policy, err := legacyPolicy(tx, item.ev)
		if err != nil {
			return err
		}
		if policy == nil {
			continue
		}
		props := propsObject(item.ev.Props)
		values := eventValueMap(item.ev)
		ambiguous := false
		for _, dim := range policy.Dimensions {
			if _, ok := values[dim]; !ok {
				leaf := lastPathSegment(dim)
				for _, other := range policy.Dimensions {
					if other != dim && lastPathSegment(other) == leaf {
						ambiguous = true
					}
				}
				if v, ok := props[leaf]; ok {
					values[dim] = v
					setNestedExample(props, strings.TrimPrefix(dim, "props."), v)
				} else {
					ambiguous = true
				}
			}
		}
		bucket, err := bucketForPolicy(item.ev.TS, policy)
		if err != nil {
			return err
		}
		if len(policy.Dimensions) == 0 && bucket.Name == "none" {
			prefix := strings.Join([]string{item.ev.ProjectID, item.ev.App, item.ev.Topic}, "|")
			switch {
			case item.ev.Source == "rollup", item.ev.UpsertKey == prefix:
				item.ev.UpsertKey = ""
			case strings.HasPrefix(item.ev.UpsertKey, prefix+"|manual="):
				item.ev.UpsertKey = strings.TrimPrefix(item.ev.UpsertKey, prefix+"|manual=")
			default:
				ambiguous = true
			}
		}
		key, err := computedUpsertKey(item.ev, item.ev.Topic, policy, bucket, values)
		if ambiguous || err != nil {
			_, err = tx.Exec(`INSERT OR REPLACE INTO analytics_migration_issues VALUES(?,?,?)`, item.id, "Cannot reconstruct legacy aggregate identity; inspect its dimensions before ingesting more observations", time.Now().UnixMilli())
			if err != nil {
				return err
			}
			continue
		}
		raw, err := json.Marshal(props)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE events SET upsert_key=?,props=? WHERE id=?`, key, string(raw), item.id)
		if err != nil {
			return fmt.Errorf("legacy aggregate %d cannot be migrated without resolving a duplicate: %w", item.id, err)
		}
	}
	return tx.Commit()
}
func legacyPolicy(db sqlRunner, ev EventInsert) (*EventIngestPolicy, error) {
	spec, err := getEventSpecLean(db, ev.ProjectID, ev.App, ev.Topic)
	if err == nil && spec.IngestMode == "upsert" {
		return spec.UpsertPolicy, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	rows, err := db.Query(`SELECT topic,rollup_policy FROM event_specs WHERE project_id=? AND app=? AND ingest_mode='raw_plus_rollup'`, ev.ProjectID, ev.App)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var topic string
		var raw sql.NullString
		if err = rows.Scan(&topic, &raw); err != nil {
			return nil, err
		}
		s := &EventSpec{Topic: topic, RollupPolicy: decodeIngestPolicy(raw)}
		if rollupTopic(s) == ev.Topic {
			return s.RollupPolicy, nil
		}
	}
	return nil, rows.Err()
}
