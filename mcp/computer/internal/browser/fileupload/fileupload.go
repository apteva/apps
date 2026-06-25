package fileupload

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"
)

type Target struct {
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
	if err := chromedp.Run(ctx, chromedp.SetUploadFiles(result.Selector, files, chromedp.ByQuery)); err != nil {
		return Result{}, err
	}

	cleanupUploadToken(ctx, result.Selector)
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
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &out)); err != nil {
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
	tokenJSON, _ := json.Marshal(token)
	js := fmt.Sprintf(`(function(selector, hasPoint, x, y, token) {
  function isFileInput(el) {
    return !!el && el.tagName === 'INPUT' && String(el.type || '').toLowerCase() === 'file';
  }
  function firstFileInput(root) {
    if (!root || !root.querySelector) return null;
    if (isFileInput(root)) return root;
    return root.querySelector('input[type="file"]');
  }
  function siblingFileInput(el) {
    if (!el) return null;
    var sibs = [el.previousElementSibling, el.nextElementSibling];
    for (var i = 0; i < sibs.length; i++) {
      var input = firstFileInput(sibs[i]);
      if (input) return input;
    }
    return null;
  }
  function relatedFileInput(el) {
    if (isFileInput(el)) return el;
    if (el && el.closest) {
      var label = el.closest('label');
      if (label) {
        if (isFileInput(label.control)) return label.control;
        var inLabel = firstFileInput(label);
        if (inLabel) return inLabel;
      }
    }
    for (var cur = el; cur && cur.nodeType === 1 && cur !== document.documentElement; cur = cur.parentElement) {
      var inside = firstFileInput(cur);
      if (inside) return inside;
      var near = siblingFileInput(cur);
      if (near) return near;
    }
    var all = Array.prototype.slice.call(document.querySelectorAll('input[type="file"]'));
    all = all.filter(function(input) { return !input.disabled; });
    if (all.length === 1) return all[0];
    if (all.length > 1) {
      var main = all.find(function(input) { return input.id === 'mainMedia'; });
      if (main) return main;
      var mixed = all.find(function(input) {
        var accept = String(input.getAttribute('accept') || '').toLowerCase();
        return accept.indexOf('image/') >= 0 && (accept.indexOf('video/') >= 0 || accept.indexOf('audio/') >= 0);
      });
      if (mixed) return mixed;
      return all[0];
    }
    return null;
  }

  var el = selector ? document.querySelector(selector) : null;
  if (!el && hasPoint) el = document.elementFromPoint(x, y);
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
})(%s, %t, %d, %d, %s)`, string(selectorJSON), target.HasPoint, target.X, target.Y, string(tokenJSON))

	var result struct {
		Result
		Error string `json:"error,omitempty"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &result)); err != nil {
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
	_ = chromedp.Run(ctx, chromedp.Evaluate(cleanupJS, nil))
}
