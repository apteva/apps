package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// The logo is a reference to an ordinary immutable asset version. Clearing or
// replacing the reference never changes that asset or a frozen content release.
func setGameLogo(ctx *sdk.AppCtx, game *Game, args map[string]any) error {
	version, ok := args["logo_version_id"].(string)
	expected, hasExpected := args["expected_logo_version_id"].(string)
	if !ok || !hasExpected {
		return errors.New("logo_version_id and expected_logo_version_id required; use empty strings to clear or for an unset logo")
	}
	if version != "" {
		asset, err := assetVersionGet(ctx, game.Scope(), version)
		if err != nil {
			return err
		}
		if asset.Kind != "sprite" || asset.Source == "" {
			return errors.New("save a PNG sprite asset with source bytes before selecting it as the logo")
		}
		if _, _, err = contentBlob(ctx, game.Scope(), asset.Source); err != nil {
			return err
		}
	}
	result, err := ctx.AppDB().Exec(`UPDATE games SET logo_version_id=? WHERE project_id=? AND id=? AND status='active' AND logo_version_id=?`, version, game.ProjectID, game.ID, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("game logo changed or game is archived; reload before saving")
	}
	return err
}

func (a *App) handleGameLogo(w http.ResponseWriter, r *http.Request) {
	project, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	ctx := getAppCtx(r)
	game, err := getGame(ctx.AppDB(), project, r.PathValue("game_id"))
	if err != nil || game.LogoVersionID == "" {
		httpErr(w, 404, "game logo not found")
		return
	}
	if version := r.URL.Query().Get("version"); version != "" && version != game.LogoVersionID {
		httpErr(w, 404, "game logo changed")
		return
	}
	asset, err := assetVersionGet(ctx, game.Scope(), game.LogoVersionID)
	if err != nil || asset.Kind != "sprite" || asset.Source == "" {
		httpErr(w, 404, "game logo asset unavailable")
		return
	}
	data, _, err := contentBlob(ctx, game.Scope(), asset.Source)
	if err != nil {
		httpErr(w, 404, "game logo source unavailable")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "logo.png", time.Time{}, bytes.NewReader(data))
}

func freezeGameLogo(ctx *sdk.AppCtx, scope GameScope, manifest *ContentManifest) error {
	game, err := getGame(ctx.AppDB(), scope.ProjectID, scope.GameID)
	if err != nil || game.LogoVersionID == "" {
		return err
	}
	logo, err := assetVersionGet(ctx, scope, game.LogoVersionID)
	if err != nil {
		return err
	}
	for _, target := range manifest.Assets {
		found := false
		for _, asset := range manifest.Assets {
			if asset.Asset == logo.AssetID && asset.Version == logo.ID && asset.Target == target.Target && asset.EngineVersion == target.EngineVersion && asset.Platform == target.Platform {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("bake and select game logo %s version %s for %s %s %s before freezing", logo.AssetID, logo.ID, target.Target, target.EngineVersion, target.Platform)
		}
	}
	manifest.Logo = &AssetDependency{Asset: logo.AssetID, Version: logo.ID}
	return nil
}
