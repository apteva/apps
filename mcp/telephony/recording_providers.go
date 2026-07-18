package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const maxProviderRecordingBytes int64 = 2 << 30

type providerRecordingMetadata struct {
	ID         string
	Status     string
	Format     string
	Track      string
	DurationMS int64
	Channels   int
}

func (a *App) handlePlivoRecordingStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	callID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/webhook/recording/plivo/"), "/")
	if callID == "" || strings.Contains(callID, "/") {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	call, err := a.db().findCall(callID)
	if err != nil || call == nil {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if call.CarrierSlug != "plivo" || a.authorizeCallRequest(r, call) != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if callUUID := strings.TrimSpace(firstNonEmpty(r.FormValue("CallUUID"), r.FormValue("call_uuid"))); callUUID == "" ||
		(call.CarrierSID != "" && callUUID != call.CarrierSID) {
		http.Error(w, "call does not match recording", http.StatusForbidden)
		return
	}
	recordingID := strings.TrimSpace(firstNonEmpty(r.FormValue("RecordingID"), r.FormValue("recording_id")))
	if !validProviderResourceID(recordingID) {
		http.Error(w, "invalid recording id", http.StatusBadRequest)
		return
	}
	durationMS := int64Arg(r.FormValue("RecordingDurationMs"))
	if durationMS <= 0 {
		durationMS = int64Arg(r.FormValue("recording_duration_ms"))
	}
	if durationMS <= 0 {
		durationMS = int64Arg(firstNonEmpty(r.FormValue("RecordingDuration"), r.FormValue("recording_duration"))) * 1000
	}
	channels := 1
	if call.RecordingChannels == "dual" {
		channels = 2
	}
	recording, err := a.db().upsertProviderRecording(call, "plivo", recordingID, "completed", "wav", durationMS, channels, "both")
	if err != nil {
		http.Error(w, "persist recording", http.StatusInternalServerError)
		return
	}
	globalCtx.WithProject(call.ProjectID).Emit("recording.ready", recordingPublic(*recording))
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleTelnyxRecordingEvent(call *callRow, body []byte) (bool, error) {
	var event struct {
		Data struct {
			EventType string         `json:"event_type"`
			Payload   map[string]any `json:"payload"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return false, err
	}
	if event.Data.EventType != "call.recording.saved" {
		return false, nil
	}
	payload := event.Data.Payload
	callControlID := stringValue(payload["call_control_id"])
	if callControlID == "" || (call.CarrierSID != "" && callControlID != call.CarrierSID) {
		return true, errors.New("Telnyx recording does not match call")
	}
	metadata := providerRecordingMetadata{
		ID: stringValue(payload["recording_id"]), Status: "completed", Format: strings.ToLower(stringValue(payload["format"])),
		Track: "both", DurationMS: durationBetween(payload["recording_started_at"], payload["recording_ended_at"]),
		Channels: providerChannelCount(payload["channels"], call.RecordingChannels),
	}
	if !validProviderResourceID(metadata.ID) {
		return true, errors.New("Telnyx recording callback has no valid recording id")
	}
	recording, err := a.db().upsertProviderRecording(call, "telnyx", metadata.ID, metadata.Status, metadata.Format, metadata.DurationMS, metadata.Channels, metadata.Track)
	if err != nil {
		return true, err
	}
	globalCtx.WithProject(call.ProjectID).Emit("recording.ready", recordingPublic(*recording))
	return true, nil
}

func (a *App) reconcileCallRecordings(ctx *sdk.AppCtx, call *callRow) error {
	input := map[string]any{}
	switch call.CarrierSlug {
	case "twilio":
		input = map[string]any{"CallSid": call.CarrierSID, "PageSize": 20}
	case "telnyx":
		input = map[string]any{"filter[call_control_id]": call.CarrierSID, "page[size]": 20}
	case "plivo":
		input = map[string]any{"call_uuid": call.CarrierSID, "limit": 20}
	default:
		return nil
	}
	raw, err := executeCarrierTool(ctx, call.CarrierConnectionID, "list_recordings", input)
	if err != nil {
		return err
	}
	items, err := recordingListItems(call.CarrierSlug, raw)
	if err != nil {
		return err
	}
	for _, item := range items {
		metadata := parseProviderRecordingMetadata(call.CarrierSlug, item, call)
		if metadata.ID == "" {
			continue
		}
		if _, err := a.db().upsertProviderRecording(call, call.CarrierSlug, metadata.ID, metadata.Status,
			metadata.Format, metadata.DurationMS, metadata.Channels, metadata.Track); err != nil {
			return err
		}
	}
	return nil
}

func recordingListItems(provider string, raw json.RawMessage) ([]map[string]any, error) {
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	key := "recordings"
	if provider == "telnyx" {
		key = "data"
	} else if provider == "plivo" {
		key = "objects"
	}
	values, _ := response[key].([]any)
	items := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			items = append(items, item)
		}
	}
	return items, nil
}

func parseProviderRecordingMetadata(provider string, item map[string]any, call *callRow) providerRecordingMetadata {
	metadata := providerRecordingMetadata{Status: "completed", Format: "wav", Track: "both"}
	switch provider {
	case "twilio":
		metadata.ID = stringValue(item["sid"])
		metadata.Status = strings.ToLower(firstNonEmpty(stringValue(item["status"]), "completed"))
		metadata.DurationMS = int64Arg(stringValue(item["duration"])) * 1000
	case "telnyx":
		metadata.ID = firstNonEmpty(stringValue(item["id"]), stringValue(item["recording_id"]))
		metadata.Format = strings.ToLower(firstNonEmpty(stringValue(item["format"]), "wav"))
		metadata.DurationMS = firstPositiveInt64(item["duration_millis"], item["duration_ms"])
		if metadata.DurationMS == 0 {
			metadata.DurationMS = durationBetween(item["recording_started_at"], item["recording_ended_at"])
		}
	case "plivo":
		metadata.ID = stringValue(item["recording_id"])
		metadata.Format = strings.ToLower(firstNonEmpty(stringValue(item["recording_format"]), "wav"))
		metadata.DurationMS = firstPositiveInt64(item["recording_duration_ms"])
	}
	metadata.Channels = providerChannelCount(item["channels"], call.RecordingChannels)
	return metadata
}

func (a *App) downloadProviderRecording(downloadCtx context.Context, ctx *sdk.AppCtx, recording *recordingRow, creds map[string]string) (string, int64, error) {
	if recording.Provider == "twilio" {
		return downloadTwilioRecordingChannels(downloadCtx, creds, recording.ProviderRecordingID, recording.Format, recording.Channels)
	}
	raw, err := executeCarrierTool(ctx, recording.CarrierConnectionID, "get_recording", map[string]any{"recording_id": recording.ProviderRecordingID})
	if err != nil {
		return "", 0, err
	}
	mediaURL, err := providerRecordingURL(recording.Provider, raw, recording.Format)
	if err != nil {
		return "", 0, err
	}
	username, password := "", ""
	if recording.Provider == "plivo" {
		username = firstNonEmpty(creds["auth_id"], creds["username"])
		password = firstNonEmpty(creds["password"], creds["auth_token"])
	}
	return downloadProviderMedia(downloadCtx, mediaURL, recording.Format, username, password)
}

func providerRecordingURL(provider string, raw json.RawMessage, format string) (string, error) {
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", err
	}
	data := response
	if provider == "telnyx" {
		if nested, ok := response["data"].(map[string]any); ok {
			data = nested
		}
		for _, key := range []string{"public_recording_urls", "recording_urls"} {
			if urls, ok := data[key].(map[string]any); ok {
				if candidate := firstNonEmpty(stringValue(urls[format]), stringValue(urls["wav"]), stringValue(urls["mp3"])); strings.HasPrefix(candidate, "https://") {
					return candidate, nil
				}
			}
		}
	} else if provider == "plivo" {
		if candidate := stringValue(data["recording_url"]); candidate != "" {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s did not return an HTTPS recording URL", provider)
}

func downloadProviderMedia(ctx context.Context, rawURL, format, username, password string) (string, int64, error) {
	if err := validateProviderMediaURL(rawURL); err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", 0, err
	}
	if username != "" && password != "" {
		req.SetBasicAuth(username, password)
	}
	client := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many recording redirects")
		}
		return validateProviderMediaURL(req.URL.String())
	}}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", 0, fmt.Errorf("provider recording download returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if resp.ContentLength > maxProviderRecordingBytes {
		return "", 0, errors.New("provider recording exceeds the 2 GB limit")
	}
	tmp, err := os.CreateTemp("", "apteva-telephony-provider-recording-*."+recordingExtension(format))
	if err != nil {
		return "", 0, err
	}
	path := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, maxProviderRecordingBytes+1))
	if err != nil {
		return "", 0, err
	}
	if written <= 0 || written > maxProviderRecordingBytes {
		return "", 0, errors.New("provider recording is empty or exceeds the 2 GB limit")
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	ok = true
	return path, written, nil
}

func validateProviderMediaURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return errors.New("provider recording URL must be an HTTPS URL without credentials")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) {
		return errors.New("provider recording URL resolves to a disallowed address literal")
	}
	return nil
}

func providerChannelCount(value any, configured string) int {
	switch strings.ToLower(stringValue(value)) {
	case "dual", "stereo", "2":
		return 2
	case "single", "mono", "1":
		return 1
	}
	if n := firstPositiveInt64(value); n == 2 {
		return 2
	}
	if configured == "dual" {
		return 2
	}
	return 1
}

func durationBetween(start, end any) int64 {
	startTime, startErr := time.Parse(time.RFC3339Nano, stringValue(start))
	endTime, endErr := time.Parse(time.RFC3339Nano, stringValue(end))
	if startErr != nil || endErr != nil || endTime.Before(startTime) {
		return 0
	}
	return endTime.Sub(startTime).Milliseconds()
}

func firstPositiveInt64(values ...any) int64 {
	for _, value := range values {
		var parsed int64
		switch typed := value.(type) {
		case float64:
			parsed = int64(typed)
		case json.Number:
			parsed, _ = typed.Int64()
		case string:
			parsed = int64Arg(typed)
		case int:
			parsed = int64(typed)
		case int64:
			parsed = typed
		}
		if parsed > 0 {
			return parsed
		}
	}
	return 0
}

func int64Arg(value string) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return parsed
}
