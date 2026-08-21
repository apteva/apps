package som

import (
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/domsnapshot"
)

func TestMergeAXDeduplicatesAndRelabels(t *testing.T) {
	js := []Element{{Label: 7, X: 10, Y: 10, W: 20, H: 20, Tag: "button", Text: "Existing"}}
	ax := []Element{
		{X: 12, Y: 11, W: 20, H: 20, Role: "button", Text: "Existing"},
		{X: 100, Y: 100, W: 40, H: 20, Role: "button", Text: "Accept"},
	}

	got := MergeAX(js, ax)
	if len(got) != 2 {
		t.Fatalf("MergeAX length = %d, want 2: %+v", len(got), got)
	}
	if got[0].Label != 1 || got[1].Label != 2 || got[1].Text != "Accept" {
		t.Fatalf("MergeAX result = %+v", got)
	}
}

func TestShouldAugmentAXForConsentOrEmptySOM(t *testing.T) {
	if !ShouldAugmentAX(nil) {
		t.Fatal("empty SOM should use AX complement")
	}
	if !ShouldAugmentAX([]Element{{Tag: "a", Text: "Cookies and advertising choices"}}) {
		t.Fatal("cookie SOM should use AX complement")
	}
	if ShouldAugmentAX([]Element{{Tag: "a", Text: "Cookie Policy"}, {Tag: "button", Text: "Continue"}}) {
		t.Fatal("ordinary page controls plus a cookie footer link should stay on the fast JS path")
	}
	if ShouldAugmentAX([]Element{{Tag: "button", Text: "Add to basket"}}) {
		t.Fatal("ordinary controls should stay on the fast JS path")
	}
	if !ShouldAugmentAX([]Element{{Tag: "iframe"}}) {
		t.Fatal("visible iframe should use the cross-frame AX complement")
	}
}

func TestDestructiveEffectOnlyClassifiesActionableControls(t *testing.T) {
	for _, role := range []string{"textbox", "searchbox", "region", "document"} {
		if got := destructiveEffectForText("Post body", role); got != "" {
			t.Fatalf("draft surface role %q classified as %q", role, got)
		}
	}
	if got := destructiveEffectForText("Publish", "button"); got != "immediate_publish" {
		t.Fatalf("Publish button effect=%q", got)
	}
	if got := destructiveEffectForText("Withdraw funds", "menuitem"); got != "financial_action" {
		t.Fatalf("Withdraw menuitem effect=%q", got)
	}
	if got := destructiveEffectForText("Set publish date", "button"); got != "" {
		t.Fatalf("configuration opener was classified as consequential: %q", got)
	}
	if got := destructiveEffectForText("Schedule post", "button"); got != "schedule_publish" {
		t.Fatalf("final schedule effect=%q", got)
	}
}

func TestSnapshotBoxesUsesBackendIDsAndViewportOffsets(t *testing.T) {
	documents := []*domsnapshot.DocumentSnapshot{{
		ScrollOffsetX: 10,
		ScrollOffsetY: 20,
		Nodes: &domsnapshot.NodeTreeSnapshot{
			BackendNodeID: []cdp.BackendNodeID{101, 202},
		},
		Layout: &domsnapshot.LayoutTreeSnapshot{
			NodeIndex: []int64{1},
			Bounds:    []domsnapshot.Rectangle{{110, 220, 80, 30}},
		},
	}}

	got := snapshotBoxes(documents)[202]
	if got != (axBox{X: 100, Y: 200, W: 80, H: 30}) {
		t.Fatalf("snapshot box = %+v", got)
	}
}

func TestSnapshotModalMembersUsesDOMAncestry(t *testing.T) {
	stringsTable := []string{"#document", "form", "button", "role", "dialog"}
	documents := []*domsnapshot.DocumentSnapshot{{
		Nodes: &domsnapshot.NodeTreeSnapshot{
			ParentIndex:   []int64{-1, 0, 1},
			NodeName:      []domsnapshot.StringIndex{0, 1, 2},
			BackendNodeID: []cdp.BackendNodeID{100, 200, 300},
			Attributes: []domsnapshot.ArrayOfStrings{
				nil,
				{3, 4},
				nil,
			},
		},
		Layout: &domsnapshot.LayoutTreeSnapshot{
			NodeIndex: []int64{1, 2},
			Bounds:    []domsnapshot.Rectangle{{0, 400, 1600, 400}, {80, 730, 80, 30}},
		},
	}}
	boxes := snapshotBoxes(documents)
	hasModal, members := snapshotModalMembers(documents, stringsTable, boxes, 1600, 800)
	if !hasModal || !members[200] || !members[300] || members[100] {
		t.Fatalf("modal membership: has=%v members=%v", hasModal, members)
	}
}

func TestSnapshotModalQualificationRejectsOversizedRoleDialog(t *testing.T) {
	attrs := map[string]string{"role": "dialog"}
	if snapshotNodeQualifiesAsModal(attrs, "aside", axBox{X: 8, Y: -328, W: 262, H: 7811}, 1600, 800) {
		t.Fatal("oversized off-viewport role=dialog should not suppress page controls")
	}
	if !snapshotNodeQualifiesAsModal(attrs, "div", axBox{X: 300, Y: 180, W: 400, H: 220}, 1000, 600) {
		t.Fatal("centered role=dialog should remain modal")
	}
	if !snapshotNodeQualifiesAsModal(map[string]string{"aria-modal": "true"}, "div", axBox{X: 0, Y: -500, W: 400, H: 1200}, 1000, 600) {
		t.Fatal("aria-modal=true should remain authoritative")
	}
}
