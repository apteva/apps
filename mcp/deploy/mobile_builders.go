package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	pkcs12 "github.com/ggpslop/go-pkcs12"
	"howett.net/plist"
)

const artifactManifestFilename = ".apteva-artifact.json"

type mobileTargetConfig struct {
	Module           string   `json:"module,omitempty"`
	Variant          string   `json:"variant,omitempty"`
	RequiredFeatures []string `json:"required_features,omitempty"`
	PackageName      string   `json:"package_name,omitempty"`
	VersionCode      string   `json:"version_code,omitempty"`
	VersionStrategy  string   `json:"version_strategy,omitempty"`
	GradleArgs       []string `json:"gradle_args,omitempty"`
	ProjectPath      string   `json:"project_path,omitempty"`
	WorkspacePath    string   `json:"workspace_path,omitempty"`
	Scheme           string   `json:"scheme,omitempty"`
	Configuration    string   `json:"configuration,omitempty"`
	TeamID           string   `json:"team_id,omitempty"`
	BundleID         string   `json:"bundle_id,omitempty"`
	VersionName      string   `json:"version_name,omitempty"`
	BuildNumber      string   `json:"build_number,omitempty"`
	DeviceFamilies   []string `json:"device_families,omitempty"`
	AppStoreAppID    string   `json:"app_store_app_id,omitempty"`
	BetaGroupID      string   `json:"beta_group_id,omitempty"`
	ReleaseType      string   `json:"release_type,omitempty"`
	SmokeOnly        bool     `json:"smoke_only,omitempty"`
}

type artifactManifest struct {
	Platform          string         `json:"platform"`
	Primary           string         `json:"primary,omitempty"`
	PackageName       string         `json:"package_name,omitempty"`
	BundleID          string         `json:"bundle_id,omitempty"`
	VersionName       string         `json:"version_name,omitempty"`
	BuildNumber       string         `json:"build_number,omitempty"`
	VersionCode       string         `json:"version_code,omitempty"`
	CertificateSHA256 string         `json:"certificate_sha256,omitempty"`
	SigningVerified   bool           `json:"signing_verified,omitempty"`
	SigningContract   string         `json:"signing_contract,omitempty"`
	DeviceFamilies    []string       `json:"device_families,omitempty"`
	Channel           string         `json:"channel,omitempty"`
	ExternalProvider  string         `json:"external_provider,omitempty"`
	ExternalID        string         `json:"external_id,omitempty"`
	ExternalStatus    string         `json:"external_status,omitempty"`
	Files             []artifactFile `json:"files,omitempty"`
}

type artifactFile struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func parseMobileTargetConfig(raw string) (mobileTargetConfig, error) {
	var cfg mobileTargetConfig
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, fmt.Errorf("target_config_json: %w", err)
	}
	return cfg, nil
}

func looksLikeAndroidProject(root string) bool {
	if !exists(filepath.Join(root, "settings.gradle")) &&
		!exists(filepath.Join(root, "settings.gradle.kts")) &&
		!exists(filepath.Join(root, "build.gradle")) &&
		!exists(filepath.Join(root, "build.gradle.kts")) {
		return false
	}
	found := false
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || found {
			return filepath.SkipAll
		}
		if info.IsDir() && (info.Name() == ".gradle" || info.Name() == "build") {
			return filepath.SkipDir
		}
		if !info.IsDir() && info.Name() == "AndroidManifest.xml" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func looksLikeIOSProject(root string) bool {
	if exists(filepath.Join(root, "project.yml")) || exists(filepath.Join(root, "Package.swift")) {
		return true
	}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".xcodeproj") || strings.HasSuffix(entry.Name(), ".xcworkspace") {
			return true
		}
	}
	return false
}

type androidBuilder struct{}

func (*androidBuilder) Framework() string { return "android" }

