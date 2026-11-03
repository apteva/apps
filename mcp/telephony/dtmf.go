package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	carrierCommandDTMF = "dtmf"
	// Agents press menu choices, not long strings.
	dtmfToolMaxDigits         = 16
	dtmfIdempotencyKeyMaxSize = 200
	// Upper bound on how long Twilio takes per played digit. It only sizes the
	// window in which the stream stop is expected; the lease is released as
	// soon as the replacement stream carries media.
	twilioDTMFDigitWindow     = 600 * time.Millisecond
	twilioDTMFReconnectWindow = 10 * time.Second
)

var errDTMFBusy = errors.New("keypad tones are already being sent on this call; retry shortly")

// ─── telephony_send_dtmf ───────────────────────────────────────────

func (a *App) toolSendDTMF(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	if agentID == 0 {
		return mcpError("could not determine calling agent id"), nil
	}
	callID := strArg(args, "call_id", "")
	if callID == "" {
		return mcpError("call_id required"), nil
	}
	digits := strings.TrimSpace(strArg(args, "digits", ""))
	if !validToolDTMFDigits(digits) {
		return mcpError(fmt.Sprintf("digits must be 1-%d characters from 0-9, * and #", dtmfToolMaxDigits)), nil
	}
	key := strings.TrimSpace(strArg(args, "idempotency_key", ""))
	if len(key) > dtmfIdempotencyKeyMaxSize {
		return mcpError(fmt.Sprintf("idempotency_key must be at most %d characters", dtmfIdempotencyKeyMaxSize)), nil
	}
	result, msg := a.sendDTMF(ctx, callID, digits, key, agentID, currentProject(ctx))
	if msg != "" {
		return mcpError(msg), nil
	}
	return result, nil
}

func validToolDTMFDigits(digits string) bool {
	if len(digits) == 0 || len(digits) > dtmfToolMaxDigits {
		return false
	}
	for _, digit := range digits {
		if !strings.ContainsRune("0123456789*#", digit) {
			return false
		}
	}
	return true
}

func (a *App) sendDTMF(ctx *sdk.AppCtx, callID, digits, key string, agentID int64, projectID string) (map[string]any, string) {
	row, err := a.db().findCall(callID)
	if err != nil || row == nil {
		return nil, "unknown call_id"
	}
	if projectID == "" || row.ProjectID != projectID {
		return nil, "call does not belong to this project"
	}
	if agentID != 0 && row.AgentID != agentID {
		return nil, "call belongs to another agent"
	}
	if isTerminalStatus(row.Status) {
		return nil, "call has ended"
	}
	if row.Status != "answered" && row.Status != "in-progress" {
		return nil, "call is not connected yet"
	}
	result := map[string]any{"call_id": row.ID, "digits": digits, "status": "sent", "duplicate": false}
	if key != "" {
		result["idempotency_key"] = key
		existing, err := a.db().claimDTMFCommand(row, key, digits)
		if err != nil {
			return nil, "record keypad command: " + err.Error()
		}
		if existing != nil {
			if existing.Digits != digits {
				return nil, "idempotency_key was already used for different digits"
			}
			if existing.Status == "sending" {
				return nil, "keypad command with this idempotency_key is still being sent"
			}
			result["duplicate"] = true
			return result, ""
		}
	}
	sendErr := a.sendCarrierDTMF(ctx, row, digits)
	if key != "" {
		status, message := "sent", ""
		if sendErr != nil {
			status, message = "failed", sendErr.Error()
		}
		if err := a.db().finishDTMFCommand(row.ID, key, status, message); err != nil {
			ctx.Logger().Warn("record keypad command outcome", "call", row.ID, "err", err)
		}
	}
	if sendErr != nil {
		return nil, sendErr.Error()
	}
	return result, ""
}

type dtmfCommand struct {
	Digits string
	Status string
}

// claimDTMFCommand records a new command, or reclaims one whose earlier
// attempt failed. It returns the existing command when the key was already
// sent or is being sent.
func (c *callsDB) claimDTMFCommand(row *callRow, key, digits string) (*dtmfCommand, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := c.db.Exec(`INSERT INTO call_dtmf_commands(call_id,idempotency_key,project_id,digits,status,created_at,updated_at)
        VALUES(?,?,?,?,'sending',?,?) ON CONFLICT(call_id,idempotency_key) DO UPDATE SET status='sending',error='',updated_at=excluded.updated_at
        WHERE call_dtmf_commands.status='failed' AND call_dtmf_commands.digits=excluded.digits`,
		row.ID, key, row.ProjectID, digits, now, now)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return nil, err
	}
	var existing dtmfCommand
	err = c.db.QueryRow(`SELECT digits,status FROM call_dtmf_commands WHERE call_id=? AND idempotency_key=?`, row.ID, key).Scan(&existing.Digits, &existing.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("keypad command disappeared while being claimed")
	}
	return &existing, err
}

func (c *callsDB) finishDTMFCommand(callID, key, status, message string) error {
	_, err := c.db.Exec(`UPDATE call_dtmf_commands SET status=?,error=?,updated_at=? WHERE call_id=? AND idempotency_key=?`,
		status, message, time.Now().UTC().Format(time.RFC3339Nano), callID, key)
	return err
}

// Twilio Media Streams cannot carry keypad events. Twilio plays digits only
// from TwiML, so the call's TwiML is replaced with the tones followed by a
// new stream. The stream stop that causes is announced as a handover, and the
// carrier command lease holds media recovery off until the replacement
// arrives. If it never does, the call is handled like any other Twilio stream
// loss once the lease ends.
func (a *App) sendTwilioDTMF(ctx *sdk.AppCtx, row *callRow, digits string) error {
	if strings.ContainsAny(strings.ToUpper(digits), "ABCD") {
		return errors.New("twilio cannot send DTMF digits A-D")
	}
	unlock := a.softphones.lockClaim(row.ID)
	token, err := a.reserveCarrierCommand(row, carrierCommandDTMF, carrierCommandDTMF)
	if errors.Is(err, errAnswerPreparationInProgress) {
		unlock()
		return errDTMFBusy
	}
	if err != nil {
		unlock()
		return err
	}
	window := time.Duration(len(digits))*twilioDTMFDigitWindow + twilioDTMFReconnectWindow
	until := time.Now().Add(window)
	_, err = a.db().db.Exec(`UPDATE call_carrier_commands SET lease_until=? WHERE call_id=? AND token=?`, ringTime(until), row.ID, token)
	if err != nil {
		a.releaseCarrierCommand(row.ID, token)
		unlock()
		return err
	}
	a.markCarrierHandover(row.ID, until)
	unlock()

	twiml := `<Response><Play digits="` + xmlEscape(digits) + `"/>` + a.twilioConnectStreamTwiML(row) + `</Response>`
	_, err = executeCarrierTool(ctx, row.CarrierConnectionID, "update_call", map[string]any{
		"CallSid": row.CarrierSID, "Twiml": twiml,
	})
	if err != nil {
		// Twilio may still have acted on a command that timed out. Then keep the
		// handover and lease: they expire on their own.
		if !ambiguousCarrierCommandError(err, context.Background()) {
			a.clearCarrierHandover(row.ID)
			a.releaseCarrierCommand(row.ID, token)
		}
		return fmt.Errorf("send DTMF through twilio: %w", err)
	}
	return nil
}
