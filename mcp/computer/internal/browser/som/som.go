// Package som implements Set-of-Mark annotation for screenshots.
//
// SoM is a grounding aid for vision-language models that aren't
// trained for pixel-precise GUI understanding. Instead of asking the
// model "what are the x,y coordinates of the login button?", we:
//
//  1. Enumerate interactive DOM elements in the viewport via read-only JS.
//  2. Paint a small colored numeric badge at the top-left of each
//     element's bounding box on the screenshot (server-side composite;
//     the page DOM is never modified).
//  3. Let the agent say "click label 7" — we look up bbox #7 and
//     dispatch the click at its center.
//
// The model reads labels (text) instead of estimating pixels. No
// coordinate guessing, no positional priors biting us.
//
// The Computer app keeps SoM enabled by default so agents can click
// visible labels instead of guessing pixel coordinates.
package som

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"strconv"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Element is one enumerated interactive target on the current page.
// Populated by running EnumScript inside the page's main world; the
// coordinates are in viewport space (same space clicks dispatch in).
type Element struct {
	ID                string                  `json:"id"`
	Label             int                     `json:"label"` // assigned by Enumerate in order
	X                 int                     `json:"x"`
	Y                 int                     `json:"y"`
	W                 int                     `json:"w"`
	H                 int                     `json:"h"`
	Tag               string                  `json:"tag"`
	Role              string                  `json:"role,omitempty"`
	Text              string                  `json:"text,omitempty"`
	AccessibleName    string                  `json:"accessible_name,omitempty"`
	Type              string                  `json:"type,omitempty"`
	Placeholder       string                  `json:"placeholder,omitempty"`
	CurrentValue      *string                 `json:"current_value,omitempty"`
	Pattern           string                  `json:"pattern,omitempty"`
	FormatHint        string                  `json:"format_hint,omitempty"`
	DateLike          bool                    `json:"date_like,omitempty"`
	Validity          *computer.FieldValidity `json:"validity,omitempty"`
	Disabled          bool                    `json:"disabled"`
	Loading           bool                    `json:"loading"`
	Dangerous         bool                    `json:"dangerous"`
	DestructiveEffect string                  `json:"destructive_effect,omitempty"`
}

// Center returns the pixel at the center of the element's bbox —
// the point a click(label=N) should dispatch to.
func (e Element) Center() (int, int) {
	return e.X + e.W/2, e.Y + e.H/2
}