func (*androidBuilder) Build(srcDir, artifactDir string, ov BuildOverrides, logW io.Writer) (string, error) {
	cfg, err := parseMobileTargetConfig(ov.TargetConfigJSON)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", err
	}
	buildEnv := mobileVersionBuildEnv(ov.Env, cfg)

	if strings.TrimSpace(ov.BuildCmd) != "" {
		if err := runMobileBuildCommand(buildContext(ov), srcDir, "sh", []string{"-c", ov.BuildCmd}, buildEnv, logW); err != nil {
			return "", fmt.Errorf("android build_cmd: %w", err)
		}
	} else {
		module := strings.Trim(strings.TrimSpace(defaultStr(cfg.Module, "app")), ":")
		variant := defaultStr(cfg.Variant, "release")
		task := ":" + module + ":bundle" + upperFirst(variant)
		bin := filepath.Join(srcDir, "gradlew")
		if exists(bin) {
			if err := os.Chmod(bin, 0o755); err != nil {
				return "", err
			}
		} else {
			bin, err = exec.LookPath("gradle")
			if err != nil {
				return "", errors.New("android build requires ./gradlew or gradle on PATH")
			}
		}
		args := []string{task, "--console=plain", "--no-daemon"}
		if cfg.VersionCode != "" || cfg.VersionName != "" {
			initPath := filepath.Join(artifactDir, ".apteva-mobile-version.gradle")
			if err := os.WriteFile(initPath, []byte(androidVersionInitScript(cfg)), 0o600); err != nil {
				return "", err
			}
			defer os.Remove(initPath)
			args = append(args, "--init-script", initPath)
		}
		args = append(args, cfg.GradleArgs...)
		if err := runMobileBuildCommand(buildContext(ov), srcDir, bin, args, buildEnv, logW); err != nil {
			return "", fmt.Errorf("gradle %s: %w", task, err)
		}
	}

	aab, err := findMobileArtifact(srcDir, ".aab", filepath.Join("outputs", "bundle"))
	if err != nil {
		return "", errors.New("Android build succeeded but produced no .aab; use a bundleRelease task or custom build_cmd")
	}
	primary := filepath.Base(aab)
	dst := filepath.Join(artifactDir, primary)
	if err := copyMobileFile(aab, dst); err != nil {
		return "", err
	}
	if err := ensureAndroidBundleSigned(dst, false, logW, ov.Credentials.AndroidSigning); err != nil {
		return "", err
	}
	manifest := artifactManifest{
		Platform: "android", Primary: primary, PackageName: cfg.PackageName,
		VersionName: cfg.VersionName, VersionCode: cfg.VersionCode,
		CertificateSHA256: normalizeCertificateFingerprint(ov.Credentials.AndroidSigning["upload_certificate_sha256"]),
		SigningVerified:   androidBundleHasSignature(dst),
		Files:             []artifactFile{mobileArtifactFile(dst, "aab")},
	}
	if manifest.SigningVerified && manifest.CertificateSHA256 != "" {
		manifest.SigningContract = mobileSigningArtifactContractVersion
	}
	if err := writeArtifactManifest(artifactDir, manifest); err != nil {
		return "", err
	}
	fmt.Fprintf(logW, "=== Android bundle: %s ===\n", dst)
	return primary, nil
}

type iosBuilder struct{}

func (*iosBuilder) Framework() string { return "ios" }

