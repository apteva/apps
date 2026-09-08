package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var errAnswerPreparationInProgress = errors.New("answer preparation is in progress; retry shortly")
var errAnswerCallEnded = errors.New("call ended during answer preparation")

type answerPreparationFailure struct{ cause error }

func (e *answerPreparationFailure) Error() string { return e.cause.Error() }
func (e *answerPreparationFailure) Unwrap() error { return e.cause }
func retryAnswerPreparation(err error) bool {
	var failed *answerPreparationFailure
	return errors.Is(err, errAnswerPreparationInProgress) || errors.As(err, &failed)
}

type realtimePreparation struct {
	done chan struct{}
	row  callRow
	err  error
}
type realtimePreparations struct {
	mu     sync.Mutex
	active map[string]*realtimePreparation
	// Zero uses the production bound. Tests can exercise slow preparation without
	// leaving real carrier webhook requests open for several seconds.
	wait time.Duration
}

func (p *realtimePreparations) timeout() time.Duration {
	if p.wait > 0 {
		return p.wait
	}
	return 5 * time.Second
}

// A request owns only its wait, not the winning preparation. Disconnecting or
// timing out a duplicate webhook must not cancel a spawn accepted by Core.
func (a *App) prepareInboundRealtime(ctx *sdk.AppCtx, row *callRow, directive, voice, greeting string, waitContexts ...context.Context) (string, error) {
	if row.PeerKind == peerKindHuman {
		return "", errors.New("call is routed to the browser softphone; answer it from the Telephony panel")
	}
	return a.sharedRealtimeWork(row, "prepare", true, func(owned *callRow) error {
		return a.runInboundPreparation(ctx, owned, directive, voice, greeting)
	}, waitContexts...)
}

// Separate phases share the same coordinator: answer API callers also reuse
// carrier activation instead of racing a second answer/cleanup against it.
func (a *App) sharedRealtimeWork(row *callRow, phase string, bounded bool, run func(*callRow) error, waitContexts ...context.Context) (string, error) {
	waitCtx := context.Background()
	if len(waitContexts) > 0 && waitContexts[0] != nil {
		waitCtx = waitContexts[0]
	}
	if err := waitCtx.Err(); err != nil {
		return "", fmt.Errorf("%w: %v", errAnswerPreparationInProgress, err)
	}
	p := &a.preparations
	key := phase + "/" + row.ProjectID + "/" + row.ID
	p.mu.Lock()
	if p.active == nil {
		p.active = make(map[string]*realtimePreparation)
	}
	flight := p.active[key]
	if flight == nil {
		flight = &realtimePreparation{done: make(chan struct{}), row: *row}
		p.active[key] = flight
		go func() {
			flight.err = run(&flight.row)
			p.mu.Lock()
			delete(p.active, key)
			close(flight.done)
			p.mu.Unlock()
		}()
	}
	p.mu.Unlock()
	var timeout <-chan time.Time
	if bounded {
		timer := time.NewTimer(p.timeout())
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case <-flight.done:
		// Agent-ring losers must not acquire the winning agent's thread.
		if flight.row.AgentID != row.AgentID || flight.row.ProjectID != row.ProjectID {
			return "", errAnswerPreparationInProgress
		}
		*row = flight.row
		if flight.err != nil {
			return "", flight.err
		}
		current, err := a.db().findCall(row.ID)
		if err != nil {
			return "", &answerPreparationFailure{err}
		}
		if current == nil || isTerminalStatus(current.Status) {
			return "", errAnswerCallEnded
		}
		if current.ThreadID != row.ThreadID || !realtimePreparationReady(current) {
			return "", errAnswerPreparationInProgress
		}
		*row = *current
		return row.ThreadID, nil
	case <-timeout:
		return "", errAnswerPreparationInProgress
	case <-waitCtx.Done():
		return "", fmt.Errorf("%w: %v", errAnswerPreparationInProgress, waitCtx.Err())
	}
}

func realtimePreparationReady(row *callRow) bool {
	return row.ThreadID != "" && !strings.HasPrefix(row.ThreadID, "pending-") &&
		row.AudioBridgeURL != "" && row.AudioBridgeURL != "pending"
}

