package checkedinput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/apteva/apps/mcp/computer/internal/browser/domselector"
)

type Target struct {
	Selector string
	X        int
	Y        int
	HasPoint bool
}

type Request struct {
	Checked bool `json:"checked"`
}

type Result struct {
	Kind            string `json:"kind"`
	Selector        string `json:"selector,omitempty"`
	Role            string `json:"role,omitempty"`
	Label           string `json:"label,omitempty"`
	PreviousChecked bool   `json:"previous_checked"`
	Checked         bool   `json:"checked"`
	Changed         bool   `json:"changed"`
}

func Set(ctx context.Context, target Target, req Request) (Result, error) {
	targetJSON, _ := json.Marshal(target)
	reqJSON, _ := json.Marshal(req)
	js := fmt.Sprintf(`(async function(target, req) {
  req = { Checked: !!(req.Checked ?? req.checked) };
  function norm(s) { return String(s || '').replace(/\s+/g, ' ').trim(); }
  function visible(el) {
    if (!el || !el.getBoundingClientRect) return false;
    var r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) return false;
    var st = window.getComputedStyle(el);
    return st.visibility !== 'hidden' && st.display !== 'none' && parseFloat(st.opacity || '1') > 0.05;
  }
%s
  function labelFor(el) {
    if (!el) return '';
    var aria = norm(el.getAttribute && (el.getAttribute('aria-label') || el.getAttribute('title')));
    if (aria) return aria;
    if (el.id) {
      var lab = document.querySelector('label[for="' + CSS.escape(el.id) + '"]');
      if (lab) return norm(lab.textContent);
    }
    var closestLabel = el.closest && el.closest('label');
    if (closestLabel) return norm(closestLabel.textContent);
    return norm(el.textContent);
  }
  function eventClick(el) {
    try { el.scrollIntoView({block:'center', inline:'nearest'}); } catch (e) {}
    var opts = {bubbles:true, cancelable:true, view:window};
    try { el.dispatchEvent(new PointerEvent('pointerdown', opts)); } catch (e) {}
    try { el.dispatchEvent(new MouseEvent('mousedown', opts)); } catch (e) {}
    try { el.dispatchEvent(new PointerEvent('pointerup', opts)); } catch (e) {}
    try { el.dispatchEvent(new MouseEvent('mouseup', opts)); } catch (e) {}
    try { el.dispatchEvent(new MouseEvent('click', opts)); } catch (e) { if (el.click) el.click(); }
  }
  function resolveTarget() {
    var el = null;
    if (target.Selector) el = document.querySelector(target.Selector);
    if (!el && target.HasPoint) el = document.elementFromPoint(target.X, target.Y);
    if (!el) return null;
    if (el.matches && el.matches('input[type="checkbox"],input[type="radio"],[role="checkbox"],[role="switch"],[aria-checked]')) return el;
    if (el.closest) return el.closest('input[type="checkbox"],input[type="radio"],[role="checkbox"],[role="switch"],[aria-checked]') || el;
    return el;
  }
  function checkedState(el) {
    if (el.matches && el.matches('input[type="checkbox"],input[type="radio"]')) return !!el.checked;
    var aria = String(el.getAttribute && el.getAttribute('aria-checked') || '').toLowerCase();
    if (aria === 'true') return true;
    if (aria === 'false') return false;
    return null;
  }
  function setNativeChecked(el, checked) {
    var desc = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'checked');
    if (desc && desc.set) desc.set.call(el, checked);
    else el.checked = checked;
  }
  var el = resolveTarget();
  if (!el) return {error:'set_checked: target not found'};
  if (!visible(el)) {
    var visibleControl = el.querySelector && el.querySelector('input[type="checkbox"],input[type="radio"],[role="checkbox"],[role="switch"],[aria-checked]');
    if (visibleControl) el = visibleControl;
  }
  var previous = checkedState(el);
  if (previous === null) return {error:'set_checked: target is not a checkbox, radio, switch, or aria-checked control'};
  var changed = false;
  if (previous !== req.Checked) {
    if (el.matches && el.matches('input[type="checkbox"],input[type="radio"]')) {
      if (el.disabled) return {error:'set_checked: target is disabled'};
      setNativeChecked(el, req.Checked);
      el.dispatchEvent(new Event('input', {bubbles:true}));
      el.dispatchEvent(new Event('change', {bubbles:true}));
      changed = true;
    } else {
      eventClick(el);
      await new Promise(function(r) { setTimeout(r, 120); });
      changed = true;
    }
  }
  var after = checkedState(el);
  if (after !== req.Checked) return {error:'set_checked: final state did not match requested state'};
  return {
    ok: true,
    kind: (el.matches && el.matches('input[type="checkbox"],input[type="radio"]')) ? 'native' : 'aria',
    selector: cssPath(el),
    role: el.getAttribute && (el.getAttribute('role') || el.getAttribute('type') || ''),
    label: labelFor(el),
    previous_checked: previous,
    checked: after,
    changed: changed
  };
})(%s, %s)`, domselector.UniqueCSSPathFunction, string(targetJSON), string(reqJSON))

	var out struct {
		Result
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	})); err != nil {
		return Result{}, err
	}
	if out.Error != "" {
		return out.Result, errors.New(out.Error)
	}
	if !out.OK {
		return out.Result, errors.New("set_checked failed")
	}
	return out.Result, nil
}
