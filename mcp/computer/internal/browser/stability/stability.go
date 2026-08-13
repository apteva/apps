// Package stability waits until a browser page has been quiet and free of
// visible loading indicators for a caller-selected interval.
package stability

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type Result struct {
	Stable            bool `json:"stable"`
	WaitedMS          int  `json:"waited_ms"`
	LoadingIndicators int  `json:"loading_indicators"`
}

func Wait(ctx context.Context, quietMS, timeoutMS int) (Result, error) {
	if quietMS <= 0 {
		quietMS = 1500
	}
	if timeoutMS <= 0 {
		timeoutMS = 10000
	}
	if timeoutMS < quietMS {
		timeoutMS = quietMS
	}
	var result Result
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		value, exception, err := cdpruntime.Evaluate(script(quietMS, timeoutMS)).WithAwaitPromise(true).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return fmt.Errorf("wait_for_stable: %s", exception.Text)
		}
		if value == nil || len(value.Value) == 0 {
			return fmt.Errorf("wait_for_stable returned no result")
		}
		return json.Unmarshal(value.Value, &result)
	}))
	if err != nil {
		return result, err
	}
	if !result.Stable {
		return result, fmt.Errorf("page did not stabilize within %s (%d visible loading indicators remain)", time.Duration(timeoutMS)*time.Millisecond, result.LoadingIndicators)
	}
	return result, nil
}

func script(quietMS, timeoutMS int) string {
	return fmt.Sprintf(`new Promise(function(resolve){
  var started=performance.now(),lastChange=started,lastResources=performance.getEntriesByType('resource').length;
  var observer=new MutationObserver(function(){lastChange=performance.now();});
  observer.observe(document.documentElement||document,{subtree:true,childList:true,attributes:true,characterData:true});
  function visible(el){var r=el.getBoundingClientRect(),s=getComputedStyle(el);return r.width>=2&&r.height>=2&&s.display!=='none'&&s.visibility!=='hidden'&&parseFloat(s.opacity||'1')>=0.1;}
  function loading(){
    var nodes=document.querySelectorAll('[aria-busy="true"],[data-loading="true"],[data-state="loading"],[role="progressbar"],[aria-label*="loading" i],[aria-label*="saving" i],[class*="spinner" i]');
    var count=0;for(var i=0;i<nodes.length;i++)if(visible(nodes[i]))count++;return count;
  }
  var timer=setInterval(function(){
    var now=performance.now(),resources=performance.getEntriesByType('resource').length;
    if(resources!==lastResources){lastResources=resources;lastChange=now;}
    var indicators=loading(),ready=document.readyState==='complete'||document.readyState==='interactive';
    if(ready&&indicators===0&&now-lastChange>=%d){clearInterval(timer);observer.disconnect();resolve({stable:true,waited_ms:Math.round(now-started),loading_indicators:0});return;}
    if(now-started>=%d){clearInterval(timer);observer.disconnect();resolve({stable:false,waited_ms:Math.round(now-started),loading_indicators:indicators});}
  },50);
})`, quietMS, timeoutMS)
}
