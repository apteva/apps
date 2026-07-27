package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var googlePlayUploadBaseURL = "https://androidpublisher.googleapis.com/upload/androidpublisher/v3"

type releaseOptions struct {
	Channel         string            `json:"channel,omitempty"`
	RolloutFraction float64           `json:"rollout_fraction,omitempty"`
	ReleaseNotes    map[string]string `json:"release_notes,omitempty"`
	SubmitForReview bool              `json:"submit_for_review,omitempty"`
	BetaGroupID     string            `json:"beta_group_id,omitempty"`
}

type mobileReleaseMeta struct {
	Platform           string            `json:"platform"`
	PackageName        string            `json:"package_name,omitempty"`
	AppID              string            `json:"app_id,omitempty"`
	BundleID           string            `json:"bundle_id,omitempty"`
	VersionName        string            `json:"version_name,omitempty"`
	BuildNumber        string            `json:"build_number,omitempty"`
	VersionCode        string            `json:"version_code,omitempty"`
	BetaGroupID        string            `json:"beta_group_id,omitempty"`
	AppStoreVersionID  string            `json:"app_store_version_id,omitempty"`
	ReviewSubmissionID string            `json:"review_submission_id,omitempty"`
	ReleaseType        string            `json:"release_type,omitempty"`
	RolloutFraction    float64           `json:"rollout_fraction,omitempty"`
	ReleaseNotes       map[string]string `json:"release_notes,omitempty"`
	SubmitForReview    bool              `json:"submit_for_review,omitempty"`
	Prepared           bool              `json:"prepared,omitempty"`
}

func isMobileDeployment(d *Deployment, b *Build) bool {
	if d != nil && (d.TargetKind == "android" || d.TargetKind == "ios") {
		return true
	}
	return b != nil && (b.Framework == "android" || b.Framework == "ios")
}

