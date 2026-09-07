package fileupload

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type Target struct {
	ID       string
	Selector string
	X        int
	Y        int
	HasPoint bool
}

type Result struct {
	Selector      string `json:"selector"`
	ID            string `json:"id,omitempty"`
	Name          string `json:"name,omitempty"`
	Accept        string `json:"accept,omitempty"`
	Multiple      bool   `json:"multiple"`
	backendNodeID cdp.BackendNodeID
}

type Payload struct {
	Name string
	MIME string
	Data []byte
}

func SetFiles(ctx context.Context, target Target, files []string) (Result, error) {
	if len(files) == 0 {
		return Result{}, fmt.Errorf("upload_file requires at least one file")
	}
	result, err := resolveUploadInput(ctx, target)
	if err != nil {
		return Result{}, err
	}
	defer cleanupUploadToken(ctx, result.Selector)
	if len(files) > 1 && !result.Multiple {
		return Result{}, errors.New("upload_file: control does not accept multiple files")
	}
	var assign chromedp.Action = chromedp.SetUploadFiles(result.Selector, files, chromedp.ByQuery)
	if result.backendNodeID != 0 {
		assign = dom.SetFileInputFiles(files).WithBackendNodeID(result.backendNodeID)
	}
	if err := cdputil.Run(ctx, assign); err != nil {
		return Result{}, err
	}

	return result, nil
}

func SetPayloads(ctx context.Context, target Target, payloads []Payload) (Result, error) {
	if len(payloads) == 0 {
		return Result{}, fmt.Errorf("upload_file requires at least one file")
	}
	result, err := resolveUploadInput(ctx, target)
	if err != nil {
		return Result{}, err
	}
	defer cleanupUploadToken(ctx, result.Selector)
	if len(payloads) > 1 && !result.Multiple {
		return Result{}, errors.New("upload_file: control does not accept multiple files")
	}
	files := make([]map[string]string, 0, len(payloads))
	for i, payload := range payloads {
		if payload.Name == "" {
			payload.Name = fmt.Sprintf("upload-%d", i+1)
		}
		files = append(files, map[string]string{
			"name": payload.Name,
			"mime": payload.MIME,
			"data": base64.StdEncoding.EncodeToString(payload.Data),
		})
	}
	selectorJSON, _ := json.Marshal(result.Selector)
	filesJSON, _ := json.Marshal(files)
	const assignFunction = `function(files) {
  function bytesFromBase64(input) {
    var bin = atob(input);
    var bytes = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return bytes;
  }
  var input = this;
  if (!input || input.tagName !== 'INPUT' || String(input.type).toLowerCase() !== 'file') return {error: 'resolved input disappeared'};
  var dt = new DataTransfer();
  for (var i = 0; i < files.length; i++) {
    var f = files[i];
    dt.items.add(new File([bytesFromBase64(f.data)], f.name, {
      type: f.mime || '',
      lastModified: Date.now()
    }));
  }
  input.files = dt.files;
  var assignedCount = input.files.length;
  input.dispatchEvent(new Event('input', {bubbles: true}));
  input.dispatchEvent(new Event('change', {bubbles: true}));
  input.removeAttribute('data-apteva-upload-token');
  return {ok: true, count: assignedCount};
}`
	var out struct {
		OK    bool   `json:"ok"`
		Count int    `json:"count"`
		Error string `json:"error,omitempty"`
	}
	if result.backendNodeID != 0 {
		err = callOnInput(ctx, result.backendNodeID, assignFunction, filesJSON, &out)
	} else {
		js := fmt.Sprintf(`(%s).call(document.querySelector(%s), %s)`, assignFunction, selectorJSON, filesJSON)
		err = cdputil.Run(ctx, chromedp.Evaluate(js, &out))
	}
	if err != nil {
		return Result{}, err
	}
	if out.Error != "" {
		return Result{}, errors.New(out.Error)
	}
	if !out.OK || out.Count != len(payloads) {
		return Result{}, fmt.Errorf("upload_file payload assignment failed")
	}
	return result, nil
}

var errNoRelatedInput = errors.New("no related input[type=file] found")

