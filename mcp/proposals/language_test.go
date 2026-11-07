package main

import (
	"context"
	"testing"
)

func TestProposalLanguage(t *testing.T) {
	for _, language := range []string{"", "en", "fr", " FR "} {
		c := sampleContent()
		c.Language = language
		if err := c.validate(true); err != nil {
			t.Fatalf("%q: %v", language, err)
		}
		expected := "en"
		if language == "fr" || language == " FR " {
			expected = "fr"
		}
		if c.Language != expected {
			t.Fatalf("%q normalized to %q", language, c.Language)
		}
	}
	c := sampleContent()
	c.Language = "es"
	if err := c.validate(false); err == nil {
		t.Fatal("unsupported language accepted")
	}
}

func TestLanguagePersistsPerRevision(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	c := sampleContent()
	c.Language = "fr"
	d, err := a.store.create(ctx, a.project, c)
	if err != nil {
		t.Fatal(err)
	}
	r := d.Revisions[0]
	published, err := a.store.publish(ctx, a.project, d.ID, r.ID, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	live := published.Revisions[0]
	draft, err := a.store.revise(ctx, a.project, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	next := draft.Revisions[0]
	if next.Content.Language != "fr" {
		t.Fatal("revision lost French language")
	}
	next.Content.Language = "en"
	if _, err := a.store.update(ctx, a.project, d.ID, next.ID, next.Version, next.Content); err != nil {
		t.Fatal(err)
	}
	offer, err := a.store.offer(ctx, a.store.db, a.project, a.store.token(a.project, live))
	if err != nil || offer.Content.Language != "fr" {
		t.Fatalf("draft changed live language: %+v, %v", offer, err)
	}
	offer.State = "expired"
	view := publicView(offer)
	if view.Content != nil || view.Language != "fr" {
		t.Fatal("closed offer lost its language")
	}
}
