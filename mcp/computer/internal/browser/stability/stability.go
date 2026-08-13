// Package stability tracks page mutations, navigation and finite network
// activity for the lifetime of a CDP target.
package stability

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type Result struct {
	Stable            bool `json:"stable"`
	WaitedMS          int  `json:"waited_ms"`
	LoadingIndicators int  `json:"loading_indicators"`
	InflightRequests  int  `json:"inflight_requests"`
	LoadingFrames     int  `json:"loading_frames"`
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
			return result, fmt.Errorf("page did not stabilize within %s (%d loading indicators, %d requests, %d frames remain)", time.Duration(timeoutMS)*time.Millisecond, state.LoadingIndicators, inflight, frames)
		}
		select {
		case <-t.ctx.Done():
			return result, t.ctx.Err()
		case <-ticker.C:
		}
	}
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