// Keep direct DOM resolution for ordinary inputs. When a Browse control has
// several possible inputs or creates one on demand, Chrome's chooser event
// identifies the input actually opened by that control, without guessing.
func resolveUploadInput(ctx context.Context, target Target) (Result, error) {
	result, err := ResolveInput(ctx, target)
	if !errors.Is(err, errNoRelatedInput) {
		return result, err
	}
	waitCtx, cancel := cdputil.Context(ctx, 5*time.Second)
	defer cancel()
	events := make(chan *page.EventFileChooserOpened, 1)
	chromedp.ListenTarget(waitCtx, func(event any) {
		if chooser, ok := event.(*page.EventFileChooserOpened); ok {
			select {
			case events <- chooser:
			default:
			}
		}
	})
	if err := cdputil.Run(waitCtx, page.SetInterceptFileChooserDialog(true)); err != nil {
		return Result{}, err
	}
	defer func() {
		// Restore interception even if the action timed out or was cancelled.
		cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		_ = chromedp.Run(cleanupCtx, page.SetInterceptFileChooserDialog(false))
	}()
	encoded, _ := json.Marshal(target)
	js := fmt.Sprintf(`(function(t) {
  var el;
  if(t.ID) {
    var state=window.__aptevaComputerSOM, saved=state&&state.targets&&state.targets[t.ID];
    if(!saved||!saved.element||!saved.element.isConnected) return {error:'upload_file: stable target is no longer connected'};
    el=saved.element;
  } else if(t.Selector) {
    var matches=document.querySelectorAll(t.Selector);
    if(matches.length!==1) return {error:'upload_file: selector must match exactly one element; matched '+matches.length};
    el=matches[0];
  }
  else if(t.HasPoint) el=document.elementFromPoint(t.X,t.Y);
  if(!el||!el.isConnected||el.disabled||el.getAttribute('aria-disabled')==='true'||el.closest('[inert]')) return {error:'upload_file: upload control is unavailable'};
  var name=(el.getAttribute('aria-label')||el.innerText||el.textContent||'').trim();
  // This fallback activates a control. Restrict it to explicit file-picker
  // names, never an arbitrary button that happens to share a file-input form.
  if(!/^(?:(?:browse|upload)(?:\s+(?:files?|images?|photos?|videos?))?|(?:choose|select|attach|add)\s+(?:a\s+)?(?:files?|images?|photos?|videos?|attachments?))\s*[,.:…]*$/i.test(name)) return {error:'upload_file: no related input and target is not an explicit file-picker control'};
  var button=el.closest('button'), link=el.closest('a[href]'), rect=el.getBoundingClientRect();
  if((button&&button.type!=='button')||link||!rect.width||!rect.height||getComputedStyle(el).visibility==='hidden') return {error:'upload_file: file-picker fallback requires a visible non-submit control'};
  var root=el.getRootNode(), hitRoot=root.elementFromPoint?root:el.ownerDocument;
  var reachable=[[.5,.5],[.2,.2],[.8,.2],[.2,.8],[.8,.8]].some(function(p){
    var hit=hitRoot.elementFromPoint(rect.left+rect.width*p[0],rect.top+rect.height*p[1]);
    return hit&&(hit===el||el.contains(hit)||(button&&button.contains(hit)));
  });
  if(!reachable) return {error:'upload_file: file-picker control is occluded'};
  el.click();
  return {ok:true};
})(%s)`, encoded)
	var activated struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := cdputil.Run(waitCtx, chromedp.Evaluate(js, &activated, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithUserGesture(true) })); err != nil {
		return Result{}, err
	}
	if !activated.OK {
		return Result{}, errors.New(activated.Error)
	}
	select {
	case event := <-events:
		if event.BackendNodeID == 0 {
			return Result{}, errors.New("upload_file: file chooser did not identify an input")
		}
		var result Result
		if err := callOnInput(waitCtx, event.BackendNodeID, `function(){return {id:this.id||'',name:this.name||'',accept:this.accept||'',multiple:!!this.multiple};}`, nil, &result); err != nil {
			return Result{}, err
		}
		result.backendNodeID = event.BackendNodeID
		return result, nil
	case <-waitCtx.Done():
		return Result{}, fmt.Errorf("upload_file: control did not open a file chooser: %w", waitCtx.Err())
	}
}

