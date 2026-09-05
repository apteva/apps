package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

// Lifecycle is execution telemetry, not proof that the approved business action
// succeeded. The receipt table accepts duplicate/out-of-order callbacks safely.
func (a *App) handleApprovalLifecycle(_ *sdk.AppCtx, event sdk.Event) error {
	life, err := sdk.DecodeAgentEventLifecycle(event)
	if err != nil {
		return err
	}
	var messageID int64
	if _, err := fmt.Sscanf(life.SourceEventID, "conversation:approval:%d:result", &messageID); err != nil || messageID <= 0 || life.SourceEventID != fmt.Sprintf("conversation:approval:%d:result", messageID) {
		return fmt.Errorf("unknown approval event identity")
	}
	_, err = a.store.db.Exec(`INSERT INTO approval_lifecycle(message_id,delivery_id,execution_id,sequence,state,reason)
 SELECT id,?,?,?,?,? FROM messages WHERE id=?
 ON CONFLICT(message_id) DO UPDATE SET delivery_id=excluded.delivery_id,execution_id=excluded.execution_id,sequence=excluded.sequence,state=excluded.state,reason=excluded.reason,updated_at=CURRENT_TIMESTAMP
 WHERE excluded.sequence>approval_lifecycle.sequence`, life.ID, life.ExecutionID, life.Sequence, life.Type, life.Reason, messageID)
	return err
}