// EnumScript is injected into the page's main world via
// chromedp.Evaluate. It returns a JSON array of visible interactive
// elements, capped at 50, ranked by an importance score (element
// type weight × area-tiebreaker) so the most likely click targets
// get the lowest labels.
//
// Smarts beyond a flat selector list — these matter on
// component-heavy UIs (Patreon, Notion, Linear, Twitter):
//
//	Nested-clickable dedup. <div onclick><input/></div> emits one
//	label (the input), not two — agents historically picked the
//	wrong one. Same for <button><svg/></button>: just the button.
//
//	Occlusion-aware. Modal overlays (Patreon's GDPR popup, Twitter's
//	"What's happening" toast) hide elements behind them at click-time
//	but the DOM still enumerates them. We sample
//	document.elementFromPoint at each candidate's center; if a
//	different element is on top, the candidate is hidden and we
//	drop it. The agent sees only what it can actually click.
//
//	Type-weighted ranking. Pure area-DESC put gigantic background
//	containers at label=1. Now: inputs/selects/textareas (5) >
//	buttons (4) > anchors (3) > role=button/link (2) > generic
//	onclick/tabindex (1). Area is the within-tier tiebreaker.
//
// The only page state retained is a non-enumerable window-side cache of stable
// target identities and their last non-loading accessible names. The DOM is
// never mutated, so page MutationObservers are not disturbed.
const EnumScript = `
(function() {
  var selectors = [
    'a[href]','button','input:not([type=hidden])','select','textarea',
    '[role=button]','[role=link]','[role=menuitem]','[role=tab]',
    '[role=checkbox]','[role=radio]','[role=switch]','[role=combobox]',
    '[role=option]','[role=treeitem]','[role=textbox]','[role=searchbox]',
    // Calendar widgets frequently expose days only as ARIA gridcells. These
    // are actionable semantic targets when direct masked-date entry fails.
    '[role=gridcell]',
    // contenteditable catches Slate.js / Lexical / ProseMirror /
    // TinyMCE / Quill / etc. rich-text editors. Patreon's body
    // editor in particular is a contenteditable div with no role.
    '[contenteditable=true]','[contenteditable=""]',
    // Upload libraries often use visible <a>/<span data-trigger="file-input-id">
    // controls next to a hidden input[type=file]. They are the visual target
    // agents can see; upload_file(label=N) resolves them to the hidden input.
    '[data-trigger]',
    '[onclick]','[tabindex]:not([tabindex="-1"])'
  ];
  var vw = window.innerWidth, vh = window.innerHeight;

  var somState = window.__aptevaComputerSOM;
  if (!somState) {
    somState = {names: Object.create(null)};
    try { Object.defineProperty(window, '__aptevaComputerSOM', {value: somState, configurable: true}); }
    catch (e) { window.__aptevaComputerSOM = somState; }
  }

  function clean(v) { return String(v || '').replace(/\s+/g, ' ').trim(); }

  // Prefer application-authored identity attributes, then fall back to a
  // structural path. This survives React/Vue replacing a button node while
  // remaining stable for the lifetime of the current document.
  function targetKey(el) {
    var attrs = ['id','data-testid','data-test','data-qa','name'];
    for (var ai = 0; ai < attrs.length; ai++) {
      var av = clean(el.getAttribute && el.getAttribute(attrs[ai]));
      if (av) return attrs[ai] + ':' + av;
    }
    var parts = [];
    for (var n = el; n && n.nodeType === 1 && parts.length < 12; n = n.parentElement) {
      var tag = (n.tagName || '').toLowerCase();
      var index = 1;
      for (var p = n.previousElementSibling; p; p = p.previousElementSibling) {
        if (p.tagName === n.tagName) index++;
      }
      parts.push(tag + ':nth-of-type(' + index + ')');
    }
    return 'path:' + parts.reverse().join('>');
  }
  function compactID(key) {
    var hash = 2166136261;
    var scoped = String(location.pathname || '') + '|' + key;
    for (var i = 0; i < scoped.length; i++) {
      hash ^= scoped.charCodeAt(i);
      hash = Math.imul(hash, 16777619);
    }
    return 'som_' + (hash >>> 0).toString(36);
  }
  function labelledBy(el) {
    var ids = clean(el.getAttribute && el.getAttribute('aria-labelledby'));
    if (!ids) return '';
    var doc = el.ownerDocument || document;
    return clean(ids.split(/\s+/).map(function(id) {
      var node = doc.getElementById(id);
      return node ? (node.innerText || node.textContent || '') : '';
    }).join(' '));
  }
  function associatedLabel(el) {
    if (!el) return '';
    var doc = el.ownerDocument || document;
    if (el.id) {
      try {
        var lab = doc.querySelector('label[for="' + CSS.escape(el.id) + '"]');
        if (lab) return clean(lab.innerText || lab.textContent);
      } catch (e) {}
    }
    var closest = el.closest && el.closest('label');
    return closest ? clean(closest.innerText || closest.textContent) : '';
  }
  function accessibleName(el) {
    return clean((el.getAttribute && el.getAttribute('aria-label')) || labelledBy(el) ||
      associatedLabel(el) || (el.getAttribute && el.getAttribute('title')) || el.innerText || el.textContent ||
      el.value || (el.getAttribute && el.getAttribute('alt')) ||
      (el.getAttribute && el.getAttribute('aria-placeholder')) ||
      (el.getAttribute && el.getAttribute('placeholder')) ||
      (el.getAttribute && el.getAttribute('data-placeholder')) ||
      (el.getAttribute && el.getAttribute('data-text')) || '');
  }
  function formatFromShape(shape) {
    var s = clean(shape).toLowerCase();
    if (!s) return '';
    s = s.replace(/year/g, 'yyyy').replace(/month/g, 'mm').replace(/day/g, 'dd');
    if (/y{2,4}[^a-z0-9]+m{1,2}[^a-z0-9]+d{1,2}/.test(s)) return 'yyyy-mm-dd';
    if (/m{1,2}[^a-z0-9]+d{1,2}[^a-z0-9]+y{2,4}/.test(s)) return 'mm/dd/yyyy';
    if (/d{1,2}[^a-z0-9]+m{1,2}[^a-z0-9]+y{2,4}/.test(s)) return 'dd/mm/yyyy';
    return '';
  }
  function fieldMetadata(el, name) {
    var tag = String(el.tagName || '').toLowerCase();
    if (tag !== 'input' && tag !== 'textarea') return null;
    var type = clean(el.type).toLowerCase();
    var placeholder = clean(el.getAttribute('placeholder'));
    var pattern = clean(el.getAttribute('pattern'));
    var current = String(el.value || '');
    var hint = type === 'date' ? 'yyyy-mm-dd' : (type === 'datetime-local' ? 'yyyy-mm-ddThh:mm' :
      formatFromShape(placeholder) || formatFromShape(pattern));
    if (!hint && /^\d{4}-\d{1,2}-\d{1,2}$/.test(current)) hint = 'yyyy-mm-dd';
    if (!hint) {
      var m = current.match(/^(\d{1,2})\/(\d{1,2})\/(\d{4})$/);
      if (m && Number(m[1]) > 12) hint = 'dd/mm/yyyy';
      else if (m && Number(m[2]) > 12) hint = 'mm/dd/yyyy';
    }
    var semantic = clean(name + ' ' + placeholder + ' ' + pattern).toLowerCase();
    var dateLike = type === 'date' || type === 'datetime-local' ||
      (/^(text|search|tel|)$/.test(type) && (!!hint || /\b(date|day|month|year|datum|fecha|data)\b/.test(semantic) || /[mdy]{1,4}\s*[\/.\-]\s*[mdy]{1,4}/i.test(semantic)));
    if (!dateLike) return null;
    if (!hint) {
      var lang = String(el.lang || document.documentElement.lang || navigator.language || '').toLowerCase();
      if (lang) hint = lang === 'en-us' || lang.indexOf('en-us-') === 0 ? 'mm/dd/yyyy' : 'dd/mm/yyyy';
    }
    var validity = el.validity ? {
      valid: !!el.validity.valid, bad_input: !!el.validity.badInput,
      pattern_mismatch: !!el.validity.patternMismatch, type_mismatch: !!el.validity.typeMismatch,
      range_underflow: !!el.validity.rangeUnderflow, range_overflow: !!el.validity.rangeOverflow,
      step_mismatch: !!el.validity.stepMismatch, value_missing: !!el.validity.valueMissing,
      message: clean(el.validationMessage)
    } : null;
    return {placeholder:placeholder,current_value:current,pattern:pattern,format_hint:hint,date_like:true,validity:validity};
  }
  function isVisible(el, styleWin) {
    if (!el || !el.getBoundingClientRect) return false;
    var r = el.getBoundingClientRect(), s;
    try { s = styleWin.getComputedStyle(el); } catch (e) { return false; }
    return r.width >= 2 && r.height >= 2 && s.display !== 'none' &&
      s.visibility !== 'hidden' && parseFloat(s.opacity || '1') >= 0.1;
  }
  function isLoading(el, styleWin) {
    for (var node = el; node && node !== node.ownerDocument.documentElement; node = node.parentElement) {
      if (node.getAttribute && (node.getAttribute('aria-busy') === 'true' ||
          node.getAttribute('data-loading') === 'true' || node.getAttribute('data-state') === 'loading')) return true;
      var cls = clean(node.className).toLowerCase();
      if (/(^|[-_\s])(loading|is-loading|pending)([-_\s]|$)/.test(cls)) return true;
    }
    var indicators = el.querySelectorAll ? el.querySelectorAll('[role="progressbar"],[aria-label*="loading" i],[aria-label*="saving" i],[class*="spinner" i],[data-loading="true"]') : [];
    for (var i = 0; i < indicators.length; i++) if (isVisible(indicators[i], styleWin)) return true;
    return false;
  }
  function destructiveEffect(name, text, tag, role, el) {
    var inputType = clean(el && el.type).toLowerCase();
    var actionable = tag === 'button' || tag === 'a' || role === 'button' || role === 'menuitem' || role === 'link' ||
      (tag === 'input' && (inputType === 'button' || inputType === 'submit' || inputType === 'reset'));
    // Content surfaces may legitimately be named "Post body", "Send a
    // message", etc. Risk describes activating a consequential control, not
    // editing a draft, so editable targets are never classified here.
    if (!actionable || (el && el.isContentEditable) || role === 'textbox' || role === 'searchbox') return '';
    var semantic = clean(name + ' ' + text).toLowerCase();
	    if (/\b(set|choose|select|edit|change)\s+(the\s+)?(publish\s+)?(date|time)\b/.test(semantic)) return '';
	    if (/\b(schedule (post|publication|publish)|confirm schedule|publish later)\b/.test(semantic) || /^schedule(?:\s+schedule)?$/.test(semantic)) return 'schedule_publish';
	    if (/\bpublish\b/.test(semantic)) return 'immediate_publish';
	    if (/\b(delete|destroy|erase)\b/.test(semantic)) return 'destructive_delete';
	    if (/\b(send|post)\b/.test(semantic)) return 'immediate_send';
	    if (/\b(pay|payout|purchase|buy|checkout|place order)\b/.test(semantic)) return 'financial_action';
	    if (/\b(withdraw|withdrawal)\b/.test(semantic)) return 'financial_action';
	    if (/\b(grant access|revoke access|change permissions|make admin|remove admin)\b/.test(semantic)) return 'permission_change';
	    if (/\b(deactivate account|close account|transfer account)\b/.test(semantic)) return 'account_change';
    return '';
  }

  // priority: lower number = wrapper / generic, higher = real input
  // element. Used for both sort ranking and contains-dedup.
  function priority(tag, role, el) {
    // contenteditable counts as a top-tier text input — it's the
    // body of rich editors (Slate/Lexical/ProseMirror).
    if (el && el.isContentEditable) return 5;
    if (tag === 'input' || tag === 'textarea' || tag === 'select') return 5;
    if (role === 'textbox' || role === 'searchbox') return 5;
    if (tag === 'button') return 4;
    if (tag === 'a') return 3;
    if (role === 'button' || role === 'link' || role === 'menuitem' ||
        role === 'tab' || role === 'checkbox' || role === 'radio' ||
        role === 'switch' || role === 'combobox' || role === 'option' ||
        role === 'treeitem' || role === 'gridcell') return 2;
    return 1; // bare onclick / tabindex
  }

  // ─── Pass 1: gather all visible candidates ───────────────────
  // Walks: main document + every same-origin iframe + open shadow
  // roots reachable from the main document. Cookie banners
  // (Cookiebot, OneTrust, Patreon's own banner) frequently render
  // inside iframes/shadow trees; without this walk their buttons
  // are invisible to the agent.
  var candidates = [];
  var seen = new WeakSet();

  // gatherFrom — collect candidates from a Document or ShadowRoot.
  // Coordinates returned by getBoundingClientRect on elements
  // INSIDE a same-origin iframe are LOCAL to that iframe's
  // viewport; offsetX/offsetY translate them into main-viewport
  // pixels so the agent's click coordinates map correctly.
  // styleWin is the window scope used for getComputedStyle —
  // matters because the iframe's own window has its own CSSOM.
  function gatherFrom(root, offsetX, offsetY, styleWin) {
    for (var si = 0; si < selectors.length; si++) {
      var els;
      try { els = root.querySelectorAll(selectors[si]); } catch (e) { continue; }
      for (var ei = 0; ei < els.length; ei++) {
        var el = els[ei];
        if (seen.has(el)) continue;
        seen.add(el);
        var r;
        try { r = el.getBoundingClientRect(); } catch (e) { continue; }
        if (r.width < 4 || r.height < 4) continue;
        // Cull post-translation against main viewport.
        var rLeft = r.left + offsetX;
        var rTop = r.top + offsetY;
        var rRight = r.right + offsetX;
        var rBottom = r.bottom + offsetY;
        if (rRight <= 0 || rBottom <= 0) continue;
        if (rLeft >= vw || rTop >= vh) continue;
        var style;
        try { style = styleWin.getComputedStyle(el); } catch (e) { continue; }
        if (style.visibility === 'hidden' || style.display === 'none') continue;
        if (parseFloat(style.opacity) < 0.1) continue;
        var x = Math.max(0, Math.round(rLeft));
        var y = Math.max(0, Math.round(rTop));
        var w = Math.min(vw, Math.round(rRight)) - x;
        var h = Math.min(vh, Math.round(rBottom)) - y;
        // Fractional rectangles can pass the raw visibility checks above but
        // round down to a zero-sized viewport intersection. Do not spend SOM
        // labels on controls that have no usable click area after clipping.
        if (w < 4 || h < 4) continue;
        var key = targetKey(el);
        var stableID = compactID(key);
        var name = accessibleName(el);
        var text = (el.innerText || el.value || name ||
                    el.getAttribute('aria-placeholder') ||
                    el.getAttribute('placeholder') ||
                    el.getAttribute('data-placeholder') ||
                    el.getAttribute('data-text') ||
                    '').trim();
        // Rich-text editors (Slate.js, Lexical, ProseMirror) render
        // their placeholder via a CSS ::before pseudo-element instead
        // of any DOM attribute — el.innerText is empty until the
        // user types. Read the computed pseudo-element content so the
        // agent sees "Start writing..." / "Type here" / etc. on the
        // body-editor label and can recognise it as a textbox.
        if (!text && (el.isContentEditable || (el.getAttribute && el.getAttribute('role') === 'textbox'))) {
          try {
            var pseudo = styleWin.getComputedStyle(el, '::before');
            var content = pseudo && pseudo.content;
            if (content && content !== 'none' && content !== 'normal' && content !== '""' && content !== "''") {
              // CSS content values are quoted ("Start writing…"). Strip
              // the surrounding quotes; ignore counter/var() shapes.
              var stripped = content.replace(/^attr\(.+\)$/, '');
              if (/^["'][\s\S]*["']$/.test(stripped)) {
                text = stripped.slice(1, -1).trim();
              }
            }
          } catch (e) { /* cross-origin or detached */ }
        }
        if (text.length > 40) text = text.substr(0, 40);
        var tag = el.tagName.toLowerCase();
        var role = el.getAttribute('role') || '';
        var field = fieldMetadata(el, name);
        var disabled = !!(el.disabled || (el.matches && el.matches(':disabled')) ||
          el.getAttribute('aria-disabled') === 'true' || (el.closest && el.closest('[inert]')));
        var loading = isLoading(el, styleWin);
		// Spinners commonly replace the visible text while autosave is active.
		// Preserve the last stable semantic name for the same logical control.
		if (loading && !name && somState.names[stableID]) name = somState.names[stableID];
		if (!loading && name) somState.names[stableID] = name;
        var effect = destructiveEffect(name, text, tag, role, el);
        candidates.push({
          el: el, id: stableID, x: x, y: y, w: w, h: h,
          tag: tag, role: role, text: text, accessible_name: name,
          type: el.type || '',
          placeholder: field ? field.placeholder : '', current_value: field ? field.current_value : null,
          pattern: field ? field.pattern : '', format_hint: field ? field.format_hint : '',
          date_like: !!field, validity: field ? field.validity : null,
          disabled: disabled, loading: loading, dangerous: effect !== '', destructive_effect: effect,
          prio: priority(tag, role, el)
        });
      }
    }
  }

  // Main document.
  gatherFrom(document, 0, 0, window);

  // Same-origin iframes. Cross-origin throws on contentDocument
  // access — we silently skip those (and label the iframe element
  // itself if it matched a selector, which it doesn't by default;
  // future improvement: add iframe to selector list as a fallback).
  var iframes = document.querySelectorAll('iframe');
  for (var fi = 0; fi < iframes.length; fi++) {
    var ifr = iframes[fi];
    var ifrRect;
    try { ifrRect = ifr.getBoundingClientRect(); } catch (e) { continue; }
    if (ifrRect.width < 4 || ifrRect.height < 4) continue;
    if (ifrRect.right <= 0 || ifrRect.bottom <= 0) continue;
    if (ifrRect.left >= vw || ifrRect.top >= vh) continue;
    var doc, win;
    try {
      doc = ifr.contentDocument;
      win = ifr.contentWindow;
    } catch (e) { continue; }
    if (!doc || !win) continue;
    gatherFrom(doc, ifrRect.left, ifrRect.top, win);
  }

  // Open shadow roots reachable from the main document. Closed
  // shadow roots are inaccessible by design — those stay invisible.
  // Coordinates are in main-viewport space (shadow DOM renders
  // within the host's box), so no offset translation needed.
  var hosts = document.querySelectorAll('*');
  for (var hi = 0; hi < hosts.length; hi++) {
    var host = hosts[hi];
    var sr;
    try { sr = host.shadowRoot; } catch (e) { continue; }
    if (!sr) continue;
    gatherFrom(sr, 0, 0, window);
  }

  // ─── Pass 1.5: modal-aware suppression ───────────────────────
  // When a modal/dialog is open, the page-behind-it is visually
  // covered but the DOM still enumerates it. Sidebar buttons and
  // background controls are technically still clickable (a click
  // there closes most modals via outside-click) but they're NEVER
  // the right next action for an agent navigating the modal flow.
  //
  // Concrete bug we hit: agent opening Patreon's video-embed
  // dialog typed the URL into a sidebar's "Paid access" radio
  // (which has the same input-tier badge color) instead of the
  // dialog's URL field, because both labels were equally available
  // and the dialog one wasn't visually privileged.
  //
  // Detection: look for an explicit dialog container. If found,
  // drop candidates whose center isn't inside its bbox. Heuristics:
  //   1. [role=dialog] — the canonical signal
  //   2. [aria-modal="true"] — same intent, different attr
  //   3. <dialog open> — native HTML dialog element
  //
  // We deliberately do NOT use a generic "fixed-position big-box"
  // heuristic — it false-positives on toolbars, footers, sidebars.
  // If the page doesn't expose a real dialog role, we leave the
  // map untouched (agents still recover via skill / coordinate
  // fallback).
  function findActiveModal() {
    var candidates = [];
    var nodes = document.querySelectorAll('[role=dialog],[aria-modal="true"],dialog[open]');
    for (var i = 0; i < nodes.length; i++) {
      var n = nodes[i];
      var r = n.getBoundingClientRect();
      // Visible + non-trivial size + on-screen
      if (r.width < 100 || r.height < 80) continue;
      if (r.right <= 0 || r.bottom <= 0 || r.left >= vw || r.top >= vh) continue;
      var s = window.getComputedStyle(n);
      if (s.visibility === 'hidden' || s.display === 'none' || parseFloat(s.opacity) < 0.1) continue;
      // Some sites use role=dialog for persistent page regions. Amazon's
      // desktop search filters are one example: the sidebar is a 7,000px-tall
      // dialog whose center is far below the viewport. Treat aria-modal and
      // native dialogs as authoritative, but require a plain role=dialog to
      // be centered in the viewport before suppressing the rest of the page.
      var strongModal = n.getAttribute('aria-modal') === 'true' ||
                        (n.tagName.toLowerCase() === 'dialog' && n.hasAttribute('open'));
      if (!strongModal) {
        var centerX = r.left + r.width / 2;
        var centerY = r.top + r.height / 2;
        if (centerX < 0 || centerX > vw || centerY < 0 || centerY > vh) continue;
      }
      candidates.push({el: n, rect: r, area: r.width * r.height});
    }
    if (candidates.length === 0) return null;
    // Multiple modals stacked? Prefer the one with HIGHEST z-index
    // (last in DOM order is also a fine tiebreaker; CSS painters use
    // both).
    candidates.sort(function(a, b){
      var za = parseInt(window.getComputedStyle(a.el).zIndex, 10) || 0;
      var zb = parseInt(window.getComputedStyle(b.el).zIndex, 10) || 0;
      return zb - za;
    });
    return candidates[0];
  }
  var activeModal = findActiveModal();
  if (activeModal) {
    var mb = activeModal.rect;
    candidates = candidates.filter(function(c) {
      // DOM containment is the reliable ownership signal. Geometry alone is
      // unsafe: large dialogs often overlap page controls beneath them, and
      // those obscured controls must not receive labels merely because their
      // centers fall inside the dialog rectangle.
      return activeModal.el.contains(c.el);
    });
  }

  // ─── Pass 2: nested-clickable dedup ──────────────────────────
  // Drop a candidate if it CONTAINS another candidate of equal or
  // higher priority (the contained one is the more specific target,
  // so the wrapper is redundant). Also drop a candidate if it is
  // CONTAINED in another candidate of strictly higher priority (the
  // outer is the real target; the inner is decorative — e.g. a
  // tabindex span inside a button).
  var keep = [];
  for (var i = 0; i < candidates.length; i++) {
    var ci = candidates[i];
    var dominated = false;
    for (var j = 0; j < candidates.length && !dominated; j++) {
      if (i === j) continue;
      var cj = candidates[j];
      if (ci.el.contains(cj.el) && cj.prio >= ci.prio) {
        dominated = true; break;  // ci is a wrapper
      }
      if (cj.el.contains(ci.el) && cj.prio > ci.prio) {
        dominated = true; break;  // ci is decorative inside a stronger target
      }
    }
    if (!dominated) keep.push(ci);
  }

  // ─── Pass 3: occlusion check (lenient — false positives hurt) ─
  // Modal overlays cover elements; the DOM still lists them, but
  // they're not clickable. We sample elementFromPoint at three
  // points along the candidate's horizontal centerline.
  //
  // CRITICAL: cost asymmetry. A false-positive (pruning a real
  // clickable) is much worse than a false-negative (keeping an
  // un-clickable one). The agent loops and gets stuck on the first;
  // recovers by trying another label on the second. So this check
  // is intentionally LENIENT.
  //
  // We only prune a candidate when the topmost element at its
  // center sample IS ITSELF a meaningful interactive (button,
  // input, [role=button], onclick handler, etc.) AND is not the
  // candidate's ancestor/descendant. A non-interactive wrapper
  // div (decorative dimmer, layout container) lets the candidate
  // through — clicks reach the candidate via pointer-events
  // bubbling in most cases. We bias toward labeling, not toward
  // pruning.
  function isUsefulInteractive(el) {
    if (!el) return false;
    var t = el.tagName;
    if (t === 'A' || t === 'BUTTON' || t === 'INPUT' ||
        t === 'TEXTAREA' || t === 'SELECT') return true;
    if (el.getAttribute('role')) return true;
    if (el.hasAttribute('onclick') || el.hasAttribute('data-trigger')) return true;
    var ti = el.getAttribute('tabindex');
    if (ti !== null && ti !== '-1') return true;
    return false;
  }
  var visible = [];
  for (var i = 0; i < keep.length; i++) {
    var c = keep[i];
    var probes = [
      [c.x + c.w / 2, c.y + c.h / 2],
      [c.x + Math.max(2, Math.min(c.w - 2, c.w * 0.25)), c.y + c.h / 2],
      [c.x + Math.max(2, Math.min(c.w - 2, c.w * 0.75)), c.y + c.h / 2]
    ];
    var pruned = false;
    for (var p = 0; p < probes.length && !pruned; p++) {
      var px = probes[p][0], py = probes[p][1];
      if (px < 0 || py < 0 || px >= vw || py >= vh) continue;
      var top = document.elementFromPoint(px, py);
      if (!top) continue;
      // Topmost relates to the candidate (self/descendant/ancestor)
      // → not occluded.
      if (top === c.el || c.el.contains(top) || top.contains(c.el)) {
        continue;
      }
      // Topmost is unrelated. Only prune if it's a real interactive
      // — otherwise treat as decorative pass-through and KEEP the
      // candidate.
      if (isUsefulInteractive(top)) {
        pruned = true;
      }
    }
    if (!pruned) visible.push(c);
  }

  // ─── Pass 4: rank, cap, label ────────────────────────────────
  // Score = priority × big-multiplier + log(area). Priority dominates
  // strictly; area is the tiebreaker so same-tier elements stay in
  // a sensible order (Publish button beats hidden secondary actions).
  function safetyPriority(c) {
    if (c.dangerous) return 3;
    if (c.loading) return 2;
    if (c.disabled) return 1;
    return 0;
  }
  function score(c) { return c.prio * 1e6 + Math.sqrt(c.w * c.h); }
  var safety = visible.filter(function(c) { return safetyPriority(c) > 0; });
  var ordinary = visible.filter(function(c) { return safetyPriority(c) === 0; });
  safety.sort(function(a, b) {
    var d = safetyPriority(b) - safetyPriority(a);
    return d || score(b) - score(a);
  });
  ordinary.sort(function(a, b) { return score(b) - score(a); });
  // Safety controls are never lost to the ordinary 50-target budget. If a
  // pathological page has >50 safety controls, retain them all.
  visible = safety.concat(ordinary.slice(0, Math.max(0, 50 - safety.length)));

  // Strip the el reference (not JSON-encodable + serializing DOM
  // nodes hangs chromedp.Evaluate) and assign final labels.
  var out = [];
  for (var k = 0; k < visible.length; k++) {
    var c = visible[k];
    out.push({
      id: c.id, x: c.x, y: c.y, w: c.w, h: c.h,
      tag: c.tag, role: c.role, text: c.text, accessible_name: c.accessible_name, type: c.type,
      placeholder: c.placeholder, current_value: c.current_value, pattern: c.pattern,
      format_hint: c.format_hint, date_like: c.date_like, validity: c.validity,
      disabled: c.disabled, loading: c.loading, dangerous: c.dangerous, destructive_effect: c.destructive_effect,
      label: k + 1
    });
  }
  return out;
})()
`

