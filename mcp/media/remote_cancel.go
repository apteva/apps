package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errRemoteCancellation = errors.New("REMOTE_CANCELLATION_FAILED")
var runRemoteCancellation = runRemote

// Explicit Bash avoids default-shell kill option differences. The workdir
// token and session leader check prevent signalling an unrelated process.
func remoteCancellationScript(work string) string {
	body := `set -eu
WORK=` + shellQuote(work) + `
mkdir -p "$WORK"
touch "$WORK/cancel.requested"
if [ ! -f "$WORK/pid" ]; then
 echo 'REMOTE_CANCEL_CONFIRMED_GUARDED_NOT_STARTED'; exit 0
fi
PID=$(cat "$WORK/pid")
case "$PID" in ''|*[!0-9]*) echo 'REMOTE_CANCEL_INVALID_PID'; exit 4;; esac
if [ "$PID" -le 1 ]; then echo 'REMOTE_CANCEL_INVALID_PID'; exit 4; fi
live_group() {
 PROCESSES=$(ps -eo pid=,pgid=,stat=) || { echo 'REMOTE_CANCEL_VERIFY_FAILED'; exit 9; }
 printf '%s\n' "$PROCESSES" | awk -v g="$PID" '$2==g && $3 !~ /^Z/ { found=1 } END { exit !found }'
}
PGID=$(ps -o pgid= -p "$PID" | tr -d ' ')
if [ -n "$PGID" ] && [ "$PGID" != "$PID" ]; then echo 'REMOTE_CANCEL_UNSAFE_GROUP'; exit 5; fi
if ! live_group; then echo 'REMOTE_CANCEL_CONFIRMED'; exit 0; fi
kill -TERM -- "-$PID" || { if ! live_group; then echo 'REMOTE_CANCEL_CONFIRMED'; exit 0; fi; echo 'REMOTE_CANCEL_TERM_FAILED'; exit 6; }
for attempt in 1 2 3 4 5; do
 if ! live_group; then echo 'REMOTE_CANCEL_CONFIRMED'; exit 0; fi
 sleep 1
done
kill -KILL -- "-$PID" || { if ! live_group; then echo 'REMOTE_CANCEL_CONFIRMED'; exit 0; fi; echo 'REMOTE_CANCEL_KILL_FAILED'; exit 7; }
for attempt in 1 2 3; do
 if ! live_group; then echo 'REMOTE_CANCEL_CONFIRMED'; exit 0; fi
 sleep 1
done
echo 'REMOTE_CANCEL_WORKERS_STILL_RUNNING'; exit 8
`
	return "bash -c " + shellQuote(body)
}

// Both cancellation and Execute completion join the same attempt. Normal
// completion stops the watcher without issuing a late kill from deferred cancel.
func registerRemoteKill(ctx context.Context, app *sdk.AppCtx, hostID, renderID int64, workDirs ...string) func() error {
	done := make(chan struct{})
	var once sync.Once
	var result error
	kill := func() {
		once.Do(func() {
			result = performRemoteCancellation(app, hostID, renderID, selectedRemoteWorkDir(renderID, workDirs))
		})
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			kill()
		case <-done:
		}
	}()
	return func() error {
		close(done)
		if ctx.Err() != nil {
			kill()
		}
		<-finished
		return result
	}
}

// Keep error evidence visible even if a cancellation request already changed
// the row's terminal status. A failed stop is never reported as confirmed.
func recordCancellationFailure(app *sdk.AppCtx, row *RenderRow, err error) {
	_, _ = app.AppDB().Exec(`UPDATE renders SET status='failed',error=?,completed_at=? WHERE id=? AND status IN ('running','cancelled')`, err.Error(), time.Now().UTC().Format(time.RFC3339), row.ID)
	emitRenderFailed(app, row.ID, row.ProjectID, row.Operation, err.Error())
}

func performRemoteCancellation(app *sdk.AppCtx, hostID, renderID int64, work string) error {
	out, exit, err := runRemoteCancellation(context.Background(), app, hostID, remoteCancellationScript(work), 20)
	confirmed := err == nil && exit == 0 && strings.Contains(out, "REMOTE_CANCEL_CONFIRMED")
	recordRenderMetric(app, &RenderRow{ID: renderID}, "cancellation_confirmed", confirmed)
	if confirmed {
		return nil
	}
	result := fmt.Errorf("%w: host_id=%d render_id=%d exit=%d; %v; %s", errRemoteCancellation, hostID, renderID, exit, err, truncate(strings.TrimSpace(out), 500))
	recordRenderMetric(app, &RenderRow{ID: renderID}, "cancellation_error", result.Error())
	app.Logger().Warn("remote render cancellation unconfirmed", "id", renderID, "err", result)
	return result
}

// A restarted sidecar has no in-memory cancel callback for an orphaned job.
// Its persisted executor identity still permits verified cleanup.
func cancelOrphanRemoteRender(app *sdk.AppCtx, row *RenderRow) error {
	if row.Status != "running" {
		return nil
	}
	var metrics struct {
		Work string `json:"remote_work_dir"`
		Host int64  `json:"remote_host_id"`
	}
	if json.Unmarshal(row.Metrics, &metrics) != nil || metrics.Work == "" {
		return nil
	}
	if metrics.Host <= 0 {
		metrics.Host, _ = strconv.ParseInt(app.Config().Get("render_host_id"), 10, 64)
	}
	if metrics.Host <= 0 {
		return fmt.Errorf("%w: cannot identify executor host for orphan render %d", errRemoteCancellation, row.ID)
	}
	if err := performRemoteCancellation(app, metrics.Host, row.ID, metrics.Work); err != nil {
		recordCancellationFailure(app, row, err)
		return err
	}
	return nil
}
