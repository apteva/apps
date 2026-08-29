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

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/cdproto/input"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type Options struct {
	TargetID                   string
	ExpectedText               string
	ExpectedEffect             string
	ConfirmConsequence         string
	EnforceConsequence         bool
	RequireExpectedIfDangerous bool
}

// ConsequenceError is returned before mouse dispatch when the caller's intent
// and explicit acknowledgement do not match the live target's consequence.
// Callers can use errors.As to return a structured, retry-safe rejection.
type ConsequenceError struct {
	Code               string
	Target             Target
	DetectedEffect     string
	ExpectedEffect     string
	ConfirmConsequence string
}

func (e *ConsequenceError) Error() string {
	if e == nil {
		return "click rejected: consequence guard failed"
	}
	actual := strings.TrimSpace(e.Target.AccessibleName)
	if actual == "" {
		actual = strings.TrimSpace(e.Target.Text)
	}
	switch e.Code {
	case "semantic_intent_mismatch":
		return fmt.Sprintf("click rejected: semantic_intent_mismatch: requested effect %q but live target %s is classified as %q; no action was executed", e.ExpectedEffect, describe(e.Target, actual), e.DetectedEffect)
	default:
		return fmt.Sprintf("click rejected: consequence_confirmation_required: live target %s is classified as %q; pass expected_effect=%q and confirm_consequence=%q only when that consequence is intended; no action was executed", describe(e.Target, actual), e.DetectedEffect, e.DetectedEffect, e.DetectedEffect)
	}
}

type Target struct {
	ID                string `json:"id,omitempty"`
	X                 int    `json:"x,omitempty"`
	Y                 int    `json:"y,omitempty"`
	Tag               string `json:"tag"`
	Role              string `json:"role,omitempty"`
	Text              string `json:"text,omitempty"`
	AccessibleName    string `json:"accessible_name,omitempty"`
	Disabled          bool   `json:"disabled"`
	Loading           bool   `json:"loading"`
	TargetLoading     bool   `json:"target_loading"`
	ContainerLoading  bool   `json:"container_loading"`
	PageLoadingCount  int    `json:"page_loading_indicators"`
	Stale             bool   `json:"stale,omitempty"`
	Dangerous         bool   `json:"dangerous"`
	Effect            string `json:"effect,omitempty"`
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
		result, exception, err := cdpruntime.Evaluate(inspectScript(x, y, options.TargetID)).WithReturnByValue(true).Do(ctx)
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
		dispatchX, dispatchY := x, y
		if options.TargetID != "" {
			dispatchX, dispatchY = target.X, target.Y
		}
		button := input.Left
		if err := input.DispatchMouseEvent(input.MousePressed, float64(dispatchX), float64(dispatchY)).
			WithButton(button).WithClickCount(int64(clickCount)).Do(ctx); err != nil {
			return err
		}
		return input.DispatchMouseEvent(input.MouseReleased, float64(dispatchX), float64(dispatchY)).
			WithButton(button).WithClickCount(int64(clickCount)).Do(ctx)
	}))
	return target, err
}

func Validate(target Target, options Options) error {
	actual := strings.TrimSpace(target.AccessibleName)
	if actual == "" {
		actual = strings.TrimSpace(target.Text)
	}
	if target.Stale {
		return fmt.Errorf("click rejected: stale_target: target_id %q no longer identifies the same live DOM element; take a fresh semantic screenshot", options.TargetID)
	}
	if target.TargetLoading || target.Loading {
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
	if options.EnforceConsequence {
		detected := CanonicalEffect(target.DestructiveEffect)
		expectedEffect := CanonicalEffect(options.ExpectedEffect)
		confirmation := CanonicalEffect(options.ConfirmConsequence)
		if target.Dangerous || detected != "" {
			if expectedEffect != "" && expectedEffect != detected {
				return &ConsequenceError{Code: "semantic_intent_mismatch", Target: target, DetectedEffect: detected, ExpectedEffect: expectedEffect, ConfirmConsequence: confirmation}
			}
			if expectedEffect == "" || confirmation != detected {
				return &ConsequenceError{Code: "consequence_confirmation_required", Target: target, DetectedEffect: detected, ExpectedEffect: expectedEffect, ConfirmConsequence: confirmation}
			}
		}
	}
	return nil
}

// CanonicalEffect maps the established SoM risk codes to the smaller public
// consequence vocabulary. Unknown values are normalized but preserved so a
// future detector fails closed instead of silently becoming safe.
func CanonicalEffect(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "immediate_publish", "immediate_external_commit":
		return "immediate_external_commit"
	case "schedule_publish", "scheduled_external_commit":
		return "scheduled_external_commit"
	case "immediate_send", "message_send":
		return "message_send"
	case "destructive_delete", "delete":
		return "delete"
	case "financial_action", "permission_change", "account_change":
		return normalized
	case "navigation_only", "open_configuration", "save_draft", "none", "":
		return normalized
	default:
		return normalized
	}
}

