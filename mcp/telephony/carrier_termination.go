package main

import (
	"errors"
	sdk "github.com/apteva/app-sdk"
)

func carrierAnswerObserved(row *callRow) bool {
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
		return nil
	}
	if row.Direction == "inbound" && !carrierAnswerObserved(row) {
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
