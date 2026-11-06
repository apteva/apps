package main

import "sync"
import sdk "github.com/apteva/app-sdk"

// Topic naming mirrors storage's pattern: short, dot-separated,
// past-tense verbs. Consumers (the dashboard panel + sibling apps)
// can match by exact topic or by prefix ("table.*", "row.*").

const (
	topicTableCreated       = "table.created"
	topicTableAltered       = "table.altered"
	topicTableDropped       = "table.dropped"
	topicRowInserted        = "row.inserted"
	topicRowUpdated         = "row.updated"
	topicRowDeleted         = "row.deleted"
	topicDiagnosticRecorded = "diagnostics.recorded"
	topicProjectionReady    = "projection.ready"
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

func (b *batchEventBuffer) compactSnapshot() []batchEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Only adjacent aggregate invalidations may be combined. Detailed updates
	// carrying row/field data and single-row events retain their original shape.
	out := make([]batchEvent, 0, len(b.events))
	for _, event := range b.events {
		ids, aggregate := aggregateIDs(event)
		if aggregate && len(out) > 0 {
			last := &out[len(out)-1]
			previous, compatible := aggregateIDs(*last)
			if compatible && last.topic == event.topic && last.data["table"] == event.data["table"] && len(previous)+len(ids) <= 4096 {
				merged := append(append([]int64{}, previous...), ids...)
				last.data = map[string]any{"table": event.data["table"], "ids": merged, "count": len(merged)}
				continue
			}
		}
		out = append(out, event)
	}
	return out
}

func aggregateIDs(event batchEvent) ([]int64, bool) {
	if event.topic != topicRowInserted && event.topic != topicRowUpdated {
		return nil, false
	}
	if len(event.data) != 3 {
		return nil, false
	}
	if _, ok := event.data["table"].(string); !ok {
		return nil, false
	}
	ids, ok := event.data["ids"].([]int64)
	if !ok {
		return nil, false
	}
	count, ok := event.data["count"].(int)
	return ids, ok && count == len(ids)
}