func (*iosBuilder) Build(srcDir, artifactDir string, ov BuildOverrides, logW io.Writer) (string, error) {
	cfg, err := parseMobileTargetConfig(ov.TargetConfigJSON)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", err
	}
	buildEnv := mobileVersionBuildEnv(ov.Env, cfg)
	if strings.TrimSpace(ov.BuildCmd) != "" {
		if err := runMobileBuildCommand(buildContext(ov), srcDir, "sh", []string{"-c", ov.BuildCmd}, buildEnv, logW); err != nil {
			return "", fmt.Errorf("ios build_cmd: %w", err)
		}
		ipa, err := findMobileArtifact(srcDir, ".ipa", "")
		if err != nil {
			return "", errors.New("iOS build_cmd succeeded but produced no .ipa")
		}
		return stageIPA(ipa, artifactDir, cfg, logW)
	}
	if runtime.GOOS != "darwin" {
		return "", errors.New("iOS App Store builds require a macOS Deploy host with Xcode; set build_cmd to delegate to a remote runner")
	}
	if _, err := exec.LookPath("xcodebuild"); err != nil {
		return "", errors.New("xcodebuild not found; install Xcode")
	}
	if exists(filepath.Join(srcDir, "project.yml")) && !hasXcodeContainer(srcDir) {
		if _, err := exec.LookPath("xcodegen"); err != nil {
			return "", errors.New("project.yml found but xcodegen is not on PATH")
		}
		if err := runMobileBuildCommand(buildContext(ov), srcDir, "xcodegen", []string{"generate"}, buildEnv, logW); err != nil {
			return "", fmt.Errorf("xcodegen generate: %w", err)
		}
	}

	containerFlag, containerPath, err := resolveIOSContainer(srcDir, cfg)
	if err != nil {
		return "", err
	}
	scheme := strings.TrimSpace(cfg.Scheme)
	if scheme == "" {
		scheme, err = discoverFirstIOSScheme(buildContext(ov), srcDir, containerFlag, containerPath, buildEnv)
		if err != nil {
			return "", err
		}
	}
	configuration := defaultStr(cfg.Configuration, "Release")
	if cfg.SmokeOnly {
		return buildIOSSimulatorSmoke(buildContext(ov), srcDir, artifactDir, containerFlag, containerPath, scheme, configuration, buildEnv, cfg, logW)
	}

	fields, err := mobileBuildCredentialFields(ov.Credentials.AppStore, "app_store")
	if err != nil {
		return "", err
	}
	issuerID := strings.TrimSpace(fields["issuer_id"])
	keyID := strings.TrimSpace(fields["key_id"])
	privateKey := normalizePEM(fields["private_key"])
	if issuerID == "" || keyID == "" || privateKey == "" {
		return "", errors.New("App Store Connect connection requires issuer_id, key_id, and private_key")
	}

	tmp, err := os.MkdirTemp("", "apteva-ios-build-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	profileUUID := ""
	if len(ov.Credentials.IOSSigning) > 0 {
		var cleanup func()
		profileUUID, cleanup, err = prepareIOSSigningAssets(tmp, ov.Credentials.IOSSigning)
		if err != nil {
			return "", err
		}
		defer cleanup()
	}
	keyDir := filepath.Join(tmp, "private_keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return "", err
	}
	keyPath := filepath.Join(keyDir, "AuthKey_"+keyID+".p8")
	if err := os.WriteFile(keyPath, []byte(privateKey), 0o600); err != nil {
		return "", err
	}
	archivePath := filepath.Join(tmp, scheme+".xcarchive")
	derivedPath := filepath.Join(tmp, "DerivedData")
	authArgs := []string{"-allowProvisioningUpdates", "-authenticationKeyPath", keyPath, "-authenticationKeyID", keyID, "-authenticationKeyIssuerID", issuerID}
	archiveArgs := []string{containerFlag, containerPath, "-scheme", scheme, "-configuration", configuration,
		"-destination", "generic/platform=iOS", "-archivePath", archivePath, "-derivedDataPath", derivedPath}
	archiveArgs = append(archiveArgs, authArgs...)
	if cfg.TeamID != "" {
		archiveArgs = append(archiveArgs, "DEVELOPMENT_TEAM="+cfg.TeamID)
	}
	if profileUUID != "" {
		archiveArgs = append(archiveArgs,
			"CODE_SIGN_STYLE=Manual", "CODE_SIGN_IDENTITY=Apple Distribution",
			"PROVISIONING_PROFILE="+profileUUID,
		)
	}
	if cfg.VersionName != "" {
		archiveArgs = append(archiveArgs, "MARKETING_VERSION="+cfg.VersionName)
	}
	if cfg.BuildNumber != "" {
		archiveArgs = append(archiveArgs, "CURRENT_PROJECT_VERSION="+cfg.BuildNumber)
	}
	archiveArgs = append(archiveArgs, "archive")
	if err := runMobileBuildCommand(buildContext(ov), srcDir, "xcodebuild", archiveArgs, buildEnv, logW); err != nil {
		return "", fmt.Errorf("xcodebuild archive: %w", err)
	}

	exportOptions := filepath.Join(tmp, "ExportOptions.plist")
	if err := os.WriteFile(exportOptions, []byte(exportOptionsPlist(cfg.TeamID)), 0o600); err != nil {
		return "", err
	}
	exportDir := filepath.Join(tmp, "export")
	exportArgs := []string{"-exportArchive", "-archivePath", archivePath, "-exportPath", exportDir, "-exportOptionsPlist", exportOptions}
	exportArgs = append(exportArgs, authArgs...)
	if err := runMobileBuildCommand(buildContext(ov), srcDir, "xcodebuild", exportArgs, buildEnv, logW); err != nil {
		return "", fmt.Errorf("xcodebuild export: %w", err)
	}
	ipa, err := findMobileArtifact(exportDir, ".ipa", "")
	if err != nil {
		return "", errors.New("xcodebuild export succeeded but produced no .ipa")
	}
	if cfg.BundleID == "" {
		cfg.BundleID = plistValue(filepath.Join(archivePath, "Info.plist"), "ApplicationProperties.CFBundleIdentifier")
	}
	version := plistValue(filepath.Join(archivePath, "Info.plist"), "ApplicationProperties.CFBundleShortVersionString")
	buildNumber := plistValue(filepath.Join(archivePath, "Info.plist"), "ApplicationProperties.CFBundleVersion")
	return stageIPAWithVersion(ipa, artifactDir, cfg, version, buildNumber, logW)
}

