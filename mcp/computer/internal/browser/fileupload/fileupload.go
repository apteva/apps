package fileupload

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"time"

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
	Selector string `json:"selector"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Accept   string `json:"accept,omitempty"`
	Multiple bool   `json:"multiple"`
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
	result, err := ResolveInput(ctx, target)
	if err != nil {
		return Result{}, err
	}
	defer cleanupUploadToken(ctx, result.Selector)
	if len(files) > 1 && !result.Multiple {
		return Result{}, errors.New("upload_file: control does not accept multiple files")
	}
	if err := cdputil.Run(ctx, chromedp.SetUploadFiles(result.Selector, files, chromedp.ByQuery)); err != nil {
		return Result{}, err
	}

	return result, nil
}

func SetPayloads(ctx context.Context, target Target, payloads []Payload) (Result, error) {
	if len(payloads) == 0 {
		return Result{}, fmt.Errorf("upload_file requires at least one file")
	}
	result, err := ResolveInput(ctx, target)
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
	js := fmt.Sprintf(`(function(selector, files) {
  function bytesFromBase64(input) {
    var bin = atob(input);
    var bytes = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return bytes;
  }
  var input = document.querySelector(selector);
  if (!input) return {error: 'resolved input disappeared'};
  var dt = new DataTransfer();
  for (var i = 0; i < files.length; i++) {
    var f = files[i];
    dt.items.add(new File([bytesFromBase64(f.data)], f.name, {
      type: f.mime || '',
      lastModified: Date.now()
    }));
  }
  input.files = dt.files;
  input.dispatchEvent(new Event('input', {bubbles: true}));
  input.dispatchEvent(new Event('change', {bubbles: true}));
  input.removeAttribute('data-apteva-upload-token');
  return {ok: true, count: input.files.length};
})(%s, %s)`, string(selectorJSON), string(filesJSON))
	var out struct {
		OK    bool   `json:"ok"`
		Count int    `json:"count"`
		Error string `json:"error,omitempty"`
	}
	if err := cdputil.Run(ctx, chromedp.Evaluate(js, &out)); err != nil {
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
  } else if (selector) el=document.querySelector(selector);
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
		return Result{}, errors.New(result.Error)
	}
	if result.Selector == "" {
		return Result{}, fmt.Errorf("no input[type=file] target resolved")
	}
	return result.Result, nil
}

func cleanupUploadToken(ctx context.Context, selector string) {
	selectorCleanupJSON, _ := json.Marshal(selector)
	cleanupJS := fmt.Sprintf(`(function(selector) {
  var input = document.querySelector(selector);
  if (!input) return false;
  input.removeAttribute('data-apteva-upload-token');
  return true;
})(%s)`, string(selectorCleanupJSON))
	_ = cdputil.Run(ctx, chromedp.Evaluate(cleanupJS, nil))
}
