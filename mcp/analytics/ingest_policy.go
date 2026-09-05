package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type rejectedEventError struct{ Outcome validationOutcome }

func (e *rejectedEventError) Error() string {
	return "event rejected by spec: " + joinViolationMessages(e.Outcome.Violations)
}

func prepareEvent(db sqlRunner, ev EventInsert) (EventInsert, validationOutcome, *EventSpec, error) {
	if ev.TS == 0 {
		ev.TS = time.Now().UnixMilli()
	}
	if ev.Source == "" {
		ev.Source = "track"
	}
	if ev.Props == "" {
		ev.Props = "{}"
	}
	var obj map[string]any
	if len(ev.Props) > 256*1024 || json.Unmarshal([]byte(ev.Props), &obj) != nil || obj == nil {
		return ev, validationOutcome{}, nil, errors.New("props must be a JSON object no larger than 256 KiB")
	}
	if len(ev.App) > 256 || len(ev.Topic) > 256 || len(ev.UpsertKey) > 512 || len(ev.DeliveryID) > 512 {
		return ev, validationOutcome{}, nil, errors.New("event identifier exceeds length limit")
	}
	spec, err := getEventSpecLean(db, ev.ProjectID, ev.App, ev.Topic)
	if errors.Is(err, sql.ErrNoRows) {
		spec = nil
	} else if err != nil {
		return ev, validationOutcome{}, nil, err
	}
	if spec != nil {
		var unresolved int
		if err := db.QueryRow(`SELECT COUNT(*) FROM analytics_migration_issues i JOIN events e ON e.id=i.event_id WHERE e.project_id=? AND e.app=? AND (e.topic=? OR e.topic=?)`, ev.ProjectID, ev.App, ev.Topic, rollupTopic(spec)).Scan(&unresolved); err != nil {
			return ev, validationOutcome{}, spec, err
		}
		if unresolved > 0 {
			return ev, validationOutcome{}, spec, errors.New("legacy aggregate identities require repair; see diagnostics")
		}
		switch spec.IngestMode {
		case "upsert":
			ev, err = eventFromPolicy(db, ev, spec.Topic, spec.UpsertPolicy, false)
		case "raw_plus_rollup":
			_, err = eventFromPolicy(db, ev, rollupTopic(spec), spec.RollupPolicy, true)
		}
		if err != nil {
			return ev, validationOutcome{}, spec, err
		}
	}
	out, err := validatePreparedEvent(db, ev, spec)
	return ev, out, spec, err
}

func rollupTopic(spec *EventSpec) string {
	if spec.RollupPolicy != nil {
		if spec.RollupPolicy.TargetTopic != "" {
			return spec.RollupPolicy.TargetTopic
		}
		if b := spec.RollupPolicy.Bucket; b != "" && b != "none" {
			return spec.Topic + "_" + b + "_rollup"
		}
	}
	return spec.Topic + "_rollup"
}

