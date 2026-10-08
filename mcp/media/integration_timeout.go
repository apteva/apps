package main

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type integrationCallResult struct {
	result *sdk.ExecuteResult
	err    error
}

var integrationCallsInFlight sync.Map

func executeIntegrationToolWithTimeout(app *sdk.AppCtx, connID int64, tool string, input map[string]any, timeout time.Duration) (*sdk.ExecuteResult, error) {
	return executeIntegrationToolWithTimeoutKey(app, "default", connID, tool, input, timeout)
}

// executeIntegrationToolWithTimeoutKey keeps independently useful call
// classes from blocking each other. Interactive media_ask requests may run
// alongside the background describer, while repeated asks still serialize
// behind their own key so a timed-out upstream call cannot fan out forever.
func executeIntegrationToolWithTimeoutKey(app *sdk.AppCtx, callClass string, connID int64, tool string, input map[string]any, timeout time.Duration) (*sdk.ExecuteResult, error) {
	return executeIntegrationToolContext(context.Background(), app, callClass, connID, tool, input, timeout)
}
func executeIntegrationToolContext(ctx context.Context, app *sdk.AppCtx, callClass string, connID int64, tool string, input map[string]any, timeout time.Duration) (*sdk.ExecuteResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	key := callClass + ":" + strconv.FormatInt(connID, 10) + ":" + tool
	if _, loaded := integrationCallsInFlight.LoadOrStore(key, struct{}{}); loaded {
		return nil, errors.New("previous integration call is still running")
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ch := make(chan integrationCallResult, 1)
	go func() {
		defer integrationCallsInFlight.Delete(key)
		var res *sdk.ExecuteResult
		var err error
		platform := app.PlatformAPI()
		// Limit the new context-aware path to interactive asks. Other call
		// classes keep their existing execution behavior.
		if client, ok := platform.(sdk.IntegrationContextClient); ok && callClass == "media_ask" {
			res, err = client.ExecuteIntegrationToolContext(callCtx, connID, tool, input)
			// A project-scoped SDK wrapper can expose the optional API while
			// its legacy inner client does not. This exact SDK error occurs
			// before dispatch. The bounded worker/held slot protects fallback.
			if err != nil && err.Error() == "context-aware integration API unavailable" && callCtx.Err() == nil {
				res, err = platform.ExecuteIntegrationTool(connID, tool, input)
			}
		} else {
			res, err = platform.ExecuteIntegrationTool(connID, tool, input)
		}
		ch <- integrationCallResult{result: res, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case out := <-ch:
		return out.result, out.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-mediaDone(app):
		return nil, errors.New("integration call cancelled: app shutting down")
	case <-timer.C:
		return nil, errors.New("integration call timed out after " + timeout.String())
	}
}
