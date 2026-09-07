package main

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const telemetryWindow = 1000

var errTelemetryIncomplete = errors.New("telemetry window saturated; complete evidence is unavailable")

func completeTelemetry(client sdk.RuntimeClient, runtime, agent string, since time.Time) ([]sdk.RuntimeTelemetryEvent, error) {
	events, err := client.ListRuntimeAgentTelemetry(runtime, agent, since, telemetryWindow)
	if err == nil && len(events) >= telemetryWindow {
		return events, errTelemetryIncomplete
	}
	return events, err
}

type telemetryHistory struct {
	client     sdk.RuntimeClient
	events     map[string]map[string]sdk.RuntimeTelemetryEvent
	cursor     map[string]time.Time
	incomplete map[string]bool
}

func newTelemetryHistory(client sdk.RuntimeClient) *telemetryHistory {
	return &telemetryHistory{client: client, events: map[string]map[string]sdk.RuntimeTelemetryEvent{}, cursor: map[string]time.Time{}, incomplete: map[string]bool{}}
}

// Poll incrementally, retaining all previously observed evidence. Overlap by one
// second because the server's since filter currently has second precision.
func (h *telemetryHistory) list(runtime, agent string, since time.Time, _ int) ([]sdk.RuntimeTelemetryEvent, error) {
	key := runtime + "/" + agent
	cursor := since
	if latest := h.cursor[key]; latest.After(cursor) {
		cursor = latest.Add(-time.Second)
	}
	batch, err := h.client.ListRuntimeAgentTelemetry(runtime, agent, cursor, telemetryWindow)
	if err != nil {
		return nil, err
	}
	if len(batch) >= telemetryWindow {
		h.incomplete[key] = true
	}
	if h.events[key] == nil {
		h.events[key] = map[string]sdk.RuntimeTelemetryEvent{}
	}
	for _, event := range batch {
		id := event.ID
		if id == "" {
			raw, _ := json.Marshal(event)
			id = string(raw)
		}
		h.events[key][id] = event
		if event.Time.After(h.cursor[key]) {
			h.cursor[key] = event.Time
		}
	}
	out := make([]sdk.RuntimeTelemetryEvent, 0, len(h.events[key]))
	for _, event := range h.events[key] {
		out = append(out, event)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if h.incomplete[key] {
		return out, errTelemetryIncomplete
	}
	return out, nil
}
