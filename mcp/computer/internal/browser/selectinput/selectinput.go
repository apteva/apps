package selectinput

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
	Text   string   `json:"text,omitempty"`
	Value  string   `json:"value,omitempty"`
	Texts  []string `json:"texts,omitempty"`
	Values []string `json:"values,omitempty"`
	Mode   string   `json:"mode,omitempty"`
}

type Option struct {
	Text     string `json:"text"`
	Value    string `json:"value,omitempty"`
	Selected bool   `json:"selected,omitempty"`
	Visible  bool   `json:"visible,omitempty"`
}

type Result struct {
	Kind        string   `json:"kind"`
	Selector    string   `json:"selector,omitempty"`
	Multiple    bool     `json:"multiple,omitempty"`
	Mode        string   `json:"mode"`
	Matched     []string `json:"matched,omitempty"`
	Selected    []string `json:"selected,omitempty"`
	Options     []Option `json:"options,omitempty"`
	ControlText string   `json:"control_text,omitempty"`
}

func Select(ctx context.Context, target Target, req Request) (Result, error) {
	if req.Mode == "" {
		req.Mode = "replace"
	}
	if len(req.Texts) == 0 && req.Text != "" {
		req.Texts = []string{req.Text}
	}
	if len(req.Values) == 0 && req.Value != "" {
		req.Values = []string{req.Value}
	}
	if len(req.Texts) == 0 && len(req.Values) == 0 {
		return Result{}, errors.New("select_option requires text/texts or value/values")
	}
	targetJSON, _ := json.Marshal(target)
	reqJSON, _ := json.Marshal(req)
	js := fmt.Sprintf(`(async function(target, req) {
  req = {
    Text: req.Text || req.text || '',
    Value: req.Value || req.value || '',
    Texts: req.Texts || req.texts || [],
    Values: req.Values || req.values || [],
    Mode: req.Mode || req.mode || ''
  };
  function norm(s) {
    return String(s || '').replace(/\s+/g, ' ').trim();
  }
%s
  function lower(s) { return norm(s).toLowerCase(); }
  function visible(el) {
    if (!el || !el.getBoundingClientRect) return false;
    var r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) return false;
    var st = window.getComputedStyle(el);
    return st.visibility !== 'hidden' && st.display !== 'none' && parseFloat(st.opacity || '1') > 0.05;
  }
  function eventClick(el) {
    if (!el) return;
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
    if (el.matches && el.matches('select,[role="combobox"],[role="listbox"],[aria-haspopup="listbox"],[aria-haspopup="menu"]')) return el;
    if (el.closest) return el.closest('select,[role="combobox"],[role="listbox"],[aria-haspopup="listbox"],[aria-haspopup="menu"]') || el;
    return el;
  }
  function optionText(opt) {
    return norm(opt.textContent || opt.label || opt.getAttribute('aria-label') || opt.value || '');
  }
  function requestKeys() {
    return {
      texts: (req.Texts || []).map(lower).filter(Boolean),
      values: (req.Values || []).map(function(v) { return String(v || ''); }).filter(Boolean)
    };
  }
  function optionMatches(opt, keys) {
    var text = lower(optionText(opt));
    var value = String(opt.value || opt.getAttribute('data-value') || opt.getAttribute('value') || '');
    if (keys.values.indexOf(value) >= 0) return true;
    if (keys.texts.indexOf(text) >= 0) return true;
    return false;
  }
  function optionLabel(opt) {
    return optionText(opt) || String(opt.value || opt.getAttribute('data-value') || opt.getAttribute('value') || '');
  }
  function selectedOptionLabel(opt) {
    return optionText(opt) || String(opt.value || '');
  }
  function nativeSelect(sel) {
    var keys = requestKeys();
    var mode = lower(req.Mode || 'replace');
    var options = Array.prototype.slice.call(sel.options || []);
    var matched = [];
    var found = new Set();
    for (var i = 0; i < options.length; i++) {
      if (optionMatches(options[i], keys)) {
        matched.push(selectedOptionLabel(options[i]));
        found.add(i);
      }
    }
    var wanted = (keys.texts.length + keys.values.length);
    if (matched.length === 0 || matched.length < wanted) {
      return {error: 'select_option: option not found', kind:'native', options: options.map(function(o) {
        return {text: selectedOptionLabel(o), value: String(o.value || ''), selected: !!o.selected, visible: true};
      })};
    }
    if (!sel.multiple || mode === 'replace') {
      for (var c = 0; c < options.length; c++) options[c].selected = false;
    }
    for (var j = 0; j < options.length; j++) {
      if (!found.has(j)) {
        if (sel.multiple && (mode === 'remove' || mode === 'toggle')) {
          // handled only for matched options below
        }
        continue;
      }
      if (mode === 'remove') options[j].selected = false;
      else if (mode === 'toggle') options[j].selected = !options[j].selected;
      else options[j].selected = true;
    }
    sel.dispatchEvent(new Event('input', {bubbles:true}));
    sel.dispatchEvent(new Event('change', {bubbles:true}));
    return {
      ok: true, kind: 'native', multiple: !!sel.multiple, mode: mode, matched: matched,
      selected: options.filter(function(o) { return o.selected; }).map(selectedOptionLabel),
      options: options.map(function(o) {
        return {text: selectedOptionLabel(o), value: String(o.value || ''), selected: !!o.selected, visible: true};
      })
    };
  }
  function customOptions() {
    return Array.prototype.slice.call(document.querySelectorAll('[role="option"],[role="menuitem"],[role="treeitem"]')).filter(visible);
  }
  function selectedLike(el) {
    var aria = lower(el.getAttribute('aria-selected') || el.getAttribute('aria-checked'));
    if (aria === 'true') return true;
    var cls = lower(el.className || '');
    return /\b(selected|checked|active)\b/.test(cls);
  }
  async function waitForOptions() {
    var start = Date.now();
    while (Date.now() - start < 2500) {
      var opts = customOptions();
      if (opts.length) return opts;
      await new Promise(function(r) { setTimeout(r, 50); });
    }
    return [];
  }
  async function customSelect(control) {
    var mode = lower(req.Mode || 'replace');
    var keys = requestKeys();
    var matched = [];
    var wanted = (keys.texts.length + keys.values.length);
    if (control.getAttribute && control.getAttribute('aria-expanded') !== 'true') {
      eventClick(control);
    } else if (!customOptions().length) {
      eventClick(control);
    }
    var opts = await waitForOptions();
    if (!opts.length) {
      return {error:'select_option: no visible role=option/menuitem nodes after opening control', kind:'custom', control_text:norm(control.textContent || '')};
    }
    var candidates = opts.filter(function(o) { return optionMatches(o, keys); });
    if (candidates.length === 0 || candidates.length < wanted) {
      return {error:'select_option: option not found', kind:'custom', control_text:norm(control.textContent || ''), options: opts.map(function(o) {
        return {text: optionLabel(o), value: String(o.getAttribute('data-value') || o.getAttribute('value') || ''), selected: selectedLike(o), visible: true};
      })};
    }
    for (var i = 0; i < candidates.length; i++) {
      opts = customOptions();
      var c = null;
      for (var j = 0; j < opts.length; j++) {
        if (optionMatches(opts[j], {texts:[lower(optionLabel(candidates[i]))], values:[String(candidates[i].getAttribute('data-value') || candidates[i].getAttribute('value') || '')].filter(Boolean)})) {
          c = opts[j]; break;
        }
      }
      if (!c) c = candidates[i];
      var already = selectedLike(c);
      if (mode === 'remove' && !already) continue;
      if ((mode === 'add' || mode === 'replace') && already) {
        matched.push(optionLabel(c));
        continue;
      }
      eventClick(c);
      matched.push(optionLabel(c));
      await new Promise(function(r) { setTimeout(r, 150); });
      if (i < candidates.length - 1 && !customOptions().length) {
        eventClick(control);
        await waitForOptions();
      }
    }
    await new Promise(function(r) { setTimeout(r, 150); });
    var after = customOptions();
    return {
      ok: true, kind: 'custom', multiple: candidates.length > 1, mode: mode,
      matched: matched, control_text: norm(control.textContent || ''),
      selected: after.filter(selectedLike).map(optionLabel),
      options: after.map(function(o) {
        return {text: optionLabel(o), value: String(o.getAttribute('data-value') || o.getAttribute('value') || ''), selected: selectedLike(o), visible: true};
      })
    };
  }

  var targetEl = resolveTarget();
  if (!targetEl) return {error:'select_option: target not found'};
  var result = targetEl.tagName && targetEl.tagName.toLowerCase() === 'select'
    ? nativeSelect(targetEl)
    : await customSelect(targetEl);
  if (result && result.ok) result.selector = cssPath(targetEl);
  return result;
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
		return out.Result, errors.New("select_option failed")
	}
	return out.Result, nil
}
