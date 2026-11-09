package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStartupRecoversRetainedStateBeyondLegacyCap(t *testing.T) {
	a, platform, _ := newTestApp(t)
	d := provisionTest(t, a, "tail-sensor", "apteva.devices/v2")
	root := a.topicRoot()
	at := formatTime(time.Now().Add(-time.Minute))
	pages := make(map[string]retainedDevicePage)
	cursor := ""
	for n := 0; n < 10; n++ {
		messages := make([]retainedDeviceMessage, 100)
		for i := range messages {
			messages[i] = retainedDeviceMessage{
				Topic:   fmt.Sprintf("%sdevices/archived-%04d/state", root, 100*n+i),
				Payload: "{}",
			}
		}
		next := fmt.Sprintf("page-%d", n+2)
		pages[cursor] = retainedDevicePage{Messages: messages, HasMore: true, NextCursor: next}
		cursor = next
	}
	// MQTT can scan a page of topics outside the filter before finding the tail.
	pages[cursor] = retainedDevicePage{HasMore: true, NextCursor: "tail"}
	fullValue := strings.Repeat("x", 6000)
	foreign := dataPayload("foreign", at, map[string]any{"foreign": true})
	pages["tail"] = retainedDevicePage{Messages: []retainedDeviceMessage{
		{Topic: "devices/" + d.ID + "/state", Payload: foreign, UpdatedAt: at},
		{Topic: root + "devices/" + d.ID + "/state", Payload: map[string]any{"binary": true}},
		{Topic: root + "devices/" + d.ID + "/state", Payload: dataPayload("tail-sample", at, map[string]any{"sample": fullValue}), UpdatedAt: at},
	}}
	platform.retainedPages = pages
	before := len(platform.callsFor("mqtt_retained_page"))
	restarted := &App{}
	if err := restarted.OnMount(a.ctx); err != nil {
		t.Fatal(err)
	}
	state, err := listState(restarted.ctx.AppDB(), d.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(state) != 1 || state[0].Key != "sample" || state[0].Value != fullValue || state[0].UpdatedAt != at {
		t.Fatal("startup did not recover the full scoped tail sample with its original timestamp")
	}
	calls := platform.callsFor("mqtt_retained_page")[before:]
	if len(calls) != len(pages) || str(calls[len(calls)-1].Input, "cursor") != "tail" {
		t.Fatalf("retained scan did not traverse every page: %d calls", len(calls))
	}
	for _, call := range calls {
		if str(call.Input, "topic_pattern") != root+"devices/+/+" {
			t.Fatalf("retained scan escaped device scope: %v", call.Input)
		}
	}
	if len(platform.callsFor("mqtt_retained_list")) != 0 || len(platform.callsFor("mqtt_retained_get")) != 0 {
		t.Fatal("retained recovery used a legacy preview or per-topic lookup")
	}
}

func TestRetainedRecoveryRejectsNonAdvancingCursor(t *testing.T) {
	a, platform, _ := newTestApp(t)
	platform.retainedPages = map[string]retainedDevicePage{
		"":     {HasMore: true, NextCursor: "same"},
		"same": {HasMore: true, NextCursor: "same"},
	}
	if err := a.reconcileRetained(); err == nil || !strings.Contains(err.Error(), "did not advance") {
		t.Fatalf("expected cursor error, got %v", err)
	}
}
