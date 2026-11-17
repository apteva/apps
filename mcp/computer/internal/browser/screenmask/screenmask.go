// Package screenmask hides credentials in the live page for the duration of
// one screenshot, then restores the page. It mirrors the isolated test
// browser's masking (testbrowser.Browser.Screenshot) for app-owned sessions.
//
// Secret values are passed to the page for this call only; they are never
// stored, logged, or returned.
package screenmask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"github.com/chromedp/chromedp"
)

const (
	MaxSecrets     = 20
	MaxSecretBytes = 1024
)

// Validate rejects masks that cannot be applied safely.
func Validate(mask computer.ScreenshotMask) error {
	if len(mask.Secrets) > MaxSecrets {
		return fmt.Errorf("mask.secrets accepts at most %d values", MaxSecrets)
	}
	for _, secret := range mask.Secrets {
		if secret == "" || len(secret) > MaxSecretBytes {
			return fmt.Errorf("each mask secret must be 1-%d bytes", MaxSecretBytes)
		}
	}
	return nil
}

const applyScript = `(function(opts){
  const marker = 'data-apteva-screenshot-mask';
  const sensitive = 'input[type=password],[sensitive]:not([sensitive=false]),[data-sensitive]:not([data-sensitive=false]),[data-test-sensitive]:not([data-test-sensitive=false])';
  const style = document.createElement('style');
  style.id = 'apteva-screenshot-mask';
  style.textContent = (opts.inputs ? 'input,textarea,select,[contenteditable]:not([contenteditable=false]),' : '') +
    sensitive + ',[' + marker + ']{visibility:hidden!important}';
  (document.head || document.documentElement).appendChild(style);
  const secrets = (opts.secrets || []).filter(Boolean);
  let marked = 0;
  if (secrets.length) {
    const hit = (text) => !!text && secrets.some((s) => text.includes(s));
    if (document.body) {
      const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      let n;
      while ((n = walk.nextNode())) {
        if (hit(n.textContent) && n.parentElement) { n.parentElement.setAttribute(marker, ''); marked++; }
      }
    }
    for (const el of document.querySelectorAll('input,textarea')) {
      if (hit(el.value)) { el.setAttribute(marker, ''); marked++; }
    }
  }
  return marked;
})`

const restoreScript = `(function(){
  document.getElementById('apteva-screenshot-mask')?.remove();
  document.querySelectorAll('[data-apteva-screenshot-mask]').forEach((e) => e.removeAttribute('data-apteva-screenshot-mask'));
  return true;
})()`

// Apply masks the page bound to ctx and returns a restore function that must
// be called after capture. If applying fails the page is restored and an
// error returned, so callers must not capture an unmasked frame.
func Apply(ctx context.Context, mask computer.ScreenshotMask) (func(), error) {
	if err := Validate(mask); err != nil {
		return nil, err
	}
	secrets := mask.Secrets
	if secrets == nil {
		secrets = []string{}
	}
	raw, err := json.Marshal(map[string]any{"inputs": mask.Inputs, "secrets": secrets})
	if err != nil {
		return nil, err
	}
	restore := func() { _ = cdputil.Run(ctx, chromedp.Evaluate(restoreScript, nil)) }
	var marked int
	if err := cdputil.Run(ctx, chromedp.Evaluate(applyScript+"("+string(raw)+")", &marked)); err != nil {
		restore()
		// The error text can echo the evaluated expression, which contains
		// the secrets; never propagate it.
		return nil, errors.New("screenshot masking failed")
	}
	return restore, nil
}
