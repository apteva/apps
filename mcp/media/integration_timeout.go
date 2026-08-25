package main

import (
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
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	key := callClass + ":" + strconv.FormatInt(connID, 10) + ":" + tool
	if _, loaded := integrationCallsInFlight.LoadOrStore(key, struct{}{}); loaded {
		return nil, errors.New("previous integration call is still running")
	}
	ch := make(chan integrationCallResult, 1)
	go func() {
		defer integrationCallsInFlight.Delete(key)
		res, err := app.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
		ch <- integrationCallResult{result: res, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case out := <-ch:
		return out.result, out.err
	case <-app.Done():
		return nil, errors.New("integration call cancelled: app shutting down")
	case <-timer.C:
		return nil, errors.New("integration call timed out after " + timeout.String())
	}
}
