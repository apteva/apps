package main

import (
	"encoding/json"
	"golang.org/x/net/html"
	"strings"
	"testing"
)

func resolvedDeleteSteps(t *testing.T, operation string) []actorStep {
	t.Helper()
	b, err := json.Marshal(realPerformerDefinition(t).Operations[operation].Steps)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.NewReplacer("{{expected_store_id}}", "100", "{{expected_store_name}}", "Fixture Store", "{{clip_id}}", "12345", "{{expected_title}}", "Fixture Clip", "{{expected_status}}", "draft", "{{library_tab}}", "Drafts").Replace(string(b))
	var steps []actorStep
	if err := json.Unmarshal([]byte(s), &steps); err != nil {
		t.Fatal(err)
	}
	return steps
}

func TestClips4SaleDeleteNativeIdentity(t *testing.T) {
	steps := resolvedDeleteSteps(t, "delete_clip")
	var extract, guard actorStep
	for i, s := range steps {
		if s.Action == "extract" && s.Fields["clip_id"].Required && s.Fields["status"].Required {
			extract, guard = s, steps[i+1]
			break
		}
	}
	for _, tc := range []struct {
		name, id, title, status string
		fail                    bool
	}{
		{"correct", "12345", "Fixture Clip", "draft", false},
		{"same_title_wrong_id", "99999", "Fixture Clip", "draft", true},
		{"changed_title", "12345", "Different Clip", "draft", true},
		{"changed_status", "12345", "Fixture Clip", "active", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Row indices are reused; clip identity must come from the native media record.
			dom := `<table><tbody><tr><td data-testid="drafts_[0]_cell_title"><img data-testid="clip_[0]_image_thumb" src="https://media.example/generated_` + tc.id + `.jpg"><span data-testid="clip_[0]_title">` + tc.title + `</span>filename.mp4 NON-NUDE</td><td aria-label="status: ` + tc.status + `">Draft</td></tr></tbody></table>`
			root, err := html.Parse(strings.NewReader(dom))
			if err != nil {
				t.Fatal(err)
			}
			item, err := extractNodeItem(root, extract.Fields, "https://workspace.clips4sale.com/content/drafts")
			if err != nil {
				t.Fatal(err)
			}
			e := actorExecution{lastValues: item}
			if err := e.assertValues(guard); (err != nil) != tc.fail {
				t.Fatalf("decision=%v values=%v", err, item)
			}
		})
	}
}

func TestClips4SaleDeleteConfirmationScope(t *testing.T) {
	var guard, selectionGuard actorStep
	for _, s := range resolvedDeleteSteps(t, "delete_clip") {
		if s.Assertions["selected_clip_ids"].EqualsSet != nil {
			selectionGuard = s
		}
		if _, ok := s.Assertions["confirm_label"]; ok {
			guard = s
		}
	}
	for _, tc := range []struct {
		name, title, label string
		ids                []string
		fail               bool
	}{
		{"one", "Delete this clip?", "Delete 1 clip", []string{"12345"}, false},
		{"extra", "Delete this clip?", "Delete 1 clip", []string{"12345", "99999"}, true},
		{"wrong", "Delete this clip?", "Delete 1 clip", []string{"99999"}, true},
		{"plural", "Delete 2 clips?", "Delete 2 clips", []string{"12345"}, true},
		{"permanent", "Permanently delete?", "Delete 1 clip", []string{"12345"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := actorExecution{lastValues: map[string]any{"dialog_title": tc.title, "confirm_label": tc.label, "selected_clip_ids": tc.ids, "recovery_notice": "This action can be undone within 7 days."}}
			err := e.assertValues(selectionGuard)
			if err == nil {
				err = e.assertValues(guard)
			}
			if (err != nil) != tc.fail {
				t.Fatal(err)
			}
		})
	}
}

func TestClips4SaleDeleteAuthorizationReplayAndPersistence(t *testing.T) {
	steps := resolvedDeleteSteps(t, "delete_clip")
	if steps[0].Assertions["delete_authorized"].EqualsExact != true {
		t.Fatal("authorization missing")
	}
	for _, auth := range []any{false, "true", nil} {
		e := actorExecution{lastValues: map[string]any{"delete_authorized": auth}}
		if e.assertValues(steps[0]) == nil {
			t.Fatal("invalid authorization accepted")
		}
	}
	commits, dialogChecked, trashChecked := 0, false, false
	for _, s := range steps {
		if _, ok := s.Assertions["confirm_label"]; ok {
			dialogChecked = true
		}
		if s.OnceKey != "" {
			commits++
			if !dialogChecked || s.OnceScope != "context" || s.OnceKey != "c4s:100:clip:12345:delete" || s.ExpectedEffect != "delete" || s.ConfirmConsequence != "delete" {
				t.Fatal("unguarded commit")
			}
		}
		if s.Action == "assert_url" && s.PathPrefix == "/content/deleted" {
			trashChecked = true
		}
		if strings.Contains(s.Locator.Selector, "force") {
			t.Fatal("permanent purge")
		}
	}
	if commits != 1 || !trashChecked {
		t.Fatal("missing single commit or trash proof")
	}
	final := steps[len(steps)-1]
	if final.Assertions["clip_id"].EqualsExact != "12345" || final.Assertions["title"].EqualsExact != "Fixture Clip" {
		t.Fatal("missing same-resource persistence proof")
	}
	for _, s := range resolvedDeleteSteps(t, "verify_deleted_clip") {
		if s.OnceKey != "" || s.ConfirmConsequence != "" || s.Action == "set_checked" || s.Action == "upload_file" {
			t.Fatal("reconciliation mutates")
		}
		if s.Action == "click" && s.ExpectedEffect != "navigation_only" && !(s.ExpectedEffect == "open_configuration" && s.Locator.Text == "My Content") {
			t.Fatal("non-navigation reconciliation")
		}
	}
}

