// Package presentation adds opt-in visual pacing to Computer actions.
//
// Hosted browser recorders capture browser pixels, not an operating-system
// pointer. Demo mode therefore renders a pointer and click pulse inside the
// page, then performs the real CDP input. The overlay never receives pointer
// events and is recreated after navigation as needed.
package presentation

import (
	"context"
	"fmt"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

const (
	DemoTypingDelayMS     = 65
	DemoPointerDurationMS = 360
	DemoClickEffectMS     = 520
	DemoPostActionDelayMS = 650
)

// ForMode validates and normalizes a public presentation_mode value.
func ForMode(mode string) (computer.PresentationOptions, error) {
	switch mode {
	case "", "fast":
		return computer.PresentationOptions{Mode: "fast"}, nil
	case "demo":
		return computer.PresentationOptions{
			Mode:              "demo",
			ShowCursor:        true,
			TypingDelayMS:     DemoTypingDelayMS,
			PointerDurationMS: DemoPointerDurationMS,
			ClickEffectMS:     DemoClickEffectMS,
			PostActionDelayMS: DemoPostActionDelayMS,
		}, nil
	default:
		return computer.PresentationOptions{}, fmt.Errorf("presentation_mode must be \"fast\" or \"demo\"")
	}
}

// BeforeClick animates an in-page pointer to the target and starts a click
// pulse before the real click. Starting the pulse first makes it visible even
// when the click immediately navigates and replaces the document.
func BeforeClick(ctx context.Context, x, y int, options computer.PresentationOptions) error {
	if !options.Enabled() || !options.ShowCursor {
		return nil
	}
	moveMS := positive(options.PointerDurationMS, DemoPointerDurationMS)
	clickMS := positive(options.ClickEffectMS, DemoClickEffectMS)
	var shown bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(pointerScript(x, y, moveMS, clickMS), &shown)); err != nil {
		return fmt.Errorf("presentation cursor: %w", err)
	}
	if err := sleepContext(ctx, time.Duration(moveMS+90)*time.Millisecond); err != nil {
		return err
	}
	// Move the browser's logical pointer too, so hover styles agree with the
	// visible pointer before the press/release events are dispatched.
	return chromedp.Run(ctx, input.DispatchMouseEvent(input.MouseMoved, float64(x), float64(y)))
}

// AfterAction applies the longer hold used by demo recordings while retaining
// each backend's existing delay in fast mode.
func AfterAction(options computer.PresentationOptions, fastDelay time.Duration) {
	if !options.Enabled() {
		time.Sleep(fastDelay)
		return
	}
	delay := time.Duration(positive(options.PostActionDelayMS, DemoPostActionDelayMS)) * time.Millisecond
	time.Sleep(delay)
}

func pointerScript(x, y, moveMS, clickMS int) string {
	return fmt.Sprintf(`(function(x,y,moveMs,clickMs){
		var root = document.documentElement || document.body;
		if (!root) return false;
		var id = "__apteva_demo_cursor";
		var cursor = document.getElementById(id);
		if (!cursor) {
			cursor = document.createElement("div");
			cursor.id = id;
			cursor.setAttribute("aria-hidden", "true");
			var s = cursor.style;
			s.position = "fixed";
			s.left = Math.round(window.innerWidth / 2) + "px";
			s.top = Math.round(window.innerHeight / 2) + "px";
			s.width = "22px";
			s.height = "22px";
			s.marginLeft = "-11px";
			s.marginTop = "-11px";
			s.border = "3px solid white";
			s.borderRadius = "999px";
			s.background = "rgba(37,99,235,.86)";
			s.boxShadow = "0 1px 5px rgba(0,0,0,.65)";
			s.pointerEvents = "none";
			s.zIndex = "2147483647";
			root.appendChild(cursor);
			void cursor.offsetWidth;
		}
		cursor.style.transition =
			"left " + moveMs + "ms cubic-bezier(.22,.8,.25,1), " +
			"top " + moveMs + "ms cubic-bezier(.22,.8,.25,1)";
		cursor.style.left = x + "px";
		cursor.style.top = y + "px";

		window.setTimeout(function(){
			var pulse = document.createElement("div");
			pulse.setAttribute("aria-hidden", "true");
			var p = pulse.style;
			p.position = "fixed";
			p.left = x + "px";
			p.top = y + "px";
			p.width = "14px";
			p.height = "14px";
			p.marginLeft = "-7px";
			p.marginTop = "-7px";
			p.border = "3px solid rgb(37,99,235)";
			p.borderRadius = "999px";
			p.pointerEvents = "none";
			p.zIndex = "2147483646";
			root.appendChild(pulse);
			var animation = pulse.animate([
				{transform:"scale(.35)", opacity:1},
				{transform:"scale(3.4)", opacity:0}
			], {duration:clickMs, easing:"ease-out"});
			animation.onfinish = function(){ pulse.remove(); };
		}, moveMs);
		return true;
	})(%d,%d,%d,%d)`, x, y, moveMS, clickMS)
}

func positive(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
