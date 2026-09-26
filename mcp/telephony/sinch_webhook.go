package main

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func sinchCallbackUpdate(r *http.Request) callbackUpdate {
	var event struct {
		Event string `json:"event"`
		Call  struct {
			CallID    string `json:"callId"`
			SessionID string `json:"sessionId"`
			CallType  string `json:"callType"`
			Result    string `json:"callResult"`
			EndTime   string `json:"endTime"`
		} `json:"call"`
	}
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil || event.Call.SessionID == "" || event.Call.CallID == "" || event.Call.CallType != "PHONE" {
		return callbackUpdate{}
	}
	status := ""
	switch event.Event {
	case "call.webhook.answered":
		status = "answered"
	case "call.webhook.busy":
		status = "busy"
	case "call.webhook.rejected":
		status = "failed"
	case "call.webhook.no_answer":
		status = "no-answer"
	case "call.webhook.failed":
		status = "failed"
	case "call.webhook.completed", "call.hangup":
		status = normalizeCallStatus(event.Call.Result)
		if status == "" {
			status = "completed"
		}
	}
	if event.Call.Result == "CANCEL" {
		status = "canceled"
	}
	return callbackUpdate{
		Status: status, ProviderEvent: event.Event, CarrierSID: event.Call.SessionID,
		Facts: lifecycleFacts{
			OccurredAt:      firstNonEmpty(r.Header.Get("ce-time"), event.Call.EndTime),
			Source:          "provider",
			ProviderEventID: firstNonEmpty(r.Header.Get("ce-id"), event.Call.CallID+":"+event.Event),
		},
	}
}

// The media socket is authenticated by its per-call URL token. The first
// Sinch message still has to negotiate exactly the PCM format accepted by the
// binary bridge; otherwise the call would appear connected with corrupt audio.
func validateSinchConnect(data []byte, expectedServiceID string) error {
	var request struct {
		Command        string `json:"command"`
		Version        int    `json:"version"`
		CallID         string `json:"callId"`
		ApplicationKey string `json:"applicationKey"`
		Codec          string `json:"codec"`
		SampleRate     int    `json:"sampleRate"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return fmt.Errorf("decode Sinch connect request: %w", err)
	}
	if request.Command != "connect" || request.Version != 1 || request.CallID == "" || expectedServiceID == "" || request.ApplicationKey != expectedServiceID ||
		!strings.EqualFold(request.Codec, "PCM") || request.SampleRate != 16000 {
		return errors.New("unsupported Sinch stream connect request")
	}
	return nil
}

// Sinch signs the raw request body and the public, configured webhook path.
// Keep this independent of call parsing so an event cannot select a call or
// project before its signature has been verified.
func verifySinchWebhook(r *http.Request, body []byte, serviceID, secret, publicPath string, now time.Time) error {
	if r == nil || r.Method != http.MethodPost || serviceID == "" || publicPath == "" ||
		!strings.HasPrefix(publicPath, "/") || strings.ContainsAny(publicPath, "?#") {
		return errors.New("invalid Sinch webhook verification context")
	}
	key, err := base64.StdEncoding.DecodeString(secret)
	if err != nil || len(key) != 16 {
		return errors.New("invalid Sinch service secret")
	}
	timestamp := r.Header.Get("x-timestamp")
	signedAt, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil || signedAt.Before(now.Add(-5*time.Minute)) || signedAt.After(now.Add(5*time.Minute)) {
		return errors.New("Sinch webhook timestamp is stale or invalid")
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	prefix := "service " + serviceID + ":"
	if !strings.HasPrefix(auth, prefix) {
		return errors.New("Sinch webhook service does not match")
	}
	provided, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, prefix))
	if err != nil || len(provided) != sha256.Size {
		return errors.New("invalid Sinch webhook signature")
	}
	contentMD5 := ""
	contentType := ""
	if len(body) > 0 {
		digest := md5.Sum(body)
		contentMD5 = base64.StdEncoding.EncodeToString(digest[:])
		contentType = r.Header.Get("Content-Type")
	}
	canonical := fmt.Sprintf("POST\n%s\n%s\nx-timestamp:%s\n%s", contentMD5, contentType, timestamp, publicPath)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(canonical))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return errors.New("invalid Sinch webhook signature")
	}
	return nil
}
