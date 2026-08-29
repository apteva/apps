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
	ID           string
	Selector     string
	X            int
	Y            int
	HasPoint     bool
	ExpectedName string
	ExpectedRole string
}

type Request struct {
	Checked bool `json:"checked"`
}

type Result struct {
	TargetID         string `json:"target_id,omitempty"`
	AccessibleName   string `json:"accessible_name,omitempty"`
	Kind             string `json:"kind"`
	Selector         string `json:"selector,omitempty"`
	Role             string `json:"role,omitempty"`
	Label            string `json:"label,omitempty"`
	PreviousChecked  bool   `json:"previous_checked"`
	Checked          bool   `json:"checked"`
	Changed          bool   `json:"changed"`
	Verified         bool   `json:"verified"`
	ActionDispatched bool   `json:"action_dispatched"`
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
    try {
      if (el.checkVisibility && !el.checkVisibility({checkOpacity:true, checkVisibilityCSS:true})) return false;
    } catch (e) {}
    for (var node = el; node && node.nodeType === 1; node = node.parentElement) {
      var st = window.getComputedStyle(node);
      if (st.visibility === 'hidden' || st.display === 'none' || parseFloat(st.opacity || '1') <= 0.05) return false;
    }
    return true;
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
    var selector = 'input[type="checkbox"],input[type="radio"],[role="checkbox"],[role="radio"],[role="switch"],[aria-checked]';
    for (var row = el.parentElement, depth = 0; row && depth < 5; row = row.parentElement, depth++) {
      var rect = row.getBoundingClientRect();
      if (rect.height > 140 || rect.width > Math.min(window.innerWidth, 1200)) break;
      var controls = row.querySelectorAll(selector), adjacent = norm(row.innerText || row.textContent);
      if (controls.length === 1 && controls[0] === el && adjacent && adjacent.length <= 180) return adjacent;
    }
    return norm(el.textContent);
  }
  function loadingMarker(el) {
    if (!el || !el.getAttribute) return false;
    if (el.getAttribute('aria-busy') === 'true' || el.getAttribute('data-loading') === 'true' || el.getAttribute('data-state') === 'loading') return true;
    return /(^|[-_\s])(loading|is-loading|pending)([-_\s]|$)/.test(norm(el.className).toLowerCase());
  }
  function targetLoading(el) {
    if (loadingMarker(el)) return true;
    var nodes = el.querySelectorAll ? el.querySelectorAll('[role="progressbar"],[aria-label*="loading" i],[aria-label*="saving" i],[class*="spinner" i],[data-loading="true"]') : [];
    for (var i = 0; i < nodes.length; i++) if (visible(nodes[i])) return true;
    var selector = 'button,input,select,textarea,[role="button"],[role="checkbox"],[role="radio"],[role="switch"],[aria-checked]';
    for (var row = el.parentElement, depth = 0; row && depth < 3; row = row.parentElement, depth++) {
      var rect = row.getBoundingClientRect();
      if (rect.height > 140 || rect.width > Math.min(window.innerWidth, 1200)) break;
      var controls = row.querySelectorAll(selector);
      if (controls.length !== 1 || controls[0] !== el) continue;
      if (loadingMarker(row)) return true;
      var rowNodes = row.querySelectorAll('[role="progressbar"],[aria-label*="loading" i],[aria-label*="saving" i],[class*="spinner" i],[data-loading="true"]');
      for (var j = 0; j < rowNodes.length; j++) if (visible(rowNodes[j])) return true;
      break;
    }
    return false;
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
    if (target.ID) {
      var state = window.__aptevaComputerSOM, saved = state && state.targets && state.targets[target.ID];
      if (!saved || !saved.element || !saved.element.isConnected) return {__stale:true};
      el = saved.element;
    }
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
  if (el.__stale) return {error:'set_checked: stale_target: target no longer identifies the same live DOM element'};
  if (!visible(el)) {
    var visibleControl = el.querySelector && el.querySelector('input[type="checkbox"],input[type="radio"],[role="checkbox"],[role="switch"],[aria-checked]');
    if (visibleControl) el = visibleControl;
  }
  var previous = checkedState(el);
  if (previous === null) return {error:'set_checked: target is not a checkbox, radio, switch, or aria-checked control'};
  var actualLabel = labelFor(el), actualRole = norm(el.getAttribute && (el.getAttribute('role') || el.getAttribute('type'))).toLowerCase();
  if (target.ExpectedName && norm(target.ExpectedName).toLowerCase() !== actualLabel.toLowerCase()) return {error:'set_checked: target name changed from "' + norm(target.ExpectedName) + '" to "' + actualLabel + '"'};
  if (target.ExpectedRole && norm(target.ExpectedRole).toLowerCase() !== actualRole) return {error:'set_checked: target role changed from "' + norm(target.ExpectedRole) + '" to "' + actualRole + '"'};
  if (targetLoading(el)) return {error:'set_checked: target is loading'};
  if (el.disabled || (el.matches && el.matches(':disabled')) || el.getAttribute('aria-disabled') === 'true' || (el.closest && el.closest('[inert]'))) return {error:'set_checked: target is disabled'};
  var changed = false;
  var actionDispatched = false;
  if (previous !== req.Checked) {
    if (el.matches && el.matches('input[type="checkbox"],input[type="radio"]')) {
      if (el.disabled) return {error:'set_checked: target is disabled'};
      setNativeChecked(el, req.Checked);
      el.dispatchEvent(new Event('input', {bubbles:true}));
      el.dispatchEvent(new Event('change', {bubbles:true}));
      changed = true;
      actionDispatched = true;
    } else {
      eventClick(el);
      await new Promise(function(r) { setTimeout(r, 120); });
      changed = true;
      actionDispatched = true;
    }
  }
  var after = checkedState(el);
  if (after !== req.Checked) return {error:'set_checked: final state did not match requested state'};
  return {
    ok: true,
    target_id: target.ID || '',
    accessible_name: actualLabel,
    kind: (el.matches && el.matches('input[type="checkbox"],input[type="radio"]')) ? 'native' : 'aria',
    selector: cssPath(el),
    role: el.getAttribute && (el.getAttribute('role') || el.getAttribute('type') || ''),
    label: actualLabel,
    previous_checked: previous,
    checked: after,
    changed: changed,
    verified: true,
    action_dispatched: actionDispatched
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
