package main

import (
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

// Inspect a menu/dialog only when the fresh SOM identifies an enabled button
// as open_configuration. Never accepts selectors, selection controls or unknown
// effects. Dismissal is a fixed Escape, with no user-supplied keyboard action.
func (e *actorExecution) inspectControl(step actorStep) error {
	if e.session == nil {
		return errors.New("inspection requires an open browser")
	}
	var shot computerSOMScreenshot
	if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "screenshot", "annotate": true, "include_som": true}), &shot); err != nil {
		return err
	}
	var found *setOfMarkTarget
	for i := range shot.SOM {
		t := &shot.SOM[i]
		if t.Disabled || !somTargetMatches(step.Locator, *t) {
			continue
		}
		if found != nil {
			return errors.New("ambiguous inspection control")
		}
		found = t
	}
	if found == nil {
		return fmt.Errorf("inspection control is absent: %s", step.Locator.Text)
	}
	if found.Effect != "open_configuration" || (found.Tag != "button" && found.Role != "button") || found.Dangerous || found.Loading {
		return errors.New("inspection target is not a verified configuration opener")
	}
	in := map[string]any{"session_id": e.session.SessionID, "action": "click", "target_id": found.ID, "som_revision": shot.SOMRevision, "expected_name": firstNonEmpty(found.AccessibleName, found.Text), "expected_effect": "open_configuration"}
	if found.ID == "" {
		return errors.New("inspection control has no semantic target ID")
	}
	var out map[string]any
	if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, in), &out); err != nil {
		return err
	}
	return e.finishInteraction(out)
}