func (a *App) runMobileRelease(d *Deployment, b *Build, opts releaseOptions) (*Release, error) {
	if b.Status != "succeeded" {
		return nil, fmt.Errorf("build %d not succeeded (status=%s)", b.ID, b.Status)
	}
	if !buildArtifactAvailable(b) {
		return nil, fmt.Errorf("build %d artifact has been pruned; rebuild before releasing", b.ID)
	}
	if cfg, cfgErr := parseMobileTargetConfig(d.TargetConfigJSON); cfgErr == nil && cfg.SmokeOnly {
		return nil, errors.New("smoke_only mobile builds cannot be published; disable smoke_only and create a signed build")
	}
	manifest, err := readArtifactManifest(b)
	if err != nil {
		return nil, err
	}
	platform := manifest.Platform
	if platform == "" {
		platform = b.Framework
	}
	channel, err := normalizeMobileChannel(platform, opts.Channel)
	if err != nil {
		return nil, err
	}
	rel, err := dbCreateReleaseForEnv(globalCtx.AppDB(), d.ID, d.EnvironmentID, b.ID)
	if err != nil {
		return nil, err
	}
	logPath, logFile, err := a.openMobileReleaseLog(rel.ID)
	if err != nil {
		_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{"status": "failed", "error": err.Error()})
		return nil, err
	}
	defer logFile.Close()
	provider := map[string]string{"android": "google_play", "ios": "app_store_connect"}[platform]
	meta := mobileReleaseMeta{
		Platform: platform, PackageName: manifest.PackageName, BundleID: manifest.BundleID,
		VersionName: manifest.VersionName, BuildNumber: manifest.BuildNumber, VersionCode: manifest.VersionCode,
		RolloutFraction: opts.RolloutFraction, ReleaseNotes: opts.ReleaseNotes,
		SubmitForReview: opts.SubmitForReview, BetaGroupID: opts.BetaGroupID,
	}
	if cfg, cfgErr := parseMobileTargetConfig(d.TargetConfigJSON); cfgErr == nil {
		if meta.PackageName == "" {
			meta.PackageName = cfg.PackageName
		}
		if meta.BundleID == "" {
			meta.BundleID = cfg.BundleID
		}
		if meta.AppID == "" {
			meta.AppID = cfg.AppStoreAppID
		}
		if meta.BetaGroupID == "" {
			meta.BetaGroupID = cfg.BetaGroupID
		}
		meta.ReleaseType = cfg.ReleaseType
	}
	metaJSON := mustJSON(meta)
	_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
		"channel": channel, "provider": provider, "external_status": "publishing",
		"release_meta_json": metaJSON, "started_at": nowUTC(), "log_path": logPath,
	})
	fmt.Fprintf(logFile, "=== publish build %d to %s/%s ===\n", b.ID, provider, channel)

	if manifest.ExternalProvider != "" {
		if manifest.ExternalProvider != provider {
			err := fmt.Errorf("cloud artifact provider %q does not match release provider %q", manifest.ExternalProvider, provider)
			_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
				"status": "failed", "error": err.Error(), "external_status": "failed",
			})
			return nil, err
		}
		if manifest.Channel != "" && manifest.Channel != channel {
			err := fmt.Errorf("cloud backend uploaded to channel %q, not requested channel %q", manifest.Channel, channel)
			_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
				"status": "failed", "error": err.Error(), "external_status": "failed",
			})
			return nil, err
		}
		externalID := strings.TrimSpace(manifest.ExternalID)
		externalStatus := manifest.ExternalStatus
		releaseStatus := "starting"
		if platform == "android" {
			if meta.VersionCode == "" {
				err := errors.New("Android store_upload result requires version_code")
				_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
					"status": "failed", "error": err.Error(), "external_status": "failed",
				})
				return nil, err
			}
			externalID = meta.VersionCode
			externalStatus = defaultStr(externalStatus, "completed")
			releaseStatus = "live"
		} else {
			if externalID == "" {
				externalID = strconv.FormatInt(b.ID, 10)
			}
			if !strings.HasPrefix(externalID, "uploaded-") {
				externalID = "uploaded-" + externalID
			}
			externalStatus = defaultStr(externalStatus, "uploaded_processing")
		}
		_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
			"status": releaseStatus, "external_id": externalID, "external_status": externalStatus,
			"release_meta_json": mustJSON(meta),
		})
		_ = dbAppendReleaseEvent(globalCtx.AppDB(), rel.ID, "upload_adopted", mustJSON(map[string]any{
			"provider": provider, "build_backend": b.BuildBackend, "external_job_id": b.ExternalJobID,
		}))
		fmt.Fprintf(logFile, "cloud backend already uploaded artifact; waiting for store processing\n")
		return dbGetRelease(globalCtx.AppDB(), rel.ID)
	}

	switch platform {
	case "android":
		err = a.publishAndroidRelease(rel.ID, b, manifest, channel, &meta, logFile)
	case "ios":
		err = a.publishIOSRelease(rel.ID, b, manifest, channel, &meta, logFile)
	default:
		err = fmt.Errorf("unsupported mobile platform %q", platform)
	}
	if err != nil {
		fmt.Fprintf(logFile, "publish failed: %v\n", err)
		_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
			"status": "failed", "stopped_at": nowUTC(), "error": err.Error(),
			"external_status": "failed", "release_meta_json": mustJSON(meta),
		})
		_ = dbAppendReleaseEvent(globalCtx.AppDB(), rel.ID, "publish_failed", mustJSON(map[string]any{"error": err.Error()}))
		emit("deploy.release.failed", map[string]any{"deployment_id": d.ID, "release_id": rel.ID, "provider": provider, "channel": channel, "error": err.Error()})
		return nil, fmt.Errorf("mobile release %d failed: %w", rel.ID, err)
	}
	_ = dbAppendReleaseEvent(globalCtx.AppDB(), rel.ID, "published", mustJSON(map[string]any{"provider": provider, "channel": channel}))
	emit("deploy.release.published", map[string]any{"deployment_id": d.ID, "environment": d.EnvironmentName, "release_id": rel.ID, "provider": provider, "channel": channel})
	a.pruneBuildArtifactsAsync("mobile_release_published")
	return dbGetRelease(globalCtx.AppDB(), rel.ID)
}