// Color by element family. Matches the tool-def description so the
// agent knows what each color means. Colors chosen for contrast
// against typical page backgrounds and readability in JPEG q60.
var (
	colorLink   = color.RGBA{R: 59, G: 130, B: 246, A: 255}  // blue  — <a>
	colorButton = color.RGBA{R: 34, G: 197, B: 94, A: 255}   // green — <button>, [role=button]
	colorInput  = color.RGBA{R: 249, G: 115, B: 22, A: 255}  // orange — <input>, <textarea>, <select>
	colorOther  = color.RGBA{R: 107, G: 114, B: 128, A: 255} // gray  — generic [onclick] / [tabindex]
	colorBorder = color.RGBA{R: 255, G: 255, B: 255, A: 255} // white badge border for contrast
	colorText   = color.White
)

func ColorFor(e Element) color.RGBA {
	switch e.Tag {
	case "a":
		return colorLink
	case "button":
		return colorButton
	case "input", "textarea", "select":
		return colorInput
	}
	if e.Role == "button" || e.Role == "switch" || e.Role == "checkbox" || e.Role == "radio" {
		return colorButton
	}
	if e.Role == "link" {
		return colorLink
	}
	if e.Role == "combobox" || e.Role == "option" {
		return colorInput
	}
	return colorOther
}

