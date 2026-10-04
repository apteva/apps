package main

import (
	"encoding/json"
	"reflect"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type eventBusPlatform struct {
	recordingPlatform
	published []publishedTaskEvent
}

type publishedTaskEvent struct {
	ProjectID string
	EventID   string
	Topic     string
	Data      any
}

func (p *eventBusPlatform) PlatformInfo() (*sdk.PlatformInfo, error) {
	info := &sdk.PlatformInfo{}
	field := reflect.ValueOf(info).Elem().FieldByName("AsyncResultNotifications")
	if field.IsValid() && field.CanSet() {
		capability := reflect.New(field.Type().Elem())
		capability.Elem().FieldByName("Version").SetInt(1)
		modes := capability.Elem().FieldByName("Modes")
		modes.Set(reflect.ValueOf([]string{"once", "stream"}))
		field.Set(capability)
	}
	return info, nil
}

func (p *eventBusPlatform) PutAppEventSubscription(sdk.AppEventSubscription) error { return nil }
func (p *eventBusPlatform) DeleteAppEventSubscription(string, string) error        { return nil }
func (p *eventBusPlatform) ListAppEventSources(string) ([]sdk.AppEventSource, error) {
	return nil, nil
}
func (p *eventBusPlatform) PublishAppEvent(projectID, eventID, topic string, data any) error {
	p.published = append(p.published, publishedTaskEvent{
		ProjectID: projectID,
		EventID:   eventID,
		Topic:     topic,
		Data:      data,
	})
	return nil
}

func newEventBusEnv(t *testing.T) (*sdk.AppCtx, *eventBusPlatform) {
	t.Helper()
	base := &recordingPlatform{
		agents: map[int64]*sdk.PlatformAgent{
			41: {ID: 41, Name: "Research", ProjectID: testProject},
			42: {ID: 42, Name: "CRM", ProjectID: testProject},
		},
		attached: map[int64]bool{41: true, 42: true},
	}
	platform := &eventBusPlatform{recordingPlatform: *base}
	ctx := tkNewEventBusAppCtx(t, platform)
	return ctx, platform
}

// Keep the event-bus test setup beside the event-bus stub without changing the
// default test environment, whose legacy platform intentionally has no bus.
func tkNewEventBusAppCtx(t *testing.T, platform *eventBusPlatform) *sdk.AppCtx {
	t.Helper()
	return newTestAppCtx(t, platform)
}

func newTestAppCtx(t *testing.T, platform sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	return tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject), tk.WithPlatform(platform))
}

func TestSaveReplyQueuesProgressAndTerminalEventsTransactionally(t *testing.T) {
	app, _ := newEventBusEnv(t)
	task, err := createTask(app.AppDB(), &Task{
		ProjectID:   testProject,
		Kind:        "ask",
		Status:      "working",
		Direction:   "local",
		FromAgentID: 41,
		ToAgentID:   42,
	})
	if err != nil {
		t.Fatal(err)
	}
	task.Status = "completed"
	if _, err := saveReply(app.AppDB(), task, 42, 41, "finished", "", nil); err != nil {
		t.Fatal(err)
	}

	rows, err := app.AppDB().Query(`SELECT topic,payload_json FROM a2a_event_outbox WHERE project_id=? ORDER BY topic`, testProject)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var topics []string
	for rows.Next() {
		var topic, raw string
		if err := rows.Scan(&topic, &raw); err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["task_id"] != float64(task.ID) || payload["message"] != "finished" {
			t.Fatalf("event payload = %#v", payload)
		}
		topics = append(topics, topic)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(topics) != 2 || topics[0] != "task.completed" || topics[1] != "task.updated" {
		t.Fatalf("outbox topics = %v, want task.completed and task.updated", topics)
	}
}

func TestAsyncResultCapabilitySuppressesLegacyRequesterDelivery(t *testing.T) {
	app, platform := newEventBusEnv(t)
	if !reflect.ValueOf(&sdk.PlatformInfo{}).Elem().FieldByName("AsyncResultNotifications").IsValid() {
		t.Skip("public SDK does not expose async result capability metadata yet")
	}
	task := &Task{ID: 7, FromAgentID: 41, ProjectID: testProject}
	if got := requesterReplyEvent(app, task, 41, "legacy event"); got != "" {
		t.Fatalf("requester event = %q, want suppressed", got)
	}
	if got := requesterReplyEvent(app, task, 42, "peer event"); got != "peer event" {
		t.Fatalf("non-requester event = %q, want peer event", got)
	}
	if len(platform.published) != 0 {
		t.Fatalf("capability detection published events unexpectedly: %+v", platform.published)
	}
}
