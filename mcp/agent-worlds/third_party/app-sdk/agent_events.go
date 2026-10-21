package sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	AgentEventLifecycleEvent = "agent.event.lifecycle"
	AgentEventClaimed        = "event.claimed"
	AgentEventActive         = "event.active"
	AgentEventSettled        = "event.settled"
	AgentEventError          = "event.error"
)

// AgentEventRequest submits one app-owned event to an agent with durable Core
// lifecycle tracking. SourceEventID is the app's stable idempotency key and
// must be reused for retries of the same logical event. The app manifest must
// declare PermInstancesWrite; targeting a non-main thread also requires
// PermThreadsWrite.
type AgentEventRequest struct {
	AgentID       int64  `json:"agent_id"`
	ThreadID      string `json:"thread_id,omitempty"`
	SourceEventID string `json:"source_event_id"`
	Message       any    `json:"message"`
}

// AgentEventReceipt is the stable mapping between the app event and Core's
// execution. Duplicate means the same source event had already been submitted.
type AgentEventReceipt struct {
	SourceEventID string `json:"source_event_id"`
	ExecutionID   string `json:"execution_id"`
	Accepted      bool   `json:"accepted"`
	Duplicate     bool   `json:"duplicate"`
	ThreadID      string `json:"thread_id"`
}

// AgentEventLifecycle is delivered to the originating app through its normal
// /events endpoint with Event.Event == AgentEventLifecycleEvent. DeliveryID is
// the stable transition ID and must be deduplicated transactionally by apps.
// AgentEventSettled means Core and causally involved threads returned to an
// idle/pace boundary; it does not mean the app's business operation succeeded.
// Apps remain responsible for recording their own completion or failure state.
type AgentEventLifecycle struct {
	ID                string    `json:"id,omitempty"`
	Type              string    `json:"type"`
	SourceEventID     string    `json:"source_event_id"`
	ExecutionID       string    `json:"execution_id"`
	ThreadID          string    `json:"thread_id"`
	ParentExecutionID string    `json:"parent_execution_id,omitempty"`
	Timestamp         time.Time `json:"timestamp"`
	Reason            string    `json:"reason,omitempty"`
	Sequence          uint64    `json:"sequence"`
}

// DecodeAgentEventLifecycle validates and decodes one lifecycle delivery. ID
// is populated from Event.DeliveryID, which is the application's deduplication
// key even when the data payload omits a redundant id field.
func DecodeAgentEventLifecycle(event Event) (*AgentEventLifecycle, error) {
	if event.Name() != AgentEventLifecycleEvent {
		return nil, fmt.Errorf("expected %s event, got %q", AgentEventLifecycleEvent, event.Name())
	}
	if strings.TrimSpace(event.DeliveryID) == "" {
		return nil, errors.New("agent lifecycle event omitted delivery_id")
	}
	raw, err := json.Marshal(event.Data)
	if err != nil {
		return nil, err
	}
	var lifecycle AgentEventLifecycle
	if err := json.Unmarshal(raw, &lifecycle); err != nil {
		return nil, fmt.Errorf("decode agent lifecycle event: %w", err)
	}
	lifecycle.ID = event.DeliveryID
	if lifecycle.Type == "" || lifecycle.ExecutionID == "" || lifecycle.SourceEventID == "" {
		return nil, errors.New("agent lifecycle event is incomplete")
	}
	return &lifecycle, nil
}

// AgentEventClient is an optional PlatformClient extension. Keeping it
// separate preserves source compatibility for custom clients and test stubs.
type AgentEventClient interface {
	SendTrackedAgentEvent(request AgentEventRequest) (*AgentEventReceipt, error)
}

func validateAgentEventRequest(request AgentEventRequest) error {
	if request.AgentID <= 0 {
		return errors.New("agent event: agent_id required")
	}
	eventID := strings.TrimSpace(request.SourceEventID)
	if eventID == "" {
		return errors.New("agent event: source_event_id required")
	}
	if len(eventID) > 1024 || strings.ContainsAny(eventID, "\r\n\x00") {
		return errors.New("agent event: source_event_id must be at most 1024 bytes and contain no control separators")
	}
	if request.Message == nil {
		return errors.New("agent event: message required")
	}
	threadID := strings.TrimSpace(request.ThreadID)
	if strings.Contains(threadID, "/") {
		return errors.New("agent event: invalid thread_id")
	}
	return nil
}

func (c *httpPlatformClient) SendTrackedAgentEvent(request AgentEventRequest) (*AgentEventReceipt, error) {
	if err := validateAgentEventRequest(request); err != nil {
		return nil, err
	}
	request.SourceEventID = strings.TrimSpace(request.SourceEventID)
	request.ThreadID = strings.TrimSpace(request.ThreadID)
	var receipt AgentEventReceipt
	path := fmt.Sprintf("/api/apps/callback/agents/%d/event", request.AgentID)
	if err := c.post(path, map[string]any{
		"message":         request.Message,
		"thread_id":       request.ThreadID,
		"source_event_id": request.SourceEventID,
		"track_lifecycle": true,
	}, &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}
