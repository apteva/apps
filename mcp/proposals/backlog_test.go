package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func pricePointer(amount int64) *int64 { return &amount }

func TestBacklogSnapshotIsolationAndRetry(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	c, err := a.store.clientSave(ctx, a.project, 0, 0, client{Name: "Northwind", Email: "client@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.store.backlogSave(ctx, a.project, c.ID, 0, 0, backlogItem{
		Name: "Stripe billing", Category: "Integrations", AmountMinor: pricePointer(250000), Currency: "EUR", Notes: "INTERNAL margin target",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.store.backlogSave(ctx, a.project, c.ID, 0, 0, backlogItem{
		Name: "Do not share yet", Category: "Later", Currency: "EUR", Notes: "PRIVATE",
	})
	if err != nil {
		t.Fatal(err)
	}
	in := input{ClientID: c.ID, ItemIDs: []int64{first.ID}, RequestKey: "new-proposal-1"}
	var created detail
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			got, err := a.store.importBacklog(ctx, a.project, in)
			if err != nil {
				t.Error(err)
				return
			}
			if got.Revisions[0].Content.Categories[0].Items[0].AmountMinor != 250000 {
				t.Error("incorrect snapshot price")
			}
		})
	}
	wg.Wait()
	created, err = a.store.importBacklog(ctx, a.project, in)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := a.store.db.QueryRow("SELECT COUNT(*) FROM proposals").Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry created %d proposals, error %v", count, err)
	}
	draft := created.Revisions[0]
	draft.Content.Sender = "Studio"
	created, err = a.store.update(ctx, a.project, created.ID, draft.ID, draft.Version, draft.Content)
	if err != nil {
		t.Fatal(err)
	}
	draft = created.Revisions[0]
	published, err := a.store.publish(ctx, a.project, created.ID, draft.ID, draft.Version)
	if err != nil {
		t.Fatal(err)
	}
	first.Name, first.AmountMinor = "Updated title", pricePointer(900000)
	if _, err := a.store.backlogSave(ctx, a.project, c.ID, first.ID, first.Version, first); err != nil {
		t.Fatal(err)
	}
	publishedRevision := published.Revisions[0]
	view := publicView(publishedRevision)
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"INTERNAL", "PRIVATE", "Do not share yet", "backlog_item_id", "client_id", "900000", "Updated title"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("public projection contains %q: %s", forbidden, raw)
		}
	}
	if view.Content.Categories[0].Items[0].AmountMinor != 250000 {
		t.Fatal("backlog edit changed published price")
	}
	if publishedRevision.Content.Categories[0].Items[0].BacklogItemID != first.ID {
		t.Fatal("public projection mutated internal metadata")
	}
	if _, err := a.store.importBacklog(ctx, a.project, input{ClientID: c.ID, ItemIDs: []int64{second.ID}, RequestKey: "unpriced-attempt"}); err == nil {
		t.Fatal("an unpriced item was imported as free")
	}
}

func TestBacklogClientAndDraftBoundaries(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	one, err := a.store.clientSave(ctx, a.project, 0, 0, client{Name: "One"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := a.store.clientSave(ctx, a.project, 0, 0, client{Name: "Two"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := a.store.backlogSave(ctx, a.project, one.ID, 0, 0, backlogItem{Name: "Feature", Category: "Build", Currency: "EUR", AmountMinor: pricePointer(100)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.backlogList(ctx, "other-project", one.ID, false, 0, 100); err != errNotFound {
		t.Fatalf("cross-project backlog read: %v", err)
	}
	if _, err := a.store.importBacklog(ctx, a.project, input{ClientID: two.ID, ItemIDs: []int64{b.ID}, RequestKey: "wrong-client"}); err == nil {
		t.Fatal("cross-client import succeeded")
	}
	created, err := a.store.importBacklog(ctx, a.project, input{ClientID: one.ID, ItemIDs: []int64{b.ID}, RequestKey: "right-client"})
	if err != nil {
		t.Fatal(err)
	}
	draft := created.Revisions[0]
	again, err := a.store.importBacklog(ctx, a.project, input{ClientID: one.ID, ItemIDs: []int64{b.ID}, ProposalID: created.ID, RevisionID: draft.ID, Version: draft.Version})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Revisions[0].Content.Categories[0].Items) != 1 {
		t.Fatal("duplicate import appended a second item")
	}
	if _, err := a.store.importBacklog(ctx, a.project, input{ClientID: one.ID, ItemIDs: []int64{b.ID}, ProposalID: created.ID, RevisionID: draft.ID, Version: draft.Version}); err != errConflict {
		t.Fatalf("stale draft version allowed: %v", err)
	}
	b.Archived = true
	if _, err := a.store.backlogSave(ctx, a.project, one.ID, b.ID, b.Version, b); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.importBacklog(ctx, a.project, input{ClientID: one.ID, ItemIDs: []int64{b.ID}, RequestKey: "archived-attempt"}); err == nil {
		t.Fatal("archived item imported")
	}
}

func TestBacklogNeedsSeparateClientReadGrant(t *testing.T) {
	a := newTestApp(t)
	caller := &sdk.Caller{DefaultEffect: "deny", Grants: []sdk.Grant{{Permission: "proposals.edit", Resource: "*", Effect: "allow"}}}
	ctx := sdk.WithCaller(context.Background(), caller)
	for _, op := range operations {
		if op.name == "backlog_create_proposal" {
			if _, err := a.execute(ctx, op, input{ClientID: 1, ItemIDs: []int64{1}, RequestKey: "private-data"}); err != errForbidden {
				t.Fatalf("backlog copied without client read grant: %v", err)
			}
		}
	}
}

func TestBacklogMigrationPreservesExistingProposals(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "app.db")+"?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	base, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(base)); err != nil {
		t.Fatal(err)
	}
	s := store{db: db}
	ctx := context.Background()
	before, err := s.create(ctx, "legacy-project", sampleContent())
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/002_client_backlog.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	after, err := s.get(ctx, "legacy-project", before.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("client backlog migration changed an existing proposal")
	}
}
