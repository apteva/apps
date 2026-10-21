package main

import (
	"errors"
	"github.com/emiago/sipgo"
)

// A 481 remains an error unless the same validated dialog has already ended.
// Retain the actual provider response as evidence, including crossed BYEs.
func sipTerminationResult(d *sipLiveDiagnostics, err error, peerEnded bool) error {
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
