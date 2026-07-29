package main

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateMobileSource(root string, d *Deployment, cloudCfg cloudBuildConfig) error {
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return err
	}
	platform := strings.ToLower(strings.TrimSpace(d.TargetKind))
	if platform == "" || platform == "service" {
		platform = strings.ToLower(strings.TrimSpace(d.Framework))
	}
	switch platform {
	case "ios":
		if err := validateIOSSource(root, target, cloudCfg); err != nil {
			return err
		}
		if target.SmokeOnly || normalizeBuildBackend(d.BuildBackend) == buildBackendLocal || globalCtx == nil {
			return nil
		}
		requirements, err := detectMobileRequirements(root, d)
		if err != nil {
			return err
		}
		setup, err := dbGetMobileSigningSetup(
			globalCtx.AppDB(), d.ID, d.EnvironmentID, normalizeBuildBackend(d.BuildBackend),
		)
		if err != nil {
			return err
		}
		if setup == nil {
			if len(requirements.Features) == 0 {
				return nil
			}
			return errors.New("iOS signing requirements are not reconciled; run mobile signing setup before submitting the build")
		}
		if setup.Status != mobileSigningStatusReady ||
			setup.RequirementsHash != requirements.Hash ||
			!mobileFeaturesContainAll(
				mobileFeaturesFromJSON(setup.ProvisionedFeaturesJSON),
				requirements.Features,
			) {
			provisioned := mobileFeaturesFromJSON(setup.ProvisionedFeaturesJSON)
			for _, feature := range requirements.Features {
				if mobileFeaturesContainAll(provisioned, []string{feature}) {
					continue
				}
				capability := defaultStr(appleCapabilityForMobileFeature[feature], feature)
				return fmt.Errorf(
					"iOS signing profile does not satisfy required capability %s. Run signing setup to repair it",
					capability,
				)
			}
			return errors.New("iOS signing requirements changed; repair mobile signing before submitting the build")
		}
		return nil
	case "android":
		return validateAndroidSource(root, target, cloudCfg)
	default:
		return nil
	}
}

