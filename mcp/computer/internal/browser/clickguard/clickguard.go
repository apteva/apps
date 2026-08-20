// Package clickguard validates the live DOM target immediately before a real
// CDP mouse event. It prevents stale-label clicks, clicks on loading/disabled
// controls, and unconfirmed raw-coordinate clicks on consequential actions.
package clickguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/input"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type Options struct {
	ExpectedText               string
	RequireExpectedIfDangerous bool
}

type Target struct {
	Tag               string `json:"tag"`
	Role              string `json:"role,omitempty"`
	Text              string `json:"text,omitempty"`
	AccessibleName    string `json:"accessible_name,omitempty"`
	Disabled          bool   `json:"disabled"`
	Loading           bool   `json:"loading"`
	Dangerous         bool   `json:"dangerous"`
	DestructiveEffect string `json:"destructive_effect,omitempty"`
	OpaqueFrame       bool   `json:"opaque_frame,omitempty"`
}

// Click performs validation and mouse dispatch inside one chromedp action.
// No page task or screenshot can run between the live hit-test and dispatch.
func Click(ctx context.Context, x, y, clickCount int, options Options) (Target, error) {
	if clickCount <= 0 {
		clickCount = 1
	}
	var target Target
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		result, exception, err := cdpruntime.Evaluate(inspectScript(x, y)).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return fmt.Errorf("inspect click target: %w", err)
		}
		if exception != nil {
			return fmt.Errorf("inspect click target: %s", exception.Text)
		}
		if result == nil || len(result.Value) == 0 {
			return errors.New("click target inspection returned no result")
		}
		if err := json.Unmarshal(result.Value, &target); err != nil {
			return fmt.Errorf("decode click target: %w", err)
		}
		if err := Validate(target, options); err != nil {
			return err
		}
		button := input.Left
		if err := input.DispatchMouseEvent(input.MousePressed, float64(x), float64(y)).
			WithButton(button).WithClickCount(int64(clickCount)).Do(ctx); err != nil {
			return err
		}
		return input.DispatchMouseEvent(input.MouseReleased, float64(x), float64(y)).
			WithButton(button).WithClickCount(int64(clickCount)).Do(ctx)
	}))
	return target, err
}

func Validate(target Target, options Options) error {
	actual := strings.TrimSpace(target.AccessibleName)
	if actual == "" {
		actual = strings.TrimSpace(target.Text)
	}
	if target.Loading {
		return fmt.Errorf("click rejected: target %s is loading; wait for a stable screenshot", describe(target, actual))
	}
	if target.Disabled {
		return fmt.Errorf("click rejected: target %s is disabled", describe(target, actual))
	}
	expected := normalize(options.ExpectedText)
	if target.OpaqueFrame {
		if options.RequireExpectedIfDangerous {
			return fmt.Errorf("click rejected: raw coordinate lands inside a cross-origin frame whose live semantics cannot be verified; use a current Set-of-Mark label")
		}
		// A label carries the current cross-frame AX/SoM target. Browser script
		// isolation prevents inspecting that element from the top frame, so keep
		// existing label behavior instead of falsely reporting a name mismatch.
		return nil
	}
	if options.RequireExpectedIfDangerous && target.Dangerous && expected == "" {
		return fmt.Errorf("click rejected: raw coordinate resolves to consequential target %s (%s); pass expected_text with its exact accessible name", describe(target, actual), target.DestructiveEffect)
	}
	if expected != "" && normalize(actual) != expected {
		return fmt.Errorf("click rejected: expected target %q but live target is %s", strings.TrimSpace(options.ExpectedText), describe(target, actual))
	}
	return nil
}