// Backend node identity also supports inputs created on demand, detached from
// the document, or inside shadow roots; no global selector fallback is needed.
func callOnInput(ctx context.Context, node cdp.BackendNodeID, function string, argument json.RawMessage, out any) error {
	return cdputil.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		object, err := dom.ResolveNode().WithBackendNodeID(node).Do(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = runtime.ReleaseObject(object.ObjectID).Do(ctx) }()
		call := runtime.CallFunctionOn(function).WithObjectID(object.ObjectID).WithReturnByValue(true)
		if argument != nil {
			call = call.WithArguments([]*runtime.CallArgument{{Value: []byte(argument)}})
		}
		value, exception, err := call.Do(ctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return fmt.Errorf("upload_file: input operation failed: %s", exception.Text)
		}
		return json.Unmarshal(value.Value, out)
	}))
}

func ResolveInput(ctx context.Context, target Target) (Result, error) {
	token := fmt.Sprintf("apteva-upload-%d", time.Now().UnixNano())
	selectorJSON, _ := json.Marshal(target.Selector)
	idJSON, _ := json.Marshal(target.ID)
	tokenJSON, _ := json.Marshal(token)
	js := fmt.Sprintf(`(function(selector, hasPoint, x, y, token, targetID) {
  function isFileInput(el) {
    return !!el && el.tagName === 'INPUT' && String(el.type || '').toLowerCase() === 'file';
  }
  function relatedFileInput(el) {
    if (!el) return null;
    if (isFileInput(el)) return el.disabled ? null : el;
    var label = el.closest && el.closest('label');
    if (label && isFileInput(label.control)) return label.control.disabled ? null : label.control;
    // Stop at the first container with inputs. Never choose between unrelated
    // fields or escape to a document-wide/site-specific fallback.
    for (var cur=el, depth=0; cur && depth<3 && cur!==document.body && cur!==document.documentElement; cur=cur.parentElement, depth++) {
      var inputs=Array.from(cur.querySelectorAll('input[type="file"]')).filter(function(n){return !n.disabled;});
      if (inputs.length > 1) return null;
      if (inputs.length === 1) return inputs[0];
      if (cur.tagName==='FORM') break;
    }
    return null;
  }

  var el = null;
  if (targetID) {
    var state=window.__aptevaComputerSOM,saved=state&&state.targets&&state.targets[targetID];
    if (!saved || !saved.element || !saved.element.isConnected) return {error:'upload_file: stable target is no longer connected'};
    el=saved.element;
  } else if (selector) {
    var matches=document.querySelectorAll(selector);
    if(matches.length!==1) return {error:'upload_file: selector must match exactly one element; matched '+matches.length};
    el=matches[0];
  }
  if (!el && !targetID && hasPoint) el = document.elementFromPoint(x, y);
  var input = relatedFileInput(el);
  if (!input) {
    return {error: 'no related input[type=file] found'};
  }
  input.setAttribute('data-apteva-upload-token', token);
  return {
    selector: '[data-apteva-upload-token="' + token + '"]',
    id: input.id || '',
    name: input.name || '',
    accept: input.getAttribute('accept') || '',
    multiple: !!input.multiple
  };
})(%s, %t, %d, %d, %s, %s)`, string(selectorJSON), target.HasPoint, target.X, target.Y, string(tokenJSON), string(idJSON))

	var result struct {
		Result
		Error string `json:"error,omitempty"`
	}
	if err := cdputil.Run(ctx, chromedp.Evaluate(js, &result)); err != nil {
		return Result{}, err
	}
	if result.Error != "" {
		if result.Error == errNoRelatedInput.Error() {
			return Result{}, errNoRelatedInput
		}
		return Result{}, errors.New(result.Error)
	}
	if result.Selector == "" {
		return Result{}, fmt.Errorf("no input[type=file] target resolved")
	}
	return result.Result, nil
}

func cleanupUploadToken(ctx context.Context, selector string) {
	if selector == "" {
		return
	}
	selectorCleanupJSON, _ := json.Marshal(selector)
	cleanupJS := fmt.Sprintf(`(function(selector) {
  var input = document.querySelector(selector);
  if (!input) return false;
  input.removeAttribute('data-apteva-upload-token');
  return true;
})(%s)`, string(selectorCleanupJSON))
	_ = cdputil.Run(ctx, chromedp.Evaluate(cleanupJS, nil))
}
