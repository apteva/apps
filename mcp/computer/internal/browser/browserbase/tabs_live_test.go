package browserbase

import (
	"os"
	"testing"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestBrowserbaseReusesProviderPageTarget(t *testing.T) {
	if os.Getenv("RUN_BROWSERBASE_TAB_TESTS") == "" {
		t.Skip("set RUN_BROWSERBASE_TAB_TESTS=1")
	}
	apiKey := os.Getenv("BROWSERBASE_API_KEY")
	projectID := os.Getenv("BROWSERBASE_PROJECT_ID")
	if apiKey == "" || projectID == "" {
		t.Fatal("BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID required")
	}

	c, err := New(apiKey, projectID, computer.DisplaySize{Width: 1200, Height: 700})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.OpenSession(computer.OpenOptions{URL: "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	tabs, err := c.ListTabs()
	if err != nil {
		t.Fatal(err)
	}
	if len(tabs) != 1 {
		t.Fatalf("Browserbase session created %d page targets, want 1: %+v", len(tabs), tabs)
	}
}