func (a *App) runInboundPreparation(ctx *sdk.AppCtx, row *callRow, directive, voice, greeting string) error {
	deadline := time.Now().Add(a.preparations.timeout())
	for {
		current, err := a.db().findCall(row.ID)
		if err != nil {
			return &answerPreparationFailure{fmt.Errorf("load answer claim: %w", err)}
		}
		if current == nil || isTerminalStatus(current.Status) {
			return errAnswerCallEnded
		}
		if current.ProjectID != row.ProjectID || current.PeerKind == peerKindHuman {
			return errAnswerPreparationInProgress
		}
		if current.Status != "pending" {
			if current.AgentID != row.AgentID {
				return errAnswerPreparationInProgress
			}
			*row = *current
			if realtimePreparationReady(current) {
				return nil
			}
			// An incomplete persisted claim may belong to another process or an
			// interrupted preparation. Observe it; never release it or spawn over it.
			if current.Status != "answering" || time.Now().After(deadline) {
				return errAnswerPreparationInProgress
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		claimed, err := a.db().claimPendingCall(row.ID, row.AgentID, row.ProjectID)
		if err != nil {
			return &answerPreparationFailure{fmt.Errorf("claim pending call: %w", err)}
		}
		if !claimed {
			if time.Now().After(deadline) {
				return errAnswerPreparationInProgress
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		// The pending thread identity doubles as an ownership token. Late attach or
		// release cannot touch a subsequent claim, and each spawn has its own name.
		token := "pending-" + row.ID + "-" + newSecret()
		res, err := a.db().db.Exec(`UPDATE calls SET thread_id=? WHERE id=? AND project_id=? AND status='answering' AND agent_id=? AND thread_id=?`, token, row.ID, row.ProjectID, row.AgentID, current.ThreadID)
		if err != nil {
			return &answerPreparationFailure{fmt.Errorf("persist preparation owner: %w", err)}
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			return errAnswerPreparationInProgress
		}
		current, err = a.db().findCall(row.ID)
		if err != nil || current == nil {
			_ = a.db().releaseRealtimePreparation(row.ID, token)
			return &answerPreparationFailure{firstError(err, errors.New("claimed call unavailable"))}
		}
		if current.ThreadID != token || current.Status != "answering" {
			if isTerminalStatus(current.Status) {
				return errAnswerCallEnded
			}
			return errAnswerPreparationInProgress
		}
		*row = *current
		threadID := "tel-" + strings.TrimPrefix(token, "pending-")
		fail := func(cause error) error {
			ctx.Logger().Warn("realtime answer preparation failed", "call", row.ID, "thread", threadID, "agent", row.AgentID, "err", cause)
			// Spawn can fail after Core accepted it. The unique thread id makes this
			// cleanup safe even if the caller hung up or another claim replaced ours.
			_ = ctx.PlatformAPI().KillThread(row.AgentID, threadID)
			_ = a.db().releaseRealtimePreparation(row.ID, token)
			return &answerPreparationFailure{cause}
		}
		ctx.Logger().Info("realtime answer preparation started", "call", row.ID, "thread", threadID, "agent", row.AgentID)
		rt, err := ctx.PlatformAPI().SpawnRealtimeThread(sdk.RealtimeSpawnRequest{
			AgentID: row.AgentID, ThreadID: threadID, Directive: strings.TrimSpace(directive), Voice: voice,
			CapabilityMode: sdk.RealtimeCapabilitiesInheritAgent, CallContext: realtimeCallContext(*row),
			TurnDetection: telephonyTurnDetection(), Ephemeral: true, InitialMessage: greeting, BridgeDisconnectTTLSeconds: 30,
		})
		if err != nil {
			return fail(fmt.Errorf("spawn realtime thread: %w", err))
		}
		if rt == nil || strings.TrimSpace(rt.AudioBridgeURL) == "" {
			return fail(errors.New("realtime spawn returned no audio bridge URL"))
		}
		res, err = a.db().db.Exec(`UPDATE calls SET thread_id=?,audio_bridge_url=?,directive=?,voice=? WHERE id=? AND status='answering' AND thread_id=?`, threadID, rt.AudioBridgeURL, strings.TrimSpace(directive), voice, row.ID, token)
		if err != nil {
			return fail(fmt.Errorf("persist call answer: %w", err))
		}
		n, err = res.RowsAffected()
		if err != nil || n != 1 {
			_ = ctx.PlatformAPI().KillThread(row.AgentID, threadID)
			current, _ = a.db().findCall(row.ID)
			if current == nil || isTerminalStatus(current.Status) {
				return errAnswerCallEnded
			}
			return errAnswerPreparationInProgress
		}
		ctx.Logger().Info("realtime answer preparation ready", "call", row.ID, "thread", threadID, "agent", row.AgentID)
		row.ThreadID = threadID
		row.AudioBridgeURL = rt.AudioBridgeURL
		row.Directive = strings.TrimSpace(directive)
		row.Voice = voice
		row.Status = "answering"
		return nil
	}
}

func (c *callsDB) releaseRealtimePreparation(id, token string) error {
	res, err := c.db.Exec(`UPDATE calls SET status='pending',audio_bridge_url='pending',thread_id=CASE WHEN thread_id LIKE 'pending-%' THEN thread_id ELSE 'pending-'||thread_id END WHERE id=? AND status='answering' AND thread_id=? AND media_active=0`, id, token)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	return c.releaseRingClaim(id)
}

// Retrying XML must not speak an error or hang up a legitimate winning answer.
func writeTwilioPreparationWait(w http.ResponseWriter, redirectURL string) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = fmt.Fprintf(w, `<Response><Pause length="1"/><Redirect method="POST">%s</Redirect></Response>`, xmlEscape(redirectURL))
}
