package main

import (
	"context"
	"errors"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const carrierActivationCommandTimeout = 8 * time.Second

// Caller holds the short per-call claim lock. The durable reservation prevents
// overlapping dispatches without blocking carrier sockets or synchronous callbacks.
func (a *App) reserveCarrierCommand(row *callRow, operation, owner string) (string, error) {
	token := newSecret()
	res, err := a.db().db.Exec(`INSERT INTO call_carrier_commands(call_id,project_id,token,owner,operation,lease_until) VALUES(?,?,?,?,?,?) ON CONFLICT(call_id) DO UPDATE SET token=excluded.token,owner=excluded.owner,operation=excluded.operation,lease_until=excluded.lease_until WHERE call_carrier_commands.lease_until<=?`, row.ID, row.ProjectID, token, owner, operation, ringTime(time.Now().Add(carrierActivationCommandTimeout+2*time.Second)), ringTime(time.Now()))
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", errAnswerPreparationInProgress
	}
	return token, nil
}
func (a *App) releaseCarrierCommand(id, token string) {
	_, _ = a.db().db.Exec(`DELETE FROM call_carrier_commands WHERE call_id=? AND token=?`, id, token)
}
func (a *App) ownsCarrierCommand(id, token string) (bool, error) {
	var owns bool
	err := a.db().db.QueryRow(`SELECT EXISTS(SELECT 1 FROM call_carrier_commands WHERE call_id=? AND token=?)`, id, token).Scan(&owns)
	return owns, err
}
func (a *App) carrierCommandPending(id string) (bool, error) {
	var pending bool
	err := a.db().db.QueryRow(`SELECT EXISTS(SELECT 1 FROM call_carrier_commands WHERE call_id=? AND lease_until>?)`, id, ringTime(time.Now())).Scan(&pending)
	return pending, err
}

// Generic human activation for programmable and direct-SIP carriers. An existing
// reservation is observed instead of issuing a second answer on browser reattach.
func (a *App) activateHumanCarrier(request context.Context, ctx *sdk.AppCtx, expected *callRow) (*callRow, error) {
	work, cancel := context.WithTimeout(request, carrierActivationCommandTimeout)
	defer cancel()
	for {
		unlock := a.softphones.lockClaim(expected.ID)
		row, err := a.db().findCall(expected.ID)
		if err != nil {
			unlock()
			return nil, err
		}
		if row == nil || isTerminalStatus(row.Status) {
			unlock()
			return nil, errAnswerCallEnded
		}
		if row.ProjectID != expected.ProjectID || row.PeerKind != peerKindHuman || row.PeerToken != expected.PeerToken || row.ThreadID != expected.ThreadID {
			unlock()
			return nil, errAnswerPreparationInProgress
		}
		if row.Status == "answered" || row.Status == "in-progress" {
			unlock()
			return row, nil
		}
		if row.Status != "answering" {
			unlock()
			return nil, errors.New("human answer claim is no longer active")
		}
		token, err := a.reserveCarrierCommand(row, "human-answer", row.PeerToken)
		if errors.Is(err, errAnswerPreparationInProgress) {
			unlock()
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-work.Done():
				timer.Stop()
				return nil, work.Err()
			case <-timer.C:
				continue
			}
		}
		if err != nil {
			unlock()
			return nil, err
		}
		unlock()
		commandErr := a.answerInboundCarrierCall(ctx, row, work)
		unlock = a.softphones.lockClaim(row.ID)
		current, err := a.db().findCall(row.ID)
		owns, ownErr := a.ownsCarrierCommand(row.ID, token)
		a.releaseCarrierCommand(row.ID, token)
		if err != nil || ownErr != nil {
			unlock()
			return nil, firstError(err, ownErr)
		}
		if current == nil || isTerminalStatus(current.Status) {
			unlock()
			return nil, errAnswerCallEnded
		}
		if !owns || current.PeerToken != row.PeerToken || current.ThreadID != row.ThreadID || current.PeerKind != peerKindHuman {
			unlock()
			return nil, errAnswerPreparationInProgress
		}
		// Positive carrier/media evidence can arrive before the command response.
		// Never reset a successfully connected call merely because its HTTP reply failed.
		if commandErr != nil && (current.MediaConnectedAt == "" || current.MediaStatus != "connected") {
			_ = a.db().resetAnswerClaim(row.ID, row.PeerToken)
			unlock()
			return nil, commandErr
		}
		if err = a.db().updateStatus(row.ID, "answered", ""); err == nil {
			current, err = a.db().findCall(row.ID)
		}
		unlock()
		return current, err
	}
}

// Waiting for a release outcome must not hold the lock needed by carrier media.
func (a *App) waitCarrierCommand(request context.Context, id string) error {
	work, cancel := context.WithTimeout(request, carrierActivationCommandTimeout+2*time.Second)
	defer cancel()
	for {
		pending, err := a.carrierCommandPending(id)
		if err != nil || !pending {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-work.Done():
			timer.Stop()
			return work.Err()
		case <-timer.C:
		}
	}
}
