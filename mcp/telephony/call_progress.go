package main

// Call progress, termination reasons, and answering machine detection.
//
// Every carrier reports progress differently. This file gives consumers one
// model: initiated → ringing → answered → terminal status, a fixed set of
// termination reasons next to the raw carrier facts, and a normalized
// answered_by value when answering machine detection is enabled. Nothing here
// changes the five terminal statuses; the richer vocabulary lives in
// termination.reason only.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	terminationCompleted     = "completed"
	terminationBusy          = "busy"
	terminationNoAnswer      = "no_answer"
	terminationRejected      = "rejected"
	terminationInvalidNumber = "invalid_number"
	terminationUnreachable   = "unreachable"
	terminationCanceled      = "canceled"
	terminationTimeLimit     = "time_limit"
	terminationFailed        = "failed"

	answeredByHuman   = "human"
	answeredByMachine = "machine"
	answeredByFax     = "fax"
	answeredBySilence = "silence"
	answeredByUnknown = "unknown"

	machineDetectionOff     = "off"
	machineDetectionDetect  = "detect"
	machineDetectionPremium = "premium"

	machineDetectionNotify = "notify"
	machineDetectionHangup = "hangup"

	topicMachineDetected = "call.machine_detected"
)

// terminationReasonFor maps a terminal status plus the raw carrier cause and
// SIP code onto the fixed reason vocabulary. The raw values stay alongside it.
func terminationReasonFor(status, cause, code string) string {
	normalized := strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(strings.TrimSpace(cause)))
	code = strings.TrimSpace(code)
	timeLimit := strings.Contains(normalized, "time_limit") || strings.Contains(normalized, "max_duration") || strings.Contains(normalized, "timelimit")
	switch status {
	case "completed":
		if timeLimit {
			return terminationTimeLimit
		}
		return terminationCompleted
	case "busy":
		return terminationBusy
	case "no-answer":
		return terminationNoAnswer
	case "canceled":
		return terminationCanceled
	case "failed":
		switch {
		case timeLimit:
			return terminationTimeLimit
		case strings.Contains(normalized, "reject"), strings.Contains(normalized, "decline"), code == "603", code == "403":
			return terminationRejected
		case strings.Contains(normalized, "unallocated"), strings.Contains(normalized, "invalid_number"), strings.Contains(normalized, "invalid_dest"),
			strings.Contains(normalized, "not_found"), code == "404", code == "484":
			return terminationInvalidNumber
		case strings.Contains(normalized, "unreachable"), strings.Contains(normalized, "no_route"), strings.Contains(normalized, "network"),
			strings.Contains(normalized, "no_user_response"), strings.Contains(normalized, "unavailable"), strings.Contains(normalized, "congestion"),
			code == "480", code == "502", code == "503", code == "504":
			return terminationUnreachable
		case strings.Contains(normalized, "busy"), code == "486":
			return terminationBusy
		case strings.Contains(normalized, "no_answer"), code == "408":
			return terminationNoAnswer
		case strings.Contains(normalized, "cancel"), code == "487":
			return terminationCanceled
		default:
			return terminationFailed
		}
	}
	return ""
}

// normalizeAnsweredBy maps carrier detection results (Twilio AnsweredBy,
// Telnyx result, Plivo Machine) onto the shared answered_by set.
func normalizeAnsweredBy(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case value == "":
		return ""
	case strings.HasPrefix(value, "human"), value == "false":
		return answeredByHuman
	case strings.Contains(value, "machine"), value == "true":
		return answeredByMachine
	case strings.Contains(value, "fax"):
		return answeredByFax
	case strings.Contains(value, "silence"):
		return answeredBySilence
	default:
		return answeredByUnknown
	}
}

// carrierNeedsSyntheticRinging reports carriers that accept an outbound dial
// but never send a ringing webhook. Telephony then synthesizes call.ringing
// once the carrier confirms the dial, so consumers see one progress model.
func carrierNeedsSyntheticRinging(carrier string) bool {
	return strings.EqualFold(strings.TrimSpace(carrier), "telnyx")
}

func machineDetectionSupported(carrier string) bool {
	switch strings.ToLower(strings.TrimSpace(carrier)) {
	case "twilio", "signalwire", "telnyx", "plivo":
		return true
	default:
		return false
	}
}