// Annotate composites SoM badges onto a raw screenshot. Accepts JPEG
// or PNG; returns the same format, same dimensions. Badges are small
// filled rects at each element's top-left corner with the label
// number in white. If APTEVA_COMPUTER_SOM_BOX is set, a 1-px outline is also
// drawn around each element's full bbox (helps the model associate
// label with region, at the cost of visual noise).
func Annotate(raw []byte, elements []Element) ([]byte, error) {
	src, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("som: decode: %w", err)
	}
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)

	drawBox := os.Getenv("APTEVA_COMPUTER_SOM_BOX") != ""

	for _, e := range elements {
		col := ColorFor(e)
		drawBadge(dst, e, col)
		if drawBox {
			drawOutline(dst, e, col)
		}
	}

	var out bytes.Buffer
	switch format {
	case "png":
		err = png.Encode(&out, dst)
	default:
		err = jpeg.Encode(&out, dst, &jpeg.Options{Quality: 75})
	}
	if err != nil {
		return nil, fmt.Errorf("som: encode: %w", err)
	}
	return out.Bytes(), nil
}

// drawBadge paints one numeric badge at element (e.X, e.Y). Badge
// size depends on label digit count so two-digit labels stay readable.
// If the badge would fall off the viewport's top/left, it's nudged
// inward so it stays visible.
func drawBadge(dst *image.RGBA, e Element, col color.RGBA) {
	label := strconv.Itoa(e.Label)
	// 7x13 basic font. Badge = label_width + 8 horizontal padding,
	// 16 tall. One or two digits fits 14+8=22 or 7+8=15 wide.
	bw := len(label)*7 + 8
	bh := 16
	bx, by := e.X, e.Y
	// Nudge to stay inside the destination.
	if bx < 0 {
		bx = 0
	}
	if by < 0 {
		by = 0
	}
	maxX := dst.Bounds().Dx()
	maxY := dst.Bounds().Dy()
	if bx+bw > maxX {
		bx = maxX - bw
	}
	if by+bh > maxY {
		by = maxY - bh
	}

	// White 1-px border for contrast against any background.
	border := image.Rect(bx-1, by-1, bx+bw+1, by+bh+1)
	draw.Draw(dst, border, &image.Uniform{colorBorder}, image.Point{}, draw.Src)
	// Filled rect.
	fill := image.Rect(bx, by, bx+bw, by+bh)
	draw.Draw(dst, fill, &image.Uniform{col}, image.Point{}, draw.Src)
	// Label text, white, centered.
	face := basicfont.Face7x13
	tx := bx + (bw-len(label)*7)/2
	ty := by + 12 // baseline for 13-px font in 16-px box
	(&font.Drawer{
		Dst:  dst,
		Src:  &image.Uniform{colorText},
		Face: face,
		Dot:  fixed.P(tx, ty),
	}).DrawString(label)
}