func prepareIOSSigningAssets(tmp string, fields map[string]string) (string, func(), error) {
	cleanup := func() {}
	privateKeyPEM := normalizePEM(fields["certificate_private_key"])
	certificatePEM := normalizePEM(fields["certificate_pem"])
	profileBase64 := strings.TrimSpace(fields["provisioning_profile_base64"])
	if privateKeyPEM == "" || certificatePEM == "" || profileBase64 == "" {
		return "", cleanup, errors.New("managed iOS signing material is incomplete")
	}
	keyBlock, _ := pem.Decode([]byte(privateKeyPEM))
	certBlock, _ := pem.Decode([]byte(certificatePEM))
	if keyBlock == nil || certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return "", cleanup, errors.New("managed iOS signing key or certificate is invalid")
	}
	var privateKey any
	privateKey, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		privateKey, err = x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	}
	if err != nil {
		return "", cleanup, errors.New("parse managed iOS signing private key")
	}
	certificate, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return "", cleanup, errors.New("parse managed iOS signing certificate")
	}
	signer, ok := privateKey.(crypto.Signer)
	if !ok {
		return "", cleanup, errors.New("managed iOS private key cannot sign")
	}
	keyPublic, _ := x509.MarshalPKIXPublicKey(signer.Public())
	certPublic, _ := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if !bytes.Equal(keyPublic, certPublic) {
		return "", cleanup, errors.New("managed iOS private key does not match its certificate")
	}
	_, actualFingerprint := certificateFingerprints(certificate)
	expectedFingerprint := normalizeCertificateFingerprint(fields["certificate_sha256"])
	if expectedFingerprint != "" && normalizeCertificateFingerprint(actualFingerprint) != expectedFingerprint {
		return "", cleanup, errors.New("managed iOS certificate fingerprint mismatch")
	}
	profile, err := base64.StdEncoding.DecodeString(profileBase64)
	if err != nil {
		return "", cleanup, errors.New("decode managed iOS provisioning profile")
	}
	profilePath := filepath.Join(tmp, "profile.mobileprovision")
	if err := os.WriteFile(profilePath, profile, 0o600); err != nil {
		return "", cleanup, err
	}
	decodedProfile, err := exec.Command("security", "cms", "-D", "-i", profilePath).Output()
	if err != nil {
		return "", cleanup, errors.New("decode managed iOS provisioning profile")
	}
	var profileMetadata struct {
		UUID string `plist:"UUID"`
	}
	if _, err := plist.Unmarshal(decodedProfile, &profileMetadata); err != nil || strings.TrimSpace(profileMetadata.UUID) == "" {
		return "", cleanup, errors.New("managed iOS provisioning profile has no UUID")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", cleanup, err
	}
	profilesDir := filepath.Join(home, "Library", "MobileDevice", "Provisioning Profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		return "", cleanup, err
	}
	installedProfile := filepath.Join(profilesDir, profileMetadata.UUID+".mobileprovision")
	if err := os.WriteFile(installedProfile, profile, 0o600); err != nil {
		return "", cleanup, err
	}
	password, err := randomSigningSecret(24)
	if err != nil {
		_ = os.Remove(installedProfile)
		return "", cleanup, err
	}
	pfx, err := pkcs12.Modern.Encode(privateKey, certificate, nil, password)
	if err != nil {
		_ = os.Remove(installedProfile)
		return "", cleanup, err
	}
	pfxPath := filepath.Join(tmp, "identity.p12")
	keychainPath := filepath.Join(tmp, "build.keychain-db")
	if err := os.WriteFile(pfxPath, pfx, 0o600); err != nil {
		_ = os.Remove(installedProfile)
		return "", cleanup, err
	}
	oldKeychains, _ := exec.Command("security", "list-keychains", "-d", "user").Output()
	runSecurity := func(args ...string) error {
		if output, commandErr := exec.Command("security", args...).CombinedOutput(); commandErr != nil {
			return fmt.Errorf("security %s: %s", args[0], strings.TrimSpace(string(output)))
		}
		return nil
	}
	if err := runSecurity("create-keychain", "-p", password, keychainPath); err != nil {
		_ = os.Remove(installedProfile)
		return "", cleanup, err
	}
	cleanup = func() {
		_ = os.Remove(installedProfile)
		keychains := parseSecurityKeychainList(oldKeychains)
		if len(keychains) > 0 {
			_ = exec.Command("security", append([]string{"list-keychains", "-d", "user", "-s"}, keychains...)...).Run()
		}
		_ = exec.Command("security", "delete-keychain", keychainPath).Run()
	}
	if err := runSecurity("unlock-keychain", "-p", password, keychainPath); err != nil {
		cleanup()
		return "", func() {}, err
	}
	keychains := append([]string{keychainPath}, parseSecurityKeychainList(oldKeychains)...)
	if err := runSecurity(append([]string{"list-keychains", "-d", "user", "-s"}, keychains...)...); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := runSecurity("import", pfxPath, "-k", keychainPath, "-P", password, "-T", "/usr/bin/codesign", "-T", "/usr/bin/security"); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := runSecurity("set-key-partition-list", "-S", "apple-tool:,apple:", "-s", "-k", password, keychainPath); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return profileMetadata.UUID, cleanup, nil
}

