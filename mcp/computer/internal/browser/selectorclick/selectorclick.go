package selectorclick

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

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
	script := `(function(selector) {
  var el;
  try {
    el = document.querySelector(selector);
  } catch (error) {
    return {status:'invalid', detail:String(error && error.message || error)};
  }
  if (!el) return {status:'unmatched'};
  try { el.scrollIntoView({block:'center', inline:'nearest'}); } catch (_) {}
  if (!el.getBoundingClientRect) return {status:'not_visible'};
  var rect = el.getBoundingClientRect();
  var style = window.getComputedStyle ? window.getComputedStyle(el) : null;
  if (rect.width < 1 || rect.height < 1 ||
      (style && (style.display === 'none' || style.visibility === 'hidden' || parseFloat(style.opacity || '1') <= 0.05))) {
    return {status:'not_visible'};
  }
  return {status:'ok', x:rect.left + rect.width / 2, y:rect.top + rect.height / 2};
})(` + string(selectorJSON) + `)`

	var out result
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &out)); err != nil {
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
