package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws"
)

var errCallNoLongerActive = errors.New("call has ended")

// The public states distinguish an in-flight request from a Telnyx success
// response. A transport failure remains unknown, never optimistically paused.
func effectiveRecordingControlState(row callRow) string {
	if isTerminalStatus(row.Status) {
		return "ended"
	}
	if row.RecordingMode != recordingModeAlways {
		return "off"
	}
	if row.RecordingControlState == "" || row.RecordingControlState == "default" {
		return "active"
	}
	if row.RecordingControlState == "pause_requested" || row.RecordingControlState == "resume_requested" {
		if requested, err := time.Parse(time.RFC3339Nano, row.RecordingRequestedAt); err == nil && time.Since(requested) > 30*time.Second {
			return "unknown"
		}
	}
	return row.RecordingControlState
}

func callControlCapabilities(row callRow) map[string]bool {
	features := carrierControlFeaturesForSlug(row.CarrierSlug)
	carrier := row.CarrierSID != "" && row.IngressPath != "sip_direct"
	human := row.PeerKind == peerKindHuman
	return map[string]bool{
		"hold_music":      features.HoldMusic && carrier && human && (row.HoldMusicURL != "" || row.HoldMusicStorageFileID > 0),
		"recording_pause": features.RecordingPause && carrier && human && row.RecordingMode == recordingModeAlways,
	}
}

func isCallControlAction(action string) bool {
	switch action {
	case "hold", "resume", "pause-recording", "resume-recording":
		return true
	}
	return false
}

