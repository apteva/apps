package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Bandwidth fetches BXML when the callee answers. The stream is bidirectional
// but non-blocking, so StopStream(wait=true) keeps the same call alive until
// Telephony closes the media socket.
func (a *App) handleBandwidthXML(w http.ResponseWriter, r *http.Request) {
	callID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/xml/bandwidth/"), "/")
	if callID == "" {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	row, err := a.db().findCall(callID)
	if err != nil || row == nil || row.CarrierSlug != "bandwidth" {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if err := a.authorizeCallRequest(r, row); err != nil || r.URL.Query().Get("project_id") != row.ProjectID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var event struct {
		EventType string `json:"eventType"`
		CallID    string `json:"callId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&event); err != nil || event.EventType != "answer" || event.CallID == "" {
		http.Error(w, "invalid Bandwidth answer event", http.StatusBadRequest)
		return
	}
	if row.CarrierSID != "" && event.CallID != row.CarrierSID {
		http.Error(w, "call ID mismatch", http.StatusForbidden)
		return
	}
	if row.CarrierSID == "" {
		if err := a.db().updateCarrierIdentity(callID, event.CallID, row.CarrierRequestID); err != nil {
			http.Error(w, "persist carrier call ID", http.StatusInternalServerError)
			return
		}
	}
	if isTerminalStatus(row.Status) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte("<Response><Hangup/></Response>"))
		return
	}
	if err := a.db().updateStatus(callID, "in-progress", ""); err != nil {
		http.Error(w, "persist answer", http.StatusInternalServerError)
		return
	}
	if row.IngressPath == "ring_group" {
		if err := a.claimAnsweredRingLeg(callID); err != nil {
			http.Error(w, "claim ring leg", http.StatusInternalServerError)
			return
		}
	}
	a.softphones.updateCallState(callID, row.Direction, "in-progress")
	streamURL := a.publicWSStreamURL("bandwidth", callID, row.CallbackSecret)
	recording := ""
	if row.RecordingMode == recordingModeAlways {
		recording = fmt.Sprintf(`<StartRecording recordingAvailableUrl="%s" recordingAvailableMethod="POST" username="apteva" password="%s" fileFormat="wav" multiChannel="%t"/>`,
			xmlEscape(a.bandwidthRecordingStatusURL(callID, row.CallbackSecret, row.ProjectID)), xmlEscape(row.CallbackSecret), row.RecordingChannels == "dual")
	}
	w.Header().Set("Content-Type", "application/xml")
	_, _ = fmt.Fprintf(w, `<Response>%s<StartStream name="apteva" mode="bidirectional" tracks="inbound" destination="%s"/><StopStream name="apteva" wait="true"/></Response>`, recording, xmlEscape(streamURL))
}

func (a *App) bandwidthRecordingStatusURL(callID, secret, projectID string) string {
	query := url.Values{"token": {secret}, "project_id": {projectID}}.Encode()
	return a.publicAppURL() + "/webhook/recording/bandwidth/" + url.PathEscape(callID) + "?" + query
}

func (a *App) handleBandwidthRecordingStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	callID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/webhook/recording/bandwidth/"), "/")
	if callID == "" || strings.Contains(callID, "/") {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	call, err := a.db().findCall(callID)
	if err != nil || call == nil || call.CarrierSlug != "bandwidth" {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if a.authorizeCallRequest(r, call) != nil || r.URL.Query().Get("project_id") != call.ProjectID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var event struct {
		EventType   string `json:"eventType"`
		CallID      string `json:"callId"`
		RecordingID string `json:"recordingId"`
		Duration    string `json:"duration"`
		FileFormat  string `json:"fileFormat"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&event); err != nil || event.EventType != "recordingAvailable" ||
		!validProviderResourceID(event.RecordingID) || event.CallID == "" || (call.CarrierSID != "" && event.CallID != call.CarrierSID) {
		http.Error(w, "invalid Bandwidth recording event", http.StatusBadRequest)
		return
	}
	channels := 1
	if call.RecordingChannels == "dual" {
		channels = 2
	}
	format := strings.ToLower(firstNonEmpty(event.FileFormat, "wav"))
	if format != "wav" && format != "mp3" {
		http.Error(w, "unsupported recording format", http.StatusBadRequest)
		return
	}
	durationMS := bandwidthDurationMS(event.Duration)
	recording, err := a.db().upsertProviderRecording(call, "bandwidth", event.RecordingID, "completed", format, durationMS, channels, "both")
	if err != nil {
		http.Error(w, "persist recording", http.StatusInternalServerError)
		return
	}
	globalCtx.WithProject(call.ProjectID).Emit("recording.ready", recordingPublic(*recording))
	w.WriteHeader(http.StatusNoContent)
}

func bandwidthDurationMS(value string) int64 {
	if !strings.HasPrefix(value, "PT") {
		return 0
	}
	duration, err := time.ParseDuration(strings.ToLower(strings.TrimPrefix(value, "PT")))
	if err != nil || duration < 0 {
		return 0
	}
	return duration.Milliseconds()
}

func bandwidthCallbackUpdate(r *http.Request) callbackUpdate {
	var event struct {
		EventType    string `json:"eventType"`
		EventTime    string `json:"eventTime"`
		CallID       string `json:"callId"`
		Cause        string `json:"cause"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&event); err != nil {
		return callbackUpdate{}
	}
	status := ""
	switch event.EventType {
	case "initiate":
		status = "ringing"
	case "answer":
		status = "answered"
	case "disconnect":
		status = "completed"
	}
	return callbackUpdate{
		Status: status, Error: event.ErrorMessage, CarrierSID: event.CallID,
		Facts: lifecycleFacts{
			OccurredAt: event.EventTime, Source: "provider",
			ProviderEventID:  event.CallID + ":" + event.EventType + ":" + event.EventTime,
			TerminationCause: event.Cause,
		},
	}
}