func normalize(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func describe(target Target, name string) string {
	if name != "" {
		return fmt.Sprintf("%q", name)
	}
	if target.Role != "" {
		return fmt.Sprintf("<%s role=%s> without an accessible name", target.Tag, target.Role)
	}
	return fmt.Sprintf("<%s> without an accessible name", target.Tag)
}

func inspectScript(x, y int) string {
	return fmt.Sprintf(`(function(){
  var opaqueFrame=false;
  function deepElementFromPoint(doc, px, py) {
    var leaf;
    try { leaf = doc.elementFromPoint(px, py); } catch (e) { return null; }
    if (!leaf) return null;
    for (var depth=0; depth<12; depth++) {
      if (leaf.shadowRoot && leaf.shadowRoot.elementFromPoint) {
        var shadowLeaf=leaf.shadowRoot.elementFromPoint(px, py);
        if (shadowLeaf && shadowLeaf!==leaf) { leaf=shadowLeaf; continue; }
      }
      if ((leaf.tagName||'').toLowerCase()==='iframe') {
        try {
          var frameRect=leaf.getBoundingClientRect(),frameDoc=leaf.contentDocument;
          if (frameDoc) {
            var frameLeaf=deepElementFromPoint(frameDoc,px-frameRect.left,py-frameRect.top);
            if (frameLeaf) return frameLeaf;
          } else opaqueFrame=true;
        } catch (e) { opaqueFrame=true; }
      }
      break;
    }
    return leaf;
  }
  var leaf = deepElementFromPoint(document, %d, %d);
  if (!leaf) return {tag:'unknown',disabled:false,loading:false,dangerous:false};
  var interactive = 'button,a[href],input,select,textarea,[role="button"],[role="link"],[role="menuitem"],[role="tab"],[role="checkbox"],[role="radio"],[role="switch"],[role="gridcell"],[onclick],[tabindex]:not([tabindex="-1"])';
  function interactiveAncestor(node) {
    for (var current=node; current;) {
      if (current.matches && current.matches(interactive)) return current;
      if (current.parentElement) { current=current.parentElement; continue; }
      var root=current.getRootNode&&current.getRootNode();
      current=root&&root.host ? root.host : null;
    }
    return node;
  }
  var el = interactiveAncestor(leaf);
  function clean(v){ return String(v || '').replace(/\s+/g,' ').trim(); }
  function labelledBy(node){
    var ids = clean(node.getAttribute && node.getAttribute('aria-labelledby'));
    if (!ids) return '';
    var doc=node.ownerDocument||document;
    return clean(ids.split(/\s+/).map(function(id){var n=doc.getElementById(id);return n?(n.innerText||n.textContent||''):'';}).join(' '));
  }
  function associatedLabel(node){
    if(!node)return '';
    var doc=node.ownerDocument||document;
    if(node.id){
      try{var lab=doc.querySelector('label[for="'+CSS.escape(node.id)+'"]');if(lab)return clean(lab.innerText||lab.textContent);}
      catch(e){}
    }
    var closest=node.closest&&node.closest('label');
    return closest?clean(closest.innerText||closest.textContent):'';
  }
  function name(node){
    var result=clean((node.getAttribute && node.getAttribute('aria-label')) || labelledBy(node) ||
      associatedLabel(node) || (node.getAttribute && node.getAttribute('title')) || node.innerText || node.textContent ||
      node.value || (node.getAttribute && node.getAttribute('alt')) ||
      (node.getAttribute && node.getAttribute('aria-placeholder')) ||
      (node.getAttribute && node.getAttribute('placeholder')) ||
      (node.getAttribute && node.getAttribute('data-placeholder')) ||
      (node.getAttribute && node.getAttribute('data-text')) || '');
    if (!result && (node.isContentEditable || (node.getAttribute&&node.getAttribute('role')==='textbox'))) {
      try {
        var view=node.ownerDocument&&node.ownerDocument.defaultView,pseudo=(view||window).getComputedStyle(node,'::before'),content=pseudo&&pseudo.content;
        if (content&&content!=='none'&&content!=='normal'&&content!=='""'&&content!=="''"&&!/^attr\(.+\)$/.test(content)) {
          result=clean(/^['"][\s\S]*['"]$/.test(content)?content.slice(1,-1):content);
        }
      } catch(e) {}
    }
    return result;
  }
  function visible(node){
    if (!node || !node.getBoundingClientRect) return false;
    var r=node.getBoundingClientRect(),view=node.ownerDocument&&node.ownerDocument.defaultView,s=(view||window).getComputedStyle(node);
    return r.width>=2&&r.height>=2&&s.display!=='none'&&s.visibility!=='hidden'&&parseFloat(s.opacity||'1')>=0.1;
  }
  function loading(node){
    for (var n=node;n&&n!==document.documentElement;n=n.parentElement) {
      if (n.getAttribute && (n.getAttribute('aria-busy')==='true'||n.getAttribute('data-loading')==='true'||n.getAttribute('data-state')==='loading')) return true;
      var cls=clean(n.className).toLowerCase();
      if (/(^|[-_\s])(loading|is-loading|pending)([-_\s]|$)/.test(cls)) return true;
    }
    var indicators=node.querySelectorAll?node.querySelectorAll('[role="progressbar"],[aria-label*="loading" i],[aria-label*="saving" i],[class*="spinner" i],[data-loading="true"]'):[];
    for(var i=0;i<indicators.length;i++) if(visible(indicators[i])) return true;
    return false;
  }
  var disabled=!!(el.disabled||(el.matches&&el.matches(':disabled'))||(el.getAttribute&&el.getAttribute('aria-disabled')==='true')||(el.closest&&el.closest('[inert]')));
  var accessible=name(el), text=clean(el.innerText||el.textContent||'');
  var semantic=clean(accessible+' '+text).toLowerCase(), effect='';
  if(/\bpublish\b/.test(semantic)) effect='immediate_publish';
  else if(/\b(delete|destroy|erase)\b/.test(semantic)) effect='destructive_delete';
  else if(/\b(send|post)\b/.test(semantic)) effect='immediate_send';
  else if(/\b(pay|payout|purchase|buy|checkout|place order)\b/.test(semantic)) effect='financial_action';
  return {tag:(el.tagName||'unknown').toLowerCase(),role:(el.getAttribute&&el.getAttribute('role'))||'',text:text.slice(0,120),accessible_name:accessible.slice(0,120),disabled:disabled,loading:loading(el),dangerous:effect!=='',destructive_effect:effect,opaque_frame:opaqueFrame};
})()`, x, y)
}
