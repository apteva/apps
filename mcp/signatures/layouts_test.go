package main

import (
	"testing"
	"time"
)

func modelReleaseLayoutArgs() ([]map[string]any, []map[string]any) {
	roles := []map[string]any{
		{"label": "Model", "role": "signer", "signing_order": 1},
		{"label": "Producer", "role": "signer", "signing_order": 1},
	}
	field := func(recipient int, fieldType string, x, y, w, h float64) map[string]any {
		return map[string]any{"recipient": recipient, "field_type": fieldType, "page": 1, "x": x, "y": y, "width": w, "height": h}
	}
	fields := []map[string]any{
		field(0, "signature", .11, .235, .52, .047), field(0, "text", .24, .289, .38, .02),
		field(1, "signature", .11, .4015, .52, .047), field(1, "date_signed", .67, .4055, .2, .03),
	}
	return roles, fields
}

func TestLayoutSaveApplyAndRoundTrip(t *testing.T) {
	db := testDB(t)
	roleSpecs, fieldSpecs := modelReleaseLayoutArgs()
	roles, err := parseLayoutRecipients(roleSpecs)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := parseLayoutFields(fieldSpecs, roles)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := saveLayout(db, "project-a", "", "Model release", "Two-party release", roles, fields)
	if err != nil {
		t.Fatal(err)
	}
	if layout.Slug != "model-release" || len(layout.Recipients) != 2 || len(layout.Fields) != 4 {
		t.Fatalf("unexpected layout: %+v", layout)
	}
	// Saving the same slug replaces it instead of duplicating.
	if _, err := saveLayout(db, "project-a", "model-release", "Model release v2", "", roles, fields); err != nil {
		t.Fatal(err)
	}
	if list, _ := listLayouts(db, "project-a"); len(list) != 1 || list[0].Name != "Model release v2" {
		t.Fatalf("upsert failed: %+v", list)
	}
	if list, _ := listLayouts(db, "project-b"); len(list) != 0 {
		t.Fatal("layout leaked across projects")
	}

	env, err := createEnvelope(db, "project-a", StorageFile{ID: 50, Name: "release.pdf", ContentType: "application/pdf"}, "h", "Release", "", "", time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	layout, _ = getLayout(db, "project-a", 0, "model-release")
	recipients, applied, err := applyLayout(db, "project-a", env.ID, layout, []map[string]any{{"name": "Jordan Rivera", "email": "jordan@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if recipients[0].Name != "Jordan Rivera" || recipients[1].Name != "Producer" || recipients[0].SigningOrder != 1 || recipients[1].SigningOrder != 1 {
		t.Fatalf("names/orders not applied: %+v", recipients)
	}
	if len(applied) != 4 {
		t.Fatalf("fields not applied: %+v", applied)
	}
	for _, f := range applied {
		wantRecipient := recipients[0].ID
		if f.Y > .3 {
			wantRecipient = recipients[1].ID
		}
		if f.RecipientID != wantRecipient {
			t.Fatalf("field %+v assigned to wrong recipient", f)
		}
	}
	if errs := validateEnvelope(db, "project-a", env.ID, 1, "manual"); len(errs) != 0 {
		t.Fatalf("layout-built envelope not sendable: %v", errs)
	}

	snapRoles, snapFields, err := layoutFromEnvelope(db, "project-a", env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapRoles) != 2 || snapRoles[0].Label != "Jordan Rivera" || len(snapFields) != 4 {
		t.Fatalf("snapshot mismatch: %+v %+v", snapRoles, snapFields)
	}
}

// Roles listed out of signing order must still get their own fields.
func TestApplyLayoutMapsRolesWhenOrderDiffersFromListing(t *testing.T) {
	db := testDB(t)
	roles, err := parseLayoutRecipients([]map[string]any{
		{"label": "Second", "signing_order": 2},
		{"label": "First", "signing_order": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := parseLayoutFields([]map[string]any{
		{"recipient": 0, "field_type": "signature", "page": 1, "x": .1, "y": .7, "width": .3, "height": .05, "label": "second"},
		{"recipient": 1, "field_type": "signature", "page": 1, "x": .1, "y": .1, "width": .3, "height": .05, "label": "first"},
	}, roles)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := saveLayout(db, "project-a", "ordered", "Ordered", "", roles, fields)
	if err != nil {
		t.Fatal(err)
	}
	env, err := createEnvelope(db, "project-a", StorageFile{ID: 51, Name: "x.pdf", ContentType: "application/pdf"}, "h", "X", "", "", time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	recipients, applied, err := applyLayout(db, "project-a", env.ID, layout, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[int64]string{}
	for _, r := range recipients {
		names[r.ID] = r.Name
	}
	for _, f := range applied {
		if (f.Label == "first") != (names[f.RecipientID] == "First") {
			t.Fatalf("field %q went to %q", f.Label, names[f.RecipientID])
		}
	}
}

func TestLayoutValidation(t *testing.T) {
	roles, _ := parseLayoutRecipients([]map[string]any{{"label": "Signer"}})
	if _, err := parseLayoutFields([]map[string]any{{"recipient": 0, "field_type": "text", "page": 1, "x": .1, "y": .1, "width": .2, "height": .05}}, roles); err == nil {
		t.Fatal("signer without a signature field accepted")
	}
	if _, err := parseLayoutFields([]map[string]any{{"recipient": 3, "field_type": "signature", "page": 1, "x": .1, "y": .1, "width": .2, "height": .05}}, roles); err == nil {
		t.Fatal("out-of-range role index accepted")
	}
	if _, err := parseLayoutFields([]map[string]any{{"recipient": 0, "field_type": "signature", "page": 1, "x": .9, "y": .1, "width": .2, "height": .05}}, roles); err == nil {
		t.Fatal("field outside the page accepted")
	}
	if _, err := parseLayoutRecipients([]map[string]any{{"label": "A", "signing_order": 2}}); err == nil {
		t.Fatal("signing order gap accepted")
	}
	if _, err := normalizeLayoutSlug("Bad Slug!", ""); err == nil {
		t.Fatal("invalid slug accepted")
	}
}
