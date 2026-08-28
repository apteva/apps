package environment

import (
	"context"
	"encoding/json"
	"fmt"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Probe reads browser-visible identity after session setup and initial
// navigation. It reports effective values, not merely requested overrides.
func Probe(ctx context.Context) (computer.EffectiveEnvironment, error) {
	var out computer.EffectiveEnvironment
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		value, exception, err := cdpruntime.Evaluate(`({locale:navigator.language||'',languages:Array.from(navigator.languages||[]),timezone:(Intl.DateTimeFormat().resolvedOptions().timeZone||''),user_agent:navigator.userAgent||'',verified:true})`).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return fmt.Errorf("probe browser environment: %s", exception.Text)
		}
		if value == nil {
			return fmt.Errorf("probe browser environment returned no value")
		}
		return json.Unmarshal(value.Value, &out)
	}))
	return out, err
}
