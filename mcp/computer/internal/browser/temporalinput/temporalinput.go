package temporalinput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/apteva/apps/mcp/computer/internal/browser/domselector"
)

type RecoveryTarget struct {
	Name     string `json:"name"`
	Role     string `json:"role,omitempty"`
	Selector string `json:"selector,omitempty"`
}

type Target struct {
	ID       string
	Selector string
	X        int
	Y        int
	HasPoint bool
}

type Request struct {
	Value string `json:"value"`
}

// Validity is the browser's post-write constraint-validation state. Keeping
// these fields in the action result makes masked and controlled input failures
// actionable without requiring another screenshot or guessed selector.
type Validity struct {
	Valid           bool   `json:"valid"`
	BadInput        bool   `json:"bad_input,omitempty"`
	PatternMismatch bool   `json:"pattern_mismatch,omitempty"`
	TypeMismatch    bool   `json:"type_mismatch,omitempty"`
	RangeUnderflow  bool   `json:"range_underflow,omitempty"`
	RangeOverflow   bool   `json:"range_overflow,omitempty"`
	StepMismatch    bool   `json:"step_mismatch,omitempty"`
	TooLong         bool   `json:"too_long,omitempty"`
	TooShort        bool   `json:"too_short,omitempty"`
	ValueMissing    bool   `json:"value_missing,omitempty"`
	CustomError     bool   `json:"custom_error,omitempty"`
	Message         string `json:"message,omitempty"`
}

