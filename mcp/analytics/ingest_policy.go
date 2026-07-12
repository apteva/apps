package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func insertEventWithPolicy(db *sql.DB, ev EventInsert) (int64, error) {
	if ev.Props == "" {
		ev.Props = "{}"
	}
	spec, err := getEventSpec(db, ev.ProjectID, ev.App, ev.Topic)
	if errors.Is(err, sql.ErrNoRows) {
		return insertEventRaw(db, ev, true)
	}
	if err != nil {
		return 0, err
	}
	validation, err := validateEventInsert(db, ev)
	if err != nil {
		return 0, err
	}
	switch spec.IngestMode {
	case "upsert":
		tx, err := db.Begin()
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		next, err := eventFromPolicy(tx, ev, spec.Topic, spec.UpsertPolicy, false)
		if err != nil {
			return 0, err
		}
		id, err := insertEventRawValidated(tx, next, validation)
		if err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return id, nil
	case "raw_plus_rollup":
		tx, err := db.Begin()
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		id, err := insertEventRawValidated(tx, ev, validation)
		if err != nil {
			return 0, err
		}
		rollupTopic := spec.Topic + "_rollup"
		if spec.RollupPolicy != nil && spec.RollupPolicy.TargetTopic != "" {
			rollupTopic = spec.RollupPolicy.TargetTopic
		} else if spec.RollupPolicy != nil && spec.RollupPolicy.Bucket != "" && spec.RollupPolicy.Bucket != "none" {
			rollupTopic = spec.Topic + "_" + spec.RollupPolicy.Bucket + "_rollup"
		}
		rollup, err := eventFromPolicy(tx, ev, rollupTopic, spec.RollupPolicy, true)
		if err != nil {
			return 0, err
		}
		if _, err := insertEventRawValidated(tx, rollup, validationOutcome{}); err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return id, nil
	default:
		return insertEventRaw(db, ev, true)
	}
}

func eventFromPolicy(db sqlRunner, ev EventInsert, targetTopic string, policy *EventIngestPolicy, rollup bool) (EventInsert, error) {
	if policy == nil || policyEmpty(*policy) {
		return EventInsert{}, errors.New("ingest policy required")
	}
	normalizeIngestPolicy(policy)
	if targetTopic == "" {
		targetTopic = ev.Topic
	}
	values := eventValueMap(ev)
	policyTS, err := timestampForPolicy(ev.TS, values, policy)
	if err != nil {
		return EventInsert{}, err
	}
	ev.TS = policyTS
	values["ts"] = policyTS
	bucket, err := bucketForPolicy(ev.TS, policy)
	if err != nil {
		return EventInsert{}, err
	}
	upsertKey, err := computedUpsertKey(ev, targetTopic, policy, bucket, values)
	if err != nil {
		return EventInsert{}, err
	}
	props, err := aggregateProps(db, ev, targetTopic, policy, bucket, values, upsertKey, rollup)
	if err != nil {
		return EventInsert{}, err
	}
	propsJSON, _ := json.Marshal(props)
	source := ev.Source
	if rollup {
		source = "rollup"
	}
	return EventInsert{
		TS:        ev.TS,
		App:       ev.App,
		Topic:     targetTopic,
		ProjectID: ev.ProjectID,
		InstallID: ev.InstallID,
		UserID:    ev.UserID,
		SessionID: ev.SessionID,
		Source:    source,
		UpsertKey: upsertKey,
		Props:     string(propsJSON),
	}, nil
}

type policyBucket struct {
	Name    string
	Label   string
	StartMS int64
}

