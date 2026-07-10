package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

const artifactManifestFilename = ".apteva-artifact.json"

type mobileTargetConfig struct {
	Module        string   `json:"module,omitempty"`
	Variant       string   `json:"variant,omitempty"`
	PackageName   string   `json:"package_name,omitempty"`
	GradleArgs    []string `json:"gradle_args,omitempty"`
	ProjectPath   string   `json:"project_path,omitempty"`
	WorkspacePath string   `json:"workspace_path,omitempty"`
	Scheme        string   `json:"scheme,omitempty"`
	Configuration string   `json:"configuration,omitempty"`
	TeamID        string   `json:"team_id,omitempty"`
	BundleID      string   `json:"bundle_id,omitempty"`
	VersionName   string   `json:"version_name,omitempty"`
	BuildNumber   string   `json:"build_number,omitempty"`
	AppStoreAppID string   `json:"app_store_app_id,omitempty"`
	BetaGroupID   string   `json:"beta_group_id,omitempty"`
	ReleaseType   string   `json:"release_type,omitempty"`
}

type artifactManifest struct {
	Platform    string         `json:"platform"`
	Primary     string         `json:"primary"`
	PackageName string         `json:"package_name,omitempty"`
	BundleID    string         `json:"bundle_id,omitempty"`
	VersionName string         `json:"version_name,omitempty"`
	BuildNumber string         `json:"build_number,omitempty"`
	Files       []artifactFile `json:"files"`
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

	if strings.TrimSpace(ov.BuildCmd) != "" {
		if err := runMobileBuildCommand(srcDir, "sh", []string{"-c", ov.BuildCmd}, ov.Env, logW); err != nil {
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
		args = append(args, cfg.GradleArgs...)
		if err := runMobileBuildCommand(srcDir, bin, args, ov.Env, logW); err != nil {
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
	if err := ensureAndroidBundleSigned(dst, false, logW); err != nil {
		return "", err
	}
	manifest := artifactManifest{
		Platform: "android", Primary: primary, PackageName: cfg.PackageName,
		Files: []artifactFile{mobileArtifactFile(dst, "aab")},
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
	if strings.TrimSpace(ov.BuildCmd) != "" {
		if err := runMobileBuildCommand(srcDir, "sh", []string{"-c", ov.BuildCmd}, ov.Env, logW); err != nil {
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
	creds, err := boundConnectionCredentials("app_store")
	if err != nil {
		return "", err
	}
	issuerID := strings.TrimSpace(creds.Fields["issuer_id"])
	keyID := strings.TrimSpace(creds.Fields["key_id"])
	privateKey := normalizePEM(creds.Fields["private_key"])
	if issuerID == "" || keyID == "" || privateKey == "" {
		return "", errors.New("App Store Connect connection requires issuer_id, key_id, and private_key")
	}

	if exists(filepath.Join(srcDir, "project.yml")) && !hasXcodeContainer(srcDir) {
		if _, err := exec.LookPath("xcodegen"); err != nil {
			return "", errors.New("project.yml found but xcodegen is not on PATH")
		}
		if err := runMobileBuildCommand(srcDir, "xcodegen", []string{"generate"}, ov.Env, logW); err != nil {
			return "", fmt.Errorf("xcodegen generate: %w", err)
		}
	}

	containerFlag, containerPath, err := resolveIOSContainer(srcDir, cfg)
	if err != nil {
		return "", err
	}
	scheme := strings.TrimSpace(cfg.Scheme)
	if scheme == "" {
		scheme, err = discoverFirstIOSScheme(srcDir, containerFlag, containerPath, ov.Env)
		if err != nil {
			return "", err
		}
	}
	configuration := defaultStr(cfg.Configuration, "Release")

	tmp, err := os.MkdirTemp("", "apteva-ios-build-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
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
	archiveArgs = append(archiveArgs, "archive")
	if err := runMobileBuildCommand(srcDir, "xcodebuild", archiveArgs, ov.Env, logW); err != nil {
		return "", fmt.Errorf("xcodebuild archive: %w", err)
	}

	exportOptions := filepath.Join(tmp, "ExportOptions.plist")
	if err := os.WriteFile(exportOptions, []byte(exportOptionsPlist(cfg.TeamID)), 0o600); err != nil {
		return "", err
	}
	exportDir := filepath.Join(tmp, "export")
	exportArgs := []string{"-exportArchive", "-archivePath", archivePath, "-exportPath", exportDir, "-exportOptionsPlist", exportOptions}
	exportArgs = append(exportArgs, authArgs...)
	if err := runMobileBuildCommand(srcDir, "xcodebuild", exportArgs, ov.Env, logW); err != nil {
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
	primary := filepath.Base(ipa)
	dst := filepath.Join(artifactDir, primary)
	if err := copyMobileFile(ipa, dst); err != nil {
		return "", err
	}
	manifest := artifactManifest{
		Platform: "ios", Primary: primary, BundleID: cfg.BundleID,
		VersionName: version, BuildNumber: buildNumber,
		Files: []artifactFile{mobileArtifactFile(dst, "ipa")},
	}
	if err := writeArtifactManifest(artifactDir, manifest); err != nil {
		return "", err
	}
	fmt.Fprintf(logW, "=== iOS archive export: %s ===\n", dst)
	return primary, nil
}

func runMobileBuildCommand(dir, bin string, args []string, userEnv map[string]string, logW io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
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
	if manifest.Primary == "" {
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

func discoverFirstIOSScheme(root, containerFlag, containerPath string, userEnv map[string]string) (string, error) {
	args := []string{containerFlag, containerPath, "-list", "-json"}
	cmd := exec.Command("xcodebuild", args...)
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

func ensureAndroidBundleSigned(bundlePath string, required bool, logW io.Writer) error {
	if androidBundleHasSignature(bundlePath) {
		fmt.Fprintln(logW, "Android bundle signature present")
		return nil
	}
	jarsigner, lookErr := exec.LookPath("jarsigner")

	creds, credErr := boundConnectionCredentials("android_signing")
	if credErr != nil {
		// Compatibility fallback for connections created while upload-key
		// fields were temporarily part of the Google Play integration.
		creds, credErr = boundConnectionCredentials("play_store")
	}
	if credErr != nil {
		if required {
			return errors.New("Android AAB is not signed; configure Gradle release signing or bind Android Upload Signing")
		}
		fmt.Fprintln(logW, "note: AAB signature was not verified; release will require a signed bundle or upload keystore")
		return nil
	}
	encoded := strings.TrimSpace(creds.Fields["upload_keystore_base64"])
	alias := strings.TrimSpace(creds.Fields["upload_key_alias"])
	storePassword := creds.Fields["upload_keystore_password"]
	keyPassword := creds.Fields["upload_key_password"]
	if encoded == "" || alias == "" || storePassword == "" {
		if required {
			return errors.New("Android AAB is not signed and the Android Upload Signing connection is incomplete")
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
	args := []string{"-keystore", keystorePath, "-storepass:env", "APTEVA_ANDROID_STOREPASS"}
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
	if !androidBundleHasSignature(bundlePath) {
		return errors.New("jarsigner completed but the Android bundle has no JAR signature")
	}
	fmt.Fprintln(logW, "Android bundle signed with configured upload key")
	return nil
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
