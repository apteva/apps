package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type liveOccupancyCall struct {
	CallID        string        `json:"call_id"`
	Direction     string        `json:"direction"`
	Status        string        `json:"status"`
	OwnerIdentity phoneIdentity `json:"owner_identity"`
}

type liveOccupancySnapshot struct {
	SnapshotAt string              `json:"snapshot_at"`
	Complete   bool                `json:"complete"`
	Calls      []liveOccupancyCall `json:"calls"`
}

// Match reserveCapacityTx's nonterminal ownership rules. Start with the sparse
// live-human index, then probe the owner PK: historical owner rows must never
// become the outer scan. One statement observes calls and owners atomically.
const liveOccupancySQL = `SELECT c.id,c.direction,c.status,o.principal
 FROM calls c INDEXED BY idx_calls_live_occupancy
 CROSS JOIN telephony_call_owners o ON o.call_id=c.id AND o.project_id=c.project_id
 WHERE c.project_id=? AND c.peer_kind='human'
 AND c.status NOT IN ('completed','failed','busy','no-answer','canceled')`

func liveOccupancyQuery(direction string) string {
	q := liveOccupancySQL
	if direction != "" {
		q += ` AND c.direction=?`
	}
	return q + ` ORDER BY c.direction,c.id`
}

func readLiveOccupancy(ctx context.Context, db *sql.DB, project, direction string) (*liveOccupancySnapshot, error) {
	if project == "" {
		return nil, errors.New("project context required")
	}
	if direction != "" && direction != "inbound" && direction != "outbound" {
		return nil, errors.New("direction must be inbound or outbound")
	}
	args := []any{project}
	if direction != "" {
		args = append(args, direction)
	}
	rows, err := db.QueryContext(ctx, liveOccupancyQuery(direction), args...)
	if err != nil {
		return nil, fmt.Errorf("read live occupancy: %w", err)
	}
	defer rows.Close()
	// QueryContext has opened the statement snapshot. This is its observation
	// time, not a guarantee that the adviser remains occupied after the read.
	result := &liveOccupancySnapshot{SnapshotAt: time.Now().UTC().Format(time.RFC3339Nano), Calls: []liveOccupancyCall{}}
	for rows.Next() {
		var call liveOccupancyCall
		var principal string
		if err := rows.Scan(&call.CallID, &call.Direction, &call.Status, &principal); err != nil {
			return nil, fmt.Errorf("read live occupancy row: %w", err)
		}
		if json.Unmarshal([]byte(principal), &call.OwnerIdentity) != nil || !call.OwnerIdentity.valid() {
			// Never turn corrupt ownership into an apparently available adviser.
			// Do not include the stored principal in errors or logs.
			return nil, errors.New("invalid live call owner identity")
		}
		result.Calls = append(result.Calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read live occupancy rows: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result.Complete = true
	return result, nil
}

func (a *App) toolLiveOccupancy(c context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	direction := ""
	if v, ok := args["direction"]; ok {
		var valid bool
		direction, valid = v.(string)
		if !valid || (direction != "inbound" && direction != "outbound") {
			return nil, errors.New("direction must be inbound or outbound")
		}
	}
	for _, key := range []string{"limit", "cursor", "offset"} {
		if _, exists := args[key]; exists {
			return nil, errors.New("live occupancy always returns a complete snapshot; pagination is unsupported")
		}
	}
	project := currentProject(ctx)
	if project == "" {
		return nil, errors.New("project context required")
	}
	started := time.Now()
	result, err := readLiveOccupancy(c, ctx.AppDB(), project, direction)
	elapsed := float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		ctx.Logger().Warn("live occupancy snapshot failed", "project_id", project, "query_duration_ms", elapsed, "error", err)
		// Return a handler error, not an error-shaped success object. This also
		// fails bound-app calls using the SDK's unwrapped JSON result format.
		return nil, fmt.Errorf("live occupancy unavailable: %w", err)
	}
	ctx.Logger().Info("live occupancy snapshot", "project_id", project, "direction", direction, "query_duration_ms", elapsed, "call_count", len(result.Calls))
	return result, nil
}