type Result struct {
	Kind            string           `json:"kind"`
	Selector        string           `json:"selector,omitempty"`
	Label           string           `json:"label,omitempty"`
	InputType       string           `json:"input_type,omitempty"`
	Placeholder     string           `json:"placeholder,omitempty"`
	Pattern         string           `json:"pattern,omitempty"`
	FormatHint      string           `json:"format_hint,omitempty"`
	DateLike        bool             `json:"date_like"`
	RequestedValue  string           `json:"requested_value"`
	NormalizedValue string           `json:"normalized_value"`
	PreviousValue   string           `json:"previous_value"`
	ActualValue     string           `json:"actual_value"`
	Value           string           `json:"value"` // compatibility alias for actual_value
	Changed         bool             `json:"changed"`
	Verified        bool             `json:"verified"`
	Strategy        string           `json:"strategy,omitempty"`
	Validity        Validity         `json:"validity"`
	ErrorCode       string           `json:"error_code,omitempty"`
	ErrorMessage    string           `json:"error_message,omitempty"`
	RecoveryTargets []RecoveryTarget `json:"recovery_targets,omitempty"`
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
  function lower(s) { return norm(s).toLowerCase(); }
%s
  function associatedLabel(el) {
    if (!el) return '';
    if (el.id) {
      try {
        var lab = (el.ownerDocument || document).querySelector('label[for="' + CSS.escape(el.id) + '"]');
        if (lab) return norm(lab.innerText || lab.textContent);
      } catch (e) {}
    }
    var closest = el.closest && el.closest('label');
    return closest ? norm(closest.innerText || closest.textContent) : '';
  }
  function labelFor(el) {
    if (!el) return '';
    var aria = norm(el.getAttribute && el.getAttribute('aria-label'));
    if (aria) return aria;
    var labelled = norm(el.getAttribute && el.getAttribute('aria-labelledby'));
    if (labelled) {
      var doc = el.ownerDocument || document;
      var joined = norm(labelled.split(/\s+/).map(function(id) {
        var node = doc.getElementById(id);
        return node ? (node.innerText || node.textContent || '') : '';
      }).join(' '));
      if (joined) return joined;
    }
    return associatedLabel(el) || norm(el.getAttribute && el.getAttribute('title')) ||
      norm(el.getAttribute && el.getAttribute('placeholder'));
  }
  function resolveTarget() {
    if (target.ID) {
      var state = window.__aptevaComputerSOM, saved = state && state.targets && state.targets[target.ID];
      // A stable identity must never fall through to an old coordinate.
      return saved && saved.element && saved.element.isConnected ? saved.element : null;
    }
    var el = null;
    if (target.Selector) {
      try { el = document.querySelector(target.Selector); }
      catch (e) { return {selectorError: String(e && e.message || e)}; }
    }
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
  function isoDateParts(raw) {
    var m = norm(raw).match(/^(\d{4})-(\d{1,2})-(\d{1,2})$/);
    return m ? {y:m[1], m:pad2(m[2]), d:pad2(m[3])} : null;
  }
  function normalizeDate(raw) {
    var s = norm(raw), parts = isoDateParts(s), m;
    if (parts) return parts.y + '-' + parts.m + '-' + parts.d;
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
  function formatFromShape(shape) {
    var s = norm(shape).toLowerCase();
    if (!s) return '';
    s = s.replace(/year/g, 'yyyy').replace(/month/g, 'mm').replace(/day/g, 'dd');
    if (/y{2,4}[^a-z0-9]+m{1,2}[^a-z0-9]+d{1,2}/.test(s)) return 'yyyy-mm-dd';
    if (/m{1,2}[^a-z0-9]+d{1,2}[^a-z0-9]+y{2,4}/.test(s)) return 'mm/dd/yyyy';
    if (/d{1,2}[^a-z0-9]+m{1,2}[^a-z0-9]+y{2,4}/.test(s)) return 'dd/mm/yyyy';
    return '';
  }
  function inferFormat(el, label, placeholder, pattern, previous) {
    var type = String(el.type || '').toLowerCase();
    if (type === 'date') return 'yyyy-mm-dd';
    if (type === 'datetime-local') return 'yyyy-mm-ddThh:mm';
    var hint = formatFromShape(placeholder) || formatFromShape(pattern);
    if (hint) return hint;
    if (/^\d{4}-\d{1,2}-\d{1,2}$/.test(previous)) return 'yyyy-mm-dd';
    var m = String(previous || '').match(/^(\d{1,2})\/(\d{1,2})\/(\d{4})$/);
    if (m) {
      if (Number(m[1]) > 12) return 'dd/mm/yyyy';
      if (Number(m[2]) > 12) return 'mm/dd/yyyy';
    }
    var semantic = norm(label + ' ' + placeholder + ' ' + pattern).toLowerCase();
    if (/\b(date|day|month|year|datum|fecha|data)\b/.test(semantic)) {
      var lang = String(el.lang || document.documentElement.lang || navigator.language || '').toLowerCase();
      if (lang === 'en-us' || lang.indexOf('en-us-') === 0) return 'mm/dd/yyyy';
      if (lang) return 'dd/mm/yyyy';
    }
    return '';
  }
  function isDateLike(el, label, placeholder, pattern, hint) {
    var type = String(el.type || '').toLowerCase();
    if (type === 'date' || type === 'datetime-local') return true;
    if (!/^(text|search|tel|)$/.test(type)) return false;
    var semantic = norm(label + ' ' + placeholder + ' ' + pattern).toLowerCase();
    return !!hint || /\b(date|day|month|year|datum|fecha|data)\b/.test(semantic) ||
      /[mdy]{1,4}\s*[\/.\-]\s*[mdy]{1,4}/i.test(semantic);
  }
  function formatISODate(raw, hint) {
    var p = isoDateParts(raw);
    if (!p) return raw;
    if (hint === 'mm/dd/yyyy') return p.m + '/' + p.d + '/' + p.y;
    if (hint === 'dd/mm/yyyy') return p.d + '/' + p.m + '/' + p.y;
    return p.y + '-' + p.m + '-' + p.d;
  }
  function normalizedFor(el, raw, dateLike, hint) {
    var type = String(el.type || '').toLowerCase();
    if (type === 'time') return normalizeTime(raw);
    if (type === 'date') return normalizeDate(raw);
    if (type === 'datetime-local') return normalizeDateTime(raw);
    if (dateLike && hint) return formatISODate(raw, hint);
    return raw;
  }
  function setNativeValue(el, value) {
    var proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    var desc = Object.getOwnPropertyDescriptor(proto, 'value');
    if (desc && desc.set) desc.set.call(el, value);
    else el.value = value;
  }
  function dispatchInput(el, value) {
    try { el.dispatchEvent(new InputEvent('input', {bubbles:true, composed:true, inputType:'insertText', data:value})); }
    catch (e) { el.dispatchEvent(new Event('input', {bubbles:true, composed:true})); }
  }
  function dispatchChange(el) { el.dispatchEvent(new Event('change', {bubbles:true, composed:true})); }
  function settle() { return new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); }); }
  function validityFor(el) {
    var v = el && el.validity;
    if (!v) return {valid:true};
    return {
      valid: !!v.valid, bad_input: !!v.badInput, pattern_mismatch: !!v.patternMismatch,
      type_mismatch: !!v.typeMismatch, range_underflow: !!v.rangeUnderflow,
      range_overflow: !!v.rangeOverflow, step_mismatch: !!v.stepMismatch,
      too_long: !!v.tooLong, too_short: !!v.tooShort, value_missing: !!v.valueMissing,
      custom_error: !!v.customError, message: norm(el.validationMessage)
    };
  }
	function visible(node) {
	  if (!node || !node.getBoundingClientRect) return false;
	  var r=node.getBoundingClientRect(),style=(node.ownerDocument.defaultView||window).getComputedStyle(node);
	  return r.width>=1&&r.height>=1&&style.display!=='none'&&style.visibility!=='hidden';
	}
	function recoveryTargets(el) {
	  var out=[],seen=new Set(),root=el;
	  for (var depth=0; root && depth<4; depth++,root=root.parentElement) {
		var nodes=root.querySelectorAll ? root.querySelectorAll('button,[role="button"]') : [];
		for (var i=0;i<nodes.length;i++) {
		  var node=nodes[i],name=labelFor(node)||norm(node.innerText||node.textContent),low=name.toLowerCase();
		  var popup=lower(node.getAttribute&&node.getAttribute('aria-haspopup'));
		  if (!name||!visible(node)||seen.has(node)||(!/\b(date|calendar|choose|picker)\b/.test(low)&&popup!=='dialog'&&popup!=='grid')) continue;
		  seen.add(node);out.push({name:name,role:lower(node.getAttribute&&node.getAttribute('role'))||'button',selector:cssPath(node)});
		  if(out.length>=6)return out;
		}
		if(out.length)return out;
	  }
	  return out;
	}
  var el = resolveTarget();
  if (el && el.selectorError) return {error:'set_temporal: invalid selector: ' + el.selectorError};
  if (!el) return {error:'set_temporal: target not found'};
  var isEditable = el.matches && el.matches('input,textarea');
  var isContentEditable = !!el.isContentEditable || (el.getAttribute && el.getAttribute('role') === 'textbox');
  if (!isEditable && !isContentEditable) return {error:'set_temporal: target is not an input, textarea, or editable textbox'};
  var previous = isContentEditable ? norm(el.textContent) : String(el.value || '');
  var label = labelFor(el);
  var placeholder = norm(el.getAttribute && el.getAttribute('placeholder'));
  var pattern = norm(el.getAttribute && el.getAttribute('pattern'));
  var hint = isContentEditable ? '' : inferFormat(el, label, placeholder, pattern, previous);
  var dateLike = !isContentEditable && isDateLike(el, label, placeholder, pattern, hint);
  var value = isContentEditable ? req.Value : normalizedFor(el, req.Value, dateLike, hint);
  var kind = isContentEditable ? 'editable' : (dateLike && String(el.type || '').toLowerCase() === 'text' ? 'date_text' : 'native');
  var selector = cssPath(el);
  var inputType = isContentEditable ? 'contenteditable' : String(el.type || el.tagName || '').toLowerCase();
  function result(actual, verified, strategy, errorCode, errorMessage) {
    return {
      ok: verified,
      kind: kind, selector: selector, label: label, input_type: inputType,
      placeholder: placeholder, pattern: pattern, format_hint: hint, date_like: dateLike,
      requested_value: req.Value, normalized_value: value, previous_value: previous,
      actual_value: actual, value: actual, changed: previous !== actual,
      verified: verified, strategy: strategy, validity: validityFor(el),
	  recovery_targets: recoveryTargets(el),
      error_code: errorCode || '', error_message: errorMessage || '', error: errorMessage || ''
    };
  }
  if (!isContentEditable && (el.disabled || el.readOnly)) {
    return result(previous, false, 'none', 'not_editable', 'set_temporal: target is disabled or readonly');
  }
  try { el.focus(); } catch (e) {}
  var strategy = 'native_setter';
  if (isContentEditable) {
    el.textContent = value;
    dispatchInput(el, value);
  } else {
    setNativeValue(el, value);
    dispatchInput(el, value);
  }
  await settle();
  var after = isContentEditable ? norm(el.textContent) : String(el.value || '');
  if (!isContentEditable && after !== value && dateLike && /^(text|search|tel|)$/.test(inputType)) {
    strategy = 'native_edit';
    try {
      el.focus();
      if (typeof el.select === 'function') el.select();
      if (!document.execCommand || !document.execCommand('insertText', false, value)) {
        setNativeValue(el, value);
        dispatchInput(el, value);
      }
    } catch (e) {
      setNativeValue(el, value);
      dispatchInput(el, value);
    }
    await settle();
    after = String(el.value || '');
  }
  dispatchChange(el);
  try { el.blur(); } catch (e) {}
  await settle();
  after = isContentEditable ? norm(el.textContent) : String(el.value || '');
  var validity = validityFor(el);
  if (after !== value) return result(after, false, strategy, 'value_mismatch', 'set_temporal: final value did not match requested value');
  if (!validity.valid) return result(after, false, strategy, 'invalid_value', 'set_temporal: browser validity constraints rejected the value');
  return result(after, true, strategy, '', '');
})(%s, %s)`, domselector.UniqueCSSPathFunction, string(targetJSON), string(reqJSON))

	var out struct {
		Result
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := cdputil.Run(ctx, chromedp.Evaluate(js, &out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	})); err != nil {
		return Result{}, err
	}
	if out.Error != "" {
		if shouldUseTrustedTextFallback(out.Result) {
			fallback := out.Result
			if retryErr := trustedTextFallback(ctx, &fallback); retryErr == nil && fallback.Verified {
				return fallback, nil
			} else if retryErr == nil {
				out.Result = fallback
			}
		}
		if out.Result.ErrorMessage == "" {
			out.Result.ErrorMessage = out.Error
		}
		return out.Result, errors.New(out.Error)
	}
	if !out.OK {
		if out.Result.ErrorMessage == "" {
			out.Result.ErrorMessage = "set_temporal failed"
		}
		return out.Result, errors.New(out.Result.ErrorMessage)
	}
	return out.Result, nil
}

func shouldUseTrustedTextFallback(result Result) bool {
	return result.ErrorCode == "value_mismatch" && result.DateLike && result.Kind == "date_text" &&
		result.Selector != "" && result.NormalizedValue != ""
}

// trustedTextFallback is deliberately narrow: it runs only after a detected
// date-like text mask reverted the standards-based native-setter path. Real
// CDP key events exercise React/mask key handlers without changing native date
// inputs or ordinary agent typing behavior.
func trustedTextFallback(ctx context.Context, result *Result) error {
	if result == nil {
		return errors.New("nil temporal result")
	}
	selectorJSON, _ := json.Marshal(result.Selector)
	focusJS := fmt.Sprintf(`(function(){
	  var el=document.querySelector(%s);if(!el||!("value" in el))return false;
	  el.focus();if(typeof el.select==="function")el.select();
	  else if(typeof el.setSelectionRange==="function")el.setSelectionRange(0,String(el.value||"").length);
	  return true;
	})()`, string(selectorJSON))
	var focused bool
	if err := cdputil.Run(ctx, chromedp.Evaluate(focusJS, &focused)); err != nil {
		return err
	}
	if !focused {
		return errors.New("temporal fallback target not found")
	}
	if err := cdputil.Run(ctx, chromedp.KeyEvent(result.NormalizedValue)); err != nil {
		return err
	}
	timer := time.NewTimer(100 * time.Millisecond)
	select {
	case <-timer.C:
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	}
	readJS := fmt.Sprintf(`(async function(){
	  function norm(s){return String(s||"").replace(/\s+/g," ").trim();}
	  var el=document.querySelector(%s);if(!el)return {actual:"",validity:{valid:false},missing:true};
	  el.dispatchEvent(new Event("change",{bubbles:true,composed:true}));
	  try{el.blur();}catch(e){}
	  await new Promise(function(resolve){requestAnimationFrame(function(){requestAnimationFrame(resolve);});});
	  var v=el.validity;
	  return {actual:String(el.value||""),validity:v?{
		valid:!!v.valid,bad_input:!!v.badInput,pattern_mismatch:!!v.patternMismatch,
		type_mismatch:!!v.typeMismatch,range_underflow:!!v.rangeUnderflow,range_overflow:!!v.rangeOverflow,
		step_mismatch:!!v.stepMismatch,too_long:!!v.tooLong,too_short:!!v.tooShort,
		value_missing:!!v.valueMissing,custom_error:!!v.customError,message:norm(el.validationMessage)
	  }:{valid:true}};
	})()`, string(selectorJSON))
	var readback struct {
		Actual   string   `json:"actual"`
		Validity Validity `json:"validity"`
		Missing  bool     `json:"missing"`
	}
	if err := cdputil.Run(ctx, chromedp.Evaluate(readJS, &readback, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	})); err != nil {
		return err
	}
	if readback.Missing {
		return errors.New("temporal fallback target disappeared")
	}
	result.Strategy = "trusted_key_events"
	result.ActualValue = readback.Actual
	result.Value = readback.Actual
	result.Changed = result.PreviousValue != readback.Actual
	result.Validity = readback.Validity
	result.Verified = readback.Actual == result.NormalizedValue && readback.Validity.Valid
	if result.Verified {
		result.ErrorCode = ""
		result.ErrorMessage = ""
		return nil
	}
	result.ErrorCode = "value_mismatch"
	if readback.Actual == result.NormalizedValue && !readback.Validity.Valid {
		result.ErrorCode = "invalid_value"
	}
	result.ErrorMessage = "set_temporal: trusted key fallback did not produce an accepted value"
	return nil
}
