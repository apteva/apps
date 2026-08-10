package selectorclick

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// Point is the viewport-coordinate center of a selector-matched element after
// the element has been brought into view. Callers must dispatch the actual
// browser mouse event themselves; Resolve never invokes HTMLElement.click().
type Point struct {
	X int
	Y int
}

type result struct {
	Status string  `json:"status"`
	Detail string  `json:"detail,omitempty"`
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
}

func Resolve(ctx context.Context, selector string) (Point, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return Point{}, fmt.Errorf("click selector is empty")
	}
	selectorJSON, _ := json.Marshal(selector)
	scrollScript := `(function(selector) {
  var el;
  try {
    el = document.querySelector(selector);
  } catch (error) {
    return {status:'invalid', detail:String(error && error.message || error)};
  }
  if (!el) return {status:'unmatched'};
  // Explicitly override a page's scroll-behavior:smooth. Measuring the box
  // while a smooth scroll is still moving can send the real mouse event to
  // the element's old viewport position.
  try { el.scrollIntoView({behavior:'instant', block:'center', inline:'nearest'}); } catch (_) {}
  return {status:'scrolled'};
})(` + string(selectorJSON) + `)`

	var out result
	if err := chromedp.Run(ctx, chromedp.Evaluate(scrollScript, &out)); err != nil {
		return Point{}, fmt.Errorf("resolve click selector %q: %w", selector, err)
	}
	switch out.Status {
	case "invalid":
		return Point{}, fmt.Errorf("invalid click selector %q: %s", selector, strings.TrimSpace(out.Detail))
	case "unmatched":
		return Point{}, fmt.Errorf("click selector %q matched no element", selector)
	case "scrolled":
		// Let layout and any scroll-linked rendering settle before measuring.
	case "":
		return Point{}, fmt.Errorf("resolve click selector %q returned no result", selector)
	default:
		return Point{}, fmt.Errorf("resolve click selector %q returned unexpected status %q", selector, out.Status)
	}

	measureScript := `(function(selector) {
  var el;
  try {
    el = document.querySelector(selector);
  } catch (error) {
    return {status:'invalid', detail:String(error && error.message || error)};
  }
  if (!el) return {status:'unmatched'};
  if (!el.getBoundingClientRect) return {status:'not_visible'};
  var rect = el.getBoundingClientRect();
  var style = window.getComputedStyle ? window.getComputedStyle(el) : null;
  if (rect.width < 1 || rect.height < 1 ||
      (style && (style.display === 'none' || style.visibility === 'hidden' || parseFloat(style.opacity || '1') <= 0.05))) {
    return {status:'not_visible'};
  }
  return {status:'ok', x:rect.left + rect.width / 2, y:rect.top + rect.height / 2};
})(` + string(selectorJSON) + `)`

	out = result{}
	if err := chromedp.Run(ctx,
		chromedp.Sleep(50*time.Millisecond),
		chromedp.Evaluate(measureScript, &out),
	); err != nil {
		return Point{}, fmt.Errorf("resolve click selector %q: %w", selector, err)
	}
	switch out.Status {
	case "ok":
		return Point{X: int(math.Round(out.X)), Y: int(math.Round(out.Y))}, nil
	case "invalid":
		return Point{}, fmt.Errorf("invalid click selector %q: %s", selector, strings.TrimSpace(out.Detail))
	case "unmatched":
		return Point{}, fmt.Errorf("click selector %q matched no element", selector)
	case "not_visible":
		return Point{}, fmt.Errorf("click selector %q matched an element without a visible box", selector)
	default:
		return Point{}, fmt.Errorf("resolve click selector %q returned no result", selector)
	}
}
