package main

import (
	"errors"
	"fmt"
)

// CanonicalMap is an exact, caller-defined allow-list. No substring, provider,
// case folding or fallback inference can broaden an approved alias set.
func validateCanonicalAssertion(a actorAssertion) error {
	if len(a.CanonicalMap) == 0 {
		return nil
	}
	if len(a.CanonicalMap) > 100 || (a.Equals == nil && a.EqualsField == "") || a.EqualsExact != nil || a.EqualsSet != nil || a.Contains != "" || a.Matches != "" || len(a.SumOf) > 0 || len(a.DifferenceOf) > 0 || a.EqualsURL != nil || a.GreaterThan != nil || a.GreaterThanField != "" || a.GreaterThanOrEqual != nil || a.LessThanOrEqual != nil || a.Tolerance != 0 {
		return errors.New("canonical_map requires only equals and/or equals_field comparisons")
	}
	for raw, canonical := range a.CanonicalMap {
		if raw == "" || canonical == "" || len(raw) > 200 || len(canonical) > 200 || actorTemplateValue(raw) || actorTemplateValue(canonical) {
			return errors.New("canonical_map requires nonempty literal labels of at most 200 bytes")
		}
	}
	return nil
}

func (e *actorExecution) compareAssertionValues(field string, actual, expected any, a actorAssertion) error {
	if len(a.CanonicalMap) == 0 {
		if !actorValuesEqual(actual, expected, a.Tolerance) {
			return fmt.Errorf("assertion failed for %q: got %v, want %v", field, actual, expected)
		}
		return nil
	}
	rawActual, aok := actual.(string)
	rawExpected, eok := expected.(string)
	canonicalActual, knownActual := a.CanonicalMap[rawActual]
	canonicalExpected, knownExpected := a.CanonicalMap[rawExpected]
	evidence := map[string]any{"field": field, "raw_actual": actual, "raw_expected": expected, "canonical_actual": canonicalActual, "canonical_expected": canonicalExpected, "matched": false}
	if len(e.assertionEvidence) < maxActorTraceEvents {
		e.assertionEvidence = append(e.assertionEvidence, evidence)
	}
	if !aok || !eok || !knownActual || !knownExpected {
		return fmt.Errorf("canonical assertion %q has an unapproved label: got %v, want %v", field, actual, expected)
	}
	if canonicalActual != canonicalExpected {
		return fmt.Errorf("canonical assertion failed for %q: %q (%s) != %q (%s)", field, rawActual, canonicalActual, rawExpected, canonicalExpected)
	}
	evidence["matched"] = true
	return nil
}

func validateEffectPhase(s actorStep) error {
	switch s.EffectPhase {
	case "": // Preserve conservative legacy commit classification.
		return nil
	case "prepare":
		if (s.Action != "click" && s.Action != "click_verified") || s.ExpectedEffect != "open_configuration" || s.ConfirmConsequence != s.ExpectedEffect || s.OnceKey == "" {
			return errors.New("prepare phase requires a guarded, acknowledged open_configuration click")
		}
	case "commit":
		if (s.Action != "click" && s.Action != "click_verified") || !consequentialActorStep(s) || s.ExpectedEffect == "" || s.ConfirmConsequence != s.ExpectedEffect || s.OnceKey == "" {
			return errors.New("commit phase requires a guarded, acknowledged consequential click")
		}
	default:
		return errors.New("effect_phase must be prepare or commit")
	}
	return nil
}