func (a *App) publishAndroidRelease(releaseID int64, b *Build, manifest artifactManifest, channel string, meta *mobileReleaseMeta, logW io.Writer) error {
	if meta.PackageName == "" {
		return errors.New("Android release requires target_config_json.package_name")
	}
	primary := filepath.Join(b.ArtifactPath, manifest.Primary)
	if err := ensureAndroidBundleSigned(primary, true, logW); err != nil {
		return err
	}
	for i := range manifest.Files {
		if manifest.Files[i].Name == manifest.Primary {
			manifest.Files[i] = mobileArtifactFile(primary, "aab")
		}
	}
	if body, err := json.Marshal(manifest); err == nil {
		_ = dbUpdateBuild(globalCtx.AppDB(), b.ID, map[string]any{"artifact_manifest_json": string(body)})
	}
	bound, err := boundIntegration("play_store")
	if err != nil {
		return err
	}
	create, err := executeIntegration(bound, "create_edit", map[string]any{"packageName": meta.PackageName})
	if err != nil {
		return err
	}
	editID := jsonStringAt(create, "id")
	if editID == "" {
		return errors.New("Google Play create_edit response missing id")
	}
	fmt.Fprintf(logW, "created Google Play edit %s\n", editID)
	committed := false
	defer func() {
		if !committed {
			_, _ = executeIntegration(bound, "delete_edit", map[string]any{"packageName": meta.PackageName, "editId": editID})
		}
	}()
	upload, err := uploadGooglePlayBundle(bound, meta.PackageName, editID, primary, logW)
	if err != nil {
		return fmt.Errorf("upload AAB: %w", err)
	}
	meta.VersionCode = jsonScalarStringAt(upload, "versionCode")
	if meta.VersionCode == "" {
		return errors.New("Google Play upload_bundle response missing versionCode")
	}
	fmt.Fprintf(logW, "uploaded AAB versionCode=%s\n", meta.VersionCode)
	status := "completed"
	release := map[string]any{"versionCodes": []string{meta.VersionCode}, "status": status}
	if channel == "production" && meta.RolloutFraction > 0 && meta.RolloutFraction < 1 {
		status = "inProgress"
		release["status"] = status
		release["userFraction"] = meta.RolloutFraction
	}
	if notes := googleReleaseNotes(meta.ReleaseNotes); len(notes) > 0 {
		release["releaseNotes"] = notes
	}
	if _, err := executeIntegration(bound, "update_track", map[string]any{
		"packageName": meta.PackageName, "editId": editID, "track": channel,
		"releases": []map[string]any{release},
	}); err != nil {
		return fmt.Errorf("update Play track: %w", err)
	}
	if _, err := executeIntegration(bound, "commit_edit", map[string]any{"packageName": meta.PackageName, "editId": editID}); err != nil {
		return fmt.Errorf("commit Play edit: %w", err)
	}
	committed = true
	_ = dbUpdateRelease(globalCtx.AppDB(), releaseID, map[string]any{
		"status": "live", "external_id": meta.VersionCode, "external_status": status,
		"release_meta_json": mustJSON(meta),
	})
	fmt.Fprintf(logW, "Google Play track %s committed (%s)\n", channel, status)
	return nil
}

