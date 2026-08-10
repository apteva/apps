package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type mobileVersionAllocation struct {
	Platform    string
	Provider    string
	AppKey      string
	VersionName string
	BuildNumber string
	VersionCode string
}

// prepareMobileBuildTarget snapshots the effective target contract on the
// build. Missing store identifiers are allocated before any backend starts.
func (a *App) prepareMobileBuildTarget(d *Deployment, build *Build) (*Deployment, error) {
	if d == nil || build == nil || (d.TargetKind != "ios" && d.TargetKind != "android") {
		return d, nil
	}
	cfg, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return nil, err
	}
	strategy := strings.ToLower(strings.TrimSpace(cfg.VersionStrategy))
	if strategy != "" && strategy != "auto" && strategy != "manual" {
		return nil, errors.New("target_config_json.version_strategy must be auto or manual")
	}
	if cfg.VersionName == "" {
		if _, storeDoc, storeErr := a.mobileStoreConfig(d); storeErr == nil {
			cfg.VersionName = strings.TrimSpace(storeDoc.VersionName)
		}
	}
	if !cfg.SmokeOnly {
		switch d.TargetKind {
		case "ios":
			if cfg.BuildNumber == "" && (strategy == "auto" || (strategy == "" && mobileIntegrationAvailable("app_store"))) {
				allocation, allocErr := a.allocateIOSBuildNumber(d, build, cfg)
				if allocErr != nil {
					return nil, allocErr
				}
				cfg.AppStoreAppID = allocation.AppKey
				cfg.BuildNumber = allocation.BuildNumber
			}
		case "android":
			if cfg.VersionCode == "" && (strategy == "auto" || (strategy == "" && mobileIntegrationAvailable("play_store"))) {
				allocation, allocErr := a.allocateAndroidVersionCode(d, build, cfg)
				if allocErr != nil {
					return nil, allocErr
				}
				cfg.VersionCode = allocation.VersionCode
			}
		}
	}
	if strategy == "auto" {
		if d.TargetKind == "ios" && cfg.BuildNumber == "" {
			return nil, errors.New("automatic iOS version allocation requires an App Store Connect integration")
		}
		if d.TargetKind == "android" && cfg.VersionCode == "" {
			return nil, errors.New("automatic Android version allocation requires a Google Play integration")
		}
	}
	cfg.VersionStrategy = strategy
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{"target_config_json": string(raw)}); err != nil {
		return nil, err
	}
	build.TargetConfigJSON = string(raw)
	effective := *d
	effective.TargetConfigJSON = string(raw)
	return &effective, nil
}

func mobileIntegrationAvailable(role string) bool {
	if globalCtx == nil {
		return false
	}
	bound := globalCtx.IntegrationFor(role)
	return bound != nil && bound.Kind == "integration" && bound.ConnectionID > 0
}

func (a *App) allocateIOSBuildNumber(d *Deployment, build *Build, cfg mobileTargetConfig) (mobileVersionAllocation, error) {
	if cfg.VersionName == "" {
		return mobileVersionAllocation{}, errors.New("automatic iOS build-number allocation requires version_name in the store listing or target config")
	}
	bound, err := boundIntegration("app_store")
	if err != nil {
		return mobileVersionAllocation{}, err
	}
	appID := strings.TrimSpace(cfg.AppStoreAppID)
	if appID == "" {
		if cfg.BundleID == "" {
			return mobileVersionAllocation{}, errors.New("automatic iOS build-number allocation requires bundle_id or app_store_app_id")
		}
		apps, listErr := executeIntegration(bound, "list_apps", map[string]any{"bundle_id": cfg.BundleID, "limit": 2})
		if listErr != nil {
			return mobileVersionAllocation{}, listErr
		}
		appID = firstJSONAPIID(apps)
		if appID == "" {
			return mobileVersionAllocation{}, fmt.Errorf("App Store Connect has no app record for bundle id %s", cfg.BundleID)
		}
	}
	builds, err := executeIntegration(bound, "list_builds", map[string]any{
		"app_id": appID, "limit": 200, "sort": "-uploadedDate",
	})
	if err != nil {
		return mobileVersionAllocation{}, err
	}
	remoteMax := maxAppleBuildNumber(builds)
	a.mobileVersionMu.Lock()
	defer a.mobileVersionMu.Unlock()
	allocation := mobileVersionAllocation{
		Platform: "ios", Provider: "app_store_connect", AppKey: appID, VersionName: cfg.VersionName,
	}
	next, err := dbReserveMobileVersion(globalCtx.AppDB(), d, build.ID, allocation, remoteMax)
	if err != nil {
		return mobileVersionAllocation{}, err
	}
	allocation.BuildNumber = strconv.FormatInt(next, 10)
	return allocation, nil
}

