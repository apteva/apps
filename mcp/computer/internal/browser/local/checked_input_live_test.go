package local

import (
	"net/url"
	"os"
	"testing"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/cdputil"
	"github.com/apteva/apps/mcp/computer/internal/browser/checkedinput"
	"github.com/chromedp/chromedp"
)

func TestAuditControlledRadioPersistsAcrossRender(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1")
	}
	c, err := New(computer.DisplaySize{Width: 900, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Like a controlled framework input, the next render restores application
	// state. Only native activation's click handler commits a new selection.
	html := `<label>Free<input id=free type=radio></label><label>Paid<input id=paid type=radio checked></label><script>
	let selection='paid'; window.clicks=0;
	window.render=()=>{free.checked=selection==='free';paid.checked=selection==='paid'};
	for(const el of [free,paid]) el.addEventListener('click',()=>{selection=el.id;window.clicks++;render()});
	</script>`
	if err := c.OpenSession(computer.OpenOptions{URL: "data:text/html," + url.PathEscape(html)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := checkedinput.Set(c.ctx, checkedinput.Target{Selector: "#free"}, checkedinput.Request{Checked: true})
		if err != nil || !result.Verified || result.ActionDispatched != (i == 0) {
			t.Fatalf("selection %d: %+v %v", i, result, err)
		}
		var valid bool
		if err := cdputil.Run(c.ctx, chromedp.Evaluate(`(render(),free.checked&&!paid.checked&&clicks===1)`, &valid)); err != nil || !valid {
			t.Fatalf("selection did not survive render, or click repeated: %v", err)
		}
	}
}