func uploadGooglePlayBundle(bound *sdk.BoundIntegration, packageName, editID, bundlePath string, logW io.Writer) (json.RawMessage, error) {
	info, err := os.Stat(bundlePath)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(googlePlayUploadBaseURL, "/") +
		"/applications/" + url.PathEscape(packageName) +
		"/edits/" + url.PathEscape(editID) + "/bundles?uploadType=media"
	client := &http.Client{Timeout: 30 * time.Minute}
	for attempt := 0; attempt < 2; attempt++ {
		creds, err := globalCtx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
		if err != nil {
			return nil, fmt.Errorf("read Google Play credentials: %w", err)
		}
		token := firstNonEmpty(creds.Fields["token"], creds.Fields["access_token"], creds.Fields["bearer_token"])
		if token == "" {
			return nil, errors.New("Google Play connection has no OAuth access token")
		}
		bundle, err := os.Open(bundlePath)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, endpoint, bundle)
		if err != nil {
			bundle.Close()
			return nil, err
		}
		req.ContentLength = info.Size()
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/octet-stream")
		fmt.Fprintf(logW, "streaming AAB upload (%d bytes)\n", info.Size())
		resp, requestErr := client.Do(req)
		bundle.Close()
		if requestErr != nil {
			return nil, requestErr
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			if _, err := executeIntegration(bound, "get_edit", map[string]any{"packageName": packageName, "editId": editID}); err != nil {
				return nil, fmt.Errorf("refresh Google Play OAuth after upload authorization failed: %w", err)
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("Google Play upload returned HTTP %d: %s", resp.StatusCode, truncateString(string(responseBody), 800))
		}
		return json.RawMessage(responseBody), nil
	}
	return nil, errors.New("Google Play upload authorization failed after OAuth refresh")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (a *App) publishIOSRelease(releaseID int64, b *Build, manifest artifactManifest, channel string, meta *mobileReleaseMeta, logW io.Writer) error {
	if runtime.GOOS != "darwin" {
		return errors.New("iOS upload requires a macOS Deploy host with Xcode")
	}
	if meta.BundleID == "" {
		return errors.New("iOS release requires a bundle_id in the artifact or target_config_json")
	}
	if meta.VersionName == "" || meta.BuildNumber == "" {
		return errors.New("iOS release requires version_name and build_number in the artifact manifest or target_config_json")
	}
	bound, err := boundIntegration("app_store")
	if err != nil {
		return err
	}
	if meta.AppID == "" {
		apps, err := executeIntegration(bound, "list_apps", map[string]any{"bundle_id": meta.BundleID, "limit": 2})
		if err != nil {
			return err
		}
		meta.AppID = firstJSONAPIID(apps)
		if meta.AppID == "" {
			return fmt.Errorf("App Store Connect has no app record for bundle id %s", meta.BundleID)
		}
	}
	creds, err := globalCtx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return fmt.Errorf("read App Store Connect credentials: %w", err)
	}
	issuerID := strings.TrimSpace(creds.Fields["issuer_id"])
	keyID := strings.TrimSpace(creds.Fields["key_id"])
	privateKey := normalizePEM(creds.Fields["private_key"])
	if issuerID == "" || keyID == "" || privateKey == "" {
		return errors.New("App Store Connect connection requires issuer_id, key_id, and private_key")
	}
	primary := filepath.Join(b.ArtifactPath, manifest.Primary)
	tmp, err := os.MkdirTemp("", "apteva-appstore-upload-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	keyPath := filepath.Join(tmp, "AuthKey_"+keyID+".p8")
	if err := os.WriteFile(keyPath, []byte(privateKey), 0o600); err != nil {
		return err
	}
	args := []string{"altool", "--upload-package", primary, "--api-key", keyID, "--api-issuer", issuerID, "--p8-file-path", keyPath, "--output-format", "json", "--show-progress"}
	out, err := runCapturedCommand(30*time.Minute, "", mobileBuildEnv(nil), logW, "xcrun", args...)
	if err != nil {
		return fmt.Errorf("App Store upload: %w", err)
	}
	deliveryID := findJSONKeyString(out, "delivery-id", "deliveryId", "deliveryUUID")
	if deliveryID == "" {
		deliveryID = "uploaded-" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	_ = dbUpdateRelease(globalCtx.AppDB(), releaseID, map[string]any{
		"status": "starting", "external_id": deliveryID, "external_status": "uploaded_processing",
		"release_meta_json": mustJSON(meta),
	})
	fmt.Fprintf(logW, "App Store upload accepted; delivery=%s, waiting for processing\n", deliveryID)
	return nil
}

func (a *App) syncPendingMobileReleases(ctx context.Context) error {
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return nil
	}
	releases, err := dbListPendingMobileReleases(globalCtx.AppDB(), 100)
	if err != nil {
		return err
	}
	for i := range releases {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if releases[i].Provider != "app_store_connect" {
			continue
		}
		if uploadedAt, parseErr := time.Parse(time.RFC3339, releases[i].CreatedAt); parseErr == nil &&
			time.Since(uploadedAt) > 48*time.Hour &&
			(releases[i].ExternalID == "" || strings.HasPrefix(releases[i].ExternalID, "uploaded-")) {
			errText := "App Store did not finish processing the uploaded build within 48 hours"
			_ = dbUpdateRelease(globalCtx.AppDB(), releases[i].ID, map[string]any{
				"status": "failed", "error": errText, "external_status": "processing_timeout", "stopped_at": nowUTC(),
			})
			continue
		}
		if err := a.syncIOSRelease(&releases[i]); err != nil {
			_ = dbUpdateRelease(globalCtx.AppDB(), releases[i].ID, map[string]any{"error": err.Error(), "external_status": "sync_error"})
		}
	}
	return nil
}

func (a *App) syncIOSRelease(rel *Release) error {
	var meta mobileReleaseMeta
	if err := json.Unmarshal([]byte(defaultStr(rel.ReleaseMetaJSON, "{}")), &meta); err != nil {
		return err
	}
	bound, err := boundIntegration("app_store")
	if err != nil {
		return err
	}
	if meta.AppStoreVersionID != "" && meta.Prepared {
		return a.syncAppStoreVersionState(bound, rel, &meta)
	}
	input := map[string]any{"app_id": meta.AppID, "processing_state": "VALID", "limit": 20, "sort": "-uploadedDate"}
	if meta.BuildNumber != "" {
		input["version"] = meta.BuildNumber
	}
	builds, err := executeIntegration(bound, "list_builds", input)
	if err != nil {
		return err
	}
	buildID := findAppleBuildID(builds, meta.BuildNumber)
	if buildID == "" {
		_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{"external_status": "processing"})
		return nil
	}
	rel.ExternalID = buildID
	if rel.Channel == "internal" || rel.Channel == "external" {
		groupID := meta.BetaGroupID
		if groupID == "" {
			groups, err := executeIntegration(bound, "list_beta_groups", map[string]any{
				"app_id": meta.AppID, "internal": rel.Channel == "internal", "limit": 20,
			})
			if err != nil {
				return err
			}
			groupID = firstJSONAPIID(groups)
			if groupID == "" {
				created, err := executeIntegration(bound, "create_beta_group", map[string]any{
					"app_id": meta.AppID, "name": "Deploy " + upperFirst(rel.Channel), "isInternalGroup": rel.Channel == "internal",
				})
				if err != nil {
					return err
				}
				groupID = jsonStringAt(created, "data", "id")
			}
		}
		if _, err := executeIntegration(bound, "add_builds_to_beta_group", map[string]any{
			"group_id": groupID,
			"body":     map[string]any{"data": []map[string]any{{"type": "builds", "id": buildID}}},
		}); err != nil {
			return err
		}
		meta.BetaGroupID = groupID
		_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
			"status": "live", "external_id": buildID, "external_status": "testflight_available", "release_meta_json": mustJSON(meta),
		})
		return nil
	}
	if rel.Channel == "production" {
		return a.prepareIOSProductionRelease(bound, rel, buildID, &meta)
	}
	return fmt.Errorf("unsupported iOS channel %q", rel.Channel)
}