func IsConsequentialEffect(effect string) bool {
	switch CanonicalEffect(effect) {
	case "immediate_external_commit", "scheduled_external_commit", "message_send", "financial_action", "delete", "permission_change", "account_change":
		return true
	default:
		return false
	}
}

// StoreResult copies the atomic live-target observation into the action's
// optional result sink. It is intentionally tiny so every CDP backend shares
// identical consequence reporting without another DOM inspection.
func StoreResult(result *computer.ClickResult, target Target, options Options, dispatched bool) {
	if result == nil {
		return
	}
	name := strings.TrimSpace(target.AccessibleName)
	if name == "" {
		name = strings.TrimSpace(target.Text)
	}
	detected := CanonicalEffect(target.DestructiveEffect)
	*result = computer.ClickResult{
		TargetName:       name,
		TargetRole:       target.Role,
		DetectedEffect:   detected,
		LegacyEffect:     target.DestructiveEffect,
		Dangerous:        target.Dangerous || detected != "",
		Confirmed:        detected != "" && CanonicalEffect(options.ExpectedEffect) == detected && CanonicalEffect(options.ConfirmConsequence) == detected,
		ActionDispatched: dispatched,
	}
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

func inspectScript(x, y int, targetID string) string {
	targetJSON, _ := json.Marshal(targetID)
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
  var requestedID=%s, saved=null;
  if(requestedID){
    var state=window.__aptevaComputerSOM;
    saved=state&&state.targets&&state.targets[requestedID];
    if(!saved||!saved.element||!saved.element.isConnected)return {id:requestedID,tag:'unknown',stale:true,disabled:false,loading:false,target_loading:false,dangerous:false};
  }
  var leaf = saved?saved.element:deepElementFromPoint(document, %d, %d);
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
  var el = saved?saved.element:interactiveAncestor(leaf);
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
  function adjacentRowLabel(node){
    if(!node||!node.matches||!node.matches('input[type="checkbox"],input[type="radio"],[role="checkbox"],[role="radio"],[role="switch"],[aria-checked]'))return '';
    var selector='input[type="checkbox"],input[type="radio"],[role="checkbox"],[role="radio"],[role="switch"],[aria-checked]';
    for(var row=node.parentElement,depth=0;row&&depth<5;row=row.parentElement,depth++){
      var rect=row.getBoundingClientRect();if(rect.height>140||rect.width>Math.min(window.innerWidth,1200))break;
      var controls=row.querySelectorAll(selector),label=clean(row.innerText||row.textContent||'');
      if(controls.length===1&&controls[0]===node&&label&&label.length<=180)return label;
    }
    return '';
  }
  function name(node){
    var result=clean((node.getAttribute && node.getAttribute('aria-label')) || labelledBy(node) ||
      associatedLabel(node) || adjacentRowLabel(node) || (node.getAttribute && node.getAttribute('title')) || node.innerText || node.textContent ||
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
    var r=node.getBoundingClientRect(),view=node.ownerDocument&&node.ownerDocument.defaultView;
    if(r.width<2||r.height<2)return false;
    try{if(node.checkVisibility&&!node.checkVisibility({checkOpacity:true,checkVisibilityCSS:true}))return false;}catch(e){}
    for(var n=node;n&&n.nodeType===1;n=n.parentElement){var s=(view||window).getComputedStyle(n);if(s.display==='none'||s.visibility==='hidden'||parseFloat(s.opacity||'1')<0.1)return false;}
    return true;
  }
  function loadingMarker(n){
    if(!n||!n.getAttribute)return false;
    if(n.getAttribute('aria-busy')==='true'||n.getAttribute('data-loading')==='true'||n.getAttribute('data-state')==='loading')return true;
    return /(^|[-_\s])(loading|is-loading|pending)([-_\s]|$)/.test(clean(n.className).toLowerCase());
  }
  function targetLoading(node){
    if(loadingMarker(node))return true;
    var indicators=node.querySelectorAll?node.querySelectorAll('[role="progressbar"],[aria-label*="loading" i],[aria-label*="saving" i],[class*="spinner" i],[data-loading="true"]'):[];
    for(var i=0;i<indicators.length;i++) if(visible(indicators[i])) return true;
    var selector='button,input,select,textarea,[role="button"],[role="checkbox"],[role="radio"],[role="switch"],[aria-checked]';
    for(var row=node.parentElement,depth=0;row&&depth<3;row=row.parentElement,depth++){var rect=row.getBoundingClientRect();if(rect.height>140||rect.width>Math.min(window.innerWidth,1200))break;var controls=row.querySelectorAll(selector);if(controls.length!==1||controls[0]!==node)continue;if(loadingMarker(row))return true;var rowIndicators=row.querySelectorAll('[role="progressbar"],[aria-label*="loading" i],[aria-label*="saving" i],[class*="spinner" i],[data-loading="true"]');for(var j=0;j<rowIndicators.length;j++)if(visible(rowIndicators[j]))return true;break;}
    return false;
  }
  function containerLoading(node){for(var n=node&&node.parentElement;n&&n!==n.ownerDocument.documentElement;n=n.parentElement)if(loadingMarker(n))return true;return false;}
  function pageLoadingCount(){var nodes=document.querySelectorAll('[role="progressbar"],[aria-label*="loading" i],[aria-label*="saving" i],[class*="spinner" i],[data-loading="true"],[aria-busy="true"]'),count=0;for(var i=0;i<nodes.length;i++)if(visible(nodes[i]))count++;return count;}
  var disabled=!!(el.disabled||(el.matches&&el.matches(':disabled'))||(el.getAttribute&&el.getAttribute('aria-disabled')==='true')||(el.closest&&el.closest('[inert]')));
  var accessible=name(el), text=clean(el.innerText||el.textContent||'');
  var semantic=clean(accessible).toLowerCase(), effect='';
  if(/^(create|new|edit|view|open)\s+(a\s+)?post\b/.test(semantic)||/^(publish|schedule)\s+(date|time)\b/.test(semantic))effect='';
  else if(/^(schedule|confirm schedule|schedule post|schedule publication|publish later)(\b|$)/.test(semantic))effect='schedule_publish';
  else if(/^(publish|publish now|post now)(\b|$)/.test(semantic))effect='immediate_publish';
  else if(/^(delete|destroy|erase|remove permanently)(\b|$)/.test(semantic))effect='destructive_delete';
  else if(/^(send|send now)(\b|$)/.test(semantic))effect='immediate_send';
  else if(/^(pay|payout|purchase|buy|checkout|place order|withdraw)(\b|$)/.test(semantic))effect='financial_action';
  else if(/^(grant access|revoke access|change permissions|make admin|remove admin)(\b|$)/.test(semantic))effect='permission_change';
  else if(/^(deactivate account|close account|transfer account)(\b|$)/.test(semantic))effect='account_change';
  var busy=targetLoading(el),rect=el.getBoundingClientRect(),dx=rect.left+rect.width/2,dy=rect.top+rect.height/2;
  try{var view=el.ownerDocument&&el.ownerDocument.defaultView;while(view&&view!==window){var frame=view.frameElement,frameRect=frame.getBoundingClientRect();dx+=frameRect.left;dy+=frameRect.top;view=frame.ownerDocument&&frame.ownerDocument.defaultView;}}catch(e){if(saved){dx=saved.x;dy=saved.y;}}
  dx=Math.round(dx);dy=Math.round(dy);
  var semanticEffect=effect;if(!semanticEffect){if(el.isContentEditable||((el.getAttribute&&el.getAttribute('role'))==='textbox'))semanticEffect='edit_draft';else if((el.getAttribute&&((el.getAttribute('aria-haspopup'))||(el.getAttribute('aria-controls'))))||/^(free access|paid access|more options|action menu)\b/.test(semantic))semanticEffect='open_configuration';else semanticEffect='navigation_only';}
  return {id:requestedID||'',x:dx,y:dy,tag:(el.tagName||'unknown').toLowerCase(),role:(el.getAttribute&&el.getAttribute('role'))||'',text:text.slice(0,120),accessible_name:accessible,disabled:disabled,loading:busy,target_loading:busy,container_loading:containerLoading(el),page_loading_indicators:pageLoadingCount(),dangerous:effect!=='',effect:semanticEffect,destructive_effect:effect,opaque_frame:opaqueFrame};
})()`, string(targetJSON), x, y)
}