func (a *App) allocateAndroidVersionCode(d *Deployment, build *Build, cfg mobileTargetConfig) (mobileVersionAllocation, error) {
	if cfg.PackageName == "" {
		return mobileVersionAllocation{}, errors.New("automatic Android version allocation requires package_name")
	}
	bound, err := boundIntegration("play_store")
	if err != nil {
		return mobileVersionAllocation{}, err
	}
	edit, err := executeIntegration(bound, "create_edit", map[string]any{"packageName": cfg.PackageName})
	if err != nil {
		return mobileVersionAllocation{}, err
	}
	editID := jsonStringAt(edit, "id")
	if editID == "" {
		return mobileVersionAllocation{}, errors.New("Google Play create_edit response missing id")
	}
	defer func() {
		_, _ = executeIntegration(bound, "delete_edit", map[string]any{"packageName": cfg.PackageName, "editId": editID})
	}()
	bundles, err := executeIntegration(bound, "list_bundles", map[string]any{"packageName": cfg.PackageName, "editId": editID})
	if err != nil {
		return mobileVersionAllocation{}, err
	}
	remoteMax := maxGoogleVersionCode(bundles)
	if tracks, trackErr := executeIntegration(bound, "list_tracks", map[string]any{"packageName": cfg.PackageName, "editId": editID}); trackErr == nil {
		if trackMax := maxGoogleTrackVersionCode(tracks); trackMax > remoteMax {
			remoteMax = trackMax
		}
	}
	a.mobileVersionMu.Lock()
	defer a.mobileVersionMu.Unlock()
	allocation := mobileVersionAllocation{
		Platform: "android", Provider: "google_play", AppKey: cfg.PackageName, VersionName: cfg.VersionName,
	}
	next, err := dbReserveMobileVersion(globalCtx.AppDB(), d, build.ID, allocation, remoteMax)
	if err != nil {
		return mobileVersionAllocation{}, err
	}
	allocation.VersionCode = strconv.FormatInt(next, 10)
	return allocation, nil
}

func dbReserveMobileVersion(db *sql.DB, d *Deployment, buildID int64, allocation mobileVersionAllocation, remoteMax int64) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	column := "build_number"
	if allocation.Platform == "android" {
		column = "version_code"
	}
	query := `SELECT COALESCE(MAX(CAST(` + column + ` AS INTEGER)), 0)
		FROM mobile_version_allocations WHERE provider = ? AND app_key = ?`
	args := []any{allocation.Provider, allocation.AppKey}
	if allocation.Platform == "ios" {
		query += ` AND version_name = ?`
		args = append(args, allocation.VersionName)
	}
	var localMax int64
	if err := tx.QueryRow(query, args...).Scan(&localMax); err != nil {
		return 0, err
	}
	next := remoteMax + 1
	if localMax >= next {
		next = localMax + 1
	}
	buildNumber, versionCode := "", ""
	if allocation.Platform == "ios" {
		buildNumber = strconv.FormatInt(next, 10)
	} else {
		versionCode = strconv.FormatInt(next, 10)
	}
	_, err = tx.Exec(`INSERT INTO mobile_version_allocations (
		deployment_id, environment_id, build_id, platform, provider, app_key,
		version_name, build_number, version_code, status, created_at, updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,'reserved',?,?)`,
		d.ID, d.EnvironmentID, buildID, allocation.Platform, allocation.Provider, allocation.AppKey,
		allocation.VersionName, buildNumber, versionCode, nowUTC(), nowUTC())
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

func dbSetMobileVersionStatus(db *sql.DB, buildID int64, status string) {
	_, _ = db.Exec(`UPDATE mobile_version_allocations SET status = ?, updated_at = ? WHERE build_id = ?`, status, nowUTC(), buildID)
}

func maxAppleBuildNumber(raw json.RawMessage) int64 {
	var payload struct {
		Data []struct {
			Attributes struct {
				Version string `json:"version"`
			} `json:"attributes"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &payload)
	var max int64
	for _, item := range payload.Data {
		if value, err := strconv.ParseInt(item.Attributes.Version, 10, 64); err == nil && value > max {
			max = value
		}
	}
	return max
}

func maxGoogleVersionCode(raw json.RawMessage) int64 {
	var payload struct {
		Bundles []struct {
			VersionCode json.Number `json:"versionCode"`
		} `json:"bundles"`
	}
	_ = json.Unmarshal(raw, &payload)
	var max int64
	for _, bundle := range payload.Bundles {
		if value, err := strconv.ParseInt(string(bundle.VersionCode), 10, 64); err == nil && value > max {
			max = value
		}
	}
	return max
}

func maxGoogleTrackVersionCode(raw json.RawMessage) int64 {
	var payload struct {
		Tracks []struct {
			Releases []struct {
				VersionCodes []string `json:"versionCodes"`
			} `json:"releases"`
		} `json:"tracks"`
	}
	_ = json.Unmarshal(raw, &payload)
	var max int64
	for _, track := range payload.Tracks {
		for _, release := range track.Releases {
			for _, rawCode := range release.VersionCodes {
				if value, err := strconv.ParseInt(rawCode, 10, 64); err == nil && value > max {
					max = value
				}
			}
		}
	}
	return max
}
