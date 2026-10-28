package main

import (
	"errors"
	sdk "github.com/apteva/app-sdk"
)

func carrierAnswerObserved(row *callRow) bool {
	if row.CarrierSlug == "telnyx" {
		return row.CarrierAnsweredAt != "" || row.MediaConnectedAt != ""
	}
	return row.AnsweredAt != "" || row.MediaConnectedAt != "" || row.Status == "answered" || row.Status == "in-progress"
}

// Ending an unanswered inbound attempt is a rejection; an established call
// needs hangup. This policy applies before the burst threshold as well as after
// it, and leaves any preceding announcement to the routing executor.
func (a *App) terminateCarrierCall(ctx *sdk.AppCtx, row *callRow) error {
	if row == nil {
		return errors.New("call unavailable")
	}
	if row.CarrierSID == "" && !a.callUsesDirectSIP(row) {
		return a.terminateUnidentifiedCarrierCall(ctx, row)
	}
	answered := carrierAnswerObserved(row)
	if a.callUsesDirectSIP(row) {
		answered = row.AnsweredAt != "" || row.MediaConnectedAt != "" || row.Status == "answered" || row.Status == "in-progress"
	}
	if row.Direction == "inbound" && !answered {
		return a.rejectInboundCarrierCall(ctx, row)
	}
	if a.callUsesDirectSIP(row) {
		gateway := a.directSIPGateway()
		if gateway == nil {
			return errors.New("direct SIP gateway is not running")
		}
		return gateway.Hangup(row)
	}
	if row.CarrierSID == "" {
		return nil
	}
	carrier, err := a.carrierForRow(ctx, nil, row)
	if err != nil {
		return err
	}
	return carrier.Hangup(ctx, row)
}

// errCarrierCallIDUnavailable keeps a placed-but-unidentified call retryable:
// the carrier accepted the request, so reporting success would leave it ringing.
var errCarrierCallIDUnavailable = errors.New("carrier call id is not available yet")

// A carrier request without a call id is still a live attempt. Plivo queues
// outbound calls under request_uuid until the leg exists; cancel that request.
// With no carrier identity at all nothing was placed, so there is nothing to end.
func (a *App) terminateUnidentifiedCarrierCall(ctx *sdk.AppCtx, row *callRow) error {
	if row.CarrierRequestID == "" {
		return nil
	}
	if row.CarrierSlug != "plivo" {
		return errCarrierCallIDUnavailable
	}
	_, err := executeCarrierTool(ctx, row.CarrierConnectionID, "cancel_call_request", map[string]any{
		"request_uuid": row.CarrierRequestID,
	})
	return err
}