func bucketForPolicy(ts int64, policy *EventIngestPolicy) (policyBucket, error) {
	name := policy.Bucket
	if name == "" {
		name = "none"
	}
	if ts == 0 {
		ts = time.Now().UnixMilli()
	}
	if name == "none" {
		return policyBucket{Name: "none"}, nil
	}
	loc := time.UTC
	if policy.Timezone != "" && policy.Timezone != "UTC" {
		loaded, err := time.LoadLocation(policy.Timezone)
		if err != nil {
			return policyBucket{}, fmt.Errorf("timezone %q: %w", policy.Timezone, err)
		}
		loc = loaded
	}
	t := time.UnixMilli(ts).In(loc)
	switch name {
	case "hour":
		start := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc)
		return policyBucket{Name: name, Label: start.Format("2006-01-02T15:00"), StartMS: start.UnixMilli()}, nil
	case "day":
		start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		return policyBucket{Name: name, Label: start.Format("2006-01-02"), StartMS: start.UnixMilli()}, nil
	case "week":
		weekday := int(t.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		start := day.AddDate(0, 0, 1-weekday)
		year, week := start.ISOWeek()
		return policyBucket{Name: name, Label: fmt.Sprintf("%04d-W%02d", year, week), StartMS: start.UnixMilli()}, nil
	case "month":
		start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
		return policyBucket{Name: name, Label: start.Format("2006-01"), StartMS: start.UnixMilli()}, nil
	default:
		return policyBucket{}, fmt.Errorf("unsupported bucket %q", name)
	}
}

func computedUpsertKey(ev EventInsert, targetTopic string, policy *EventIngestPolicy, bucket policyBucket, values map[string]any) (string, error) {
	parts := []string{ev.ProjectID, ev.App, targetTopic}
	if bucket.Name != "" && bucket.Name != "none" {
		parts = append(parts, bucket.Name+"="+bucket.Label)
	}
	for _, dim := range policy.Dimensions {
		if dim == "" {
			continue
		}
		if !validEventPropertyKey(dim) {
			return "", fmt.Errorf("unsupported dimension %q", dim)
		}
		value, ok := values[dim]
		if !ok || value == nil || value == "" {
			return "", fmt.Errorf("dimension %q missing", dim)
		}
		parts = append(parts, dim+"="+fmt.Sprint(value))
	}
	if len(parts) == 3 && ev.UpsertKey != "" {
		parts = append(parts, "manual="+ev.UpsertKey)
	}
	return strings.Join(parts, "|"), nil
}

func aggregateProps(db sqlRunner, ev EventInsert, targetTopic string, policy *EventIngestPolicy, bucket policyBucket, values map[string]any, upsertKey string, rollup bool) (map[string]any, error) {
	props := map[string]any{}
	if !rollup {
		props = propsObject(ev.Props)
	}
	if existing := existingUpsertProps(db, ev.ProjectID, ev.App, targetTopic, upsertKey); existing != nil {
		for k, v := range existing {
			props[k] = v
		}
		if !rollup {
			for k, v := range propsObject(ev.Props) {
				props[k] = v
			}
		}
	}
	if bucket.Name != "" && bucket.Name != "none" {
		props[bucket.Name] = bucket.Label
		props["bucket"] = bucket.Name
		props["bucket_start"] = bucket.StartMS
	}
	for _, dim := range policy.Dimensions {
		if dim == "" {
			continue
		}
		props[lastPathSegment(dim)] = values[dim]
	}
	output := strings.TrimPrefix(policy.OutputProperty, "props.")
	if output == "" {
		output = "value"
	}
	incoming, hasIncoming := policyValue(policy, values)
	switch policy.Operation {
	case "replace":
		if hasIncoming {
			props[output] = incoming
		}
	case "increment", "sum":
		inc := 1.0
		if hasIncoming {
			n, ok := numericValue(incoming)
			if !ok {
				return nil, fmt.Errorf("policy value for %s must be numeric", policy.Operation)
			}
			inc = n
		}
		current, _ := numericValue(props[output])
		props[output] = current + inc
	case "min", "max":
		if !hasIncoming {
			return nil, fmt.Errorf("policy value required for %s", policy.Operation)
		}
		in, ok := numericValue(incoming)
		if !ok {
			return nil, fmt.Errorf("policy value for %s must be numeric", policy.Operation)
		}
		current, ok := numericValue(props[output])
		if !ok {
			props[output] = in
		} else if policy.Operation == "min" {
			props[output] = math.Min(current, in)
		} else {
			props[output] = math.Max(current, in)
		}
	}
	return props, nil
}

