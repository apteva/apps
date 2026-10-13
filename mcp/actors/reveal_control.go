package main

import (
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

type actorReveal struct {
	ScrollTargetName string `json:"scroll_target_name"`
	Direction        string `json:"direction"`
	MaxScrolls       int    `json:"max_scrolls"`
}

func validateActorReveal(s actorStep) error {
	if s.Reveal == nil || s.Reveal.ScrollTargetName == "" || (s.Reveal.Direction != "up" && s.Reveal.Direction != "down") || s.Reveal.MaxScrolls < 1 || s.Reveal.MaxScrolls > 80 {
		return errors.New("reveal_control requires a named scroll region, up/down direction and max_scrolls in 1..80")
	}
	if !s.Locator.SOMOnly || !s.Locator.Exact || s.Locator.Text == "" || s.Locator.Selector != "" || s.OnceKey != "" || s.ConfirmConsequence != "" || s.ExpectedEffect != "" || s.EffectPhase != "" || s.Optional {
		return errors.New("reveal_control requires an exact som_only locator and cannot click or skip a missing control")
	}
	return nil
}

// Reveal observes fresh SOM after overlapping, target-verified scrolls. It
// never clicks. A subsequent click must resolve its own fresh target/revision
// and retain the ordinary consequence and publish-once guards.
func (e *actorExecution) revealControl(s actorStep) error {
	if err := validateActorReveal(s); err != nil {
		return err
	}
	if e.session == nil {
		return errors.New("reveal_control requires an open browser")
	}
	direction, reversed := s.Reveal.Direction, false
	for scrolls := 0; scrolls <= s.Reveal.MaxScrolls; scrolls++ {
		if err := e.checkpoint(); err != nil {
			return err
		}
		shot, err := e.readScrollSnapshot()
		if err != nil {
			return err
		}
		var matches []setOfMarkTarget
		for _, target := range shot.SOM {
			if !target.Disabled && somTargetMatches(s.Locator, target) {
				matches = append(matches, target)
			}
		}
		if len(matches) > 1 {
			return errors.New("reveal_control target is ambiguous")
		}
		if len(matches) == 1 {
			t := matches[0]
			if t.ID == "" || t.Loading {
				return errors.New("reveal_control target has no stable semantic identity or is loading")
			}
			e.addTrace("reveal_control", "observed", "", map[string]any{"target_id": t.ID, "som_revision": shot.Revision, "name": firstNonEmpty(t.AccessibleName, t.Text), "role": t.Role, "effect": t.Effect, "scrolls": scrolls, "click_dispatched": false})
			return nil
		}
		if scrolls == s.Reveal.MaxScrolls {
			break
		}
		var region *readScrollRegion
		for i := range shot.Regions {
			if shot.Regions[i].Name == s.Reveal.ScrollTargetName {
				if region != nil {
					return errors.New("reveal_control scroll region is ambiguous")
				}
				region = &shot.Regions[i]
			}
		}
		if region == nil || region.ID == "" || region.Height <= 0 {
			return errors.New("reveal_control verified scroll region is missing")
		}
		if (direction == "up" && region.Top <= 0) || (direction == "down" && region.Top >= region.MaxY-1) {
			if reversed {
				break
			}
			reversed = true
			if direction == "up" {
				direction = "down"
			} else {
				direction = "up"
			}
		}
		amount := max(1, min(600, int(region.Height*0.65)))
		var out map[string]any
		args := withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "scroll", "target_id": region.ID, "som_revision": shot.Revision, "expected_name": region.Name, "expected_role": region.Role, "direction": direction, "amount": amount})
		if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", args, &out); err != nil {
			return err
		}
		sc := mapFromAny(out["scroll"])
		if sc["wrong_target"] == true || sc["ambiguous"] == true || stringFromAny(sc["actual_target_id"]) != region.ID {
			return errors.New("reveal_control scroll target was not verified")
		}
		e.addTrace("reveal_control", "scrolled", "", map[string]any{"scroll_target_id": region.ID, "som_revision": shot.Revision, "direction": direction, "amount": amount, "before_scroll_top": region.Top, "scrolls": scrolls + 1})
	}
	return fmt.Errorf("reveal_control target %q missing after bounded verified scrolling; no click dispatched", s.Locator.Text)
}