func (a *App) prepareIOSProductionRelease(bound *sdk.BoundIntegration, rel *Release, buildID string, meta *mobileReleaseMeta) error {
	if meta.VersionName == "" {
		return errors.New("production App Store release requires version_name in the artifact manifest")
	}
	versionID := meta.AppStoreVersionID
	if versionID == "" {
		versions, err := executeIntegration(bound, "list_app_versions", map[string]any{"app_id": meta.AppID, "platform": "IOS", "version_string": meta.VersionName, "limit": 10})
		if err != nil {
			return err
		}
		versionID = firstJSONAPIID(versions)
		if versionID == "" {
			releaseType := strings.ToUpper(defaultStr(meta.ReleaseType, "MANUAL"))
			created, err := executeIntegration(bound, "create_app_version", map[string]any{
				"app_id": meta.AppID, "platform": "IOS", "versionString": meta.VersionName, "releaseType": releaseType,
			})
			if err != nil {
				return err
			}
			versionID = jsonStringAt(created, "data", "id")
		}
	}
	if versionID == "" {
		return errors.New("App Store version response missing id")
	}
	if _, err := executeIntegration(bound, "set_app_version_build", map[string]any{
		"version_id": versionID, "body": map[string]any{"data": map[string]any{"type": "builds", "id": buildID}},
	}); err != nil {
		return err
	}
	meta.AppStoreVersionID = versionID
	meta.Prepared = true
	status := "ready_for_review"
	if meta.SubmitForReview {
		created, err := executeIntegration(bound, "create_review_submission", map[string]any{"app_id": meta.AppID, "platform": "IOS"})
		if err != nil {
			return err
		}
		submissionID := jsonStringAt(created, "data", "id")
		if submissionID == "" {
			return errors.New("review submission response missing id")
		}
		if _, err := executeIntegration(bound, "create_review_submission_item", map[string]any{"submission_id": submissionID, "version_id": versionID}); err != nil {
			return err
		}
		if _, err := executeIntegration(bound, "submit_review_submission", map[string]any{"submission_id": submissionID, "submitted": true}); err != nil {
			return err
		}
		meta.ReviewSubmissionID = submissionID
		status = "waiting_for_review"
	}
	_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
		"external_id": buildID, "external_status": status, "release_meta_json": mustJSON(meta),
	})
	return nil
}

func (a *App) syncAppStoreVersionState(bound *sdk.BoundIntegration, rel *Release, meta *mobileReleaseMeta) error {
	version, err := executeIntegration(bound, "get_app_version", map[string]any{"version_id": meta.AppStoreVersionID})
	if err != nil {
		return err
	}
	state := jsonStringAt(version, "data", "attributes", "appStoreState")
	if state == "" {
		state = jsonStringAt(version, "data", "attributes", "appVersionState")
	}
	normalized := strings.ToLower(state)
	fields := map[string]any{"external_status": normalized}
	switch state {
	case "READY_FOR_SALE", "PRE_ORDER_READY_FOR_SALE", "PENDING_APPLE_RELEASE":
		fields["status"] = "live"
	case "REJECTED", "METADATA_REJECTED", "INVALID_BINARY":
		fields["status"] = "failed"
		fields["error"] = "App Store state: " + state
	}
	return dbUpdateRelease(globalCtx.AppDB(), rel.ID, fields)
}

