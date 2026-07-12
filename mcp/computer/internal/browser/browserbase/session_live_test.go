package browserbase

import (
	"os"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestBrowserbaseExistingSessionLive(t *testing.T) {
	connectURL := os.Getenv("BROWSERBASE_CONNECT_URL")
	if connectURL == "" {
		t.Skip("set BROWSERBASE_CONNECT_URL")
	}
	c := &Computer{display: computer.DisplaySize{Width: 1600, Height: 800}}
	if err := c.establishCDP(connectURL); err != nil {
		t.Fatal(err)
	}
	defer c.releaseCDP()
	started := time.Now()
	if url := os.Getenv("COMPUTER_NAVIGATION_TEST_URL"); url != "" {
		if _, err := c.Execute(computer.Action{Type: "navigate", URL: url}); err != nil {
			t.Fatal(err)
		}
	} else if _, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true}); err != nil {
		t.Fatal(err)
	}
	for _, target := range c.LastSetOfMark() {
		t.Logf("label=%d tag=%s role=%s text=%q", target.Label, target.Tag, target.Role, target.Text)
	}
	t.Logf("elapsed=%s url=%s som=%d", time.Since(started), c.CurrentURL(), len(c.LastSetOfMark()))
}
