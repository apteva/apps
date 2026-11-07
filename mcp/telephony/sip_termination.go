package main

import (
	"context"
	"errors"
	"time"

	"github.com/emiago/sipgo"
)

const (
	sipByeAttemptTimeout = 3 * time.Second
	sipByeRetryBudget    = 10 * time.Second
	sipByeRetryDelay     = 500 * time.Millisecond
)

// The peer ended the dialog while a BYE was still being retried.
var errSIPByeCrossed = errors.New("peer ended the dialog during BYE retries")

// A short network fault must not leave a call marked failed when the carrier
// was simply unreachable for a moment. Only silence is retried: any SIP response
// is final, and each try is a new transaction. A 481 after a lost try counts as
// ended, because an earlier BYE may have reached the carrier without its reply
// getting back. A shutdown's deadline wins, including one that starts during the
// retries, so stopDeadline is read again before each try. It is zero until then.
func sendSIPByeWithRetry(send func(context.Context) error, peerEnded func() bool, stopDeadline func() time.Time, retrying func(error)) (error, bool) {
	budget := time.Now().Add(sipByeRetryBudget)
	limit := func() time.Time {
		if stopDeadline != nil {
			if stop := stopDeadline(); !stop.IsZero() && stop.Before(budget) {
				return stop
			}
		}
		return budget
	}
	for attempt := 1; ; attempt++ {
		deadline := time.Now().Add(sipByeAttemptTimeout)
		if limit().Before(deadline) {
			deadline = limit()
		}
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		err := send(ctx)
		cancel()
		var response sipgo.ErrDialogResponse
		if err == nil || errors.As(err, &response) {
			return err, attempt > 1
		}
		if !time.Now().Add(sipByeRetryDelay).Before(limit()) {
			return err, attempt > 1
		}
		if retrying != nil {
			retrying(err)
		}
		time.Sleep(sipByeRetryDelay)
		if peerEnded != nil && peerEnded() {
			return errSIPByeCrossed, true
		}
	}
}

// A 481 remains an error unless the same validated dialog has already ended.
// Retain the actual provider response as evidence, including crossed BYEs.
func sipTerminationResult(d *sipLiveDiagnostics, err error, peerEnded bool) error {
	if errors.Is(err, errSIPByeCrossed) {
		d.event("sip_bye_crossed", "carrier", "", 0)
		return nil
	}
	status := 200
	detail := ""
	if err != nil {
		status = 0
		detail = transportEvidenceError(err)
		var response sipgo.ErrDialogResponse
		if errors.As(err, &response) && response.Res != nil {
			status = response.Res.StatusCode
		}
	}
	if peerEnded && status == 481 {
		d.event("sip_bye_crossed", "carrier", detail, status)
		return nil
	}
	d.event("sip_bye_result", "local", detail, status)
	return err
}

// A 481 on a later try means the dialog is gone at the carrier.
func sipByeRetryFinished(err error, retried bool) bool {
	var response sipgo.ErrDialogResponse
	return retried && errors.As(err, &response) && response.Res != nil && response.Res.StatusCode == 481
}
