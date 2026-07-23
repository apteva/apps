// Package presentation adds opt-in visual pacing to Computer actions.
//
// Hosted browser recorders capture browser pixels, not an operating-system
// pointer. Demo mode therefore renders a pointer and click pulse inside the
// page, then performs the real CDP input. The overlay never receives pointer
// events and is recreated after navigation as needed.
package presentation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
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
// pulse before the real click. The overlay does not dispatch any browser input
// events; the backend remains solely responsible for the original click.
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
	return nil
}

// CueTarget renders a post-action cue over the control the backend already
// resolved. It never scrolls, focuses, clicks, types, or dispatches an event.
// Selector lookup is visual-only; fallback coordinates are used when a hidden
// control (notably a file input) has no visible associated label.
func CueTarget(
	ctx context.Context,
	selector string,
	fallbackX, fallbackY int,
	hasFallback bool,
	caption string,
	options computer.PresentationOptions,
) error {
	if !options.Enabled() || !options.ShowCursor {
		return nil
	}
	moveMS := positive(options.PointerDurationMS, DemoPointerDurationMS)
	clickMS := positive(options.ClickEffectMS, DemoClickEffectMS)
	var shown bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		targetCueScript(selector, fallbackX, fallbackY, hasFallback, caption, moveMS, clickMS),
		&shown,
	)); err != nil {
		return fmt.Errorf("presentation target cue: %w", err)
	}
	return nil
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
	return targetCueScript("", x, y, true, "", moveMS, clickMS)
}

func targetCueScript(
	selector string,
	fallbackX, fallbackY int,
	hasFallback bool,
	caption string,
	moveMS, clickMS int,
) string {
	selectorJSON, _ := json.Marshal(selector)
	captionJSON, _ := json.Marshal(caption)
	return fmt.Sprintf(`(function(selector,fallbackX,fallbackY,hasFallback,caption,moveMs,clickMs){
		var root = document.documentElement || document.body;
		if (!root) return false;
		function visibleRect(element) {
			if (!element || typeof element.getBoundingClientRect !== "function") return null;
			var rect = element.getBoundingClientRect();
			var style = window.getComputedStyle(element);
			if (!rect || rect.width <= 0 || rect.height <= 0 ||
				style.display === "none" || style.visibility === "hidden") return null;
			return rect;
		}
		var x = fallbackX;
		var y = fallbackY;
		var found = false;
		if (selector) {
			var element = null;
			try { element = document.querySelector(selector); } catch (_) {}
			var rect = visibleRect(element);
			if (!rect && element) {
				var label = element.closest && element.closest("label");
				if (!label && element.id) {
					try {
						label = document.querySelector('label[for="' +
							CSS.escape(element.id) + '"]');
					} catch (_) {}
				}
				rect = visibleRect(label);
			}
			if (rect) {
				x = Math.round(rect.left + rect.width / 2);
				y = Math.round(rect.top + rect.height / 2);
				found = x >= 0 && y >= 0 &&
					x <= window.innerWidth && y <= window.innerHeight;
			}
		}
		if (!found && hasFallback) {
			x = fallbackX;
			y = fallbackY;
			found = Number.isFinite(x) && Number.isFinite(y);
		}
		if (!found) return false;

		var id = "__apteva_demo_cursor";
		var cursor = document.getElementById(id);
		if (!cursor) {
			cursor = document.createElement("div");
			cursor.id = id;
			cursor.setAttribute("aria-hidden", "true");
			cursor.setAttribute("data-apteva-presentation", "true");
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
			pulse.setAttribute("data-apteva-presentation", "true");
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

			if (caption) {
				var oldCaption = document.getElementById("__apteva_demo_caption");
				if (oldCaption) oldCaption.remove();
				var bubble = document.createElement("div");
				bubble.id = "__apteva_demo_caption";
				bubble.setAttribute("aria-hidden", "true");
				bubble.setAttribute("data-apteva-presentation", "true");
				bubble.textContent = caption;
				var b = bubble.style;
				b.position = "fixed";
				b.left = Math.min(Math.max(8, x + 16), Math.max(8, window.innerWidth - 180)) + "px";
				b.top = Math.max(8, y - 38) + "px";
				b.maxWidth = "164px";
				b.padding = "5px 9px";
				b.borderRadius = "7px";
				b.background = "rgba(15,23,42,.92)";
				b.color = "white";
				b.font = "600 13px/1.25 -apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif";
				b.letterSpacing = ".01em";
				b.boxShadow = "0 2px 8px rgba(0,0,0,.32)";
				b.pointerEvents = "none";
				b.zIndex = "2147483647";
				root.appendChild(bubble);
				var bubbleAnimation = bubble.animate([
					{transform:"translateY(3px)", opacity:0},
					{transform:"translateY(0)", opacity:1, offset:.18},
					{transform:"translateY(0)", opacity:1, offset:.72},
					{transform:"translateY(-2px)", opacity:0}
				], {duration:Math.max(clickMs + 350, 800), easing:"ease-out"});
				bubbleAnimation.onfinish = function(){ bubble.remove(); };
			}
		}, moveMs);
		return true;
	})(%s,%d,%d,%t,%s,%d,%d)`,
		string(selectorJSON), fallbackX, fallbackY, hasFallback, string(captionJSON), moveMS, clickMS)
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
