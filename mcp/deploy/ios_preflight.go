package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
	"howett.net/plist"
)

var iosBuildSettingPattern = regexp.MustCompile(`(?m)^\s*"?([A-Za-z0-9_]+)"?\s*=\s*([^;]*);`)

var iosOrientationValues = map[string]struct{}{
	"UIInterfaceOrientationPortrait":           {},
	"UIInterfaceOrientationPortraitUpsideDown": {},
	"UIInterfaceOrientationLandscapeLeft":      {},
	"UIInterfaceOrientationLandscapeRight":     {},
}

var iosOrientationPlistKeys = []string{
	"UISupportedInterfaceOrientations",
	"UISupportedInterfaceOrientations~iphone",
	"UISupportedInterfaceOrientations~ipad",
}

type iosPlistReference struct {
	baseDir string
	value   string
}

type iosProjectMetadata struct {
	orientationValues     []string
	plistReferences       []iosPlistReference
	entitlementReferences []iosPlistReference
}

func hasIOSSupportedOrientations(root string) (bool, error) {
	metadata, err := readIOSProjectMetadata(root)
	if err != nil {
		return false, err
	}
	for _, value := range metadata.orientationValues {
		if containsIOSOrientation(value) {
			return true, nil
		}
	}

	referenced, err := resolveIOSInfoPlists(root, metadata.plistReferences)
	if err != nil {
		return false, err
	}
	var referencedErrors []error
	for _, path := range referenced {
		ok, err := plistHasIOSSupportedOrientations(path)
		if err != nil {
			referencedErrors = append(referencedErrors, fmt.Errorf("%s: %w", relativeDisplayPath(root, path), err))
			continue
		}
		if ok {
			return true, nil
		}
	}

	fallback, err := discoverIOSPlists(root, referenced)
	if err != nil {
		return false, err
	}
	for _, path := range fallback {
		ok, err := plistHasIOSSupportedOrientations(path)
		if err != nil {
			continue
		}
		if ok {
			return true, nil
		}
	}
	if len(referencedErrors) > 0 {
		return false, fmt.Errorf("parse referenced Info.plist: %w", errors.Join(referencedErrors...))
	}
	return false, nil
}

func readIOSProjectMetadata(root string) (iosProjectMetadata, error) {
	var metadata iosProjectMetadata
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if shouldSkipIOSGeneratedDir(info.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		switch info.Name() {
		case "project.yml":
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var document yaml.Node
			if err := yaml.Unmarshal(body, &document); err != nil {
				return fmt.Errorf("parse %s: %w", relativeDisplayPath(root, path), err)
			}
			collectIOSYAMLSettings(&document, filepath.Dir(path), &metadata)
		case "project.pbxproj":
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			projectDir := filepath.Dir(filepath.Dir(path))
			collectIOSPBXSettings(string(body), projectDir, &metadata)
		}
		return nil
	})
	return metadata, err
}

func collectIOSYAMLSettings(node *yaml.Node, baseDir string, metadata *iosProjectMetadata) {
	if node == nil {
		return
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := strings.TrimSpace(node.Content[i].Value)
			value := node.Content[i+1]
			switch {
			case isIOSOrientationBuildSetting(key):
				metadata.orientationValues = append(metadata.orientationValues, yamlScalarValues(value)...)
			case key == "INFOPLIST_FILE":
				for _, ref := range yamlScalarValues(value) {
					metadata.plistReferences = append(metadata.plistReferences, iosPlistReference{baseDir: baseDir, value: ref})
				}
			case key == "CODE_SIGN_ENTITLEMENTS":
				for _, ref := range yamlScalarValues(value) {
					metadata.entitlementReferences = append(metadata.entitlementReferences, iosPlistReference{baseDir: baseDir, value: ref})
				}
			}
		}
	}
	for _, child := range node.Content {
		collectIOSYAMLSettings(child, baseDir, metadata)
	}
}

func yamlScalarValues(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.ScalarNode {
		return []string{node.Value}
	}
	var values []string
	for _, child := range node.Content {
		values = append(values, yamlScalarValues(child)...)
	}
	return values
}

