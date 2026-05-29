package main

import (
	"encoding/json"
	"testing"
)

func TestManifestParses(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "creators" {
		t.Fatalf("name = %q", m.Name)
	}
	if len(m.Requires.Apps) < 3 {
		t.Fatalf("expected storage, billing, and optional deps, got %#v", m.Requires.Apps)
	}
}

func TestMemberCanAccessAttachment(t *testing.T) {
	tierIDs, _ := json.Marshal([]int64{10})
	post := &Post{Status: "published", Visibility: "tier", TierIDs: tierIDs}
	att := &Attachment{Visibility: "inherit"}
	tierID := int64(10)
	member := &Member{Status: "active", TierID: &tierID}
	if !memberCanAccessAttachment(member, post, att) {
		t.Fatal("active member in gated tier should access inherited attachment")
	}

	otherTier := int64(11)
	member.TierID = &otherTier
	if memberCanAccessAttachment(member, post, att) {
		t.Fatal("member in another tier should not access tier-gated post")
	}

	publicPost := &Post{Status: "published", Visibility: "public"}
	publicAtt := &Attachment{Visibility: "public"}
	if !memberCanAccessAttachment(nil, publicPost, publicAtt) {
		t.Fatal("public post + public attachment should not require member")
	}

	privateAtt := &Attachment{Visibility: "private"}
	member.TierID = &tierID
	if memberCanAccessAttachment(member, post, privateAtt) {
		t.Fatal("private attachment should not be downloadable through member portal")
	}
}