func normalizeMachineDetection(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", machineDetectionOff:
		return machineDetectionOff, nil
	case machineDetectionDetect, machineDetectionPremium:
		return mode, nil
	}
	return "", errors.New("machine_detection must be off, detect, or premium")
}

func normalizeMachineDetectionAction(action string) (string, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "", machineDetectionNotify:
		return machineDetectionNotify, nil
	case machineDetectionHangup:
		return action, nil
	}
	return "", errors.New("machine_detection_action must be notify or hangup")
}

// ─── install-level outbound defaults ────────────────────────────────

type outboundSettings struct {
	ProjectID              string `json:"project_id"`
	MachineDetection       string `json:"machine_detection"`
	MachineDetectionAction string `json:"machine_detection_action"`
	// DefaultTimeoutSec is the ring timeout used when a caller omits
	// timeout_sec. Zero keeps the built-in defaults (30 s for agent calls,
	// 60 s for the softphone).
	DefaultTimeoutSec int    `json:"default_timeout_sec"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

func defaultOutboundSettings(projectID string) outboundSettings {
	return outboundSettings{ProjectID: projectID, MachineDetection: machineDetectionOff, MachineDetectionAction: machineDetectionNotify}
}

func (c *callsDB) outboundSettings(projectID string) (outboundSettings, error) {
	settings := defaultOutboundSettings(projectID)
	err := c.db.QueryRow(`SELECT machine_detection, machine_detection_action, COALESCE(default_timeout_sec, 0), updated_at
        FROM outbound_settings WHERE project_id = ?`, projectID).
		Scan(&settings.MachineDetection, &settings.MachineDetectionAction, &settings.DefaultTimeoutSec, &settings.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	return settings, err
}

func (c *callsDB) saveOutboundSettings(settings outboundSettings) error {
	settings.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_, err := c.db.Exec(`INSERT INTO outbound_settings (project_id, machine_detection, machine_detection_action, default_timeout_sec, updated_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(project_id) DO UPDATE SET machine_detection = excluded.machine_detection,
          machine_detection_action = excluded.machine_detection_action,
          default_timeout_sec = excluded.default_timeout_sec, updated_at = excluded.updated_at`,
		settings.ProjectID, settings.MachineDetection, settings.MachineDetectionAction, settings.DefaultTimeoutSec, settings.UpdatedAt)
	return err
}

func outboundSettingsPublic(settings outboundSettings) map[string]any {
	return map[string]any{
		"project_id":               settings.ProjectID,
		"machine_detection":        settings.MachineDetection,
		"machine_detection_action": settings.MachineDetectionAction,
		"default_timeout_sec":      settings.DefaultTimeoutSec,
		"updated_at":               settings.UpdatedAt,
	}
}

// outboundTimeoutDefault returns the project ring timeout for callers that
// omit timeout_sec, or the built-in default when none is configured.
func (a *App) outboundTimeoutDefault(projectID string, builtin int) int {
	settings, err := a.db().outboundSettings(projectID)
	if err != nil || settings.DefaultTimeoutSec <= 0 {
		return builtin
	}
	return settings.DefaultTimeoutSec
}

func (a *App) toolOutboundSettingsGet(_ context.Context, ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	settings, err := a.db().outboundSettings(currentProject(ctx))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return outboundSettingsPublic(settings), nil
}

func (a *App) toolOutboundSettingsSet(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	settings, err := a.db().outboundSettings(currentProject(ctx))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if value, ok := args["machine_detection"].(string); ok {
		mode, err := normalizeMachineDetection(value)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		settings.MachineDetection = mode
	}
	if value, ok := args["machine_detection_action"].(string); ok {
		action, err := normalizeMachineDetectionAction(value)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		settings.MachineDetectionAction = action
	}
	if _, ok := args["default_timeout_sec"]; ok {
		timeout := intArg(args, "default_timeout_sec", -1)
		if timeout != 0 && (timeout < 5 || timeout > 120) {
			return mcpError("default_timeout_sec must be 0 (built-in default) or between 5 and 120 seconds"), nil
		}
		settings.DefaultTimeoutSec = timeout
	}
	if settings.MachineDetection != machineDetectionOff {
		if carrier, _ := recordingCarrierSupport(ctx); carrier != "none" && !machineDetectionSupported(carrier) {
			return mcpError("answering machine detection is not supported for the bound carrier " + carrier), nil
		}
	}
	if err := a.db().saveOutboundSettings(settings); err != nil {
		return mcpError(err.Error()), nil
	}
	return outboundSettingsPublic(settings), nil
}

// resolveMachineDetection combines an explicit per-call request with the
// project defaults and refuses detection on carriers that cannot report it.
func (a *App) resolveMachineDetection(ctx *sdk.AppCtx, projectID, carrier, requestedMode, requestedAction string) (string, string, error) {
	settings, err := a.db().outboundSettings(projectID)
	if err != nil {
		return "", "", errors.New("load outbound settings: " + err.Error())
	}
	mode := settings.MachineDetection
	if strings.TrimSpace(requestedMode) != "" {
		if mode, err = normalizeMachineDetection(requestedMode); err != nil {
			return "", "", err
		}
	}
	action := settings.MachineDetectionAction
	if strings.TrimSpace(requestedAction) != "" {
		if action, err = normalizeMachineDetectionAction(requestedAction); err != nil {
			return "", "", err
		}
	}
	if mode == "" {
		mode = machineDetectionOff
	}
	if action == "" {
		action = machineDetectionNotify
	}
	if mode != machineDetectionOff && !machineDetectionSupported(carrier) {
		return "", "", errors.New("answering machine detection is not supported for provider " + carrier)
	}
	return mode, action, nil
}

// ─── carrier dial parameters ────────────────────────────────────────

func applyTwilioMachineDetection(input map[string]any, req carrierPlaceRequest, callbackURL string) {
	switch req.MachineDetection {
	case machineDetectionDetect:
		input["MachineDetection"] = "Enable"
	case machineDetectionPremium:
		input["MachineDetection"] = "DetectMessageEnd"
	default:
		return
	}
	input["AsyncAmd"] = "true"
	input["AsyncAmdStatusCallback"] = callbackURL
	input["AsyncAmdStatusCallbackMethod"] = "POST"
}

func applyTelnyxMachineDetection(input map[string]any, req carrierPlaceRequest) {
	switch req.MachineDetection {
	case machineDetectionDetect:
		input["answering_machine_detection"] = "detect"
	case machineDetectionPremium:
		input["answering_machine_detection"] = "premium"
	}
}

func applyPlivoMachineDetection(input map[string]any, req carrierPlaceRequest, callbackURL string) {
	if req.MachineDetection == "" || req.MachineDetection == machineDetectionOff {
		return
	}
	input["machine_detection"] = "true"
	input["machine_detection_type"] = "async"
	input["machine_detection_url"] = callbackURL
	input["machine_detection_method"] = "POST"
}

// ─── progress application ───────────────────────────────────────────

// applyProgressUpdate runs after a carrier callback has been persisted. It
// synthesizes ringing for carriers that never report it and records answering
// machine detection results.
func (a *App) applyProgressUpdate(row *callRow, update callbackUpdate) error {
	if row == nil {
		return nil
	}
	if update.Status == "initiated" && row.Direction == "outbound" && carrierNeedsSyntheticRinging(row.CarrierSlug) {
		fresh, err := a.db().findCall(row.ID)
		if err != nil {
			return err
		}
		if fresh != nil && fresh.Status == "initiated" {
			if err := a.synthesizeRinging(row.ID, update.Facts.OccurredAt); err != nil {
				return fmt.Errorf("synthesize ringing: %w", err)
			}
		}
	}
	if update.AnsweredBy != "" {
		return a.recordAnsweredBy(row.ID, update.AnsweredBy, update.Facts)
	}
	return nil
}

func (a *App) synthesizeRinging(callID, occurredAt string) error {
	_, err := a.db().updateStatusWithFacts(callID, "ringing", "", lifecycleFacts{Source: "telephony", OccurredAt: occurredAt, Synthesized: true})
	return err
}

func (c *callsDB) setAnsweredBy(id, answeredBy string) error {
	_, err := c.db.Exec(`UPDATE calls SET answered_by = ?, updated_at = ? WHERE id = ?`,
		answeredBy, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

// enqueueCallEvent records a lifecycle event that is not derived from a
// status transition, such as call.machine_detected, in the durable outbox.
func (c *callsDB) enqueueCallEvent(callID, topic, occurredAt string, facts lifecycleFacts) (bool, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	call, err := scanCall(tx.QueryRow(`SELECT `+callSelectColumns+` FROM calls WHERE id = ?`, callID))
	if err != nil {
		return false, err
	}
	created, err := enqueueLifecycleEventTx(tx, call, topic, normalizedEventTime(occurredAt, time.Now().UTC()), facts)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	c.committed(call.ProjectID)
	return created, nil
}

// recordAnsweredBy persists the detection result once, emits
// call.machine_detected, pushes the change to a connected softphone, and
// applies the hangup action when the call reached a machine or fax.
func (a *App) recordAnsweredBy(callID, answeredBy string, facts lifecycleFacts) error {
	row, err := a.db().findCall(callID)
	if err != nil {
		return err
	}
	if row == nil {
		return errors.New("unknown call")
	}
	if row.AnsweredBy == answeredBy {
		return nil
	}
	if err := a.db().setAnsweredBy(callID, answeredBy); err != nil {
		return fmt.Errorf("persist answered_by: %w", err)
	}
	row.AnsweredBy = answeredBy
	facts.Source = firstNonEmpty(facts.Source, "provider")
	if _, err := a.db().enqueueCallEvent(callID, topicMachineDetected, facts.OccurredAt, facts); err != nil {
		return fmt.Errorf("record machine detection event: %w", err)
	}
	if globalCtx != nil {
		_ = a.publishLifecycleEvents(globalCtx.WithProject(row.ProjectID), callID)
	}
	a.pushSoftphoneStatus(row)
	if row.MachineDetectionAction == machineDetectionHangup && (answeredBy == answeredByMachine || answeredBy == answeredByFax) && !isTerminalStatus(row.Status) {
		return a.hangupDetectedMachine(row)
	}
	return nil
}

func (a *App) hangupDetectedMachine(row *callRow) error {
	if globalCtx == nil {
		return errors.New("app context unavailable")
	}
	ctx := globalCtx.WithProject(row.ProjectID)
	if row.CarrierSID != "" {
		carrier, err := a.carrierForRow(ctx, nil, row)
		if err != nil {
			return fmt.Errorf("resolve carrier for machine hangup: %w", err)
		}
		if err := carrier.Hangup(ctx, row); err != nil {
			return fmt.Errorf("machine hangup: %w", err)
		}
	}
	if row.ThreadID != "" {
		if err := a.killCallThread(ctx, row); err != nil {
			ctx.Logger().Warn("kill thread after machine detection", "call", row.ID, "err", err)
		}
	}
	_, err := a.db().updateStatusWithFacts(row.ID, "completed", "", lifecycleFacts{
		Source: "telephony", TerminationCause: "machine_detected", TerminationInitiator: "telephony",
	})
	return err
}

// terminationPublic is the shared termination object for call payloads.
func terminationPublic(r callRow) map[string]any {
	termination := map[string]any{}
	addOptionalString(termination, "reason", r.TerminationReason)
	addOptionalString(termination, "cause", r.TerminationCause)
	addOptionalString(termination, "code", r.TerminationCode)
	addOptionalString(termination, "initiator", r.TerminationInitiator)
	return termination
}

// handleCallRead serves GET /calls/{id} with the same visibility rules as the
// list endpoint, so a softphone can read one call without scanning the list.
func (a *App) handleCallRead(w http.ResponseWriter, r *http.Request, callID string) {
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	row, err := a.db().findCall(callID)
	if err != nil || row == nil || row.ProjectID != project {
		http.Error(w, "call not found", http.StatusNotFound)
		return
	}
	detail := []callRow{*row}
	if err := a.db().attachRingOffers(project, detail); err != nil {
		http.Error(w, "load ring offers", http.StatusInternalServerError)
		return
	}
	if err := a.db().attachRecordingSummaries(project, detail); err != nil {
		http.Error(w, "load recording summaries", http.StatusInternalServerError)
		return
	}
	detail = a.filterPhoneCalls(r, detail)
	if len(detail) == 0 {
		http.Error(w, "call not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"call": callsPanelPublic(detail, phoneUserFrom(r) == nil)[0]})
}
