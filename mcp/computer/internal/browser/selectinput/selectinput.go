package selectinput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/apteva/apps/mcp/computer/internal/browser/domselector"
)

type Target struct {
	ID       string
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
	Kind             string   `json:"kind"`
	ControlKind      string   `json:"control_kind,omitempty"`
	Selector         string   `json:"selector,omitempty"`
	Multiple         bool     `json:"multiple,omitempty"`
	Mode             string   `json:"mode"`
	RequestedOptions []string `json:"requested_options,omitempty"`
	Matched          []string `json:"matched,omitempty"`
	Selected         []string `json:"selected,omitempty"`
	Options          []Option `json:"options,omitempty"`
	ControlText      string   `json:"control_text,omitempty"`
	PreviousValue    string   `json:"previous_value,omitempty"`
	CurrentValue     string   `json:"current_value,omitempty"`
	MenuOpen         bool     `json:"menu_open,omitempty"`
	OptionAvailable  bool     `json:"option_available"`
	Recoverable      bool     `json:"recoverable,omitempty"`
	ErrorCode        string   `json:"error_code,omitempty"`
	ErrorMessage     string   `json:"error_message,omitempty"`
}

func Select(ctx context.Context, target Target, req Request) (Result, error) {
	if req.Mode == "" {
		req.Mode = "replace"
	}
	switch req.Mode {
	case "replace", "add", "remove", "toggle":
	default:
		return Result{}, errors.New("select_option: invalid mode")
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
    if (target.ID) {
      var state = window.__aptevaComputerSOM, saved = state && state.targets && state.targets[target.ID];
      // A stable identity must never fall through to an old coordinate.
      return saved && saved.element && saved.element.isConnected ? saved.element : null;
    }
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
      values: (req.Values || []).map(function(v) { return String(v ?? ''); })
    };
  }
	function requestedOptions() {
	  return (req.Texts || []).map(norm).filter(Boolean).concat((req.Values || []).map(function(v) { return String(v ?? ''); }));
	}
	function controlKind(control) {
	  var tag = String(control && control.tagName || '').toLowerCase();
	  var role = lower(control && control.getAttribute && control.getAttribute('role'));
	  if (tag === 'select') return 'native_select';
	  if (tag === 'button' && role === 'combobox') return 'button_combobox';
	  if (role === 'combobox') return tag ? tag + '_combobox' : 'custom_combobox';
	  if (role === 'listbox') return tag ? tag + '_listbox' : 'custom_listbox';
	  return tag ? tag + '_custom' : 'custom_control';
	}
  function optionMatches(opt, keys) {
    var text = lower(optionText(opt));
    var value = String(opt.value || opt.getAttribute('data-value') || opt.getAttribute('value') || '');
    if (keys.values.indexOf(value) >= 0) return true;
    if (keys.texts.indexOf(text) >= 0) return true;
    return false;
  }
  function matchingError(options,keys){
    var requests=keys.texts.map(function(k){return {texts:[k],values:[]};}).concat(keys.values.map(function(k){return {texts:[],values:[k]};}));
    for(var request of requests){var count=options.filter(function(o){return optionMatches(o,request);}).length;if(count>1)return 'ambiguous';if(count===0)return 'missing';}
    return '';
  }
  function optionLabel(opt) {
    return optionText(opt) || String(opt.value || opt.getAttribute('data-value') || opt.getAttribute('value') || '');
  }
  function selectedOptionLabel(opt) {
    return optionText(opt) || String(opt.value || '');
  }
  async function nativeSelect(sel) {
    if(sel.disabled || sel.matches(':disabled')) return {error:'select_option: control is disabled'};
    var keys = requestKeys();
    var mode = lower(req.Mode || 'replace');
    var options = Array.prototype.slice.call(sel.options || []);
	var previous = options.filter(function(o) { return o.selected; }).map(selectedOptionLabel).join(', ');
	var requested = requestedOptions();
    var matched = [];
    var found = new Set();
    for (var i = 0; i < options.length; i++) {
      if (optionMatches(options[i], keys) && !options[i].disabled && !(options[i].parentElement.tagName==='OPTGROUP' && options[i].parentElement.disabled)) {
        matched.push(selectedOptionLabel(options[i]));
        found.add(i);
      }
    }
	var wanted = Math.max(keys.texts.length, keys.values.length);
    var matchError=matchingError(options,keys);
    if(matchError==='ambiguous'||(!sel.multiple&&found.size>1))return {error:'select_option: ambiguous option; use a unique value',error_code:'option_ambiguous'};
    if (matchError || matched.length === 0 || matched.length < wanted) {
	  return {error: 'select_option: option not found', error_code:'native_select_option_unavailable',
		kind:'native', control_kind:'native_select', mode:mode, selector:cssPath(sel),
		requested_options:requested, previous_value:previous, current_value:previous,
		menu_open:false, option_available:false, recoverable:false, options: options.map(function(o) {
        return {text: selectedOptionLabel(o), value: String(o.value || ''), selected: !!o.selected, visible: true};
      })};
    }
    if (mode === 'replace' || (!sel.multiple && mode === 'add')) {
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
    var expected = options.filter(function(o){return o.selected;}).map(function(o){return o.index;}).join(',');
    sel.dispatchEvent(new Event('input', {bubbles:true}));
    sel.dispatchEvent(new Event('change', {bubbles:true}));
    await new Promise(function(r){setTimeout(r,150);});
    if (!sel.isConnected || expected !== Array.from(sel.options).filter(function(o){return o.selected;}).map(function(o){return o.index;}).join(',')) return {error:'select_option: selection reverted after reconciliation'};
    return {
	  ok: true, kind: 'native', control_kind:'native_select', selector:cssPath(sel),
	  multiple: !!sel.multiple, mode: mode, requested_options:requested, matched: matched,
      selected: options.filter(function(o) { return o.selected; }).map(selectedOptionLabel),
	  previous_value:previous,
	  current_value:options.filter(function(o) { return o.selected; }).map(selectedOptionLabel).join(', '),
	  menu_open:false, option_available:true,
      options: options.map(function(o) {
        return {text: selectedOptionLabel(o), value: String(o.value || ''), selected: !!o.selected, visible: true};
      })
    };
  }
  var popupRoot = null;
  function customOptions() {
    if (!popupRoot || !popupRoot.isConnected) return [];
    return Array.from(popupRoot.querySelectorAll('[role="option"],[role="menuitem"],[role="treeitem"]')).filter(visible);
  }
  function resolvePopup(control, before) {
    var ids=(control.getAttribute('aria-controls')||control.getAttribute('aria-owns')||'').split(/\s+/).filter(Boolean);
    var roots=ids.map(function(id){return document.getElementById(id);}).filter(Boolean);
    if(control.matches('[role="listbox"],[role="menu"],[role="tree"]')) roots.push(control);
    if(roots.length===1) return roots[0];
    if(roots.length>1) return null;
    // Compatibility for unlabelled portals: require exactly one newly opened
    // popup, never a pre-existing unrelated list/menu.
    var added=Array.from(document.querySelectorAll('[role="listbox"],[role="menu"],[role="tree"]')).filter(function(n){return visible(n)&&!before.has(n);});
    return added.length===1?added[0]:null;
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
	var wanted = Math.max(keys.texts.length, keys.values.length);
	var previous = norm(control.value || control.textContent || '');
	var requested = requestedOptions();
	var kind = controlKind(control);
	var selector = cssPath(control);
    if(control.disabled||control.getAttribute('aria-disabled')==='true') return {error:'select_option: control is disabled'};
    var before=new Set(Array.from(document.querySelectorAll('[role="listbox"],[role="menu"],[role="tree"]')).filter(visible));
    popupRoot=resolvePopup(control,before);
    if (control.getAttribute && control.getAttribute('aria-expanded') !== 'true') {
      eventClick(control);
    } else if (!customOptions().length) {
      eventClick(control);
    }
    for(var attempt=0;attempt<50&&!popupRoot;attempt++){popupRoot=resolvePopup(control,before);if(!popupRoot)await new Promise(function(r){setTimeout(r,50);});}
    var opts = await waitForOptions();
	var menuOpen = (control.getAttribute && control.getAttribute('aria-expanded') === 'true') || opts.length > 0;
    if (!opts.length) {
	  return {error:'select_option: no visible role=option/menuitem nodes after opening control',
		error_code:'custom_combobox_menu_unavailable', kind:'custom', control_kind:kind,
		selector:selector, mode:mode, requested_options:requested, control_text:norm(control.textContent || ''),
		previous_value:previous, current_value:norm(control.value || control.textContent || ''),
		menu_open:menuOpen, option_available:false, recoverable:true};
    }
    var candidates = opts.filter(function(o) { return optionMatches(o, keys); });
    var matchError=matchingError(opts,keys);
    if(matchError==='ambiguous')return {error:'select_option: ambiguous option; use a unique value',error_code:'option_ambiguous'};
    if (matchError || candidates.length === 0 || candidates.length < wanted) {
	  return {error:'select_option: option not found', error_code:'custom_combobox_option_unavailable',
		kind:'custom', control_kind:kind, selector:selector, mode:mode, requested_options:requested,
		control_text:norm(control.textContent || ''), previous_value:previous,
		current_value:norm(control.value || control.textContent || ''), menu_open:menuOpen,
		option_available:false, recoverable:false, options: opts.map(function(o) {
        return {text: optionLabel(o), value: String(o.getAttribute('data-value') || o.getAttribute('value') || ''), selected: selectedLike(o), visible: true};
      })};
    }
    var multiple = popupRoot.getAttribute('aria-multiselectable')==='true' || control.getAttribute('aria-multiselectable')==='true';
    var expectedLabels = new Set(opts.filter(selectedLike).map(optionLabel));
    if(mode==='replace')expectedLabels.clear();
    for(var c of candidates){var label=optionLabel(c); if(mode==='remove'||(mode==='toggle'&&expectedLabels.has(label)))expectedLabels.delete(label);else expectedLabels.add(label);}
    if(mode==='replace' && popupRoot && multiple) {
      var remove=opts.filter(function(o){return selectedLike(o)&&!optionMatches(o,keys);});
      for(var old of remove){ if(old.getAttribute('aria-disabled')==='true')return {error:'select_option: selected option is disabled'}; eventClick(old); await new Promise(function(r){setTimeout(r,150);}); }
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
      if(c.disabled||c.getAttribute('aria-disabled')==='true')return {error:'select_option: option is disabled'};
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
    var allAfter = popupRoot && popupRoot.isConnected ? Array.from(popupRoot.querySelectorAll('[role="option"],[role="menuitem"],[role="treeitem"]')) : [];
    var selectedLabels = allAfter.filter(selectedLike).map(optionLabel);
    var hasSelectionState = allAfter.some(function(o){return o.hasAttribute('aria-selected')||o.hasAttribute('aria-checked');});
    var observed = new Set(selectedLabels);
    var verified = expectedLabels.size===observed.size && Array.from(expectedLabels).every(function(label){return observed.has(label);});
    if(!multiple&&!hasSelectionState){var displayed=lower(control.value||control.textContent||'');verified=expectedLabels.size===1&&Array.from(expectedLabels).some(function(label){return displayed===lower(label);});}
    if(!verified)return {error:'select_option: selection could not be verified after reconciliation',error_code:'selection_unverified',kind:'custom',mode:mode,matched:matched,selected:selectedLabels,option_available:true};
    return {
	  ok: true, kind: 'custom', control_kind:kind, selector:selector,
	  multiple: multiple, mode: mode, requested_options:requested,
      matched: matched, control_text: norm(control.textContent || ''),
	  previous_value:previous, current_value:norm(control.value || control.textContent || ''),
	  menu_open:(control.getAttribute && control.getAttribute('aria-expanded') === 'true') || after.length > 0,
	  option_available:true,
      selected: selectedLabels,
      options: after.map(function(o) {
        return {text: optionLabel(o), value: String(o.getAttribute('data-value') || o.getAttribute('value') || ''), selected: selectedLike(o), visible: true};
      })
    };
  }

  var targetEl = resolveTarget();
  if (!targetEl) return {error:'select_option: target not found'};
  var result = targetEl.tagName && targetEl.tagName.toLowerCase() === 'select'
    ? await nativeSelect(targetEl)
    : await customSelect(targetEl);
	if (result && !result.selector) result.selector = cssPath(targetEl);
  return result;
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
		if out.Result.ErrorMessage == "" {
			out.Result.ErrorMessage = out.Error
		}
		return out.Result, errors.New(out.Error)
	}
	if !out.OK {
		return out.Result, errors.New("select_option failed")
	}
	return out.Result, nil
}
