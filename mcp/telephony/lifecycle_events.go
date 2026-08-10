package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const lifecycleSchemaVersion = 1

type lifecycleFacts struct {
	OccurredAt           string
	Source               string
	PreviousStatus       string
	ProviderEventID      string
	ProviderSequence     int64
	DurationSeconds      int
	TerminationCause     string
	TerminationCode      string
	TerminationInitiator string
}

type lifecycleEventRow struct {
	EventID    string
	CallID     string
	ProjectID  string
	Topic      string
	Revision   int64
	OccurredAt string
	Payload    map[string]any
}

type lifecycleCursor struct {
	UpdatedAt string `json:"updated_at,omitempty"`
	ID        string `json:"id,omitempty"`
	Revision  int64  `json:"revision,omitempty"`
}

func (c *callsDB) updateStatus(id, status, errMsg string) error {
	created, err := c.updateStatusWithFacts(id, status, errMsg, lifecycleFacts{Source: "telephony"})
	if err == nil && created && c.afterTransition != nil {
		c.afterTransition(id)
	}
	return err
}

func (c *callsDB) updateStatusWithFacts(id, status, errMsg string, facts lifecycleFacts) (bool, error) {
	status = normalizeCallStatus(status)
	if status == "" {
		return false, nil
	}
	tx, err := c.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	current, err := scanCall(tx.QueryRow(`SELECT `+callSelectColumns+` FROM calls WHERE id = ?`, id))
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	occurredAt := normalizedEventTime(facts.OccurredAt, now)
	lateNonTerminal := isTerminalStatus(current.Status) && !isTerminalStatus(status)
	allowLateFacts := !lateNonTerminal ||
		(facts.Source == "provider" && facts.OccurredAt != "" && eventNotAfter(occurredAt, current.EndedAt))
	nextStatus := current.Status
	transitionAccepted := canTransitionStatus(current.Status, status)
	if transitionAccepted {
		nextStatus = status
	}
	errorToStore := ""
	if transitionAccepted || current.Status == status {
		errorToStore = errMsg
	}

	answeredAt := current.AnsweredAt
	if allowLateFacts && (status == "answered" || status == "in-progress") {
		answeredAt = earliestRFC3339(answeredAt, occurredAt)
	}
	endedAt := current.EndedAt
	if isTerminalStatus(status) {
		endedAt = earliestRFC3339(endedAt, occurredAt)
	}
	providerOccurredAt := current.ProviderOccurredAt
	if facts.Source == "provider" {
		providerOccurredAt = latestRFC3339(providerOccurredAt, occurredAt)
	}
	durationSeconds := current.DurationSeconds
	if facts.DurationSeconds > 0 {
		durationSeconds = facts.DurationSeconds
	}
	if durationSeconds == 0 && endedAt != "" {
		durationSeconds = elapsedSeconds(current.PlacedAt, endedAt)
	}
	talkDurationSeconds := current.TalkDurationSeconds
	if answeredAt != "" && endedAt != "" {
		talkDurationSeconds = elapsedSeconds(answeredAt, endedAt)
	}
	providerSequence := current.ProviderSequence
	if facts.ProviderSequence > providerSequence {
		providerSequence = facts.ProviderSequence
	}
	providerEventID := firstNonEmpty(facts.ProviderEventID, current.ProviderEventID)
	terminationCause := current.TerminationCause
	terminationCode := current.TerminationCode
	terminationInitiator := current.TerminationInitiator
	if !isTerminalStatus(current.Status) || current.Status == status {
		terminationCause = firstNonEmpty(facts.TerminationCause, terminationCause)
		terminationCode = firstNonEmpty(facts.TerminationCode, terminationCode)
		terminationInitiator = firstNonEmpty(facts.TerminationInitiator, terminationInitiator)
	}

	_, err = tx.Exec(`UPDATE calls SET
        status = ?,
        error_message = CASE WHEN ? <> '' THEN ? ELSE error_message END,
        answered_at = NULLIF(?, ''),
        ended_at = NULLIF(?, ''),
        updated_at = ?,
        provider_occurred_at = ?,
        duration_seconds = ?,
        talk_duration_seconds = ?,
        termination_cause = ?,
        termination_code = ?,
        termination_initiator = ?,
        provider_sequence = ?,
        provider_event_id = ?,
        media_active = CASE WHEN ? THEN 0 ELSE media_active END
        WHERE id = ?`,
		nextStatus, errorToStore, errorToStore, answeredAt, endedAt, now.Format(time.RFC3339Nano),
		providerOccurredAt, durationSeconds, talkDurationSeconds, terminationCause,
		terminationCode, terminationInitiator, providerSequence, providerEventID,
		isTerminalStatus(nextStatus), id)
	if err != nil {
		return false, err
	}
	updated, err := scanCall(tx.QueryRow(`SELECT `+callSelectColumns+` FROM calls WHERE id = ?`, id))
	if err != nil {
		return false, err
	}
	topic := publicLifecycleTopic(status)
	if !allowLateFacts {
		topic = ""
	}
	if isTerminalStatus(current.Status) && isTerminalStatus(status) && current.Status != status {
		topic = ""
	}
	created := false
	if topic != "" {
		facts.PreviousStatus = current.Status
		created, err = enqueueLifecycleEventTx(tx, updated, topic, occurredAt, facts)
		if err != nil {
			return false, err
		}
	}
	if isTerminalStatus(nextStatus) {
		if _, err := tx.Exec(`UPDATE inbound_event_outbox
            SET delivered_at = COALESCE(NULLIF(delivered_at, ''), ?), last_error = ''
            WHERE call_id = ?`, now.Format(time.RFC3339Nano), id); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return created, nil
}

func enqueueLifecycleEventTx(tx *sql.Tx, call *callRow, topic, occurredAt string, facts lifecycleFacts) (bool, error) {
	if call == nil || topic == "" {
		return false, nil
	}
	var exists int
	err := tx.QueryRow(`SELECT 1 FROM call_events WHERE call_id = ? AND topic = ?`, call.ID, topic).Scan(&exists)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	call.LifecycleRevision++
	if _, err := tx.Exec(`UPDATE calls SET lifecycle_revision = ? WHERE id = ?`, call.LifecycleRevision, call.ID); err != nil {
		return false, err
	}
	eventID := lifecycleEventID(*call, topic, facts)
	payload := lifecycleEventPublic(*call, eventID, topic, occurredAt, facts)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.Exec(`INSERT INTO call_events
        (event_id, call_id, project_id, topic, revision, occurred_at, payload_json, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, call.ID, call.ProjectID, topic, call.LifecycleRevision, occurredAt, string(encoded), now)
	if err != nil {
		return false, err
	}
	return true, nil
}

func publicLifecycleTopic(status string) string {
	switch status {
	case "initiated":
		return "call.initiated"
	case "ringing":
		return "call.ringing"
	case "answered", "in-progress":
		return "call.answered"
	case "completed", "failed", "busy", "no-answer", "canceled":
		return "call." + strings.ReplaceAll(status, "-", "_")
	default:
		return ""
	}
}

func lifecycleEventID(call callRow, topic string, facts lifecycleFacts) string {
	if facts.ProviderEventID != "" {
		return fmt.Sprintf("%s:%s:%s", firstNonEmpty(call.CarrierSlug, "provider"), facts.ProviderEventID, topic)
	}
	return fmt.Sprintf("telephony:%s:%d:%s", call.ID, call.LifecycleRevision, topic)
}

func lifecycleEventPublic(call callRow, eventID, topic, occurredAt string, facts lifecycleFacts) map[string]any {
	source := firstNonEmpty(facts.Source, "telephony")
	eventStatus := call.Status
	if strings.HasPrefix(topic, "call.") {
		eventStatus = strings.ReplaceAll(strings.TrimPrefix(topic, "call."), "_", "-")
		if eventStatus == "incoming" {
			eventStatus = "pending"
		}
	}
	payload := map[string]any{
		"schema_version":   lifecycleSchemaVersion,
		"event_id":         eventID,
		"topic":            topic,
		"call_id":          call.ID,
		"provider":         call.CarrierSlug,
		"provider_call_id": firstNonEmpty(call.CarrierSID, call.CarrierRequestID),
		"direction":        call.Direction,
		"from_number":      call.FromNumber,
		"to_number":        call.ToNumber,
		"status":           eventStatus,
		"previous_status":  facts.PreviousStatus,
		"agent_id":         call.AgentID,
		"route_id":         call.RouteID,
		"occurred_at":      occurredAt,
		"placed_at":        call.PlacedAt,
		"revision":         call.LifecycleRevision,
		"source":           source,
	}
	addOptionalString(payload, "answered_at", call.AnsweredAt)
	addOptionalString(payload, "ended_at", call.EndedAt)
	addOptionalString(payload, "provider_event_id", facts.ProviderEventID)
	if facts.ProviderSequence > 0 {
		payload["provider_sequence"] = facts.ProviderSequence
	}
	if call.DurationSeconds > 0 {
		payload["duration_seconds"] = call.DurationSeconds
	}
	if call.TalkDurationSeconds > 0 {
		payload["talk_duration_seconds"] = call.TalkDurationSeconds
	}
	if call.ErrorMessage != "" {
		payload["error_message"] = call.ErrorMessage
	}
	termination := map[string]any{}
	addOptionalString(termination, "cause", call.TerminationCause)
	addOptionalString(termination, "code", call.TerminationCode)
	addOptionalString(termination, "initiator", call.TerminationInitiator)
	if len(termination) > 0 {
		payload["termination"] = termination
	}
	return payload
}

func addOptionalString(target map[string]any, key, value string) {
	if value != "" {
		target[key] = value
	}
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

func normalizedEventTime(raw string, fallback time.Time) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback.UTC().Format(time.RFC3339Nano)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return fallback.UTC().Format(time.RFC3339Nano)
}

func earliestRFC3339(current, candidate string) string {
	if current == "" {
		return candidate
	}
	currentTime, currentErr := time.Parse(time.RFC3339Nano, current)
	candidateTime, candidateErr := time.Parse(time.RFC3339Nano, candidate)
	if currentErr != nil {
		currentTime, currentErr = time.Parse(time.RFC3339, current)
	}
	if candidateErr != nil {
		candidateTime, candidateErr = time.Parse(time.RFC3339, candidate)
	}
	if currentErr != nil || candidateErr != nil || candidateTime.Before(currentTime) {
		return candidate
	}
	return current
}

func latestRFC3339(current, candidate string) string {
	if current == "" {
		return candidate
	}
	currentTime, currentErr := time.Parse(time.RFC3339Nano, current)
	candidateTime, candidateErr := time.Parse(time.RFC3339Nano, candidate)
	if currentErr != nil {
		currentTime, currentErr = time.Parse(time.RFC3339, current)
	}
	if candidateErr != nil {
		candidateTime, candidateErr = time.Parse(time.RFC3339, candidate)
	}
	if currentErr != nil || candidateErr != nil || candidateTime.After(currentTime) {
		return candidate
	}
	return current
}

func elapsedSeconds(start, end string) int {
	startTime, err := time.Parse(time.RFC3339Nano, start)
	if err != nil {
		startTime, err = time.Parse(time.RFC3339, start)
	}
	if err != nil {
		return 0
	}
	endTime, err := time.Parse(time.RFC3339Nano, end)
	if err != nil {
		endTime, err = time.Parse(time.RFC3339, end)
	}
	if err != nil || endTime.Before(startTime) {
		return 0
	}
	return int(endTime.Sub(startTime).Round(time.Second) / time.Second)
}

func eventNotAfter(occurredAt, terminalAt string) bool {
	if terminalAt == "" {
		return true
	}
	occurred, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		occurred, err = time.Parse(time.RFC3339, occurredAt)
	}
	if err != nil {
		return false
	}
	terminal, err := time.Parse(time.RFC3339Nano, terminalAt)
	if err != nil {
		terminal, err = time.Parse(time.RFC3339, terminalAt)
	}
	return err == nil && !occurred.After(terminal)
}

func (a *App) publishLifecycleEvents(ctx *sdk.AppCtx, callID string) error {
	if ctx == nil {
		return errors.New("app context unavailable")
	}
	rows, err := ctx.AppDB().Query(`SELECT event_id, call_id, project_id, topic, revision, occurred_at, payload_json
        FROM call_events
        WHERE published_at = '' AND (? = '' OR call_id = ?)
        ORDER BY created_at, event_id LIMIT 100`, callID, callID)
	if err != nil {
		return err
	}
	var events []lifecycleEventRow
	for rows.Next() {
		var event lifecycleEventRow
		var payloadJSON string
		if err := rows.Scan(&event.EventID, &event.CallID, &event.ProjectID, &event.Topic,
			&event.Revision, &event.OccurredAt, &payloadJSON); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(payloadJSON), &event.Payload); err != nil {
			rows.Close()
			return fmt.Errorf("decode lifecycle event %s: %w", event.EventID, err)
		}
		events = append(events, event)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, event := range events {
		ctx.WithProject(event.ProjectID).Emit(event.Topic, event.Payload)
		if _, err := ctx.AppDB().Exec(`UPDATE call_events SET published_at = ?
            WHERE event_id = ? AND published_at = ''`, time.Now().UTC().Format(time.RFC3339Nano), event.EventID); err != nil {
			return err
		}
	}
	return nil
}

func (c *callsDB) listLifecycleEvents(callID string, afterRevision int64, limit int) ([]lifecycleEventRow, error) {
	rows, err := c.db.Query(`SELECT event_id, call_id, project_id, topic, revision, occurred_at, payload_json
        FROM call_events WHERE call_id = ? AND revision > ?
        ORDER BY revision LIMIT ?`, callID, afterRevision, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lifecycleEventRow
	for rows.Next() {
		var event lifecycleEventRow
		var payloadJSON string
		if err := rows.Scan(&event.EventID, &event.CallID, &event.ProjectID, &event.Topic,
			&event.Revision, &event.OccurredAt, &payloadJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payloadJSON), &event.Payload); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (a *App) toolCallsList(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID := currentProject(ctx)
	if projectID == "" {
		return mcpError("project context required"), nil
	}
	limit := intArg(args, "limit", 50)
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	cursor, err := decodeLifecycleCursor(strArg(args, "cursor", ""))
	if err != nil {
		return mcpError("invalid cursor"), nil
	}
	updatedSince := strings.TrimSpace(strArg(args, "updated_since", ""))
	if updatedSince != "" {
		parsed, err := time.Parse(time.RFC3339, updatedSince)
		if err != nil {
			return mcpError("updated_since must be RFC3339"), nil
		}
		updatedSince = parsed.UTC().Format(time.RFC3339Nano)
	}
	status := normalizeCallStatus(strArg(args, "status", ""))
	if raw := strings.TrimSpace(strArg(args, "status", "")); raw != "" && status == "" {
		return mcpError("unsupported call status"), nil
	}
	direction := strings.ToLower(strings.TrimSpace(strArg(args, "direction", "")))
	if direction != "" && direction != "inbound" && direction != "outbound" {
		return mcpError("direction must be inbound or outbound"), nil
	}
	providerCallID := strings.TrimSpace(strArg(args, "provider_call_id", ""))
	rows, err := a.db().listCallsForReconciliation(projectID, updatedSince, direction, status, providerCallID, cursor, limit+1)
	if err != nil {
		return mcpError("list calls: " + err.Error()), nil
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	nextCursor := ""
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		nextCursor = encodeLifecycleCursor(lifecycleCursor{UpdatedAt: last.UpdatedAt, ID: last.ID})
	}
	return map[string]any{
		"calls":       reconciliationCallsPublic(rows),
		"has_more":    hasMore,
		"next_cursor": nextCursor,
	}, nil
}

func (a *App) toolCallGet(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID := currentProject(ctx)
	callID := strings.TrimSpace(strArg(args, "call_id", ""))
	providerCallID := strings.TrimSpace(strArg(args, "provider_call_id", ""))
	if projectID == "" {
		return mcpError("project context required"), nil
	}
	if (callID == "") == (providerCallID == "") {
		return mcpError("provide exactly one of call_id or provider_call_id"), nil
	}
	call, err := a.db().findCallForProject(projectID, callID, providerCallID)
	if err != nil {
		return mcpError("get call: " + err.Error()), nil
	}
	if call == nil {
		return mcpError("call not found"), nil
	}
	return map[string]any{"call": reconciliationCallPublic(*call)}, nil
}

func (a *App) toolCallEventsList(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID := currentProject(ctx)
	callID := strings.TrimSpace(strArg(args, "call_id", ""))
	if projectID == "" || callID == "" {
		return mcpError("project context and call_id required"), nil
	}
	call, err := a.db().findCallForProject(projectID, callID, "")
	if err != nil {
		return mcpError("get call: " + err.Error()), nil
	}
	if call == nil {
		return mcpError("call not found"), nil
	}
	cursor, err := decodeLifecycleCursor(strArg(args, "cursor", ""))
	if err != nil {
		return mcpError("invalid cursor"), nil
	}
	limit := intArg(args, "limit", 100)
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	events, err := a.db().listLifecycleEvents(callID, cursor.Revision, limit+1)
	if err != nil {
		return mcpError("list call events: " + err.Error()), nil
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	public := make([]map[string]any, 0, len(events))
	for _, event := range events {
		public = append(public, event.Payload)
	}
	nextCursor := ""
	if hasMore && len(events) > 0 {
		nextCursor = encodeLifecycleCursor(lifecycleCursor{Revision: events[len(events)-1].Revision})
	}
	return map[string]any{"events": public, "has_more": hasMore, "next_cursor": nextCursor}, nil
}

func (c *callsDB) listCallsForReconciliation(projectID, updatedSince, direction, status, providerCallID string, cursor lifecycleCursor, limit int) ([]callRow, error) {
	query := `project_id = ?`
	args := []any{projectID}
	if updatedSince != "" {
		query += ` AND updated_at >= ?`
		args = append(args, updatedSince)
	}
	if direction != "" {
		query += ` AND direction = ?`
		args = append(args, direction)
	}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if providerCallID != "" {
		query += ` AND (carrier_sid = ? OR carrier_request_id = ?)`
		args = append(args, providerCallID, providerCallID)
	}
	if cursor.UpdatedAt != "" {
		query += ` AND (updated_at > ? OR (updated_at = ? AND id > ?))`
		args = append(args, cursor.UpdatedAt, cursor.UpdatedAt, cursor.ID)
	}
	query += ` ORDER BY updated_at, id LIMIT ?`
	args = append(args, limit)
	return c.listWhere(query, args...)
}

func (c *callsDB) findCallForProject(projectID, callID, providerCallID string) (*callRow, error) {
	query := `SELECT ` + callSelectColumns + ` FROM calls WHERE project_id = ?`
	args := []any{projectID}
	if callID != "" {
		query += ` AND id = ?`
		args = append(args, callID)
	} else {
		query += ` AND (carrier_sid = ? OR carrier_request_id = ?) ORDER BY updated_at DESC LIMIT 1`
		args = append(args, providerCallID, providerCallID)
	}
	call, err := scanCall(c.db.QueryRow(query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return call, err
}

func reconciliationCallsPublic(rows []callRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, reconciliationCallPublic(row))
	}
	return out
}

func reconciliationCallPublic(call callRow) map[string]any {
	payload := lifecycleEventPublic(call, "", "", call.UpdatedAt, lifecycleFacts{})
	delete(payload, "event_id")
	delete(payload, "topic")
	delete(payload, "occurred_at")
	delete(payload, "source")
	payload["updated_at"] = call.UpdatedAt
	payload["thread_id"] = call.ThreadID
	return payload
}

func encodeLifecycleCursor(cursor lifecycleCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeLifecycleCursor(raw string) (lifecycleCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return lifecycleCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return lifecycleCursor{}, err
	}
	var cursor lifecycleCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return lifecycleCursor{}, err
	}
	if cursor.Revision < 0 {
		return lifecycleCursor{}, errors.New("invalid revision")
	}
	return cursor, nil
}

type callbackUpdate struct {
	Status     string
	Error      string
	CarrierSID string
	Facts      lifecycleFacts
}

func callbackUpdateFor(carrier string, r *http.Request) callbackUpdate {
	if carrier == "telnyx" {
		return telnyxCallbackUpdate(r)
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "json") {
		var body map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			return callbackUpdate{}
		}
		duration := intValue(body["duration"])
		return callbackUpdate{
			Status:     normalizeCallStatus(firstString(body, "CallStatus", "Status", "status", "Event", "event")),
			Error:      firstString(body, "ErrorMessage", "error", "reason", "StatusReason"),
			CarrierSID: firstString(body, "CallSid", "CallUUID", "uuid", "call_uuid"),
			Facts: lifecycleFacts{
				OccurredAt:           firstString(body, "Timestamp", "timestamp", "EventTime", "event_time"),
				Source:               "provider",
				ProviderEventID:      firstString(body, "EventId", "EventUUID", "event_id"),
				ProviderSequence:     int64(intValue(body["sequence"])),
				DurationSeconds:      duration,
				TerminationCause:     firstString(body, "HangupCause", "reason", "StatusReason"),
				TerminationCode:      firstString(body, "SipResponseCode", "ErrorCode"),
				TerminationInitiator: firstString(body, "HangupSource", "hangup_source"),
			},
		}
	}
	status, errMsg := callbackStatus(r)
	sequence, _ := strconv.ParseInt(firstNonEmpty(r.FormValue("SequenceNumber"), r.FormValue("sequence")), 10, 64)
	duration, _ := strconv.Atoi(firstNonEmpty(r.FormValue("CallDuration"), r.FormValue("Duration"), r.FormValue("duration")))
	carrierSID := firstNonEmpty(r.FormValue("CallSid"), r.FormValue("CallUUID"), r.FormValue("uuid"))
	providerEventID := firstNonEmpty(r.FormValue("EventId"), r.FormValue("EventUUID"), r.FormValue("event_id"))
	if providerEventID == "" && carrierSID != "" && sequence > 0 {
		providerEventID = fmt.Sprintf("%s:%d", carrierSID, sequence)
	}
	terminationCause := firstNonEmpty(r.FormValue("HangupCauseName"), r.FormValue("HangupCause"), r.FormValue("StatusReason"))
	terminationInitiator := firstNonEmpty(r.FormValue("HangupSource"), r.FormValue("hangup_source"))
	return callbackUpdate{
		Status: status, Error: providerCallbackError(status, errMsg, terminationCause, terminationInitiator), CarrierSID: carrierSID,
		Facts: lifecycleFacts{
			OccurredAt:           firstNonEmpty(r.FormValue("Timestamp"), r.FormValue("EventTime"), r.FormValue("event_time")),
			Source:               "provider",
			ProviderEventID:      providerEventID,
			ProviderSequence:     sequence,
			DurationSeconds:      duration,
			TerminationCause:     terminationCause,
			TerminationCode:      firstNonEmpty(r.FormValue("SipResponseCode"), r.FormValue("ErrorCode")),
			TerminationInitiator: terminationInitiator,
		},
	}
}

func telnyxCallbackUpdate(r *http.Request) callbackUpdate {
	var body struct {
		Data struct {
			ID         string `json:"id"`
			EventType  string `json:"event_type"`
			OccurredAt string `json:"occurred_at"`
			Payload    struct {
				CallControlID string `json:"call_control_id"`
				HangupCause   string `json:"hangup_cause"`
				HangupSource  string `json:"hangup_source"`
				SIPCode       string `json:"sip_hangup_cause"`
			} `json:"payload"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		return callbackUpdate{Error: "invalid Telnyx callback"}
	}
	status := telnyxStatusFromEvent(body.Data.EventType, body.Data.Payload.HangupCause)
	return callbackUpdate{
		Status: status, Error: providerCallbackError(status, "", body.Data.Payload.HangupCause, body.Data.Payload.HangupSource),
		CarrierSID: body.Data.Payload.CallControlID,
		Facts: lifecycleFacts{
			OccurredAt:           body.Data.OccurredAt,
			Source:               "provider",
			ProviderEventID:      body.Data.ID,
			TerminationCause:     body.Data.Payload.HangupCause,
			TerminationCode:      body.Data.Payload.SIPCode,
			TerminationInitiator: body.Data.Payload.HangupSource,
		},
	}
}

func providerCallbackError(status, explicit, cause, initiator string) string {
	if explicit != "" {
		return explicit
	}
	if status == "completed" {
		return ""
	}
	return firstNonEmpty(cause, initiator)
}

func telnyxStatusFromEvent(eventType, hangupCause string) string {
	switch eventType {
	case "call.initiated":
		return "initiated"
	case "call.ringing":
		return "ringing"
	case "call.answered":
		return "answered"
	case "streaming.started", "call.streaming.started":
		return "in-progress"
	case "streaming.stopped", "call.streaming.stopped":
		return "media-disconnected"
	case "streaming.failed", "call.streaming.failed":
		return "failed"
	case "call.hangup":
		return telnyxHangupStatus(hangupCause)
	default:
		return ""
	}
}
