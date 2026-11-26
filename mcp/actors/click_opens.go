package main

import (
	"errors"
	"fmt"
)

// errActorWaitNotMet marks a wait_for whose conditions were not met, as
// opposed to a transport or navigation failure.
var errActorWaitNotMet = errors.New("wait_for required conditions not met")

// actorClickOpens names what a configuration click must open. The engine
// waits for it and, if it never appears while the page stays put, clicks once
// more: opening a dialog or picker writes nothing, so a second attempt is safe.
type actorClickOpens struct {
	Conditions []actorWaitCondition `json:"conditions"`
	Match      string               `json:"match,omitempty"`
	TimeoutMS  any                  `json:"timeout_ms,omitempty"`
}

func (o *actorClickOpens) waitStep() actorStep {
	return actorStep{Action: "wait_for", Conditions: o.Conditions, Match: o.Match, TimeoutMS: o.TimeoutMS}
}

func validateClickOpens(step actorStep) error {
	if step.Opens == nil {
		return nil
	}
	if step.Action != "click" {
		return errors.New("opens is only supported on click")
	}
	// Only a click that merely opens configuration may be repeated; anything
	// that can commit stays single-shot behind its own guards.
	if step.ExpectedEffect != "open_configuration" || step.OnceKey != "" || step.ConfirmConsequence != "" || step.EffectPhase != "" {
		return errors.New("opens requires expected_effect open_configuration without once_key, confirm_consequence or effect_phase")
	}
	if len(step.Opens.Conditions) == 0 {
		return errors.New("opens.conditions is required")
	}
	if err := validateWaitStep(step.Opens.waitStep()); err != nil {
		return fmt.Errorf("opens: %w", err)
	}
	return nil
}

// clickAndConfirmOpen clicks, waits for the configuration to appear and, when
// it has not appeared and the page has not moved, checks once more (a late
// dialog must not be closed by clicking its backdrop) and then clicks one
// more time. It fails before any later step if the configuration never opens.
func (e *actorExecution) clickAndConfirmOpen(step actorStep) error {
	wait := step.Opens.waitStep()
	if err := e.clickOnce(step); err != nil {
		return err
	}
	pageURL := e.currentURL
	err := e.waitFor(wait)
	if err == nil || !errors.Is(err, errActorWaitNotMet) {
		return err
	}
	if e.currentURL != pageURL {
		return fmt.Errorf("click did not open the expected configuration and the page changed to %s; not clicking again", e.currentURL)
	}
	recheck := wait
	recheck.TimeoutMS = 500
	if again := e.waitFor(recheck); again == nil {
		e.addTrace("click", "opened_late", "expected configuration appeared after the wait", nil)
		return nil
	} else if !errors.Is(again, errActorWaitNotMet) {
		return again
	}
	e.addTrace("click", "retry", "expected configuration did not open; clicking once more", map[string]any{"url": pageURL})
	if err := e.clickOnce(step); err != nil {
		return fmt.Errorf("second click after configuration did not open: %w", err)
	}
	if err := e.waitFor(wait); err != nil {
		if errors.Is(err, errActorWaitNotMet) {
			return fmt.Errorf("configuration did not open after 2 clicks: %w", err)
		}
		return err
	}
	e.addTrace("click", "opened_on_retry", "expected configuration opened after the second click", nil)
	return nil
}
