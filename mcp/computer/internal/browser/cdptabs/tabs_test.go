package cdptabs

import (
	"testing"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestHideInactiveBlankPlaceholders(t *testing.T) {
	tabs := []computer.TabInfo{
		{ID: "blank", URL: "about:blank", Title: "about:blank"},
		{ID: "page", URL: "https://example.com", Title: "Example", Active: true},
	}
	got := hideInactiveBlankPlaceholders(tabs)
	if len(got) != 1 || got[0].ID != "page" {
		t.Fatalf("want only content tab, got %+v", got)
	}
}

func TestHideInactiveBlankPlaceholdersKeepsActiveBlank(t *testing.T) {
	tabs := []computer.TabInfo{
		{ID: "blank", URL: "about:blank", Title: "about:blank", Active: true},
		{ID: "page", URL: "https://example.com", Title: "Example"},
	}
	got := hideInactiveBlankPlaceholders(tabs)
	if len(got) != 2 {
		t.Fatalf("active blank tab should remain visible, got %+v", got)
	}
}

func TestHideInactiveBlankPlaceholdersKeepsOnlyBlankSession(t *testing.T) {
	tabs := []computer.TabInfo{
		{ID: "blank", URL: "about:blank", Title: "about:blank"},
	}
	got := hideInactiveBlankPlaceholders(tabs)
	if len(got) != 1 || got[0].ID != "blank" {
		t.Fatalf("blank-only session should remain visible, got %+v", got)
	}
}
