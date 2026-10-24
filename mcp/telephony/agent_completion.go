package main

import (
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// Only an explicit completion from the authenticated, call-specific Core
// socket is a hangup instruction. EOF, replacement, and generic normal closes
// retain transport-recovery semantics. Never apply this to a human bridge.
func agentCoreCompletion(row *callRow, err error) bool {
	if row == nil || row.PeerKind != peerKindRealtime {
		return false
	}
	var closed wsutil.ClosedError
	return errors.As(err, &closed) && closed.Code == ws.StatusNormalClosure && (closed.Reason == "realtime conversation completed" || closed.Reason == "realtime completion timed out")
}
func (a *App) completeAgentFromCore(ctx *sdk.AppCtx, row *callRow, err error) error {
	if !agentCoreCompletion(row, err) {
		return nil
	}
	current, readErr := a.db().findCall(row.ID)
	if readErr != nil {
		return readErr
	}
	if current == nil || current.ProjectID != row.ProjectID || current.AgentID != row.AgentID || current.ThreadID != row.ThreadID || current.PeerKind != peerKindRealtime {
		return fmt.Errorf("completion belongs to a replaced call owner")
	}
	if isTerminalStatus(current.Status) {
		return nil
	}
	if message := a.hangupCall(ctx, row.ID, row.AgentID, row.ProjectID); message != "" {
		return fmt.Errorf("agent completion hangup failed: %s", message)
	}
	return nil
}
func (a *App) handleAgentCompletionClose(row *callRow, err error) bool {
	if !agentCoreCompletion(row, err) {
		return false
	}
	if completionErr := a.completeAgentFromCore(globalCtx, row, err); completionErr != nil {
		globalCtx.Logger().Warn("agent completion hangup failed", "call", row.ID)
		return false
	} else {
		globalCtx.Logger().Info("agent completion hangup confirmed", "call", row.ID)
		return true
	}
}