func (a *App) toolPromoteMobile(ctx *sdk.AppCtx, base *Deployment, args map[string]any) (any, error) {
	var source *Release
	if releaseID := int64(intArg(args, "release_id")); releaseID > 0 {
		rel, err := dbGetRelease(ctx.AppDB(), releaseID)
		if err != nil || rel == nil || rel.DeploymentID != base.ID || rel.Provider == "" {
			return nil, fmt.Errorf("mobile release %d not found for deployment", releaseID)
		}
		source = rel
	}
	buildID := int64(intArg(args, "build_id"))
	if source == nil {
		releases, err := dbListReleases(ctx.AppDB(), base.ID, 100)
		if err != nil {
			return nil, err
		}
		for i := range releases {
			if releases[i].Provider == "" || (buildID > 0 && releases[i].BuildID != buildID) {
				continue
			}
			if releases[i].Status == "live" || releases[i].Status == "starting" {
				source = &releases[i]
				break
			}
		}
	}
	if source == nil {
		return nil, errors.New("no published mobile release found; deploy_release to an internal channel first")
	}
	build, err := dbGetBuild(ctx.AppDB(), source.BuildID)
	if err != nil || build == nil {
		return nil, errors.New("source release build not found")
	}
	env, err := dbGetEnvironment(ctx.AppDB(), source.EnvironmentID)
	if err != nil || env == nil {
		return nil, errors.New("source release environment not found")
	}
	d := effectiveDeploymentForEnvironment(base, env)
	opts := releaseOptionsFromArgs(args)
	opts.Channel = defaultStr(strArg(args, "target_channel"), "production")
	rel, err := a.promoteMobileRelease(d, build, source, opts)
	if err != nil {
		return nil, err
	}
	emit("deploy.promoted", map[string]any{
		"deployment_id": base.ID, "build_id": build.ID, "source_release_id": source.ID,
		"target_channel": rel.Channel, "release_id": rel.ID,
	})
	return map[string]any{"build": build, "source_release": source, "release": rel, "deployment": d}, nil
}

func (a *App) promoteMobileRelease(d *Deployment, build *Build, source *Release, opts releaseOptions) (*Release, error) {
	channel, err := normalizeMobileChannel(d.TargetKind, opts.Channel)
	if err != nil {
		return nil, err
	}
	var meta mobileReleaseMeta
	if err := json.Unmarshal([]byte(defaultStr(source.ReleaseMetaJSON, "{}")), &meta); err != nil {
		return nil, err
	}
	meta.RolloutFraction = opts.RolloutFraction
	if opts.ReleaseNotes != nil {
		meta.ReleaseNotes = opts.ReleaseNotes
	}
	meta.SubmitForReview = opts.SubmitForReview
	if opts.BetaGroupID != "" {
		meta.BetaGroupID = opts.BetaGroupID
	}
	meta.Prepared = false
	meta.AppStoreVersionID = ""
	meta.ReviewSubmissionID = ""
	rel, err := dbCreateReleaseForEnv(globalCtx.AppDB(), d.ID, d.EnvironmentID, build.ID)
	if err != nil {
		return nil, err
	}
	logPath, logFile, err := a.openMobileReleaseLog(rel.ID)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{
		"channel": channel, "provider": source.Provider, "external_id": source.ExternalID,
		"external_status": "promoting", "started_at": nowUTC(), "log_path": logPath,
		"release_meta_json": mustJSON(meta),
	})
	rel.Channel = channel
	rel.Provider = source.Provider
	rel.ExternalID = source.ExternalID
	rel.ReleaseMetaJSON = mustJSON(meta)
	fmt.Fprintf(logFile, "=== promote release %d to %s ===\n", source.ID, channel)
	if source.Provider == "google_play" {
		if meta.VersionCode == "" {
			meta.VersionCode = source.ExternalID
		}
		if err := a.publishAndroidVersionToTrack(rel.ID, channel, &meta, logFile); err != nil {
			_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{"status": "failed", "error": err.Error(), "external_status": "failed"})
			return nil, fmt.Errorf("mobile promotion %d failed: %w", rel.ID, err)
		}
	} else if source.Provider == "app_store_connect" {
		if err := a.syncIOSRelease(rel); err != nil {
			_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{"status": "failed", "error": err.Error(), "external_status": "failed"})
			return nil, fmt.Errorf("mobile promotion %d failed: %w", rel.ID, err)
		}
	} else {
		return nil, fmt.Errorf("unsupported mobile provider %q", source.Provider)
	}
	return dbGetRelease(globalCtx.AppDB(), rel.ID)
}

func (a *App) publishAndroidVersionToTrack(releaseID int64, channel string, meta *mobileReleaseMeta, logW io.Writer) error {
	if meta.PackageName == "" || meta.VersionCode == "" {
		return errors.New("Android promotion requires package_name and version_code from a prior release")
	}
	bound, err := boundIntegration("play_store")
	if err != nil {
		return err
	}
	create, err := executeIntegration(bound, "create_edit", map[string]any{"packageName": meta.PackageName})
	if err != nil {
		return err
	}
	editID := jsonStringAt(create, "id")
	if editID == "" {
		return errors.New("Google Play create_edit response missing id")
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = executeIntegration(bound, "delete_edit", map[string]any{"packageName": meta.PackageName, "editId": editID})
		}
	}()
	status := "completed"
	release := map[string]any{"versionCodes": []string{meta.VersionCode}, "status": status}
	if channel == "production" && meta.RolloutFraction > 0 && meta.RolloutFraction < 1 {
		status = "inProgress"
		release["status"] = status
		release["userFraction"] = meta.RolloutFraction
	}
	if notes := googleReleaseNotes(meta.ReleaseNotes); len(notes) > 0 {
		release["releaseNotes"] = notes
	}
	if _, err := executeIntegration(bound, "update_track", map[string]any{
		"packageName": meta.PackageName, "editId": editID, "track": channel, "releases": []map[string]any{release},
	}); err != nil {
		return err
	}
	if _, err := executeIntegration(bound, "commit_edit", map[string]any{"packageName": meta.PackageName, "editId": editID}); err != nil {
		return err
	}
	committed = true
	_ = dbUpdateRelease(globalCtx.AppDB(), releaseID, map[string]any{
		"status": "live", "external_id": meta.VersionCode, "external_status": status, "release_meta_json": mustJSON(meta),
	})
	fmt.Fprintf(logW, "Google Play versionCode=%s promoted to %s (%s)\n", meta.VersionCode, channel, status)
	return nil
}

