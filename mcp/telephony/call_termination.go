package main

import (
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo/sip"
)

// Termination initiators recorded on a call, next to the carriers' own
// values ("caller", "callee", ...).
const (
	terminationByCarrier   = "carrier"   // the remote side ended the SIP dialog
	terminationByUser      = "user"      // an authenticated application user
	terminationByOperator  = "operator"  // a trusted operator request without a user identity
	terminationByAgent     = "agent"     // the call's agent
	terminationByTelephony = "telephony" // Telephony itself: limits, failures, cleanup
)

// hangupActor is who asked Telephony to end a call. It becomes the call's
// termination unless another side had already ended it.
type hangupActor struct {
	Initiator string
	Principal string // user identity key or agent:<id>; kept in the audit only
	Action    string // recorded as the termination cause
	RequestID string
	// Path names how the request arrived (hangup, agent_tool, completion,
	// transfer, ring_leg); Session is the browser session generation that
	// sent it, when it sent a valid one. Both show on the termination event.
	Path    string
	Session string
}

func (a hangupActor) facts() lifecycleFacts {
	return lifecycleFacts{Source: "telephony", TerminationInitiator: a.Initiator, TerminationCause: a.Action, TerminationPath: a.Path, TerminationSession: a.Session}
}

// hangupResult is the call as it ended. AlreadyEnded is true when the call
// had ended, or another side ended it first, so the request changed nothing.
type hangupResult struct {
	Call         *callRow
	AlreadyEnded bool
}

func (r hangupResult) response() map[string]any {
	return map[string]any{"ok": true, "call_id": r.Call.ID, "status": r.Call.Status, "already_ended": r.AlreadyEnded, "termination": terminationPublic(*r.Call)}
}

// requestTermination marks that actor asked to end the call while the carrier
// or SIP dialog is being ended, so the path that records the end attributes it
// to them rather than to Telephony.
func (a *App) requestTermination(callID string, facts lifecycleFacts) func() {
	a.terminationRequests.Store(callID, facts)
	return func() { a.terminationRequests.CompareAndDelete(callID, facts) }
}

func (a *App) requestedTermination(callID string) lifecycleFacts {
	if a != nil {
		if requested, ok := a.terminationRequests.Load(callID); ok {
			return requested.(lifecycleFacts)
		}
	}
	return lifecycleFacts{Source: "telephony", TerminationInitiator: terminationByTelephony}
}

// finishHangup reports the authoritative termination and audits the request.
func (a *App) finishHangup(call *callRow, actor hangupActor, previous string, requestedAt time.Time) hangupResult {
	ended := isTerminalStatus(previous) || call.TerminationInitiator != actor.Initiator || call.TerminationCause != actor.Action
	outcome := "ended"
	if ended {
		outcome = "already_ended"
	}
	a.auditHangup(call, actor, previous, outcome, "", requestedAt)
	return hangupResult{Call: call, AlreadyEnded: ended}
}

func (a *App) auditHangup(call *callRow, actor hangupActor, previous, outcome, failure string, requestedAt time.Time) {
	_, err := a.db().db.Exec(`INSERT INTO telephony_hangup_audit(id,call_id,project_id,initiator,principal,action,request_id,requested_at,previous_status,outcome,termination_initiator,error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		newSecret(), call.ID, call.ProjectID, actor.Initiator, actor.Principal, actor.Action, actor.RequestID, ringTime(requestedAt), previous, outcome, call.TerminationInitiator, failure)
	if err != nil {
		log.Printf("telephony hangup audit call=%s err=%v", call.ID, err)
	}
}

// sipSessionEndHook runs, in tests only, once a path has claimed an inbound
// SIP session's end and before it stores the call.
var sipSessionEndHook func(leg string)

// sipSessionEnd records how the carrier ended a SIP session and lets an
// explicit hang-up wait for whichever path ended it first. The zero value is
// ready to use.
type sipSessionEnd struct {
	once   sync.Once
	done   chan struct{}
	remote atomic.Pointer[lifecycleFacts]
}

func (e *sipSessionEnd) channel() chan struct{} {
	e.once.Do(func() { e.done = make(chan struct{}) })
	return e.done
}

func (e *sipSessionEnd) finished() { close(e.channel()) }

func (e *sipSessionEnd) wait(limit time.Duration) {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-e.channel():
	case <-timer.C:
	}
}

// record keeps what the SIP signalling says about how the carrier leg ended:
// the carrier's BYE or final response, or our own dial timeout.
func (e *sipSessionEnd) record(initiator string, reason lifecycleFacts) *lifecycleFacts {
	reason.Source, reason.TerminationInitiator = "telephony", initiator
	recorded := &reason
	if !e.remote.CompareAndSwap(nil, recorded) {
		return nil
	}
	return recorded
}

// forget drops a reason recorded for signalling that was then rejected.
func (e *sipSessionEnd) forget(recorded *lifecycleFacts) {
	if recorded != nil {
		e.remote.CompareAndSwap(recorded, nil)
	}
}

// facts is the termination for a session ended on leg. Only the carrier leg
// uses recorded signalling; a local end is whoever asked for it.
func (e *sipSessionEnd) facts(app *App, callID, leg string) lifecycleFacts {
	if leg == "carrier" {
		if remote := e.remote.Load(); remote != nil {
			return *remote
		}
		return lifecycleFacts{Source: "telephony", TerminationInitiator: terminationByCarrier}
	}
	return app.requestedTermination(callID)
}

// sipByeTermination reads a BYE's Reason header. Q.850 causes use the names
// carriers such as Telnyx report; SIP causes are response codes.
func sipByeTermination(request *sip.Request) lifecycleFacts {
	header := request.GetHeader("Reason")
	if header == nil {
		return lifecycleFacts{}
	}
	matches := sipReasonCausePattern.FindStringSubmatch(header.Value())
	if len(matches) != 3 {
		return lifecycleFacts{}
	}
	if !strings.EqualFold(matches[1], "Q.850") {
		return lifecycleFacts{TerminationCode: matches[2]}
	}
	return lifecycleFacts{TerminationCause: q850CauseName(matches[2])}
}

func q850CauseName(number string) string {
	switch number {
	case "1":
		return "unallocated_number"
	case "16":
		return "normal_clearing"
	case "17":
		return "user_busy"
	case "18":
		return "no_user_response"
	case "19":
		return "no_answer"
	case "21":
		return "call_rejected"
	case "31":
		return "normal_unspecified"
	}
	if _, err := strconv.Atoi(number); err != nil {
		return ""
	}
	return "q850_cause_" + number
}
