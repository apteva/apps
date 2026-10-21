package sdk

// TelemetryClient is the optional, unified read API: history, live events,
// resumable delivery and safe agent/runtime inventory. PlatformClient is unchanged.
// SubscribeTelemetry retains its original live-only signature and delegates to
// WatchTelemetry on the same connection path.

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// TelemetrySubscription filters live content, preserving the original contract.
type TelemetrySubscription struct {
	Events       []string
	ThreadPrefix string
	AgentID      int64
}

// TelemetryStreamEvent mirrors the platform's telemetry event shape.
type TelemetryStreamEvent struct {
	ServerID  string          `json:"server_id,omitempty"`
	ProjectID string          `json:"project_id,omitempty"`
	RuntimeID string          `json:"runtime_id,omitempty"`
	Seq       uint64          `json:"seq,omitempty"`
	ID        string          `json:"id"`
	AgentID   int64           `json:"instance_id"`
	ThreadID  string          `json:"thread_id"`
	Type      string          `json:"type"`
	Time      time.Time       `json:"time"`
	Data      json.RawMessage `json:"data"`
}

func subscribeTelemetry(ctx context.Context, watch func(context.Context, TelemetryWatch) (<-chan TelemetryDelivery, error), sub TelemetrySubscription) (<-chan TelemetryStreamEvent, error) {
	deliveries, err := watch(ctx, TelemetryWatch{
		Query:    TelemetryQuery{AgentID: sub.AgentID, Events: sub.Events, ThreadPrefix: sub.ThreadPrefix, Projection: "content"},
		LiveOnly: true,
	})
	if err != nil {
		return nil, err
	}
	out := make(chan TelemetryStreamEvent, 256)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case delivery, open := <-deliveries:
				if !open {
					return
				}
				if delivery.Event == nil {
					continue
				}
				select {
				case out <- *delivery.Event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}
func (c *telemetryClient) SubscribeTelemetry(ctx context.Context, sub TelemetrySubscription) (<-chan TelemetryStreamEvent, error) {
	return subscribeTelemetry(ctx, func(ctx context.Context, w TelemetryWatch) (<-chan TelemetryDelivery, error) {
		return c.watchTelemetry(ctx, w, true)
	}, sub)
}
func (c *httpPlatformClient) SubscribeTelemetry(ctx context.Context, sub TelemetrySubscription) (<-chan TelemetryStreamEvent, error) {
	return c.telemetry().SubscribeTelemetry(ctx, sub)
}
func (p *projectScopedClient) SubscribeTelemetry(ctx context.Context, sub TelemetrySubscription) (<-chan TelemetryStreamEvent, error) {
	// Keep wrappers around existing custom clients compatible too.
	client, ok := p.inner.(interface {
		SubscribeTelemetry(context.Context, TelemetrySubscription) (<-chan TelemetryStreamEvent, error)
	})
	if !ok {
		return nil, errors.New("telemetry API unavailable")
	}
	return client.SubscribeTelemetry(ctx, sub)
}
