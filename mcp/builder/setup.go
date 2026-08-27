package main

import (
	"errors"
	"net/http"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	setupPending      = "pending"
	setupAttached     = "attached"
	setupActionNeeded = "action_required"
	setupFailed       = "failed"
	setupUnsupported  = "unsupported"
)

var productionRetryDelays = []time.Duration{
	0,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	12 * time.Second,
	20 * time.Second,
}

type SetupStatus struct {
	State     string                    `json:"state"`
	Attempts  int                       `json:"attempts"`
	LastError string                    `json:"last_error,omitempty"`
	ErrorCode string                    `json:"error_code,omitempty"`
	Result    *sdk.EnsureAppToolsResult `json:"result,omitempty"`
	UpdatedAt string                    `json:"updated_at"`
}

func (a *App) currentSetupStatus() SetupStatus {
	a.setupMu.RLock()
	defer a.setupMu.RUnlock()
	return a.setup
}

func (a *App) setSetupStatus(status SetupStatus) {
	a.setupMu.Lock()
	a.setup = status
	a.setupMu.Unlock()
}

func (a *App) reconcileHelperOnce(api sdk.AgentToolsClient) (*sdk.EnsureAppToolsResult, error) {
	if api == nil {
		return nil, errors.New("AgentToolsAPI is unavailable")
	}
	return api.EnsureAppToolsAttached(sdk.EnsureAppToolsRequest{
		AgentKind:           sdk.AgentKindPlatformHelper,
		IncludeRequiredApps: []string{"conversations"},
	})
}

func retryableSetupError(err error) bool {
	if sdk.IsAgentToolsErrorCode(err, "caller_tools_not_ready") ||
		sdk.IsAgentToolsErrorCode(err, "required_app_not_bound") ||
		sdk.IsAgentToolsErrorCode(err, "target_agent_not_found") {
		return true
	}
	var platformErr *sdk.AgentToolsError
	if !errors.As(err, &platformErr) {
		// Local transport failures are safe to retry during the bounded startup
		// window. The explicit setup endpoint remains the later recovery path.
		return true
	}
	return platformErr.StatusCode >= http.StatusInternalServerError
}

func setupErrorCode(err error) string {
	var platformErr *sdk.AgentToolsError
	if errors.As(err, &platformErr) {
		return platformErr.Code
	}
	return ""
}

func setupStateForError(err error) string {
	if sdk.IsAgentToolsErrorCode(err, "target_agent_not_found") {
		return setupActionNeeded
	}
	if retryableSetupError(err) {
		return setupPending
	}
	return setupFailed
}

func (a *App) reconcileHelperLoop(ctx *sdk.AppCtx, api sdk.AgentToolsClient) {
	delays := a.retryDelays
	if len(delays) == 0 {
		delays = productionRetryDelays
	}
	for attempt, delay := range delays {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}

		result, err := a.reconcileHelperOnce(api)
		if err == nil {
			a.setSetupStatus(SetupStatus{
				State: setupAttached, Attempts: attempt + 1, Result: result, UpdatedAt: nowUTC(),
			})
			ctx.Logger().Info("Builder and Conversations attached to Helper", "attempt", attempt+1, "changed", result.Changed)
			return
		}
		status := SetupStatus{
			State: setupStateForError(err), Attempts: attempt + 1,
			LastError: err.Error(), ErrorCode: setupErrorCode(err), UpdatedAt: nowUTC(),
		}
		a.setSetupStatus(status)
		if !retryableSetupError(err) {
			ctx.Logger().Error("Builder Helper attachment failed", "err", err)
			return
		}
	}
	status := a.currentSetupStatus()
	if status.State == setupPending {
		status.LastError = "Builder is waiting for app activation to finish; open Builder to retry setup"
		status.UpdatedAt = nowUTC()
		a.setSetupStatus(status)
	}
}

func (a *App) handleSetupReconcile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if a.ctx == nil || a.ctx.AgentToolsAPI() == nil {
		writeJSON(w, http.StatusNotImplemented, SetupStatus{
			State: setupUnsupported, LastError: "this platform does not expose AgentToolsAPI", UpdatedAt: nowUTC(),
		})
		return
	}
	previous := a.currentSetupStatus()
	result, err := a.reconcileHelperOnce(a.ctx.AgentToolsAPI())
	if err != nil {
		status := SetupStatus{
			State: setupStateForError(err), Attempts: previous.Attempts + 1,
			LastError: err.Error(), ErrorCode: setupErrorCode(err), UpdatedAt: nowUTC(),
		}
		a.setSetupStatus(status)
		writeJSON(w, http.StatusConflict, status)
		return
	}
	status := SetupStatus{
		State: setupAttached, Attempts: previous.Attempts + 1, Result: result, UpdatedAt: nowUTC(),
	}
	a.setSetupStatus(status)
	writeJSON(w, http.StatusOK, status)
}