func parseSecurityKeychainList(raw []byte) []string {
	fields := strings.Fields(string(raw))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(strings.TrimSpace(field), `"`)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func mobileVersionBuildEnv(base map[string]string, cfg mobileTargetConfig) map[string]string {
	out := make(map[string]string, len(base)+3)
	for key, value := range base {
		out[key] = value
	}
	if cfg.VersionName != "" {
		out["APTEVA_VERSION_NAME"] = cfg.VersionName
	}
	if cfg.BuildNumber != "" {
		out["APTEVA_BUILD_NUMBER"] = cfg.BuildNumber
	}
	if cfg.VersionCode != "" {
		out["APTEVA_VERSION_CODE"] = cfg.VersionCode
	}
	return out
}

func androidVersionInitScript(cfg mobileTargetConfig) string {
	var assignments []string
	if cfg.VersionCode != "" {
		if code, err := strconv.ParseInt(cfg.VersionCode, 10, 64); err == nil && code > 0 {
			assignments = append(assignments, fmt.Sprintf("androidExt.defaultConfig.versionCode = %d", code))
		}
	}
	if cfg.VersionName != "" {
		assignments = append(assignments, "androidExt.defaultConfig.versionName = "+strconv.Quote(cfg.VersionName))
	}
	return `allprojects { targetProject ->
    targetProject.plugins.withId("com.android.application") {
        def androidExt = targetProject.extensions.findByName("android")
        if (androidExt != null) {
            ` + strings.Join(assignments, "\n            ") + `
        }
    }
}
`
}

func buildIOSSimulatorSmoke(ctx context.Context, srcDir, artifactDir, containerFlag, containerPath, scheme, configuration string, env map[string]string, cfg mobileTargetConfig, logW io.Writer) (string, error) {
	tmp, err := os.MkdirTemp("", "apteva-ios-smoke-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	derivedPath := filepath.Join(tmp, "DerivedData")
	args := []string{
		containerFlag, containerPath,
		"-scheme", scheme,
		"-configuration", configuration,
		"-sdk", "iphonesimulator",
		"-destination", "generic/platform=iOS Simulator",
		"-derivedDataPath", derivedPath,
		"CODE_SIGNING_ALLOWED=NO",
		"build",
	}
	if err := runMobileBuildCommand(ctx, srcDir, "xcodebuild", args, env, logW); err != nil {
		return "", fmt.Errorf("xcodebuild simulator smoke: %w", err)
	}
	appPath, err := findIOSAppBundle(filepath.Join(derivedPath, "Build", "Products"))
	if err != nil {
		return "", errors.New("iOS simulator smoke build produced no .app")
	}
	name := "ios-simulator-smoke.zip"
	dst := filepath.Join(artifactDir, name)
	if err := zipDirectoryTree(filepath.Dir(appPath), filepath.Base(appPath), dst); err != nil {
		return "", err
	}
	manifest := artifactManifest{
		Platform: "ios", Primary: name, BundleID: cfg.BundleID,
		VersionName: cfg.VersionName, BuildNumber: cfg.BuildNumber,
		Files: []artifactFile{mobileArtifactFile(dst, "simulator-smoke")},
	}
	if err := writeArtifactManifest(artifactDir, manifest); err != nil {
		return "", err
	}
	fmt.Fprintf(logW, "=== iOS unsigned simulator smoke: %s ===\n", dst)
	return name, nil
}

func findIOSAppBundle(root string) (string, error) {
	var matches []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".app") {
			matches = append(matches, path)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", os.ErrNotExist
	}
	sort.Strings(matches)
	return matches[0], nil
}

func stageIPA(ipa, artifactDir string, cfg mobileTargetConfig, logW io.Writer) (string, error) {
	return stageIPAWithVersion(ipa, artifactDir, cfg, "", "", logW)
}

func stageIPAWithVersion(ipa, artifactDir string, cfg mobileTargetConfig, version, buildNumber string, logW io.Writer) (string, error) {
	if version == "" {
		version = cfg.VersionName
	}
	if buildNumber == "" {
		buildNumber = cfg.BuildNumber
	}
	if len(cfg.DeviceFamilies) == 0 {
		cfg.DeviceFamilies, _ = readIPADeviceFamilies(ipa)
	}
	primary := filepath.Base(ipa)
	dst := filepath.Join(artifactDir, primary)
	if err := copyMobileFile(ipa, dst); err != nil {
		return "", err
	}
	manifest := artifactManifest{
		Platform: "ios", Primary: primary, BundleID: cfg.BundleID,
		VersionName: version, BuildNumber: buildNumber, DeviceFamilies: cfg.DeviceFamilies,
		Files: []artifactFile{mobileArtifactFile(dst, "ipa")},
	}
	if err := writeArtifactManifest(artifactDir, manifest); err != nil {
		return "", err
	}
	fmt.Fprintf(logW, "=== iOS archive export: %s ===\n", dst)
	return primary, nil
}

func readIPADeviceFamilies(path string) ([]string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, file := range zr.File {
		name := filepath.ToSlash(file.Name)
		if !strings.HasPrefix(name, "Payload/") || !strings.HasSuffix(name, ".app/Info.plist") {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(reader, 4<<20))
		reader.Close()
		if readErr != nil {
			return nil, readErr
		}
		var document map[string]any
		if _, err := plist.Unmarshal(body, &document); err != nil {
			return nil, err
		}
		return normalizeIOSDeviceFamilies(document["UIDeviceFamily"]), nil
	}
	return nil, errors.New("IPA contains no application Info.plist")
}

func normalizeIOSDeviceFamilies(value any) []string {
	seen := map[string]bool{}
	var out []string
	var values []any
	switch typed := value.(type) {
	case []any:
		values = typed
	case []uint64:
		for _, item := range typed {
			values = append(values, item)
		}
	case []int64:
		for _, item := range typed {
			values = append(values, item)
		}
	default:
		values = append(values, value)
	}
	for _, raw := range values {
		family := strings.TrimSpace(fmt.Sprint(raw))
		switch family {
		case "1":
			family = "iphone"
		case "2":
			family = "ipad"
		}
		if family != "" && !seen[family] {
			seen[family] = true
			out = append(out, family)
		}
	}
	sort.Strings(out)
	return out
}

func runMobileBuildCommand(parent context.Context, dir, bin string, args []string, userEnv map[string]string, logW io.Writer) error {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Minute)
	defer cancel()
	fmt.Fprintf(logW, "+ %s %s (cwd=%s)\n", bin, strings.Join(args, " "), dir)
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Stdout = logW
	cmd.Stderr = logW
	cmd.Env = mobileBuildEnv(userEnv)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		return errors.New("mobile build timed out after 45 minutes")
	}
}

