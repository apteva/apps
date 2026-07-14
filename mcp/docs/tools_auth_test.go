package main

import (
	"context"
	"strconv"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestTemplateAuthorizationUsesResolvedResource(t *testing.T) {
	caller := &sdk.Caller{
		DefaultEffect: "deny",
		Resources:     []sdk.ResourceDecl{{Name: "template", Matcher: "id_set"}},
		Grants: []sdk.Grant{{
			Effect: "allow", Permission: "docs.read", Resource: "7",
		}},
	}
	ctx := sdk.WithCaller(context.Background(), caller)
	if err := authorizeTemplate(ctx, "docs.read", 7); err != nil {
		t.Fatalf("allowed template: %v", err)
	}
	if err := authorizeTemplate(ctx, "docs.read", 8); err == nil {
		t.Fatal("different template should be denied")
	}
	if err := authorizeBroad(ctx, "docs.read"); err == nil {
		t.Fatal("resource grant must not authorize broad access")
	}
}

func TestNilCallerRemainsBackwardCompatible(t *testing.T) {
	if err := authorizeTemplate(context.Background(), "docs.read", 7); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizedRenderPaginationFiltersBeforeLimit(t *testing.T) {
	db := testDB(t)
	allowedID, _ := createTemplate(db, &Template{Slug: "allowed", Name: "Allowed", Body: "# A"})
	deniedID, _ := createTemplate(db, &Template{Slug: "denied", Name: "Denied", Body: "# D"})
	if _, err := db.Exec(`INSERT INTO renders (template_id, template_slug, output_file_id, data_snapshot, rendered_at)
		VALUES (?, 'allowed', 'a', '{}', '2026-01-01 00:00:00'),
		       (?, 'denied', 'd', '{}', '2026-01-02 00:00:00')`, allowedID, deniedID); err != nil {
		t.Fatal(err)
	}
	caller := &sdk.Caller{
		DefaultEffect: "deny",
		Resources:     []sdk.ResourceDecl{{Name: "template", Matcher: "id_set"}},
		Grants:        []sdk.Grant{{Effect: "allow", Permission: "docs.read", Resource: strconv.FormatInt(allowedID, 10)}},
	}
	rows, err := listAuthorizedRenders(db, caller, RenderFilters{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TemplateID != allowedID {
		t.Fatalf("rows = %+v", rows)
	}
}