func validHoldMusicURL(raw string) error {
	if raw == "" {
		return nil // deliberately disable hold until an approved asset exists
	}
	if len(raw) > 2048 || strings.TrimSpace(raw) != raw {
		return errors.New("invalid hold music URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || u.Fragment != "" {
		return errors.New("hold music must be an HTTPS URL without credentials or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return errors.New("hold music must use a public DNS hostname")
	}
	if port := u.Port(); port != "" && port != "443" {
		return errors.New("hold music must use the standard HTTPS port")
	}
	return nil
}

func holdMusicFromStorage(ctx *sdk.AppCtx, project string, fileID int64) (string, error) {
	if ctx == nil || ctx.PlatformAPI() == nil || fileID <= 0 {
		return "", errors.New("Storage is unavailable")
	}
	args := map[string]any{"id": fileID, "_project_id": project}
	var found struct {
		Found bool `json:"found"`
		File  struct {
			ID          int64  `json:"id"`
			ProjectID   string `json:"project_id"`
			ContentType string `json:"content_type"`
			SizeBytes   int64  `json:"size_bytes"`
		} `json:"file"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get", args, &found); err != nil {
		return "", fmt.Errorf("read hold music from Storage: %w", err)
	}
	if !found.Found || found.File.ID != fileID || found.File.ProjectID != project || found.File.SizeBytes <= 0 {
		return "", errors.New("hold music file is missing or outside this project")
	}
	switch strings.ToLower(found.File.ContentType) {
	case "audio/mpeg", "audio/mp3", "audio/wav", "audio/wave", "audio/x-wav":
	default:
		return "", errors.New("hold music must be an MP3 or WAV file")
	}
	var signed struct {
		URL       string `json:"url"`
		FileID    int64  `json:"file_id"`
		ExpiresAt int64  `json:"expires_at"`
	}
	urlArgs := map[string]any{"id": fileID, "_project_id": project, "ttl_seconds": 7 * 24 * 3600,
		"delivery": "proxy", "disposition": "inline"}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", urlArgs, &signed); err != nil {
		return "", fmt.Errorf("sign hold music from Storage: %w", err)
	}
	if signed.FileID != fileID || signed.ExpiresAt <= time.Now().Add(time.Hour).Unix() || validHoldMusicURL(signed.URL) != nil {
		return "", errors.New("Storage did not return an externally reachable HTTPS hold music URL")
	}
	return signed.URL, nil
}

func (a *App) handleCallControlSettings(w http.ResponseWriter, r *http.Request) {
	// This is installation administration, not a delegated phone-user action.
	if phoneUserFrom(r) != nil {
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, "project not allowed", http.StatusForbidden)
		return
	}
	var music string
	var storageFileID int64
	err = a.db().db.QueryRow(`SELECT hold_music_url,hold_music_storage_file_id FROM call_control_settings WHERE project_id=?`, project).Scan(&music, &storageFileID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "load call control settings", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"hold_music_url": music, "hold_music_storage_file_id": storageFileID, "hold_available": music != "" || storageFileID > 0})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		HoldMusicURL           *string `json:"hold_music_url"`
		HoldMusicStorageFileID *int64  `json:"hold_music_storage_file_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil || (body.HoldMusicURL == nil && body.HoldMusicStorageFileID == nil) {
		http.Error(w, "hold_music_url or hold_music_storage_file_id is required", http.StatusBadRequest)
		return
	}
	if body.HoldMusicURL != nil && body.HoldMusicStorageFileID != nil && *body.HoldMusicURL != "" && *body.HoldMusicStorageFileID > 0 {
		http.Error(w, "choose either a URL or a Storage file", http.StatusBadRequest)
		return
	}
	if body.HoldMusicURL != nil {
		music = *body.HoldMusicURL
		storageFileID = 0
	}
	if body.HoldMusicStorageFileID != nil {
		storageFileID = *body.HoldMusicStorageFileID
		music = ""
	}
	if err := validHoldMusicURL(music); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if storageFileID < 0 {
		http.Error(w, "invalid Storage file ID", http.StatusBadRequest)
		return
	}
	if storageFileID > 0 {
		if _, err := holdMusicFromStorage(globalCtx.WithProject(project), project, storageFileID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	_, err = a.db().db.Exec(`INSERT INTO call_control_settings(project_id,hold_music_url,hold_music_storage_file_id,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(project_id) DO UPDATE SET hold_music_url=excluded.hold_music_url,hold_music_storage_file_id=excluded.hold_music_storage_file_id,updated_at=excluded.updated_at`,
		project, music, storageFileID, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		http.Error(w, "save call control settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"hold_music_url": music, "hold_music_storage_file_id": storageFileID, "hold_available": music != "" || storageFileID > 0})
}

func (a *App) handleCallControl(w http.ResponseWriter, r *http.Request, callID, action string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app context unavailable", http.StatusServiceUnavailable)
		return
	}
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, "project not allowed", http.StatusForbidden)
		return
	}
	unlock := a.softphones.lockClaim(callID)
	defer unlock()
	row, err := a.db().findCall(callID)
	if err != nil {
		http.Error(w, "load call", http.StatusInternalServerError)
		return
	}
	p := phoneUserFrom(r)
	if row == nil || row.ProjectID != project || p == nil || !a.phoneCallAllowed(p, row, false) {
		http.Error(w, "call not found", http.StatusNotFound)
		return
	}
	if isTerminalStatus(row.Status) {
		http.Error(w, "call has ended", http.StatusGone)
		return
	}
	if row.Status != "answered" && row.Status != "in-progress" {
		http.Error(w, "call is not connected", http.StatusConflict)
		return
	}
	cap := callControlCapabilities(*row)
	if (action == "hold" || action == "resume") && !cap["hold_music"] && row.HoldState == "active" {
		http.Error(w, "hold music is unavailable for this call", http.StatusNotImplemented)
		return
	}
	if (action == "pause-recording" || action == "resume-recording") && !cap["recording_pause"] {
		http.Error(w, "recording pause is unavailable for this call", http.StatusNotImplemented)
		return
	}
	adapter, err := a.carrierForRow(globalCtx.WithProject(project), nil, row)
	if err != nil {
		http.Error(w, "carrier unavailable", http.StatusNotImplemented)
		return
	}
	controller, ok := adapter.(carrierCallController)
	if !ok {
		http.Error(w, "carrier does not support call controls", http.StatusNotImplemented)
		return
	}
	currentRecording := effectiveRecordingControlState(*row)
	if action == "hold" && (row.HoldState == "held" || row.HoldState == "starting" && !controlRequestExpired(*row)) ||
		action == "resume" && (row.HoldState == "active" || row.HoldState == "stopping" && !controlRequestExpired(*row)) ||
		action == "pause-recording" && (currentRecording == "paused" || currentRecording == "pause_requested") ||
		action == "resume-recording" && (currentRecording == "active" || currentRecording == "resume_requested") {
		writeJSON(w, callControlResult(*row))
		return
	}
	if (action == "pause-recording" && currentRecording != "active" && currentRecording != "unknown") ||
		(action == "resume-recording" && currentRecording != "paused" && currentRecording != "unknown") {
		http.Error(w, "recording transition is still pending", http.StatusConflict)
		return
	}
	if action == "resume" && row.HoldState == "active" {
		writeJSON(w, callControlResult(*row))
		return
	}
	if action == "hold" && row.HoldState == "stopping" {
		http.Error(w, "resume is still in progress", http.StatusConflict)
		return
	}
	musicURL := row.HoldMusicURL
	if action == "hold" && row.HoldMusicStorageFileID > 0 {
		musicURL, err = holdMusicFromStorage(globalCtx.WithProject(project), project, row.HoldMusicStorageFileID)
		if err != nil {
			http.Error(w, "hold music is unavailable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
	}
	// Preserve the command ID for a retry after an uncertain provider response.
	revision, previousAction := row.HoldControlRevision, row.HoldControlAction
	if action == "pause-recording" || action == "resume-recording" {
		revision, previousAction = row.RecordingControlRevision, row.RecordingControlAction
	}
	if previousAction != action {
		revision++
	}
	commandID := telnyxCommandID(row.ID, fmt.Sprintf("control:%s:%d", action, revision))
	pending := ""
	switch action {
	case "hold":
		pending = "starting"
	case "resume":
		pending = "stopping"
	case "pause-recording":
		pending = "pause_requested"
	case "resume-recording":
		pending = "resume_requested"
	}
	if err := a.db().setCallControl(row.ID, action, pending, revision, ""); err != nil {
		if errors.Is(err, errCallNoLongerActive) {
			http.Error(w, "call has ended", http.StatusGone)
			return
		}
		http.Error(w, "persist control request", http.StatusInternalServerError)
		return
	}
	if action == "hold" {
		row.HoldClientState = base64.StdEncoding.EncodeToString([]byte(commandID))
		if _, err := a.db().db.Exec(`UPDATE calls SET hold_client_state=? WHERE id=?`, row.HoldClientState, row.ID); err != nil {
			http.Error(w, "persist hold identity", http.StatusInternalServerError)
			return
		}
	}
	if requested, findErr := a.db().findCall(row.ID); findErr == nil && requested != nil {
		a.notifyCallControl(requested)
	}
	if action == "hold" {
		hub := a.softphones.hubFor(row.ID)
		hub.setHeld(true)
		// Clear already queued bridge playback before starting carrier music.
		interrupt, _ := json.Marshal(realtimeBridgeControl{Type: "interrupt", Source: "hold"})
		hub.toPeer(ws.OpText, interrupt)
	}
	ctx := globalCtx.WithProject(project)
	switch action {
	case "hold":
		err = controller.StartHoldMusic(ctx, row, musicURL, commandID)
	case "resume":
		err = controller.StopHoldMusic(ctx, row, commandID)
	case "pause-recording":
		err = controller.PauseRecording(ctx, row, commandID)
	case "resume-recording":
		err = controller.ResumeRecording(ctx, row, commandID)
	}
	if err != nil {
		_ = a.db().setCallControl(row.ID, action, "unknown", revision, "carrier command was not confirmed")
		updated, _ := a.db().findCall(row.ID)
		if updated != nil {
			a.notifyCallControl(updated)
		}
		http.Error(w, "carrier command was not confirmed; check call state before retrying", http.StatusBadGateway)
		return
	}
	// Hold/resume are confirmed by playback webhooks. Telnyx documents no
	// recording pause/resume webhook; its successful result=ok response is the
	// confirmation boundary chosen for those two actions.
	if action == "pause-recording" || action == "resume-recording" {
		confirmed := "paused"
		if action == "resume-recording" {
			confirmed = "active"
		}
		if err := a.db().setCallControl(row.ID, action, confirmed, revision, ""); err != nil {
			if errors.Is(err, errCallNoLongerActive) {
				http.Error(w, "call has ended", http.StatusGone)
				return
			}
			http.Error(w, "persist recording control state", http.StatusInternalServerError)
			return
		}
	}
	updated, err := a.db().findCall(row.ID)
	if err != nil || updated == nil {
		http.Error(w, "load control state", http.StatusInternalServerError)
		return
	}
	a.notifyCallControl(updated)
	if action == "hold" || action == "resume" {
		w.WriteHeader(http.StatusAccepted)
	}
	writeJSON(w, callControlResult(*updated))
}

func controlRequestExpired(row callRow) bool {
	requested, err := time.Parse(time.RFC3339Nano, row.HoldRequestedAt)
	return err != nil || time.Since(requested) > 30*time.Second
}

func (c *callsDB) confirmHoldPlayback(id, eventType, clientState string) (string, bool, error) {
	next, expected := "held", "starting"
	if eventType == "call.playback.ended" {
		next, expected = "active", "stopping"
	}
	result, err := c.db.Exec(`UPDATE calls SET hold_state=?,control_error='',updated_at=?
		WHERE id=? AND hold_state=? AND hold_client_state=? AND status NOT IN ('completed','failed','no-answer','busy','canceled','cancelled')`,
		next, time.Now().UTC().Format(time.RFC3339Nano), id, expected, clientState)
	if err != nil {
		return "", false, err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 && eventType == "call.playback.ended" {
		result, err = c.db.Exec(`UPDATE calls SET hold_state='unknown',control_error='hold music stopped unexpectedly',updated_at=?
			WHERE id=? AND hold_state='held' AND hold_client_state=?`, time.Now().UTC().Format(time.RFC3339Nano), id, clientState)
		if err == nil {
			n, err = result.RowsAffected()
			next = "unknown"
		}
	}
	return next, n > 0, err
}

func (c *callsDB) setCallControl(id, action, state string, revision int64, controlError string) error {
	column := "hold_state"
	revisionColumn, actionColumn, requestedColumn := "hold_control_revision", "hold_control_action", "hold_requested_at"
	if action == "pause-recording" || action == "resume-recording" {
		column = "recording_control_state"
		revisionColumn, actionColumn, requestedColumn = "recording_control_revision", "recording_control_action", "recording_requested_at"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := c.db.Exec(`UPDATE calls SET `+column+`=?,`+revisionColumn+`=?,`+actionColumn+`=?,`+requestedColumn+`=?,control_revision=control_revision+1,control_action=?,control_error=?,control_requested_at=?,updated_at=? WHERE id=? AND status NOT IN ('completed','failed','no-answer','busy','canceled','cancelled')`,
		state, revision, action, now, action, controlError, now, now, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errCallNoLongerActive
	}
	return nil
}

func callControlResult(row callRow) map[string]any {
	return map[string]any{"call_id": row.ID, "hold_state": row.HoldState,
		"recording_state": effectiveRecordingControlState(row), "control_error": row.ControlError,
		"capabilities": callControlCapabilities(row)}
}

func (a *App) notifyCallControl(row *callRow) {
	a.pushSoftphoneStatus(row)
	a.callChanges.notify(row.ProjectID)
}