func mobileBuildEnv(user map[string]string) []string {
	out := []string{}
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || strings.HasPrefix(strings.ToUpper(key), "APTEVA_") {
			continue
		}
		out = append(out, entry)
	}
	for key, value := range user {
		out = append(out, key+"="+value)
	}
	return out
}

func findMobileArtifact(root, suffix, pathFragment string) (string, error) {
	var matches []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && (info.Name() == ".git" || info.Name() == ".gradle") {
			return filepath.SkipDir
		}
		if info.IsDir() || !strings.HasSuffix(strings.ToLower(path), suffix) {
			return nil
		}
		if pathFragment == "" || strings.Contains(path, pathFragment) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", os.ErrNotExist
	}
	sort.Strings(matches)
	return matches[len(matches)-1], nil
}

func writeArtifactManifest(artifactDir string, manifest artifactManifest) error {
	body, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(artifactDir, artifactManifestFilename), body, 0o644)
}

func readArtifactManifest(build *Build) (artifactManifest, error) {
	var manifest artifactManifest
	if build == nil {
		return manifest, errors.New("build required")
	}
	raw := strings.TrimSpace(build.ArtifactManifestJSON)
	if raw == "" || raw == "{}" {
		body, err := os.ReadFile(filepath.Join(build.ArtifactPath, artifactManifestFilename))
		if err != nil {
			return manifest, errors.New("build has no mobile artifact manifest")
		}
		raw = string(body)
	}
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		return manifest, fmt.Errorf("decode artifact manifest: %w", err)
	}
	if manifest.Primary == "" && manifest.ExternalProvider == "" {
		return manifest, errors.New("artifact manifest has no primary file")
	}
	return manifest, nil
}

