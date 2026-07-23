package temporalinput

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
	Value string `json:"value"`
}

type Result struct {
	Kind          string `json:"kind"`
	Selector      string `json:"selector,omitempty"`
	Label         string `json:"label,omitempty"`
	InputType     string `json:"input_type,omitempty"`
	PreviousValue string `json:"previous_value"`
	Value         string `json:"value"`
	Changed       bool   `json:"changed"`
}

func Set(ctx context.Context, target Target, req Request) (Result, error) {
	if req.Value == "" {
		return Result{}, errors.New("set_temporal requires value")
	}
	targetJSON, _ := json.Marshal(target)
	reqJSON, _ := json.Marshal(req)
	js := fmt.Sprintf(`(async function(target, req) {
  req = { Value: String(req.Value ?? req.value ?? '') };
  function norm(s) { return String(s || '').replace(/\s+/g, ' ').trim(); }
%s
  function labelFor(el) {
    if (!el) return '';
    var aria = norm(el.getAttribute && (el.getAttribute('aria-label') || el.getAttribute('placeholder') || el.getAttribute('title')));
    if (aria) return aria;
    if (el.id) {
      var lab = document.querySelector('label[for="' + CSS.escape(el.id) + '"]');
      if (lab) return norm(lab.textContent);
    }
    return '';
  }
  function resolveTarget() {
    var el = null;
    if (target.Selector) el = document.querySelector(target.Selector);
    if (!el && target.HasPoint) el = document.elementFromPoint(target.X, target.Y);
    if (!el) return null;
    if (el.matches && el.matches('input,textarea,[contenteditable="true"],[role="textbox"]')) return el;
    if (el.closest) return el.closest('input,textarea,[contenteditable="true"],[role="textbox"]') || el;
    return el;
  }
  function pad2(n) { n = Number(n); return n < 10 ? '0' + n : String(n); }
  function normalizeTime(raw) {
    var s = norm(raw);
    var m = s.match(/^(\d{1,2})(?::(\d{2}))?\s*([ap]m)$/i);
    if (m) {
      var h = Number(m[1]), min = m[2] || '00', ap = m[3].toLowerCase();
      if (ap === 'pm' && h < 12) h += 12;
      if (ap === 'am' && h === 12) h = 0;
      return pad2(h) + ':' + min;
    }
    m = s.match(/^(\d{1,2}):(\d{2})(?::\d{2})?$/);
    if (m) return pad2(m[1]) + ':' + m[2];
    return s;
  }
  function normalizeDate(raw) {
    var s = norm(raw);
    var m = s.match(/^(\d{4})-(\d{1,2})-(\d{1,2})$/);
    if (m) return m[1] + '-' + pad2(m[2]) + '-' + pad2(m[3]);
    m = s.match(/^(\d{1,2})\/(\d{1,2})\/(\d{4})$/);
    if (m) return m[3] + '-' + pad2(m[1]) + '-' + pad2(m[2]);
    return s;
  }
  function normalizeDateTime(raw) {
    var s = norm(raw).replace('T', ' ');
    var parts = s.match(/^(.+?)\s+(\d{1,2}(?::\d{2})?(?:\s*[ap]m)?|\d{1,2}:\d{2}(?::\d{2})?)$/i);
    if (!parts) return s.replace(' ', 'T');
    return normalizeDate(parts[1]) + 'T' + normalizeTime(parts[2]);
  }
  function normalizedFor(el, raw) {
    var type = String(el.type || '').toLowerCase();
    if (type === 'time') return normalizeTime(raw);
    if (type === 'date') return normalizeDate(raw);
    if (type === 'datetime-local') return normalizeDateTime(raw);
    return raw;
  }
  function setNativeValue(el, value) {
    var proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    var desc = Object.getOwnPropertyDescriptor(proto, 'value');
    if (desc && desc.set) desc.set.call(el, value);
    else el.value = value;
  }
  var el = resolveTarget();
  if (!el) return {error:'set_temporal: target not found'};
  var isEditable = el.matches && el.matches('input,textarea');
  var isContentEditable = !!el.isContentEditable || (el.getAttribute && el.getAttribute('role') === 'textbox');
  if (!isEditable && !isContentEditable) return {error:'set_temporal: target is not an input, textarea, or editable textbox'};
  var previous = isContentEditable ? norm(el.textContent) : String(el.value || '');
  var value = isContentEditable ? req.Value : normalizedFor(el, req.Value);
  try { el.focus(); } catch (e) {}
  if (isContentEditable) {
    el.textContent = value;
  } else {
    if (el.disabled || el.readOnly) return {error:'set_temporal: target is disabled or readonly'};
    setNativeValue(el, value);
  }
  el.dispatchEvent(new Event('input', {bubbles:true}));
  el.dispatchEvent(new Event('change', {bubbles:true}));
  try { el.blur(); } catch (e) {}
  var after = isContentEditable ? norm(el.textContent) : String(el.value || '');
  if (!isContentEditable && after !== value) return {error:'set_temporal: final value did not match requested value'};
  return {
    ok: true,
    kind: isContentEditable ? 'editable' : 'native',
    selector: cssPath(el),
    label: labelFor(el),
    input_type: isContentEditable ? 'contenteditable' : String(el.type || el.tagName || '').toLowerCase(),
    previous_value: previous,
    value: after,
    changed: previous !== after
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
		return out.Result, errors.New("set_temporal failed")
	}
	return out.Result, nil
}
