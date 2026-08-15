// Package stability tracks page mutations, navigation and finite network
// activity for the lifetime of a CDP target.
package stability

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type Result = computer.WaitResult

// TimeoutError carries the last observed browser state. Callers that need the
// historical error contract can keep treating it as an error; the MCP layer
// unwraps it into a nonfatal timed_out result so it never implies that an
// earlier click or edit was rolled back.
type TimeoutError struct {
	Kind   string
	Result Result
}

func (e *TimeoutError) Error() string {
	if e == nil {
		return "browser wait timed out"
	}
	if e.Kind == "outcome" {
		return fmt.Sprintf("browser outcome was not observed within %s", time.Duration(e.Result.WaitedMS)*time.Millisecond)
	}
	return fmt.Sprintf("page did not stabilize within %s (%d loading indicators, %d requests, %d frames remain)",
		time.Duration(e.Result.WaitedMS)*time.Millisecond, e.Result.LoadingIndicators, e.Result.InflightRequests, e.Result.LoadingFrames)
}

type Tracker struct {
	ctx          context.Context
	mu           sync.Mutex
	inflight     map[network.RequestID]struct{}
	frames       map[cdp.FrameID]struct{}
	lastActivity time.Time
}

// New attaches before navigation whenever possible, so requests already in
// flight when wait_for_stable is called are still represented.
func New(ctx context.Context) (*Tracker, error) {
	t := &Tracker{ctx: ctx, inflight: make(map[network.RequestID]struct{}), frames: make(map[cdp.FrameID]struct{}), lastActivity: time.Now()}
	chromedp.ListenTarget(ctx, t.observe)
	err := chromedp.Run(ctx, network.Enable(), page.Enable(), chromedp.ActionFunc(func(ctx context.Context) error {
		if _, err := page.AddScriptToEvaluateOnNewDocument(installScript).Do(ctx); err != nil {
			return err
		}
		_, exception, err := cdpruntime.Evaluate(installScript).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return fmt.Errorf("install stability observer: %s", exception.Text)
		}
		return nil
	}))
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Tracker) observe(event any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	switch ev := event.(type) {
	case *network.EventRequestWillBeSent:
		if ev.Type == network.ResourceTypeWebSocket || ev.Type == network.ResourceTypeEventSource {
			return
		}
		t.inflight[ev.RequestID] = struct{}{}
		t.lastActivity = now
	case *network.EventLoadingFinished:
		delete(t.inflight, ev.RequestID)
		t.lastActivity = now
	case *network.EventLoadingFailed:
		delete(t.inflight, ev.RequestID)
		t.lastActivity = now
	case *page.EventFrameStartedLoading:
		t.frames[ev.FrameID] = struct{}{}
		t.lastActivity = now
	case *page.EventFrameStoppedLoading:
		delete(t.frames, ev.FrameID)
		t.lastActivity = now
	case *page.EventNavigatedWithinDocument:
		t.lastActivity = now
	}
}

type domState struct {
	Ready             bool `json:"ready"`
	QuietForMS        int  `json:"quiet_for_ms"`
	LoadingIndicators int  `json:"loading_indicators"`
}

func (t *Tracker) Wait(quietMS, timeoutMS int) (Result, error) {
	if quietMS <= 0 {
		quietMS = 1500
	}
	if timeoutMS <= 0 {
		timeoutMS = 10000
	}
	if timeoutMS < quietMS {
		timeoutMS = quietMS
	}
	started := time.Now()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := readDOMState(t.ctx)
		if err != nil {
			return Result{}, err
		}
		t.mu.Lock()
		inflight, frames := len(t.inflight), len(t.frames)
		activityQuiet := time.Since(t.lastActivity) >= time.Duration(quietMS)*time.Millisecond
		t.mu.Unlock()
		result := Result{WaitedMS: int(time.Since(started) / time.Millisecond), LoadingIndicators: state.LoadingIndicators, InflightRequests: inflight, LoadingFrames: frames}
		if state.Ready && state.QuietForMS >= quietMS && state.LoadingIndicators == 0 && inflight == 0 && frames == 0 && activityQuiet {
			result.Stable = true
			return result, nil
		}
		if result.WaitedMS >= timeoutMS {
			result.TimedOut = true
			return result, &TimeoutError{Kind: "stable", Result: result}
		}
		select {
		case <-t.ctx.Done():
			return result, t.ctx.Err()
		case <-ticker.C:
		}
	}
}

