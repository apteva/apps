package main

import "sync"
import sdk "github.com/apteva/app-sdk"

// Topic naming mirrors storage's pattern: short, dot-separated,
// past-tense verbs. Consumers (the dashboard panel + sibling apps)
// can match by exact topic or by prefix ("table.*", "row.*").

const (
	topicTableCreated = "table.created"
	topicTableAltered = "table.altered"
	topicTableDropped = "table.dropped"
	topicRowInserted  = "row.inserted"
	topicRowUpdated   = "row.updated"
	topicRowDeleted   = "row.deleted"
)

// Emissions are best-effort UI invalidations. The panel reloads authoritative
// state after reconnect; consumers must not treat these as a durable job queue.
func emit(ctx *sdk.AppCtx, topic string, data map[string]any) {
	if ctx == nil {
		return
	}
	if buffer, ok := requestContext(ctx).Value(batchEventBufferKey{}).(*batchEventBuffer); ok && buffer != nil {
		buffer.add(topic, data)
		return
	}
	ctx.Emit(topic, data)
}

type batchEventBufferKey struct{}
type batchWriteTxKey struct{}

type batchEvent struct {
	topic string
	data  map[string]any
}

type batchEventBuffer struct {
	mu     sync.Mutex
	events []batchEvent
}

func (b *batchEventBuffer) add(topic string, data map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, batchEvent{topic: topic, data: data})
}

func (b *batchEventBuffer) snapshot() []batchEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]batchEvent, len(b.events))
	copy(out, b.events)
	return out
}