func existingUpsertProps(db sqlRunner, projectID, app, topic, upsertKey string) map[string]any {
	if upsertKey == "" {
		return nil
	}
	var raw string
	err := db.QueryRow(
		`SELECT props FROM events WHERE project_id=? AND app=? AND topic=? AND upsert_key=?`,
		projectID, app, topic, upsertKey,
	).Scan(&raw)
	if err != nil {
		return nil
	}
	return propsObject(raw)
}

func timestampForPolicy(fallback int64, values map[string]any, policy *EventIngestPolicy) (int64, error) {
	key := strings.TrimSpace(policy.TimestampProperty)
	if key == "" {
		return fallback, nil
	}
	if !validEventPropertyKey(key) {
		return 0, fmt.Errorf("unsupported timestamp_property %q", key)
	}
	raw, ok := values[key]
	if !ok || raw == nil || raw == "" {
		return 0, fmt.Errorf("timestamp_property %q missing", key)
	}
	loc := time.UTC
	if policy.Timezone != "" && policy.Timezone != "UTC" {
		loaded, err := time.LoadLocation(policy.Timezone)
		if err != nil {
			return 0, fmt.Errorf("timezone %q: %w", policy.Timezone, err)
		}
		loc = loaded
	}
	if n, ok := numericValue(raw); ok {
		if n <= 0 || n > math.MaxInt64 {
			return 0, fmt.Errorf("timestamp_property %q must be a positive unix millisecond value", key)
		}
		return int64(n), nil
	}
	s, ok := raw.(string)
	if !ok {
		return 0, fmt.Errorf("timestamp_property %q must be a date, RFC3339 timestamp, or unix milliseconds", key)
	}
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		var parsed time.Time
		var err error
		if layout == time.RFC3339Nano {
			parsed, err = time.Parse(layout, s)
		} else {
			parsed, err = time.ParseInLocation(layout, s, loc)
		}
		if err == nil {
			return parsed.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("timestamp_property %q has invalid value %q", key, s)
}

func propsObject(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func policyValue(policy *EventIngestPolicy, values map[string]any) (any, bool) {
	key := strings.TrimSpace(policy.ValueKey)
	if key != "" {
		v, ok := values[key]
		return v, ok
	}
	if s, ok := policy.Value.(string); ok {
		if validEventPropertyKey(s) {
			v, found := values[s]
			return v, found
		}
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return n, true
		}
		return s, true
	}
	if policy.Value != nil {
		return policy.Value, true
	}
	return nil, false
}

func numericValue(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		n, err := x.Float64()
		return n, err == nil
	case string:
		n, err := strconv.ParseFloat(x, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func lastPathSegment(path string) string {
	path = strings.TrimPrefix(path, "props.")
	if path == "" {
		return "value"
	}
	parts := strings.Split(path, ".")
	return parts[len(parts)-1]
}

func previewEventIngest(db *sql.DB, ev EventInsert) map[string]any {
	spec, err := getEventSpec(db, ev.ProjectID, ev.App, ev.Topic)
	if err != nil {
		return nil
	}
	out := map[string]any{"ingest_mode": spec.IngestMode}
	if spec.IngestMode == "upsert" && spec.UpsertPolicy != nil {
		if next, err := eventFromPolicy(db, ev, spec.Topic, spec.UpsertPolicy, false); err == nil {
			out["upsert"] = map[string]any{
				"topic":      next.Topic,
				"upsert_key": next.UpsertKey,
				"props":      propsObject(next.Props),
			}
		}
	}
	if spec.IngestMode == "raw_plus_rollup" && spec.RollupPolicy != nil {
		target := spec.RollupPolicy.TargetTopic
		if target == "" {
			target = spec.Topic + "_" + spec.RollupPolicy.Bucket + "_rollup"
		}
		if rollup, err := eventFromPolicy(db, ev, target, spec.RollupPolicy, true); err == nil {
			out["rollup"] = map[string]any{
				"topic":      rollup.Topic,
				"upsert_key": rollup.UpsertKey,
				"props":      propsObject(rollup.Props),
			}
		}
	}
	return out
}