type outcomeState struct {
	URL     string `json:"url"`
	Matches []bool `json:"matches"`
	Error   string `json:"error,omitempty"`
}

// WaitForOutcome waits for explicit browser-visible evidence rather than
// global network/frame quiescence. This is suitable for SPAs with analytics,
// autosave polling, or embedded media that may never become globally idle.
func (t *Tracker) WaitForOutcome(conditions []computer.WaitCondition, match string, quietMS, timeoutMS int) (Result, error) {
	if len(conditions) == 0 {
		return Result{}, fmt.Errorf("wait_for requires at least one condition")
	}
	match = strings.ToLower(strings.TrimSpace(match))
	if match == "" {
		match = "any"
	}
	if match != "any" && match != "all" {
		return Result{}, fmt.Errorf("wait_for match must be any or all")
	}
	if quietMS < 0 {
		quietMS = 0
	}
	if timeoutMS <= 0 {
		timeoutMS = 10000
	}
	if timeoutMS < quietMS {
		timeoutMS = quietMS
	}
	started := time.Now()
	var matchedSince time.Time
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := readOutcomeState(t.ctx, conditions)
		if err != nil {
			return Result{}, err
		}
		if state.Error != "" {
			return Result{}, fmt.Errorf("wait_for: %s", state.Error)
		}
		results := make([]computer.WaitConditionResult, len(conditions))
		matchedCount := 0
		for i, condition := range conditions {
			matched := i < len(state.Matches) && state.Matches[i]
			if matched {
				matchedCount++
			}
			results[i] = computer.WaitConditionResult{Index: i, Type: condition.Type, Matched: matched, TargetID: condition.TargetID}
		}
		matched := matchedCount > 0
		if match == "all" {
			matched = matchedCount == len(conditions)
		}
		result := Result{
			Matched: matched, WaitedMS: int(time.Since(started) / time.Millisecond), Match: match,
			CurrentURL: state.URL, Conditions: results,
		}
		if matched {
			if matchedSince.IsZero() {
				matchedSince = time.Now()
			}
			if quietMS == 0 || time.Since(matchedSince) >= time.Duration(quietMS)*time.Millisecond {
				return result, nil
			}
		} else {
			matchedSince = time.Time{}
		}
		if result.WaitedMS >= timeoutMS {
			result.TimedOut = true
			return result, &TimeoutError{Kind: "outcome", Result: result}
		}
		select {
		case <-t.ctx.Done():
			return result, t.ctx.Err()
		case <-ticker.C:
		}
	}
}

func readOutcomeState(ctx context.Context, conditions []computer.WaitCondition) (outcomeState, error) {
	rawConditions, err := json.Marshal(conditions)
	if err != nil {
		return outcomeState{}, err
	}
	var state outcomeState
	expression := outcomeScript + "(" + string(rawConditions) + ")"
	err = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		value, exception, err := cdpruntime.Evaluate(expression).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return fmt.Errorf("wait_for: %s", exception.Text)
		}
		if value == nil || len(value.Value) == 0 {
			return fmt.Errorf("wait_for returned no DOM state")
		}
		return json.Unmarshal(value.Value, &state)
	}))
	return state, err
}

func readDOMState(ctx context.Context) (domState, error) {
	var state domState
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		value, exception, err := cdpruntime.Evaluate(snapshotScript).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return fmt.Errorf("wait_for_stable: %s", exception.Text)
		}
		if value == nil || len(value.Value) == 0 {
			return fmt.Errorf("wait_for_stable returned no DOM state")
		}
		return json.Unmarshal(value.Value, &state)
	}))
	return state, err
}

// Wait is retained as a safe fallback for callers that do not own a session
// tracker. New creates a tracker immediately and therefore observes the whole
// wait interval (session backends keep one from target creation onward).
func Wait(ctx context.Context, quietMS, timeoutMS int) (Result, error) {
	t, err := New(ctx)
	if err != nil {
		return Result{}, err
	}
	return t.Wait(quietMS, timeoutMS)
}

func WaitForOutcome(ctx context.Context, conditions []computer.WaitCondition, match string, quietMS, timeoutMS int) (Result, error) {
	t, err := New(ctx)
	if err != nil {
		return Result{}, err
	}
	return t.WaitForOutcome(conditions, match, quietMS, timeoutMS)
}

