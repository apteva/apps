// Package elementread reads the text and value of elements matched by a CSS
// selector without mutating, focusing, or scrolling the page.
package elementread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"github.com/chromedp/chromedp"
)

const (
	// MaxElements bounds an all=true read.
	MaxElements = 20
	// MaxTextChars bounds the text and value of each element (matches the
	// isolated test browser's locator cap).
	MaxTextChars = 4096
	// MaxAttributes bounds the attributes returned per element.
	MaxAttributes = 40
	// MaxSelectorChars rejects pathological selectors before evaluation.
	MaxSelectorChars = 1000
)

// ErrInvalidSelector is returned when the page rejects the selector syntax.
var ErrInvalidSelector = errors.New("invalid CSS selector")

const readScript = `(() => {
  const selector = __SELECTOR__;
  const limit = __LIMIT__;
  const includeHidden = __INCLUDE_HIDDEN__;
  const maxChars = __MAX_CHARS__;
  const maxAttributes = __MAX_ATTRIBUTES__;
  let nodes;
  try {
    nodes = document.querySelectorAll(selector);
  } catch (e) {
    return { error: "invalid_selector", message: String(e && e.message || e) };
  }
  const clip = (s) => {
    s = s == null ? "" : String(s);
    return s.length > maxChars ? { v: s.slice(0, maxChars), t: true } : { v: s, t: false };
  };
  const credentialAutocomplete = /(^|\s)(current-password|new-password|one-time-code|cc-number|cc-csc|cc-exp|cc-exp-month|cc-exp-year)(\s|$)/i;
  const marked = (el) => {
    for (const name of ["sensitive", "data-sensitive", "data-test-sensitive"]) {
      if (el.hasAttribute(name)) {
        const v = (el.getAttribute(name) || "").trim().toLowerCase();
        if (v !== "false" && v !== "0") return true;
      }
    }
    return false;
  };
  const sensitive = (el) => {
    const tag = el.tagName.toLowerCase();
    if (tag === "input" && (el.type || "").toLowerCase() === "password") return true;
    if (credentialAutocomplete.test(el.getAttribute("autocomplete") || "")) return true;
    if (marked(el)) return true;
    const owner = el.closest && el.closest("[sensitive]:not([sensitive=false]),[data-sensitive]:not([data-sensitive=false]),[data-test-sensitive]:not([data-test-sensitive=false])");
    return !!owner;
  };
  const visible = (el) => {
    if (!el.isConnected) return false;
    if (typeof el.checkVisibility === "function") {
      if (!el.checkVisibility({ visibilityProperty: true, contentVisibilityAuto: true })) return false;
    } else {
      const style = getComputedStyle(el);
      if (style.display === "none" || style.visibility === "hidden" || style.visibility === "collapse") return false;
    }
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };
  const read = (el, isVisible) => {
    const tag = el.tagName.toLowerCase();
    const secret = sensitive(el);
    const out = { tag, text: "", visible: isVisible, truncated: false };
    if (secret) {
      // Like the isolated test browser: a sensitive element exposes neither
      // text nor value, only that something was withheld.
      out.redacted = true;
    } else {
      // innerText of a hidden element falls back to textContent, which is what
      // test-status markers rely on.
      const rawText = typeof el.innerText === "string" ? el.innerText : el.textContent;
      const text = clip((rawText || "").replace(/[ \t]+\n/g, "\n").trim());
      out.text = text.v;
      out.truncated = text.t;
    }
    const hasValue = (tag === "input" || tag === "textarea" || tag === "select" || tag === "option" ||
      tag === "button" || tag === "output" || tag === "progress" || tag === "meter" || tag === "data") && "value" in el;
    if (hasValue) {
      if (secret) {
        out.value_redacted = true;
      } else {
        const value = clip(el.value);
        out.value = value.v;
        out.truncated = out.truncated || value.t;
      }
    }
    if (tag === "input" && /^(checkbox|radio)$/i.test(el.type || "")) {
      out.checked = !!el.checked;
    }
    const attributes = {};
    let kept = 0;
    for (const attr of Array.from(el.attributes)) {
      if (kept >= maxAttributes) { out.attributes_truncated = true; break; }
      // The value attribute of a sensitive control can hold the secret.
      if (secret && attr.name === "value") continue;
      attributes[attr.name] = clip(attr.value).v.slice(0, 512);
      kept++;
    }
    if (kept > 0) out.attributes = attributes;
    return out;
  };
  const elements = [];
  let count = 0;
  for (const node of nodes) {
    const isVisible = visible(node);
    if (!includeHidden && !isVisible) continue;
    count++;
    if (elements.length < limit) elements.push(read(node, isVisible));
  }
  return { count, elements };
})()`

type scriptResult struct {
	Error    string                 `json:"error"`
	Message  string                 `json:"message"`
	Count    int                    `json:"count"`
	Elements []computer.ElementRead `json:"elements"`
}

// Run reads the first element matching selector, or up to MaxElements when
// opts.All is true. Hidden elements are included unless opts.VisibleOnly. A selector with no match returns Count=0 and no error.
func Run(ctx context.Context, selector string, opts computer.ElementReadOptions) (computer.ElementReadResult, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return computer.ElementReadResult{}, fmt.Errorf("%w: selector required", ErrInvalidSelector)
	}
	if len(selector) > MaxSelectorChars {
		return computer.ElementReadResult{}, fmt.Errorf("%w: selector exceeds %d characters", ErrInvalidSelector, MaxSelectorChars)
	}
	quoted, err := json.Marshal(selector)
	if err != nil {
		return computer.ElementReadResult{}, err
	}
	limit := 1
	if opts.All {
		limit = MaxElements
	}
	script := strings.NewReplacer(
		"__SELECTOR__", string(quoted),
		"__LIMIT__", fmt.Sprint(limit),
		"__MAX_CHARS__", fmt.Sprint(MaxTextChars),
		"__MAX_ATTRIBUTES__", fmt.Sprint(MaxAttributes),
		"__INCLUDE_HIDDEN__", fmt.Sprint(!opts.VisibleOnly),
	).Replace(readScript)
	var out scriptResult
	if err := cdputil.Run(ctx, chromedp.Evaluate(script, &out)); err != nil {
		return computer.ElementReadResult{}, err
	}
	if out.Error == "invalid_selector" {
		return computer.ElementReadResult{}, fmt.Errorf("%w: %s", ErrInvalidSelector, out.Message)
	}
	if out.Elements == nil {
		out.Elements = []computer.ElementRead{}
	}
	return computer.ElementReadResult{Selector: selector, Count: out.Count, Elements: out.Elements}, nil
}