func validateMobileCloudContract(d *Deployment, cloudCfg cloudBuildConfig) error {
	if d == nil || (d.TargetKind != "ios" && d.TargetKind != "android") {
		return nil
	}
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return err
	}
	mode := resolvedCloudArtifactMode(cloudCfg, d)
	switch d.TargetKind {
	case "ios":
		if !target.SmokeOnly && strings.TrimSpace(target.BundleID) == "" {
			return errors.New("iOS cloud builds require target_config_json.bundle_id")
		}
		if mode == "store_upload" &&
			(strings.TrimSpace(target.VersionName) == "" || strings.TrimSpace(target.BuildNumber) == "") {
			return errors.New("iOS store_upload requires target_config_json.version_name and build_number")
		}
	case "android":
		if strings.TrimSpace(target.PackageName) == "" {
			return errors.New("Android cloud builds require target_config_json.package_name")
		}
		if mode == "store_upload" {
			if strings.TrimSpace(target.VersionCode) == "" {
				return errors.New("Android store_upload requires target_config_json.version_code")
			}
			if strings.TrimSpace(cloudCfg.StoreChannel) == "" {
				return errors.New("Android store_upload requires store_channel or a Build & Publish channel")
			}
			if _, err := normalizeMobileChannel("android", cloudCfg.StoreChannel); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateIOSSource(root string, cfg mobileTargetConfig, cloudCfg cloudBuildConfig) error {
	if !looksLikeIOSProject(root) {
		return errors.New("target is iOS but the source has no project.yml, Package.swift, .xcodeproj, or .xcworkspace")
	}
	if !cfg.SmokeOnly {
		if strings.TrimSpace(cfg.BundleID) == "" {
			return errors.New("signed iOS builds require target_config_json.bundle_id")
		}
		if resolvedCloudArtifactMode(cloudCfg, &Deployment{TargetKind: "ios"}) == "store_upload" {
			if strings.TrimSpace(cfg.VersionName) == "" || strings.TrimSpace(cfg.BuildNumber) == "" {
				return errors.New("iOS store_upload requires target_config_json.version_name and build_number")
			}
		}
	}
	if cfg.SmokeOnly {
		return nil
	}
	iconSet, err := findIOSAppIconSet(root)
	if err != nil {
		return err
	}
	if iconSet == "" {
		return errors.New("iOS source has no populated .appiconset asset catalog")
	}
	hasOrientations, err := hasIOSSupportedOrientations(root)
	if err != nil {
		return err
	}
	if !hasOrientations {
		return errors.New("iOS source does not declare supported interface orientations")
	}
	return nil
}

func findIOSAppIconSet(root string) (string, error) {
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if found != "" {
			return filepath.SkipAll
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "DerivedData" || name == "build" {
				return filepath.SkipDir
			}
			if !strings.HasSuffix(strings.ToLower(name), ".appiconset") {
				return nil
			}
			body, err := os.ReadFile(filepath.Join(path, "Contents.json"))
			if err != nil {
				return nil
			}
			var contents struct {
				Images []struct {
					Filename string `json:"filename"`
				} `json:"images"`
			}
			if json.Unmarshal(body, &contents) != nil {
				return nil
			}
			for _, image := range contents.Images {
				if image.Filename != "" && exists(filepath.Join(path, image.Filename)) {
					found = path
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return found, err
}

func validateAndroidSource(root string, cfg mobileTargetConfig, cloudCfg cloudBuildConfig) error {
	if !looksLikeAndroidProject(root) {
		return errors.New("target is Android but the source has no Gradle project with AndroidManifest.xml")
	}
	if strings.TrimSpace(cfg.PackageName) == "" {
		return errors.New("Android builds require target_config_json.package_name")
	}
	if resolvedCloudArtifactMode(cloudCfg, &Deployment{TargetKind: "android"}) == "store_upload" &&
		strings.TrimSpace(cfg.VersionCode) == "" {
		return errors.New("Android store_upload requires target_config_json.version_code")
	}
	manifestPath, iconRef, err := findAndroidManifestIcon(root)
	if err != nil {
		return err
	}
	if manifestPath == "" {
		return errors.New("Android source has no parseable AndroidManifest.xml")
	}
	if iconRef == "" {
		return errors.New("Android manifest does not declare android:icon")
	}
	if !androidResourceExists(filepath.Dir(manifestPath), iconRef) {
		return fmt.Errorf("Android launcher icon resource %q does not exist", iconRef)
	}
	return nil
}

func findAndroidManifestIcon(root string) (string, string, error) {
	var manifestPath, iconRef string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if manifestPath != "" {
			return filepath.SkipAll
		}
		if info.IsDir() && (info.Name() == ".git" || info.Name() == ".gradle" || info.Name() == "build") {
			return filepath.SkipDir
		}
		if info.IsDir() || info.Name() != "AndroidManifest.xml" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var document struct {
			Application struct {
				Attrs []xml.Attr `xml:",any,attr"`
			} `xml:"application"`
		}
		if xml.Unmarshal(body, &document) != nil {
			return nil
		}
		manifestPath = path
		for _, attr := range document.Application.Attrs {
			if attr.Name.Local == "icon" {
				iconRef = strings.TrimSpace(attr.Value)
				break
			}
		}
		return filepath.SkipAll
	})
	return manifestPath, iconRef, err
}

func androidResourceExists(sourceSetDir, ref string) bool {
	if !strings.HasPrefix(ref, "@") {
		return true
	}
	parts := strings.SplitN(strings.TrimPrefix(ref, "@"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	resDir := filepath.Join(sourceSetDir, "res")
	entries, err := os.ReadDir(resDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() || (entry.Name() != parts[0] && !strings.HasPrefix(entry.Name(), parts[0]+"-")) {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(resDir, entry.Name(), parts[1]+".*"))
		if len(matches) > 0 {
			return true
		}
	}
	return false
}