const outcomeScript = `(function(conditions){
  function norm(value, sensitive){value=String(value||'').replace(/\s+/g,' ').trim();return sensitive?value:value.toLowerCase();}
  function visible(el){if(!el)return false;var r=el.getBoundingClientRect(),s=getComputedStyle(el);return r.width>=2&&r.height>=2&&s.display!=='none'&&s.visibility!=='hidden'&&parseFloat(s.opacity||'1')>=0.1;}
  function role(el){var explicit=el.getAttribute('role');if(explicit)return explicit.toLowerCase();var tag=el.tagName.toLowerCase();if(tag==='button')return 'button';if(tag==='a'&&el.hasAttribute('href'))return 'link';if(tag==='textarea')return 'textbox';if(tag==='select')return 'combobox';if(tag==='input'){var type=(el.type||'text').toLowerCase();if(type==='checkbox')return 'checkbox';if(type==='radio')return 'radio';if(type==='button'||type==='submit')return 'button';return 'textbox';}return '';}
  function name(el){var labelled=el.getAttribute('aria-labelledby'),text='';if(labelled){labelled.split(/\s+/).forEach(function(id){var n=document.getElementById(id);if(n)text+=' '+(n.innerText||n.textContent||'');});}return el.getAttribute('aria-label')||text||el.getAttribute('alt')||el.getAttribute('title')||el.getAttribute('placeholder')||el.innerText||el.textContent||el.value||'';}
  function semanticPresent(condition){var wanted=norm(condition.name||condition.value,condition.case_sensitive),wantedRole=norm(condition.role,true);var nodes=document.querySelectorAll('button,a[href],input,textarea,select,[role],[tabindex]');for(var i=0;i<nodes.length;i++){var el=nodes[i];if(!visible(el))continue;if(wantedRole&&role(el)!==wantedRole)continue;if(norm(name(el),condition.case_sensitive)===wanted)return true;}return false;}
  var bodyText=(document.body&&document.body.innerText)||(document.documentElement&&document.documentElement.innerText)||'';
  var current=location.href;
  var matches=[];
  try{
    for(var i=0;i<conditions.length;i++){
      var c=conditions[i]||{},type=String(c.type||'').toLowerCase(),value=norm(c.value,c.case_sensitive),actual=norm(current,c.case_sensitive),matched=false;
      if(type==='url_changed')matched=actual!==value;
      else if(type==='url_equals')matched=actual===value;
      else if(type==='url_contains')matched=actual.indexOf(value)>=0;
      else if(type==='text_present')matched=norm(bodyText,c.case_sensitive).indexOf(value)>=0;
      else if(type==='text_absent')matched=norm(bodyText,c.case_sensitive).indexOf(value)<0;
      else if(type==='selector_present'||type==='selector_absent'){
        var present=false,nodes=document.querySelectorAll(c.selector||'');for(var j=0;j<nodes.length;j++)if(visible(nodes[j])){present=true;break;}matched=type==='selector_present'?present:!present;
      } else if(type==='target_present'||type==='target_absent'){
        var semantic=semanticPresent(c);matched=type==='target_present'?semantic:!semantic;
      }
      matches.push(matched);
    }
    return {url:current,matches:matches};
  }catch(error){return {url:current,matches:matches,error:String(error&&error.message||error)};}
})`

const installScript = `(function(){
  if(window.__aptevaComputerStability)return true;
  var state={lastMutation:performance.now()};
  var observer=new MutationObserver(function(){state.lastMutation=performance.now();});
  observer.observe(document.documentElement||document,{subtree:true,childList:true,attributes:true,characterData:true});
  Object.defineProperty(window,'__aptevaComputerStability',{value:state,configurable:true});
  return true;
})()`

const snapshotScript = `(function(){
  if(!window.__aptevaComputerStability){` + installScript + `}
  function visible(el){var r=el.getBoundingClientRect(),s=getComputedStyle(el);return r.width>=2&&r.height>=2&&s.display!=='none'&&s.visibility!=='hidden'&&parseFloat(s.opacity||'1')>=0.1;}
  var nodes=document.querySelectorAll('[aria-busy="true"],[data-loading="true"],[data-state="loading"],[role="progressbar"],[aria-label*="loading" i],[aria-label*="saving" i],[class*="spinner" i]');
  var count=0;for(var i=0;i<nodes.length;i++)if(visible(nodes[i]))count++;
  var state=window.__aptevaComputerStability;
  return {ready:document.readyState==='complete'||document.readyState==='interactive',quiet_for_ms:Math.round(performance.now()-state.lastMutation),loading_indicators:count};
})()`