func insertEventWithPolicy(db sqlDatabase, original EventInsert) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(mustJSON(original))))
	if original.DeliveryID != "" {
		var id int64
		var previous string
		err = tx.QueryRow(`SELECT event_id,fingerprint FROM ingest_receipts WHERE project_id=? AND app=? AND topic=? AND delivery_id=?`, original.ProjectID, original.App, original.Topic, original.DeliveryID).Scan(&id, &previous)
		if err == nil {
			if previous != fingerprint {
				return 0, errors.New("delivery_id already used with different event data")
			}
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	next, validation, spec, err := prepareEvent(tx, original)
	if err != nil {
		return 0, err
	}
	if validation.Reject {
		if err = recordEventSpecViolations(tx, 0, validation.Violations); err != nil {
			return 0, err
		}
		if err = tx.Commit(); err != nil {
			return 0, err
		}
		return 0, &rejectedEventError{validation}
	}
	// A corrected raw upsert needs both its old and new rollup groups rebuilt.
	var previous *EventInsert
	if spec != nil && spec.IngestMode == "raw_plus_rollup" && original.UpsertKey != "" {
		old := original
		err = tx.QueryRow(`SELECT ts,props FROM events WHERE project_id=? AND app=? AND topic=? AND upsert_key=?`, original.ProjectID, original.App, original.Topic, original.UpsertKey).Scan(&old.TS, &old.Props)
		if err == nil {
			previous = &old
		} else if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	id, err := insertEventRawValidated(tx, next, validation)
	if err != nil {
		return 0, err
	}
	if spec != nil && spec.IngestMode == "raw_plus_rollup" {
		if previous != nil {
			err = rebuildCorrectedRollups(tx, original, *previous, spec)
		} else {
			var rollup EventInsert
			rollup, err = eventFromPolicy(tx, next, rollupTopic(spec), spec.RollupPolicy, true)
			if err == nil {
				_, err = insertEventRawValidated(tx, rollup, validationOutcome{})
			}
		}
		if err != nil {
			return 0, err
		}
	}
	if original.DeliveryID != "" {
		_, err = tx.Exec(`INSERT INTO ingest_receipts VALUES(?,?,?,?,?,?,?)`, original.ProjectID, original.App, original.Topic, original.DeliveryID, id, fingerprint, time.Now().UnixMilli())
		if err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// Corrections are uncommon. Rebuild only affected buckets from original raw
// events so min/max, timestamp moves and dimension changes remain exact.
func rebuildCorrectedRollups(tx *sql.Tx, current, old EventInsert, spec *EventSpec) error {
	keys := map[string]bool{}
	for _, ev := range []EventInsert{current, old} {
		next, err := eventFromPolicy(tx, ev, rollupTopic(spec), spec.RollupPolicy, true)
		if err != nil {
			return err
		}
		keys[next.UpsertKey] = true
	}
	for key := range keys {
		if _, err := tx.Exec(`DELETE FROM events WHERE project_id=? AND app=? AND topic=? AND upsert_key=?`, current.ProjectID, current.App, rollupTopic(spec), key); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT ts,props FROM events WHERE project_id=? AND app=? AND topic=? ORDER BY ts,id`, current.ProjectID, current.App, current.Topic)
	if err != nil {
		return err
	}
	events := []EventInsert{}
	for rows.Next() {
		ev := current
		ev.UpsertKey = ""
		if err = rows.Scan(&ev.TS, &ev.Props); err != nil {
			rows.Close()
			return err
		}
		events = append(events, ev)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ev := range events {
		next, err := eventFromPolicy(tx, ev, rollupTopic(spec), spec.RollupPolicy, true)
		if err != nil {
			return err
		}
		if keys[next.UpsertKey] {
			if _, err = insertEventRawValidated(tx, next, validationOutcome{}); err != nil {
				return err
			}
		}
	}
	return nil
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
	identityEvent := ev
	if rollup {
		identityEvent.UpsertKey = ""
	}
	upsertKey, err := computedUpsertKey(identityEvent, targetTopic, policy, bucket, values)
	if err != nil {
		return EventInsert{}, err
	}
	props, err := aggregateProps(db, ev, targetTopic, policy, bucket, values, upsertKey, rollup)
	if err != nil {
		return EventInsert{}, err
	}
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return EventInsert{}, fmt.Errorf("policy result: %w", err)
	}
	source := ev.Source
	if rollup {
		source = "rollup"
	}
	return EventInsert{
		DeliveryID: ev.DeliveryID,
		TS:         ev.TS,
		App:        ev.App,
		Topic:      targetTopic,
		ProjectID:  ev.ProjectID,
		InstallID:  ev.InstallID,
		UserID:     ev.UserID,
		SessionID:  ev.SessionID,
		Source:     source,
		UpsertKey:  upsertKey,
		Props:      string(propsJSON),
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
		start := t.Add(-time.Duration(t.Minute())*time.Minute - time.Duration(t.Second())*time.Second - time.Duration(t.Nanosecond()))
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
	parts := []any{ev.ProjectID, ev.App, targetTopic, bucket.Name, bucket.StartMS, policy.Timezone}
	dimensions := append([]string{}, policy.Dimensions...)
	sort.Strings(dimensions)
	for _, dim := range dimensions {
		if !validEventPropertyKey(dim) {
			return "", fmt.Errorf("unsupported dimension %q", dim)
		}
		value, ok := values[dim]
		if !ok || value == nil || value == "" {
			return "", fmt.Errorf("dimension %q missing", dim)
		}
		switch value.(type) {
		case string, bool, float64, int64, int, json.Number:
		default:
			return "", fmt.Errorf("dimension %q must be scalar", dim)
		}
		parts = append(parts, []any{dim, value})
	}
	if len(policy.Dimensions) == 0 && bucket.Name == "none" && ev.UpsertKey != "" {
		parts = append(parts, ev.UpsertKey)
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("v2:%x", sha256.Sum256(raw)), nil
}

func aggregateProps(db sqlRunner, ev EventInsert, targetTopic string, policy *EventIngestPolicy, bucket policyBucket, values map[string]any, upsertKey string, rollup bool) (map[string]any, error) {
	props := map[string]any{}
	if !rollup {
		props = propsObject(ev.Props)
	}
	existing, err := existingUpsertProps(db, ev.ProjectID, ev.App, targetTopic, upsertKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
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
		setNestedExample(props, strings.TrimPrefix(dim, "props."), values[dim])
	}
	output := strings.TrimPrefix(policy.OutputProperty, "props.")
	if output == "" {
		output = "value"
	}
	previous := eventValueMap(EventInsert{Props: mustJSON(existing)})["props."+output]
	incoming, hasIncoming := policyValue(policy, values)
	switch policy.Operation {
	case "replace":
		if hasIncoming {
			setNestedExample(props, output, incoming)
		}
	case "increment", "sum":
		if !hasIncoming && (policy.Operation == "sum" || policy.ValueKey != "" || policy.Value != nil) {
			return nil, errors.New("policy operand missing")
		}
		inc := 1.0
		if hasIncoming {
			n, ok := numericValue(incoming)
			if !ok {
				return nil, fmt.Errorf("policy value for %s must be numeric", policy.Operation)
			}
			inc = n
		}
		current, _ := numericValue(previous)
		setNestedExample(props, output, current+inc)
	case "min", "max":
		if !hasIncoming {
			return nil, fmt.Errorf("policy value required for %s", policy.Operation)
		}
		in, ok := numericValue(incoming)
		if !ok {
			return nil, fmt.Errorf("policy value for %s must be numeric", policy.Operation)
		}
		current, ok := numericValue(previous)
		if !ok {
			setNestedExample(props, output, in)
		} else if policy.Operation == "min" {
			setNestedExample(props, output, math.Min(current, in))
		} else {
			setNestedExample(props, output, math.Max(current, in))
		}
	}
	return props, nil
}

func existingUpsertProps(db sqlRunner, projectID, app, topic, upsertKey string) (map[string]any, error) {
	if upsertKey == "" {
		return nil, nil
	}
	var raw string
	err := db.QueryRow(
		`SELECT props FROM events WHERE project_id=? AND app=? AND topic=? AND upsert_key=?`,
		projectID, app, topic, upsertKey,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return propsObject(raw), nil
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
		if n <= 0 || n >= float64(math.MaxInt64) {
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
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case float32:
		return float64(x), !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0)
	case int:
		return float64(x), !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0)
	case int64:
		return float64(x), !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0)
	case json.Number:
		n, err := x.Float64()
		return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
	case string:
		n, err := strconv.ParseFloat(x, 64)
		return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
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

func previewEventIngest(db sqlRunner, ev EventInsert) map[string]any {
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
			target = rollupTopic(spec)
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

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
