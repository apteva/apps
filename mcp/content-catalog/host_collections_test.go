package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func approvedVideo(t *testing.T, a *App, ctx *sdk.AppCtx, session string, file int64) string {
	t.Helper()
	id := attach(t, a, ctx, session, file)
	if _, err := a.assetReview(ctx, map[string]any{"asset_id": id, "review_status": "approved"}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestHostingCreatesSessionCollectionAndReusesAfterRename(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	if _, err := a.sessionUpdate(ctx, map[string]any{"id": session, "title": "Holly Hypnotized"}); err != nil {
		t.Fatal(err)
	}
	before, _ := sessionByID(ctx.AppDB(), "project-a", session)
	first := approvedVideo(t, a, ctx, session, 1)
	second := approvedVideo(t, a, ctx, session, 3)
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": first}); err != nil {
		t.Fatal(err)
	}
	s, err := sessionByID(ctx.AppDB(), "project-a", session)
	if err != nil {
		t.Fatal(err)
	}
	if s.HostCollectionID != "collection-1" || s.Revision != before.Revision+1 || p.collections[0]["name"] != "Holly Hypnotized" {
		t.Fatalf("collection not saved under the session name: session=%+v, collections=%v", s, p.collections)
	}
	if _, err = a.sessionUpdate(ctx, map[string]any{"id": session, "title": "Holly renamed", "notes": "Keep the same collection"}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.hostingRequest(ctx, map[string]any{"asset_id": second}); err != nil {
		t.Fatal(err)
	}
	if p.collectionCreates != 1 || p.collectionLists != 1 || p.starts != 2 || strings.Join(p.fetchedCollections, ",") != "collection-1,collection-1" {
		t.Fatalf("collection/upload calls wrong: %+v", p)
	}
}

func TestHostingCollectionOverridesRemainAuthoritative(t *testing.T) {
	for _, sessionOverride := range []bool{false, true} {
		t.Run(fmt.Sprint(sessionOverride), func(t *testing.T) {
			a, ctx, p, brand, session := setupCatalog(t)
			if _, err := a.brandUpdate(ctx, map[string]any{"id": brand, "host_collection_id": "brand-default"}); err != nil {
				t.Fatal(err)
			}
			expected := "brand-default"
			if sessionOverride {
				expected = "session-override"
				if _, err := a.sessionUpdate(ctx, map[string]any{"id": session, "host_collection_id": expected}); err != nil {
					t.Fatal(err)
				}
			}
			id := approvedVideo(t, a, ctx, session, 1)
			if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err != nil {
				t.Fatal(err)
			}
			if p.collectionCreates != 0 || p.collectionLists != 0 || p.fetchedCollections[0] != expected {
				t.Fatalf("configured collection ignored: %+v", p)
			}
		})
	}
}

func TestHostingCollectionTimeoutRecoversByObservationAfterRestart(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	id := approvedVideo(t, a, ctx, session, 1)
	p.collectionCreateError = errors.New("response timed out after creation")
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err == nil {
		t.Fatal("uncertain creation accepted")
	}
	if p.starts != 0 {
		t.Fatal("video uploaded despite uncertain collection")
	}
	// The remote collection exists, despite the lost response. A restarted App
	// reads the durable reservation and adopts it without another creation.
	a = &App{}
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err != nil {
		t.Fatal(err)
	}
	if p.collectionCreates != 1 || p.starts != 1 {
		t.Fatalf("unsafe retry: %+v", p)
	}
	s, _ := sessionByID(ctx.AppDB(), "project-a", session)
	if s.HostCollectionID != "collection-1" {
		t.Fatalf("recovered collection not saved: %+v", s)
	}
}

func TestUnresolvedCollectionNeverCreatesAgain(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	id := approvedVideo(t, a, ctx, session, 1)
	p.collectionCreateError = errors.New("ambiguous failure")
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err == nil {
		t.Fatal("expected creation error")
	}
	p.collections = nil
	if _, err := (&App{}).hostingRequest(ctx, map[string]any{"asset_id": id}); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("expected uncertain reservation: %v", err)
	}
	if p.collectionCreates != 1 || p.starts != 0 {
		t.Fatalf("retried an ambiguous mutation: %+v", p)
	}
	// An explicit operator-selected collection can resolve the blocked policy.
	if _, err := a.sessionUpdate(ctx, map[string]any{"id": session, "host_collection_id": "verified-by-operator"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err != nil {
		t.Fatal(err)
	}
	if p.collectionCreates != 1 || p.fetchedCollections[0] != "verified-by-operator" {
		t.Fatalf("explicit recovery failed: %+v", p)
	}
}

func TestCollectionLookupFailureCanRetryWithoutMutation(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	id := approvedVideo(t, a, ctx, session, 1)
	p.collectionListError = errors.New("host unavailable")
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err == nil {
		t.Fatal("lookup error ignored")
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM host_collections`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 || p.collectionCreates != 0 || p.starts != 0 {
		t.Fatal("failed read reserved or mutated the host")
	}
	p.collectionListError = nil
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionLookupPaginatesAndRejectsAmbiguousNames(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprint(duplicate), func(t *testing.T) {
			a, ctx, p, _, session := setupCatalog(t)
			for i := 0; i < 100; i++ {
				p.collections = append(p.collections, map[string]any{"guid": fmt.Sprint(i), "name": fmt.Sprintf("Other %d", i), "videoLibraryId": 42})
			}
			p.collections = append(p.collections, map[string]any{"guid": "existing", "name": "Shoot", "videoLibraryId": 42})
			if duplicate {
				p.collections = append(p.collections, map[string]any{"guid": "another", "name": "Shoot", "videoLibraryId": 42})
			}
			id := approvedVideo(t, a, ctx, session, 1)
			_, err := a.hostingRequest(ctx, map[string]any{"asset_id": id})
			if duplicate {
				if err == nil || p.starts != 0 {
					t.Fatalf("ambiguous name accepted: %v", err)
				}
			} else if err != nil || p.fetchedCollections[0] != "existing" {
				t.Fatalf("second page collection not reused: %v", err)
			}
			if p.collectionLists != 2 || p.collectionCreates != 0 {
				t.Fatalf("unexpected collection calls: %+v", p)
			}
		})
	}
}

func TestCollectionWrongLibraryCannotUpload(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	id := approvedVideo(t, a, ctx, session, 1)
	p.collectionResponseLibrary = "999"
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err == nil {
		t.Fatal("wrong library accepted")
	}
	if p.starts != 0 {
		t.Fatal("uploaded to an unverified library")
	}
	s, _ := sessionByID(ctx.AppDB(), "project-a", session)
	if s.HostCollectionID != "" {
		t.Fatal("stored wrong-library collection")
	}
}

func TestConcurrentHostingCreatesOnlyOneCollection(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	ids := []string{approvedVideo(t, a, ctx, session, 1), approvedVideo(t, a, ctx, session, 3)}
	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := a.hostingRequest(ctx, map[string]any{"asset_id": id})
			errs <- err
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if p.collectionCreates != 1 || p.starts != 2 {
		t.Fatalf("concurrent requests duplicated collection: %+v", p)
	}
}

func TestSeparateAppCannotRepeatPendingCollectionCreation(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	id := approvedVideo(t, a, ctx, session, 1)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	p.beforeCollectionCreate = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); done <- err }()
	<-entered
	// A second process has no shared Go mutex, but sees the DB reservation.
	result, err := (&App{}).hostingRequest(ctx, map[string]any{"asset_id": id})
	if err != nil || result.(map[string]any)["was_existing"] != true {
		t.Errorf("pending operation was not reused: %v, %v", result, err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p.collectionCreates != 1 || p.starts != 1 {
		t.Fatalf("durable reservation failed: %+v", p)
	}
}

func TestSessionEditDuringCreationIsPreservedAndRetryUsesSavedResult(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	id := approvedVideo(t, a, ctx, session, 1)
	p.beforeCollectionCreate = func() {
		if _, err := ctx.AppDB().Exec(`UPDATE sessions SET notes='concurrent edit',revision=revision+1 WHERE id=?`, session); err != nil {
			t.Error(err)
		}
	}
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale session write accepted: %v", err)
	}
	s, _ := sessionByID(ctx.AppDB(), "project-a", session)
	if s.Notes != "concurrent edit" || s.HostCollectionID != "" || p.starts != 0 {
		t.Fatalf("concurrent edit lost: %+v", s)
	}
	if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err != nil {
		t.Fatal(err)
	}
	s, _ = sessionByID(ctx.AppDB(), "project-a", session)
	if s.Notes != "concurrent edit" || s.HostCollectionID != "collection-1" || p.collectionCreates != 1 || p.collectionLists != 1 || p.starts != 1 {
		t.Fatalf("saved provider result not reused: %+v, %+v", s, p)
	}
}

func TestIneligibleHostingAndReadsNeverCreateCollections(t *testing.T) {
	for _, state := range []string{"pending", "archived_asset", "archived_session", "image", "missing_checksum"} {
		t.Run(state, func(t *testing.T) {
			a, ctx, p, _, session := setupCatalog(t)
			file := int64(1)
			if state == "image" {
				file = 2
			}
			id := attach(t, a, ctx, session, file)
			if state != "pending" {
				if _, err := a.assetReview(ctx, map[string]any{"asset_id": id, "review_status": "approved"}); err != nil {
					t.Fatal(err)
				}
			}
			switch state {
			case "archived_asset":
				_, _ = ctx.AppDB().Exec(`UPDATE assets SET lifecycle='archived' WHERE id=?`, id)
			case "archived_session":
				_, _ = ctx.AppDB().Exec(`UPDATE sessions SET lifecycle='archived' WHERE id=?`, session)
			case "missing_checksum":
				_, _ = ctx.AppDB().Exec(`UPDATE assets SET sha256='' WHERE id=?`, id)
			}
			if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err == nil {
				t.Fatal("ineligible request accepted")
			}
			if _, err := a.assetGet(ctx, map[string]any{"id": id}); err != nil {
				t.Fatal(err)
			}
			if _, err := a.sessionsList(ctx, map[string]any{}); err != nil {
				t.Fatal(err)
			}
			if p.collectionLists != 0 || p.collectionCreates != 0 || p.starts != 0 {
				t.Fatalf("ineligible/read call contacted host: %+v", p)
			}
		})
	}
}

func TestExistingHostingWithoutCollectionPolicyDoesNotCreateCollection(t *testing.T) {
	for _, backfill := range []bool{true, false} {
		t.Run(fmt.Sprint(backfill), func(t *testing.T) {
			a, ctx, p, _, session := setupCatalog(t)
			id := approvedVideo(t, a, ctx, session, 1)
			if backfill {
				p.videoCollection = "legacy"
				if _, err := a.hostingLinkExisting(ctx, map[string]any{"asset_id": id, "remote_id": "video-1", "connection_id": int64(21)}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := ctx.AppDB().Exec(`INSERT INTO hostings(id,project_id,asset_id,provider,connection_id,library_id,collection_id,source_sha256,remote_id,status) VALUES('old','project-a',?,'bunny',21,'42','legacy','sha-1','video-1','ready')`, id); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.hostingRequest(ctx, map[string]any{"asset_id": id}); err != nil {
				t.Fatal(err)
			}
			if p.starts != 0 || p.collectionLists != 0 || p.collectionCreates != 0 {
				t.Fatalf("existing video triggered mutation: %+v", p)
			}
		})
	}
}
