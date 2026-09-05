package main

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
	ctx.Emit(topic, data)
}
