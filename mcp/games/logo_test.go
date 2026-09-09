package main

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func saveLogoFixture(t *testing.T, ctx *sdk.AppCtx, scope GameScope, parent string) AssetVersion {
	t.Helper()
	value, err := assetSave(ctx, scope, map[string]any{"asset_id": "logo", "name": "Game logo", "kind": "sprite", "expected_parent": parent, "spec": AssetSpec{License: "Owned fixture"}}, spriteBytes(t), "image/png", nil)
	if err != nil {
		t.Fatal(err)
	}
	return value.(AssetVersion)
}

func updateLogoFixture(ctx *sdk.AppCtx, scope GameScope, version, expected string) error {
	_, err := gameAction(ctx, "logo_set", map[string]any{"game_id": scope.GameID, "_project_id": scope.ProjectID, "logo_version_id": version, "expected_logo_version_id": expected})
	return err
}

func TestGameLogoPinsAssetAndPreservesHistory(t *testing.T) {
	ctx, _, scope := contentFixture(t)
	logo := saveLogoFixture(t, ctx, scope, "")
	if err := updateLogoFixture(ctx, scope, logo.ID, ""); err != nil {
		t.Fatal(err)
	}
	newer := saveLogoFixture(t, ctx, scope, logo.ID)
	game, err := getGame(ctx.AppDB(), scope.ProjectID, scope.GameID)
	if err != nil || game.LogoVersionID != logo.ID {
		t.Fatal("logo followed the editing head", game, err)
	}
	if _, err = gameAction(ctx, "update", map[string]any{"game_id": scope.GameID, "name": "Renamed"}); err != nil {
		t.Fatal(err)
	}
	if err = updateLogoFixture(ctx, scope, newer.ID, ""); err == nil {
		t.Fatal("stale logo replacement accepted")
	}
	if err = updateLogoFixture(ctx, scope, newer.ID, logo.ID); err != nil {
		t.Fatal(err)
	}
	if err = initializeGames(ctx); err != nil {
		t.Fatal(err)
	}
	games, err := listGames(ctx.AppDB(), scope.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, game := range games {
		if game.ID == scope.GameID {
			found = game.LogoVersionID == newer.ID && game.Name == "Renamed"
		}
	}
	if !found {
		t.Fatal("logo missing from game catalog after restart")
	}
	if err = updateLogoFixture(ctx, scope, "", newer.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{logo.ID, newer.ID} {
		if _, err = assetVersionGet(ctx, scope, id); err != nil {
			t.Fatal("clearing logo lost an asset version", err)
		}
	}
	if _, _, err = contentBlob(ctx, scope, logo.Source); err != nil {
		t.Fatal("clearing logo lost source bytes", err)
	}
}

func TestGameLogoRejectsForeignAndUnsuitableAssets(t *testing.T) {
	ctx, _, scope := contentFixture(t)
	logo := saveLogoFixture(t, ctx, scope, "")
	other, err := gameAction(ctx, "create", map[string]any{"slug": "other", "name": "Other"})
	if err != nil {
		t.Fatal(err)
	}
	otherScope := other.(map[string]any)["game"].(*Game).Scope()
	if err = updateLogoFixture(ctx, otherScope, logo.ID, ""); err == nil {
		t.Fatal("foreign game logo accepted")
	}
	t.Setenv("APTEVA_PROJECT_ID", "")
	if err = updateLogoFixture(ctx, GameScope{"foreign", scope.GameID}, logo.ID, ""); err == nil {
		t.Fatal("foreign project logo accepted")
	}
	t.Setenv("APTEVA_PROJECT_ID", scope.ProjectID)
	for _, kind := range []string{"sprite", "blob", "music"} {
		value, err := assetSave(ctx, scope, map[string]any{"asset_id": "draft-" + kind, "name": "Draft", "kind": kind, "expected_parent": "", "spec": AssetSpec{License: "Fixture"}}, nil, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = updateLogoFixture(ctx, scope, value.(AssetVersion).ID, ""); err == nil {
			t.Fatal("unsuitable logo accepted", kind)
		}
	}
	if _, err = gameAction(ctx, "logo_set", map[string]any{"game_id": scope.GameID, "logo_version_id": logo.ID}); err == nil {
		t.Fatal("missing concurrency guard accepted")
	}
	if _, err = gameAction(ctx, "archive", map[string]any{"game_id": scope.GameID}); err != nil {
		t.Fatal(err)
	}
	if err = updateLogoFixture(ctx, scope, logo.ID, ""); err == nil {
		t.Fatal("archived game logo changed")
	}
}

func TestGameLogoIncludedInFrozenBuildAndRetainedAfterClear(t *testing.T) {
	ctx, _, scope := contentFixture(t)
	logo := saveLogoFixture(t, ctx, scope, "")
	hero := saveSprite(t, ctx, scope)
	if err := updateLogoFixture(ctx, scope, logo.ID, ""); err != nil {
		t.Fatal(err)
	}
	heroR := bakeFixture(t, ctx, scope, hero, "generic")
	logoR := bakeFixture(t, ctx, scope, logo, "generic")
	if _, err := contentFreeze(ctx, scope, map[string]any{"rendition_ids": []string{heroR.ID}}); err == nil {
		t.Fatal("build omitted game logo")
	}
	logoGodot := bakeFixture(t, ctx, scope, logo, "godot")
	if _, err := contentFreeze(ctx, scope, map[string]any{"rendition_ids": []string{heroR.ID, logoGodot.ID}}); err == nil {
		t.Fatal("build used logo for wrong target")
	}
	digest := freezeFixture(t, ctx, scope, heroR, logoR)
	if err := updateLogoFixture(ctx, scope, "", logo.ID); err != nil {
		t.Fatal(err)
	}
	manifest, err := contentManifestGet(ctx, scope, digest)
	if err != nil || manifest.Logo == nil || manifest.Logo.Asset != logo.AssetID || manifest.Logo.Version != logo.ID {
		t.Fatal("frozen logo changed", manifest.Logo, err)
	}
	archive, err := contentZip(ctx, scope, digest)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range z.File {
		if file.Name == logoR.Files[0].Path {
			r, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(r)
			r.Close()
			found = err == nil && contentSHA(data) == logoR.Files[0].SHA256
		}
	}
	if !found {
		t.Fatal("export lost logo image bytes")
	}
}

func TestGameLogoHTTPAndExistingDatabaseUpgrade(t *testing.T) {
	f := newFixture(t)
	if err := initializeContent(f.ctx); err != nil {
		t.Fatal(err)
	}
	logo := saveLogoFixture(t, f.ctx, f.pid, "")
	// Reproduce a populated v0.4 games table; the upgrade must keep its assets.
	if _, err := f.ctx.AppDB().Exec(`ALTER TABLE games DROP COLUMN logo_version_id`); err != nil {
		t.Fatal(err)
	}
	if err := initializeGames(f.ctx); err != nil {
		t.Fatal(err)
	}
	path := "/admin/games/" + f.pid.GameID + "/logo?project_id=" + f.pid.ProjectID
	values := map[string]string{"game_id": f.pid.GameID}
	w := doReq(f.app.handleGames, "POST", path, map[string]any{"logo_version_id": logo.ID, "expected_logo_version_id": ""}, values)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	w = doReq(f.app.handleGameLogo, "GET", path+"&version="+logo.ID, nil, values)
	if w.Code != http.StatusOK || contentSHA(w.Body.Bytes()) != logo.Source || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("logo image unavailable", w.Code, w.Header())
	}
	t.Setenv("APTEVA_PROJECT_ID", "")
	for _, bad := range []string{path + "&version=stale", "/admin/games/" + f.pid.GameID + "/logo?project_id=foreign"} {
		if w = doReq(f.app.handleGameLogo, "GET", bad, nil, values); w.Code == http.StatusOK {
			t.Fatal("foreign or stale logo served")
		}
	}
}