// drawOutline strokes a 1-px rectangle around the element's full bbox.
// Helps the model associate label with region. Opt-in via APTEVA_COMPUTER_SOM_BOX.
func drawOutline(dst *image.RGBA, e Element, col color.RGBA) {
	x0, y0 := e.X, e.Y
	x1, y1 := e.X+e.W, e.Y+e.H
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > dst.Bounds().Dx() {
		x1 = dst.Bounds().Dx()
	}
	if y1 > dst.Bounds().Dy() {
		y1 = dst.Bounds().Dy()
	}
	// Top + bottom lines
	draw.Draw(dst, image.Rect(x0, y0, x1, y0+1), &image.Uniform{col}, image.Point{}, draw.Src)
	draw.Draw(dst, image.Rect(x0, y1-1, x1, y1), &image.Uniform{col}, image.Point{}, draw.Src)
	// Left + right lines
	draw.Draw(dst, image.Rect(x0, y0, x0+1, y1), &image.Uniform{col}, image.Point{}, draw.Src)
	draw.Draw(dst, image.Rect(x1-1, y0, x1, y1), &image.Uniform{col}, image.Point{}, draw.Src)
}

// UnmarshalElements parses the JSON array returned by EnumScript.
// Separate from Evaluate-call site so we can unit-test it.
func UnmarshalElements(data []byte) ([]Element, error) {
	var els []Element
	if err := json.Unmarshal(data, &els); err != nil {
		return nil, err
	}
	return els, nil
}

// Enabled is the app-level SoM gate. The Computer app always annotates
// screenshots so every agent sees the same label-first interaction surface.
func Enabled() bool {
	return true
}
