package main

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SocialCast", "socialcast"},
		{"Paid Kit", "paid-kit"},
		{"  Hypno Beauties  ", "hypno-beauties"},
		{"Marco's Personal Brand", "marco-s-personal-brand"},
		{"hello/world__test", "hello-world-test"},
		{"", "profile"},
		{"!!!", "profile"},
		{strings.Repeat("a", 100), strings.Repeat("a", 64)},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestProfileCreate_FirstAutoDefaults(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}

	out, err := app.toolProfileCreate(ctx, map[string]any{"name": "SocialCast"})
	if err != nil {
		t.Fatal(err)
	}
	p := out.(map[string]any)["profile"].(*Profile)
	if !p.IsDefault {
		t.Error("first profile should auto-default")
	}
	if p.Slug != "socialcast" {
		t.Errorf("slug = %q, want socialcast", p.Slug)
	}
}

func TestProfileCreate_SlugCollisionGetsSuffix(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}

	app.toolProfileCreate(ctx, map[string]any{"name": "Acme"})
	out, _ := app.toolProfileCreate(ctx, map[string]any{"name": "Acme"})
	p := out.(map[string]any)["profile"].(*Profile)
	if p.Slug == "acme" {
		t.Errorf("expected disambiguated slug, got %q", p.Slug)
	}
	if !strings.HasPrefix(p.Slug, "acme-") {
		t.Errorf("slug should start with acme-, got %q", p.Slug)
	}
}

func TestProfileCreate_SecondPromoteTakesDefault(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}

	app.toolProfileCreate(ctx, map[string]any{"name": "First"})
	out, _ := app.toolProfileCreate(ctx, map[string]any{"name": "Second", "is_default": true})
	second := out.(map[string]any)["profile"].(*Profile)
	if !second.IsDefault {
		t.Error("explicit is_default=true should take effect")
	}
	// First should now be demoted.
	listOut, _ := app.toolProfileList(ctx, map[string]any{})
	rows := listOut.(map[string]any)["profiles"].([]Profile)
	for _, p := range rows {
		if p.Slug == "first" && p.IsDefault {
			t.Error("previous default should have been demoted")
		}
	}
}

// resolveProfileArg returns -1 for an unknown slug — caller error,
// not 'no scope'. Account_add and post_create both surface this as
// a 4xx rather than silently widening to project-wide.
func TestResolveProfileArg(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}

	app.toolProfileCreate(ctx, map[string]any{"name": "Acme"})

	// Empty: 0 (no scope).
	if got := resolveProfileArg(ctx, "test-proj", map[string]any{}); got != 0 {
		t.Errorf("empty args: got %d, want 0", got)
	}
	// Known slug: resolves to id.
	got := resolveProfileArg(ctx, "test-proj", map[string]any{"profile": "acme"})
	if got <= 0 {
		t.Errorf("known slug: got %d, want positive id", got)
	}
	// Unknown slug: -1 (loud failure).
	if got := resolveProfileArg(ctx, "test-proj", map[string]any{"profile": "nope"}); got != -1 {
		t.Errorf("unknown slug: got %d, want -1", got)
	}
	// Numeric id wins over slug.
	if got := resolveProfileArg(ctx, "test-proj", map[string]any{"profile_id": 42}); got != 42 {
		t.Errorf("numeric arg: got %d, want 42", got)
	}
}

// Deleting a profile reassigns its accounts/posts, refuses if it's
// the default with siblings still present.
func TestProfileDelete_RefusesDefaultWithSiblings(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}

	out, _ := app.toolProfileCreate(ctx, map[string]any{"name": "Default"})
	def := out.(map[string]any)["profile"].(*Profile)
	app.toolProfileCreate(ctx, map[string]any{"name": "Other"})

	delOut, err := app.toolProfileDelete(ctx, map[string]any{"id": def.ID})
	if err != nil {
		t.Fatal(err)
	}
	res := delOut.(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError=true, got %+v", res)
	}
}

func TestProfileDelete_ReassignsAccountsAndPosts(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}

	out, _ := app.toolProfileCreate(ctx, map[string]any{"name": "From"})
	from := out.(map[string]any)["profile"].(*Profile)
	out2, _ := app.toolProfileCreate(ctx, map[string]any{"name": "To"})
	to := out2.(map[string]any)["profile"].(*Profile)

	// Seed an account + post on the From profile.
	ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status, profile_id)
		 VALUES ('test-proj', 'twitter', 99, 'x', 'active', ?)`, from.ID)
	ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status, profile_id)
		 VALUES ('test-proj', 'hi', 'published', ?)`, from.ID)

	// Demote From so we can delete it.
	app.toolProfileUpdate(ctx, map[string]any{"id": to.ID, "is_default": true})

	delOut, err := app.toolProfileDelete(ctx, map[string]any{
		"id":          from.ID,
		"reassign_to": to.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delOut.(map[string]any)["deleted"] != from.ID {
		t.Errorf("delete result: %+v", delOut)
	}

	// Account + post are now on the target profile.
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM social_accounts WHERE profile_id=?`, to.ID).Scan(&n)
	if n != 1 {
		t.Errorf("account didn't move: %d on target", n)
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts WHERE profile_id=?`, to.ID).Scan(&n)
	if n != 1 {
		t.Errorf("post didn't move: %d on target", n)
	}
}
