package main

import (
	"fmt"
	"strings"
	"testing"
)

// Responses hand back next_cursor; passing it back as "cursor" must advance
// the page instead of being ignored and restarting from the first page.
func TestActorListFollowsNextCursorAsCursor(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	for i := 0; i < 5; i++ {
		if _, err := app.toolActorSave(ctx, map[string]any{"name": fmt.Sprintf("Actor %d", i), "definition": testActorDefinition()}); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"cursor", "after"} {
		seen := map[any]bool{}
		args := map[string]any{"limit": 2}
		for pages := 0; ; pages++ {
			if pages > 5 {
				t.Fatalf("%s: pagination did not terminate", key)
			}
			out, err := app.toolActorList(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			page := out.(map[string]any)
			for _, item := range page["actors"].([]map[string]any) {
				if seen[item["id"]] {
					t.Fatalf("%s: actor %v returned twice", key, item["id"])
				}
				seen[item["id"]] = true
			}
			if page["has_more"] != true {
				break
			}
			args = map[string]any{"limit": 2, key: page["next_cursor"]}
		}
		if len(seen) != 5 {
			t.Fatalf("%s: saw %d actors, want 5", key, len(seen))
		}
	}
}

func TestPageAfterArgRejectsConflictingCursor(t *testing.T) {
	if _, err := pageAfterArg(map[string]any{"after": 3, "cursor": 7}); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("err=%v", err)
	}
	if got, err := pageAfterArg(map[string]any{"after": 4, "cursor": float64(4)}); err != nil || got != 4 {
		t.Fatalf("got=%d err=%v", got, err)
	}
	if got, err := pageAfterArg(map[string]any{"cursor": "9"}); err != nil || got != 9 {
		t.Fatalf("got=%d err=%v", got, err)
	}
	if got, err := pageAfterArg(map[string]any{}); err != nil || got != 0 {
		t.Fatalf("got=%d err=%v", got, err)
	}
}