func mobileArtifactFile(path, kind string) artifactFile {
	info, _ := os.Stat(path)
	return artifactFile{Name: filepath.Base(path), Type: kind, Size: fileSize(info), SHA256: hashMobileFile(path)}
}

func fileSize(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	return info.Size()
}

func hashMobileFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	_, _ = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

func copyMobileFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func upperFirst(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "Release"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func hasXcodeContainer(root string) bool {
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".xcodeproj") || strings.HasSuffix(entry.Name(), ".xcworkspace") {
			return true
		}
	}
	return false
}

func resolveIOSContainer(root string, cfg mobileTargetConfig) (string, string, error) {
	if cfg.WorkspacePath != "" {
		return "-workspace", absoluteFrom(root, cfg.WorkspacePath), nil
	}
	if cfg.ProjectPath != "" {
		return "-project", absoluteFrom(root, cfg.ProjectPath), nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", "", err
	}
	for _, suffix := range []string{".xcworkspace", ".xcodeproj"} {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), suffix) {
				flag := "-project"
				if suffix == ".xcworkspace" {
					flag = "-workspace"
				}
				return flag, filepath.Join(root, entry.Name()), nil
			}
		}
	}
	return "", "", errors.New("no .xcworkspace or .xcodeproj found; set project_path/workspace_path or provide project.yml with xcodegen")
}

