package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// verifyStagedCloudMobileArtifact is the trust boundary between a build
// provider and Deploy. Provider metadata is required, but never sufficient:
// Deploy verifies the downloaded bundle and managed signer itself.
func (a *App) verifyStagedCloudMobileArtifact(d *Deployment, build *Build, distDir string) error {
	if d == nil || d.TargetKind != "android" {
		return nil
	}
	targetJSON := d.TargetConfigJSON
	if build != nil && strings.TrimSpace(build.TargetConfigJSON) != "" && strings.TrimSpace(build.TargetConfigJSON) != "{}" {
		targetJSON = build.TargetConfigJSON
	}
	target, err := parseMobileTargetConfig(targetJSON)
	if err != nil {
		return err
	}
	if target.SmokeOnly {
		return nil
	}
	manifest, err := readArtifactManifestFile(filepath.Join(distDir, artifactManifestFilename))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("Android cloud artifact has no signing manifest")
		}
		return fmt.Errorf("read Android cloud artifact manifest: %w", err)
	}
	if manifest.Platform != "android" {
		return fmt.Errorf("Android cloud artifact reports platform %q", manifest.Platform)
	}
	if manifest.SigningContract != mobileSigningArtifactContractVersion {
		return fmt.Errorf(
			"Android cloud artifact does not implement signing contract %s; the build adapter is stale or incompatible",
			mobileSigningArtifactContractVersion,
		)
	}
	if !manifest.SigningVerified {
		return errors.New("build provider returned an Android App Bundle without verified signing evidence")
	}
	primary, err := confinedArtifactPath(distDir, manifest.Primary)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Ext(primary), ".aab") {
		return fmt.Errorf("Android cloud artifact primary file %q is not an AAB", manifest.Primary)
	}
	credentials, err := a.androidSigningCredentials(d)
	if err != nil {
		return err
	}
	expectedFingerprint := normalizeCertificateFingerprint(credentials["upload_certificate_sha256"])
	if expectedFingerprint == "" {
		return errors.New("managed Android upload certificate fingerprint is missing")
	}
	if normalizeCertificateFingerprint(manifest.CertificateSHA256) != expectedFingerprint {
		return fmt.Errorf(
			"build provider reported Android certificate %s, expected managed upload certificate %s",
			manifest.CertificateSHA256, expectedFingerprint,
		)
	}
	actualFingerprint, err := verifyAndroidBundleSignaturePureGo(primary, expectedFingerprint)
	if err != nil {
		return err
	}
	manifest.SigningVerified = true
	manifest.CertificateSHA256 = actualFingerprint
	manifest.Files = []artifactFile{mobileArtifactFile(primary, "aab")}
	if err := writeArtifactManifest(distDir, manifest); err != nil {
		return err
	}
	if build != nil && build.LogPath != "" {
		_ = appendCloudBuildLog(build.LogPath, "verified downloaded Android App Bundle signature for "+actualFingerprint)
	}
	return nil
}

func confinedArtifactPath(root, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.IsAbs(name) {
		return "", errors.New("Android cloud artifact manifest has an invalid primary file")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(root, filepath.Clean(filepath.FromSlash(name))))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("Android cloud artifact primary file escapes the artifact directory")
	}
	if info, statErr := os.Stat(path); statErr != nil || info.IsDir() {
		if statErr != nil {
			return "", fmt.Errorf("Android cloud artifact primary file: %w", statErr)
		}
		return "", errors.New("Android cloud artifact primary file is a directory")
	}
	return path, nil
}