func (a *App) toolRollout(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	releaseID := int64(intArg(args, "release_id"))
	rel, err := dbGetRelease(ctx.AppDB(), releaseID)
	if err != nil || rel == nil || rel.Provider != "google_play" || rel.Channel != "production" {
		return nil, errors.New("release_id must be a Google Play production release")
	}
	fraction, _ := args["fraction"].(float64)
	if fraction <= 0 || fraction > 1 {
		return nil, errors.New("fraction must be > 0 and <= 1")
	}
	var meta mobileReleaseMeta
	if err := json.Unmarshal([]byte(defaultStr(rel.ReleaseMetaJSON, "{}")), &meta); err != nil {
		return nil, err
	}
	meta.RolloutFraction = fraction
	logPath := rel.LogPath
	if logPath == "" {
		logPath = filepath.Join(a.dataDir, "releases", strconv.FormatInt(rel.ID, 10), "runtime.log")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}
	logW, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer logW.Close()
	if err := a.publishAndroidVersionToTrack(rel.ID, "production", &meta, logW); err != nil {
		return nil, err
	}
	fresh, _ := dbGetRelease(ctx.AppDB(), rel.ID)
	return map[string]any{"release": fresh, "fraction": fraction}, nil
}

func (a *App) toolHalt(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	releaseID := int64(intArg(args, "release_id"))
	rel, err := dbGetRelease(ctx.AppDB(), releaseID)
	if err != nil || rel == nil || rel.Provider == "" {
		return nil, errors.New("mobile release not found")
	}
	var meta mobileReleaseMeta
	if err := json.Unmarshal([]byte(defaultStr(rel.ReleaseMetaJSON, "{}")), &meta); err != nil {
		return nil, err
	}
	boundRole := "play_store"
	if rel.Provider == "app_store_connect" {
		boundRole = "app_store"
	}
	bound, err := boundIntegration(boundRole)
	if err != nil {
		return nil, err
	}
	if rel.Provider == "google_play" {
		create, err := executeIntegration(bound, "create_edit", map[string]any{"packageName": meta.PackageName})
		if err != nil {
			return nil, err
		}
		editID := jsonStringAt(create, "id")
		_, err = executeIntegration(bound, "update_track", map[string]any{
			"packageName": meta.PackageName, "editId": editID, "track": rel.Channel,
			"releases": []map[string]any{{"versionCodes": []string{meta.VersionCode}, "status": "halted"}},
		})
		if err == nil {
			_, err = executeIntegration(bound, "commit_edit", map[string]any{"packageName": meta.PackageName, "editId": editID})
		}
		if err != nil {
			return nil, err
		}
	} else {
		if rel.ExternalID == "" || strings.HasPrefix(rel.ExternalID, "uploaded-") {
			return nil, errors.New("App Store build is not processed yet")
		}
		if _, err := executeIntegration(bound, "expire_build", map[string]any{"build_id": rel.ExternalID, "expired": true}); err != nil {
			return nil, err
		}
	}
	_ = dbUpdateRelease(ctx.AppDB(), rel.ID, map[string]any{"status": "stopped", "stopped_at": nowUTC(), "external_status": "halted"})
	fresh, _ := dbGetRelease(ctx.AppDB(), rel.ID)
	return map[string]any{"halted": true, "release": fresh}, nil
}

func boundIntegration(role string) (*sdk.BoundIntegration, error) {
	if globalCtx == nil || globalCtx.PlatformAPI() == nil {
		return nil, errors.New("platform unavailable")
	}
	bound := globalCtx.IntegrationFor(role)
	if bound == nil || bound.Kind != "integration" || bound.ConnectionID <= 0 {
		return nil, fmt.Errorf("no %s integration is bound to Deploy", role)
	}
	return bound, nil
}