func collectIOSPBXSettings(body, baseDir string, metadata *iosProjectMetadata) {
	for _, match := range iosBuildSettingPattern.FindAllStringSubmatch(body, -1) {
		key := strings.TrimSpace(match[1])
		value := strings.TrimSpace(match[2])
		switch {
		case isIOSOrientationBuildSetting(key):
			metadata.orientationValues = append(metadata.orientationValues, value)
		case key == "INFOPLIST_FILE":
			metadata.plistReferences = append(metadata.plistReferences, iosPlistReference{baseDir: baseDir, value: value})
		case key == "CODE_SIGN_ENTITLEMENTS":
			metadata.entitlementReferences = append(metadata.entitlementReferences, iosPlistReference{baseDir: baseDir, value: value})
		}
	}
}

func isIOSOrientationBuildSetting(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "infoplist_key_uisupportedinterfaceorientations",
		"infoplist_key_uisupportedinterfaceorientations_iphone",
		"infoplist_key_uisupportedinterfaceorientations_ipad":
		return true
	default:
		return false
	}
}

func containsIOSOrientation(value string) bool {
	for _, token := range strings.FieldsFunc(value, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_')
	}) {
		if _, ok := iosOrientationValues[token]; ok {
			return true
		}
	}
	return false
}

func resolveIOSInfoPlists(root string, references []iosPlistReference) ([]string, error) {
	return resolveIOSProjectFiles(root, references, "INFOPLIST_FILE")
}

func resolveIOSProjectFiles(root string, references []iosPlistReference, setting string) ([]string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var paths []string
	for _, ref := range references {
		path, ok := resolveIOSProjectFileReference(ref)
		if !ok {
			continue
		}
		pathAbs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		pathReal, err := filepath.EvalSymlinks(pathAbs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !pathWithinRoot(rootReal, pathReal) {
			return nil, fmt.Errorf("%s resolves outside source root: %s", setting, ref.value)
		}
		if _, ok := seen[pathReal]; ok {
			continue
		}
		seen[pathReal] = struct{}{}
		paths = append(paths, pathReal)
	}
	return paths, nil
}

func resolveIOSProjectFileReference(ref iosPlistReference) (string, bool) {
	value := strings.Trim(strings.TrimSpace(ref.value), `"'`)
	for _, variable := range []string{
		"$(SRCROOT)", "${SRCROOT}", "$(SOURCE_ROOT)", "${SOURCE_ROOT}",
		"$(PROJECT_DIR)", "${PROJECT_DIR}",
	} {
		value = strings.ReplaceAll(value, variable, ref.baseDir)
	}
	if strings.Contains(value, "$(") || strings.Contains(value, "${") {
		return "", false
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), true
	}
	return filepath.Clean(filepath.Join(ref.baseDir, value)), true
}

func pathWithinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func discoverIOSPlists(root string, referenced []string) ([]string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, path := range referenced {
		seen[path] = struct{}{}
	}
	var paths []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if shouldSkipIOSGeneratedDir(info.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(info.Name()), ".plist") {
			return nil
		}
		pathReal, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil
		}
		if !pathWithinRoot(rootReal, pathReal) {
			return nil
		}
		if _, ok := seen[pathReal]; ok {
			return nil
		}
		seen[pathReal] = struct{}{}
		paths = append(paths, pathReal)
		return nil
	})
	return paths, err
}

func shouldSkipIOSGeneratedDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "deriveddata", "build", ".build", ".swiftpm", "node_modules", "pods", "carthage", "vendor":
		return true
	default:
		return false
	}
}

func plistHasIOSSupportedOrientations(path string) (bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var document map[string]any
	if _, err := plist.Unmarshal(body, &document); err != nil {
		return false, err
	}
	for _, key := range iosOrientationPlistKeys {
		if plistValueContainsIOSOrientation(document[key]) {
			return true, nil
		}
	}
	return false, nil
}

func plistValueContainsIOSOrientation(value any) bool {
	switch typed := value.(type) {
	case string:
		return containsIOSOrientation(typed)
	case []string:
		for _, item := range typed {
			if containsIOSOrientation(item) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if plistValueContainsIOSOrientation(item) {
				return true
			}
		}
	}
	return false
}

func relativeDisplayPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