func TestClips4SaleDeleteDoesNotDependOnEphemeralToast(t *testing.T) {
	for _, name := range []string{"delete_clip", "delete_clip_from_row"} {
		steps := resolvedDeleteSteps(t, name)
		commit := -1
		for i, s := range steps {
			if s.OnceKey != "" {
				commit = i
			}
		}
		if commit < 0 {
			t.Fatal("missing commit", name)
		}
		for _, s := range steps[commit+1:] {
			for _, c := range s.Conditions {
				if strings.Contains(c.Selector, "snackbar") {
					t.Fatal("transient toast used as persisted receipt", name)
				}
			}
		}
		closing := steps[commit+1]
		if closing.Action != "wait_for" || len(closing.Conditions) != 1 || closing.Conditions[0].Type != "selector_absent" {
			t.Fatal("must wait for confirmation to close", name)
		}
		if !closing.Optional || steps[commit+2].Action != "dismiss_inspection" {
			t.Fatal("stale confirmation must be dismissed before positive Trash verification", name)
		}
	}
}

func TestClips4SaleDeleteReadbackRequiresDeletedStatusAndPagination(t *testing.T) {
	steps := resolvedDeleteSteps(t, "verify_deleted_clip")
	collection := false
	for _, s := range steps {
		if s.ReadViews != nil {
			collection = true
			if !s.ReadViews.UseCurrentPage || len(s.ReadViews.Views) != 1 {
				t.Fatal("verification must use the observed Deleted view")
			}
			v := s.ReadViews.Views[0]
			if v.Name != "deleted" || v.Total == nil || v.Pagination.Mode != "next" || !v.Fields["clip_id"].Required || !v.Fields["status"].Required {
				t.Fatal("incomplete deleted collection evidence")
			}
		}
	}
	if !collection {
		t.Fatal("no paginated readback")
	}
	for _, status := range []string{"trash", "active", "draft", ""} {
		e := actorExecution{lastValues: map[string]any{"clip_id": "12345", "title": "Fixture Clip", "status": status}}
		if err := e.assertValues(steps[len(steps)-1]); (err == nil) != (status == "trash") {
			t.Fatalf("status %q accepted incorrectly: %v", status, err)
		}
	}
}

func TestClips4SaleDeletionAuditHasNoWriteControls(t *testing.T) {
	d := realPerformerDefinition(t)
	for _, name := range []string{"get_clip_inventory", "get_store_launch_status"} {
		op, ok := d.Operations[name]
		if !ok {
			t.Fatal("missing read operation", name)
		}
		for _, s := range op.Steps {
			if s.OnceKey != "" || s.ConfirmConsequence != "" || s.Action == "set_checked" || s.Action == "upload_file" || s.Action == "fill_secret" {
				t.Fatal("audit mutates", name, s.Action)
			}
			if s.Action == "click" && s.ExpectedEffect != "navigation_only" && !(s.Locator.Text == "My Content" && s.ExpectedEffect == "open_configuration") {
				t.Fatal("non-navigation audit control", name)
			}
		}
	}
}

func TestClips4SaleDeleteStoreNameExcludesAvatarInitials(t *testing.T) {
	steps := resolvedDeleteSteps(t, "delete_clip")
	var extract, guard actorStep
	for i, s := range steps {
		if s.Fields["store_name"].Required {
			extract, guard = s, steps[i+1]
			break
		}
	}
	for _, avatar := range []string{"", "EG", "MS"} {
		dom := `<div data-testid="dashboard-sidebar-widget-div-my-stores"><a href="/stores"><div class="avatar">` + avatar + `</div><div><div><p>Fixture Store</p></div><div><p>Store ID: 100</p></div><div><p>0 clips</p></div></div></a></div>`
		root, err := html.Parse(strings.NewReader(dom))
		if err != nil {
			t.Fatal(err)
		}
		item, err := extractNodeItem(root, extract.Fields, "https://workspace.clips4sale.com/")
		if err != nil {
			t.Fatal(err)
		}
		e := actorExecution{lastValues: item}
		if err := e.assertValues(guard); err != nil {
			t.Fatalf("avatar %q changed store identity: %v", avatar, err)
		}
	}
}
