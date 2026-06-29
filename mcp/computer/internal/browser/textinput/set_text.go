package textinput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type Target struct {
	Selector string
	X        int
	Y        int
	HasPoint bool
}

type SetRequest struct {
	Text        string `json:"text"`
	Mode        string `json:"mode,omitempty"`
	NewlineMode string `json:"newline_mode,omitempty"`
}

type SetResult struct {
	Kind          string `json:"kind"`
	Selector      string `json:"selector,omitempty"`
	Label         string `json:"label,omitempty"`
	InputType     string `json:"input_type,omitempty"`
	PreviousValue string `json:"previous_value"`
	Value         string `json:"value"`
	Changed       bool   `json:"changed"`
	Mode          string `json:"mode"`
	NewlineMode   string `json:"newline_mode"`
}

func Set(ctx context.Context, target Target, req SetRequest) (SetResult, error) {
	targetJSON, _ := json.Marshal(target)
	reqJSON, _ := json.Marshal(req)
	js := fmt.Sprintf(`(async function(target, req) {
  req = {
    Text: String(req.Text ?? req.text ?? ''),
    Mode: String(req.Mode ?? req.mode ?? 'replace').toLowerCase(),
    NewlineMode: String(req.NewlineMode ?? req.newline_mode ?? 'preserve').toLowerCase()
  };
  if (!req.Mode) req.Mode = 'replace';
  if (!req.NewlineMode) req.NewlineMode = 'preserve';
  if (req.Mode !== 'replace' && req.Mode !== 'append') return {error:'set_text: mode must be replace or append'};
  if (req.NewlineMode !== 'preserve' && req.NewlineMode !== 'compact') return {error:'set_text: newline_mode must be preserve or compact'};
  function norm(s) { return String(s || '').replace(/\s+/g, ' ').trim(); }
  function compactNewlines(s) { return String(s).replace(/\r\n/g, '\n').replace(/\r/g, '\n').replace(/\n{2,}/g, '\n'); }
  function cssPath(el) {
    if (!el || !el.tagName) return '';
    if (el.id) return '#' + CSS.escape(el.id);
    var parts = [];
    for (var cur = el; cur && cur.nodeType === 1 && parts.length < 4; cur = cur.parentElement) {
      var part = cur.tagName.toLowerCase();
      if (cur.classList && cur.classList.length) part += '.' + CSS.escape(cur.classList[0]);
      parts.unshift(part);
    }
    return parts.join(' > ');
  }
  function labelFor(el) {
    if (!el) return '';
    var aria = norm(el.getAttribute && (el.getAttribute('aria-label') || el.getAttribute('placeholder') || el.getAttribute('title')));
    if (aria) return aria;
    if (el.id) {
      var lab = document.querySelector('label[for="' + CSS.escape(el.id) + '"]');
      if (lab) return norm(lab.textContent);
    }
    var closestLabel = el.closest && el.closest('label');
    if (closestLabel) return norm(closestLabel.textContent);
    return '';
  }
  function isTextTarget(el) {
    if (!el || !el.matches) return false;
    if (el.matches('textarea')) return true;
    if (el.matches('input:not([type]),input[type=""],input[type="text"],input[type="search"],input[type="url"],input[type="tel"],input[type="email"],input[type="password"],input[type="number"]')) return true;
    if (el.matches('[contenteditable="true"],[contenteditable=""],[role="textbox"]')) return true;
    return false;
  }
  function resolveTarget() {
    var el = null;
    if (target.Selector) el = document.querySelector(target.Selector);
    if (!el && target.HasPoint) el = document.elementFromPoint(target.X, target.Y);
    if (!el) return null;
    if (isTextTarget(el)) return el;
    if (el.closest) return el.closest('textarea,input:not([type="hidden"]),[contenteditable="true"],[contenteditable=""],[role="textbox"]') || el;
    return el;
  }
  function valueOf(el, contentEditable) {
    return contentEditable ? String(el.innerText || el.textContent || '') : String(el.value || '');
  }
  function setNativeValue(el, value) {
    var proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    var desc = Object.getOwnPropertyDescriptor(proto, 'value');
    if (desc && desc.set) desc.set.call(el, value);
    else el.value = value;
  }
  function dispatchTextEvents(el, value) {
    try { el.dispatchEvent(new InputEvent('input', {bubbles:true, inputType:'insertText', data:value})); }
    catch (e) { el.dispatchEvent(new Event('input', {bubbles:true})); }
    el.dispatchEvent(new Event('change', {bubbles:true}));
  }
  var el = resolveTarget();
  if (!el) return {error:'set_text: target not found'};
  var isNative = el.matches && el.matches('input,textarea');
  var isContentEditable = !!el.isContentEditable || (el.getAttribute && el.getAttribute('role') === 'textbox');
  if (!isNative && !isContentEditable) return {error:'set_text: target is not a text input, textarea, contenteditable, or textbox'};
  if (isNative && (el.disabled || el.readOnly)) return {error:'set_text: target is disabled or readonly'};
  var previous = valueOf(el, isContentEditable);
  var incoming = req.NewlineMode === 'compact' ? compactNewlines(req.Text) : req.Text;
  var value = req.Mode === 'append' ? previous + incoming : incoming;
  try { el.focus(); } catch (e) {}
  if (isContentEditable) {
    el.textContent = value;
  } else {
    setNativeValue(el, value);
  }
  dispatchTextEvents(el, value);
  try { el.blur(); } catch (e) {}
  var after = valueOf(el, isContentEditable);
  return {
    ok: true,
    kind: isContentEditable ? 'contenteditable' : 'native',
    selector: cssPath(el),
    label: labelFor(el),
    input_type: isContentEditable ? 'contenteditable' : String(el.type || el.tagName || '').toLowerCase(),
    previous_value: previous,
    value: after,
    changed: previous !== after,
    mode: req.Mode,
    newline_mode: req.NewlineMode
  };
})(%s, %s)`, string(targetJSON), string(reqJSON))

	var out struct {
		SetResult
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	})); err != nil {
		return SetResult{}, err
	}
	if out.Error != "" {
		return out.SetResult, errors.New(out.Error)
	}
	if !out.OK {
		return out.SetResult, errors.New("set_text failed")
	}
	return out.SetResult, nil
}