func boundConnectionCredentials(role string) (*sdk.ConnectionCredentials, error) {
	bound, err := boundIntegration(role)
	if err != nil {
		return nil, err
	}
	creds, err := globalCtx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("read %s credentials: %w", role, err)
	}
	return creds, nil
}

func executeIntegration(bound *sdk.BoundIntegration, tool string, input map[string]any) (json.RawMessage, error) {
	if bound == nil || globalCtx == nil || globalCtx.PlatformAPI() == nil {
		return nil, errors.New("integration platform unavailable")
	}
	res, err := globalCtx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, input)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %w", bound.AppSlug, tool, err)
	}
	if res == nil || !res.Success || res.Status >= 400 {
		status := 0
		var data []byte
		if res != nil {
			status, data = res.Status, res.Data
		}
		return nil, fmt.Errorf("%s.%s returned HTTP %d: %s", bound.AppSlug, tool, status, truncateString(string(data), 800))
	}
	return res.Data, nil
}

func (a *App) openMobileReleaseLog(releaseID int64) (string, *os.File, error) {
	dir := filepath.Join(a.dataDir, "releases", strconv.FormatInt(releaseID, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "runtime.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	return path, f, err
}

func normalizeMobileChannel(platform, channel string) (string, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" {
		channel = "internal"
	}
	allowed := map[string]map[string]bool{
		"android": {"internal": true, "alpha": true, "beta": true, "production": true},
		"ios":     {"internal": true, "external": true, "production": true},
	}
	if !allowed[platform][channel] {
		return "", fmt.Errorf("channel %q is not supported for %s", channel, platform)
	}
	return channel, nil
}

func releaseOptionsFromArgs(args map[string]any) releaseOptions {
	opts := releaseOptions{Channel: strArg(args, "channel"), SubmitForReview: boolArg(args, "submit_for_review"), BetaGroupID: strArg(args, "beta_group_id")}
	if v, ok := args["rollout_fraction"].(float64); ok {
		opts.RolloutFraction = v
	}
	if raw, ok := args["release_notes"].(map[string]any); ok {
		opts.ReleaseNotes = map[string]string{}
		for key, value := range raw {
			opts.ReleaseNotes[key] = fmt.Sprint(value)
		}
	}
	if raw, ok := args["release_notes"].(map[string]string); ok {
		opts.ReleaseNotes = raw
	}
	return opts
}

func googleReleaseNotes(notes map[string]string) []map[string]any {
	out := []map[string]any{}
	for language, text := range notes {
		out = append(out, map[string]any{"language": language, "text": text})
	}
	return out
}

func runCapturedCommand(timeout time.Duration, dir string, env []string, logW io.Writer, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var captured bytes.Buffer
	fmt.Fprintf(logW, "+ %s %s\n", bin, strings.Join(redactCommandArgs(args), " "))
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = io.MultiWriter(logW, &captured)
	cmd.Stderr = io.MultiWriter(logW, &captured)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return captured.Bytes(), err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return captured.Bytes(), err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		<-done
		return captured.Bytes(), ctx.Err()
	}
}

func redactCommandArgs(args []string) []string {
	out := append([]string{}, args...)
	for i := range out {
		if i > 0 && (out[i-1] == "--p8-file-path" || out[i-1] == "-authenticationKeyPath") {
			out[i] = "<private-key>"
		}
	}
	return out
}

func jsonStringAt(raw json.RawMessage, path ...string) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	for _, key := range path {
		obj, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = obj[key]
	}
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func jsonScalarStringAt(raw json.RawMessage, path ...string) string {
	return strings.TrimSuffix(jsonStringAt(raw, path...), ".0")
}

func firstJSONAPIID(raw json.RawMessage) string {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) == nil && len(payload.Data) > 0 {
		return payload.Data[0].ID
	}
	return ""
}

func findAppleBuildID(raw json.RawMessage, buildNumber string) string {
	var payload struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Version         string `json:"version"`
				ProcessingState string `json:"processingState"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	for _, item := range payload.Data {
		if item.Attributes.ProcessingState == "VALID" && (buildNumber == "" || item.Attributes.Version == buildNumber) {
			return item.ID
		}
	}
	return ""
}

func findJSONKeyString(raw []byte, keys ...string) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	keySet := map[string]bool{}
	for _, key := range keys {
		keySet[key] = true
	}
	var walk func(any) string
	walk = func(v any) string {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				if keySet[key] {
					return fmt.Sprint(child)
				}
				if found := walk(child); found != "" {
					return found
				}
			}
		case []any:
			for _, child := range x {
				if found := walk(child); found != "" {
					return found
				}
			}
		}
		return ""
	}
	return walk(value)
}

func mustJSON(value any) string {
	body, _ := json.Marshal(value)
	return string(body)
}

func truncateString(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}