func discoverFirstIOSScheme(ctx context.Context, root, containerFlag, containerPath string, userEnv map[string]string) (string, error) {
	args := []string{containerFlag, containerPath, "-list", "-json"}
	cmd := exec.CommandContext(ctx, "xcodebuild", args...)
	cmd.Dir = root
	cmd.Env = mobileBuildEnv(userEnv)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("xcodebuild -list: %w", err)
	}
	var payload struct {
		Project struct {
			Schemes []string `json:"schemes"`
		} `json:"project"`
		Workspace struct {
			Schemes []string `json:"schemes"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", err
	}
	schemes := payload.Project.Schemes
	if len(schemes) == 0 {
		schemes = payload.Workspace.Schemes
	}
	if len(schemes) == 0 {
		return "", errors.New("no shared Xcode scheme found; set target_config_json.scheme")
	}
	return schemes[0], nil
}

func exportOptionsPlist(teamID string) string {
	team := ""
	if teamID != "" {
		team = "\n  <key>teamID</key><string>" + xmlEscape(teamID) + "</string>"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>method</key><string>app-store-connect</string>
  <key>destination</key><string>export</string>
  <key>signingStyle</key><string>automatic</string>
  <key>manageAppVersionAndBuildNumber</key><false/>
  <key>uploadSymbols</key><true/>` + team + `
</dict></plist>
`
}

func plistValue(plist, key string) string {
	cmd := exec.Command("/usr/bin/plutil", "-extract", key, "raw", plist)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func absoluteFrom(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func normalizePEM(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, `\n`, "\n"))
}

func mobileBuildCredentialFields(supplied map[string]string, role string) (map[string]string, error) {
	if len(supplied) > 0 {
		return supplied, nil
	}
	if globalCtx == nil {
		return nil, fmt.Errorf("%s signing credentials were not supplied to the runner", role)
	}
	creds, err := boundConnectionCredentials(role)
	if err != nil {
		return nil, err
	}
	return creds.Fields, nil
}

func ensureAndroidBundleSigned(bundlePath string, required bool, logW io.Writer, supplied ...map[string]string) error {
	var fields map[string]string
	if len(supplied) > 0 && len(supplied[0]) > 0 {
		fields = supplied[0]
	}
	expectedFingerprint := normalizeCertificateFingerprint(fields["upload_certificate_sha256"])
	if androidBundleHasSignature(bundlePath) {
		if err := verifyAndroidBundleSignature(bundlePath, expectedFingerprint, logW); err != nil {
			return err
		}
		fmt.Fprintln(logW, "Android bundle signature verified against the managed upload certificate")
		return nil
	}
	jarsigner, lookErr := exec.LookPath("jarsigner")

	if len(fields) == 0 {
		if required {
			return errors.New("Android AAB is not signed and Deploy has no managed signing identity")
		}
		fmt.Fprintln(logW, "note: AAB signature was not verified; release will require a signed bundle or upload keystore")
		return nil
	}
	encoded := strings.TrimSpace(fields["upload_keystore_base64"])
	alias := strings.TrimSpace(fields["upload_key_alias"])
	storePassword := fields["upload_keystore_password"]
	keyPassword := fields["upload_key_password"]
	if encoded == "" || alias == "" || storePassword == "" {
		if required {
			return errors.New("Android AAB is not signed and the managed signing identity is incomplete")
		}
		fmt.Fprintln(logW, "note: AAB signature was not verified; no upload keystore configured")
		return nil
	}
	if lookErr != nil {
		return errors.New("jarsigner not found; install a JDK to sign the Android App Bundle")
	}
	keystore, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode upload_keystore_base64: %w", err)
	}
	tmp, err := os.MkdirTemp("", "apteva-android-sign-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	keystorePath := filepath.Join(tmp, "upload.keystore")
	if err := os.WriteFile(keystorePath, keystore, 0o600); err != nil {
		return err
	}
	env := mobileBuildEnv(nil)
	env = append(env, "APTEVA_ANDROID_STOREPASS="+storePassword)
	args := []string{
		"-keystore", keystorePath, "-storetype", "PKCS12",
		"-storepass:env", "APTEVA_ANDROID_STOREPASS",
		"-sigalg", "SHA256withRSA", "-digestalg", "SHA-256",
	}
	if keyPassword != "" {
		env = append(env, "APTEVA_ANDROID_KEYPASS="+keyPassword)
		args = append(args, "-keypass:env", "APTEVA_ANDROID_KEYPASS")
	}
	args = append(args, bundlePath, alias)
	fmt.Fprintln(logW, "+ jarsigner <bundle> <upload-key-alias>")
	cmd := exec.Command(jarsigner, args...)
	cmd.Env = env
	cmd.Stdout = logW
	cmd.Stderr = logW
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sign Android bundle: %w", err)
	}
	if err := verifyAndroidBundleSignature(bundlePath, expectedFingerprint, logW); err != nil {
		return err
	}
	fmt.Fprintln(logW, "Android bundle signed and verified with the managed upload key")
	return nil
}

func verifyAndroidBundleSignature(bundlePath, expectedFingerprint string, logW io.Writer) error {
	if expectedFingerprint == "" {
		return errors.New("managed Android upload certificate fingerprint is missing")
	}
	actualFingerprint, err := verifyAndroidBundleSignaturePureGo(bundlePath, expectedFingerprint)
	if err != nil {
		return err
	}
	fmt.Fprintf(logW, "Android App Bundle signature and payload verified for certificate %s\n", actualFingerprint)
	return nil
}

func androidBundleSignerFingerprint(bundlePath string) (string, error) {
	return verifyAndroidBundleSignaturePureGo(bundlePath, "")
}

func androidBundleHasSignature(path string) bool {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	defer reader.Close()
	hasSF := false
	hasBlock := false
	for _, file := range reader.File {
		name := strings.ToUpper(filepath.ToSlash(file.Name))
		if !strings.HasPrefix(name, "META-INF/") {
			continue
		}
		if strings.HasSuffix(name, ".SF") {
			hasSF = true
		}
		if strings.HasSuffix(name, ".RSA") || strings.HasSuffix(name, ".DSA") || strings.HasSuffix(name, ".EC") {
			hasBlock = true
		}
	}
	return hasSF && hasBlock
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}
