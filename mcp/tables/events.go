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

// emit is the one indirection every mutation path uses. ctx.Emit is
// fire-and-forget: a missed event is recoverable via the dashboard's
// since-cursor reconnect, so we never bubble errors back into the
// handler.
func emit(ctx *sdk.AppCtx, topic string, data map[string]any) {
	if ctx == nil {
		return
	}
	ctx.Emit(topic, data)
}
