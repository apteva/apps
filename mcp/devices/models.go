package main

import (
	"encoding/json"
	"strings"
	"time"
)

type Device struct {
	ID                string         `json:"id"`
	ProjectID         string         `json:"project_id,omitempty"`
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	Protocol          string         `json:"protocol"`
	Model             string         `json:"model"`
	Manufacturer      string         `json:"manufacturer"`
	Firmware          string         `json:"firmware"`
	TopicRoot         string         `json:"topic_root"`
	MQTTUsername      string         `json:"mqtt_username"`
	MQTTClientID      string         `json:"mqtt_client_id,omitempty"`
	Enabled           bool           `json:"enabled"`
	Status            string         `json:"status"`
	Availability      string         `json:"availability"`
	Manifest          map[string]any `json:"manifest"`
	Metadata          map[string]any `json:"metadata"`
	CredentialVersion int            `json:"credential_version"`
	LastSeen          *string        `json:"last_seen,omitempty"`
	ConnectedAt       *string        `json:"connected_at,omitempty"`
	DisconnectedAt    *string        `json:"disconnected_at,omitempty"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
	State             []StateValue   `json:"state,omitempty"`
}

type StateValue struct {
	Key        string `json:"key"`
	Value      any    `json:"value"`
	ValueType  string `json:"value_type"`
	Unit       string `json:"unit,omitempty"`
	Source     string `json:"source"`
	ObservedAt string `json:"observed_at"`
	MessageID  string `json:"message_id,omitempty"`
	UpdatedAt  string `json:"updated_at"`
}

type Command struct {
	ID             string         `json:"id"`
	DeviceID       string         `json:"device_id"`
	Operation      string         `json:"operation"`
	Target         string         `json:"target,omitempty"`
	Arguments      map[string]any `json:"arguments"`
	Request        map[string]any `json:"request"`
	Status         string         `json:"status"`
	Result         any            `json:"result,omitempty"`
	Error          string         `json:"error,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	TimeoutMS      int            `json:"timeout_ms"`
	CreatedAt      string         `json:"created_at"`
	SentAt         *string        `json:"sent_at,omitempty"`
	DeadlineAt     string         `json:"deadline_at"`
	CompletedAt    *string        `json:"completed_at,omitempty"`
}

func jsonObject(raw string) map[string]any {
	out := map[string]any{}
	_ = decodeJSON(raw, &out)
	return out
}

func jsonValue(raw *string) any {
	if raw == nil || *raw == "" {
		return nil
	}
	var out any
	if decodeJSON(*raw, &out) != nil {
		return *raw
	}
	return out
}

func nowText() string { return formatTime(time.Now()) }

func formatTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func decodeJSON(raw string, out any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(out)
}
