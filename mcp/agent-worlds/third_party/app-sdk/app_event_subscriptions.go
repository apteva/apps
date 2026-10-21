package sdk

import (
	"errors"
	"net/url"
)

// AppBusDeliveryEvent wraps a durably delivered app event. Event.Data contains
// event_id, subscription_key, revision, topic, occurred_at, and data. The outer
// Event contains the trusted source install and project; acknowledge only after
// persisting the delivery. Retries retain DeliveryID and event_id.
const AppBusDeliveryEvent = "app.bus.delivery"

type AppEventSubscription struct {
	Key             string `json:"key"`
	ProjectID       string `json:"project_id"`
	SourceInstallID int64  `json:"source_install_id"`
	Topic           string `json:"topic"`
	Revision        int    `json:"revision"`
	Enabled         bool   `json:"enabled"`
}
type AppEventSource struct {
	InstallID int64       `json:"install_id"`
	App       string      `json:"app"`
	Name      string      `json:"name"`
	Events    []EventDecl `json:"events"`
}

// EventBusClient is optional so existing PlatformClient implementations remain
// source compatible. Subscriptions always deliver to the calling installation.
type EventBusClient interface {
	PutAppEventSubscription(AppEventSubscription) error
	DeleteAppEventSubscription(projectID, key string) error
	ListAppEventSources(projectID string) ([]AppEventSource, error)
	PublishAppEvent(projectID, eventID, topic string, data any) error
}

func (c *AppCtx) EventBusAPI() EventBusClient {
	if c == nil || c.platform == nil {
		return nil
	}
	if scoped, ok := c.platform.(*projectScopedClient); ok {
		api, _ := scoped.inner.(EventBusClient)
		return api
	}
	api, _ := c.platform.(EventBusClient)
	return api
}
func (c *httpPlatformClient) PutAppEventSubscription(s AppEventSubscription) error {
	if s.Key == "" || s.ProjectID == "" || s.SourceInstallID <= 0 || s.Topic == "" || s.Revision < 1 {
		return errors.New("event subscription requires key, project, source install, topic and revision")
	}
	return c.post("/api/apps/callback/event-subscriptions", s, nil)
}
func (c *httpPlatformClient) DeleteAppEventSubscription(projectID, key string) error {
	return c.delete("/api/apps/callback/event-subscriptions/"+url.PathEscape(key)+"?project_id="+url.QueryEscape(projectID), nil)
}
func (c *httpPlatformClient) ListAppEventSources(projectID string) ([]AppEventSource, error) {
	var out []AppEventSource
	err := c.get("/api/apps/callback/event-subscriptions/sources?project_id="+url.QueryEscape(projectID), &out)
	return out, err
}
func (c *httpPlatformClient) PublishAppEvent(projectID, eventID, topic string, data any) error {
	if eventID == "" || topic == "" {
		return errors.New("event_id and topic required")
	}
	return c.post("/api/app-events/internal/emit", map[string]any{"project_id": projectID, "event_id": eventID, "topic": topic, "data": data}, nil)
}
