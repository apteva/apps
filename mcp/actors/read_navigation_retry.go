package main

import (
	"errors"
	"strings"
)

// ReadRetries is deliberately separate from the legacy operation-wide retry
// limit. It cannot be attached to uploads, clicks or other write actions.
func validateReadRetries(step actorStep) error {
	if step.ReadRetries == nil {
		return nil
	}
	if *step.ReadRetries < 0 || *step.ReadRetries > 2 {
		return errors.New("read_retries must be between 0 and 2")
	}
	if step.Action != "goto" || !step.ReadOnly || step.Optional || step.OnceKey != "" || step.OnceScope != "" || step.ExpectedEffect != "" || step.ConfirmConsequence != "" || step.EffectPhase != "" || step.SecretRef != "" {
		return errors.New("read_retries requires an explicitly read_only goto without effects or secrets")
	}
	return nil
}

func transientReadNavigationError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"action_timeout", "context deadline exceeded", "navigation timeout", "navigation timed out", "connection reset", "temporarily unavailable"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
