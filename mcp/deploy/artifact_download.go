package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	errBuildArtifactNotReady = errors.New("build artifact is not ready")
	errBuildArtifactPruned   = errors.New("build artifact has been pruned")
)

type buildArtifactDownload struct {
	Path      string
	Filename  string
	Directory bool
	Size      int64
}

func buildArtifactDownloadURL(buildID int64, projectID string) string {
	path := "/api/apps/deploy/api/builds/" + strconv.FormatInt(buildID, 10) + "/artifact"
	if strings.TrimSpace(projectID) == "" {
		return path
	}
	query := url.Values{}
	query.Set("project_id", projectID)
	return path + "?" + query.Encode()
}

func buildWithArtifactDownloadURL(build *Build, projectID string) *Build {
	if build == nil {
		return nil
	}
	decorated := *build
	if decorated.Status == "succeeded" && strings.TrimSpace(decorated.ArtifactPath) != "" {
		decorated.ArtifactDownloadURL = buildArtifactDownloadURL(decorated.ID, projectID)
	}
	return &decorated
}

func buildsWithArtifactDownloadURLs(builds []Build, projectID string) []Build {
	decorated := make([]Build, len(builds))
	for i := range builds {
		decorated[i] = *buildWithArtifactDownloadURL(&builds[i], projectID)
	}
	return decorated
}

func resolveBuildArtifactDownload(deployment *Deployment, build *Build) (buildArtifactDownload, error) {
	if deployment == nil || build == nil || build.Status != "succeeded" {
		return buildArtifactDownload{}, errBuildArtifactNotReady
	}
	root := strings.TrimSpace(build.ArtifactPath)
	if root == "" {
		return buildArtifactDownload{}, errBuildArtifactPruned
	}
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return buildArtifactDownload{}, errBuildArtifactPruned
		}
		return buildArtifactDownload{}, err
	}
	if !info.IsDir() {
		return buildArtifactDownload{
			Path: root, Filename: artifactDownloadFilename(deployment, build, artifactManifest{}, filepath.Ext(root)), Size: info.Size(),
		}, nil
	}

	manifest, found, err := artifactManifestForDownload(build)
	if err != nil {
		return buildArtifactDownload{}, err
	}
	if found && strings.TrimSpace(manifest.Primary) != "" {
		primary, err := confinedDownloadArtifactPath(root, manifest.Primary)
		if err != nil {
			return buildArtifactDownload{}, err
		}
		primaryInfo, err := os.Stat(primary)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return buildArtifactDownload{}, errBuildArtifactPruned
			}
			return buildArtifactDownload{}, err
		}
		if primaryInfo.IsDir() {
			return buildArtifactDownload{}, errors.New("artifact manifest primary file is a directory")
		}
		return buildArtifactDownload{
			Path: primary, Filename: artifactDownloadFilename(deployment, build, manifest, filepath.Ext(primary)), Size: primaryInfo.Size(),
		}, nil
	}

	return buildArtifactDownload{
		Path: root, Filename: artifactDownloadFilename(deployment, build, manifest, ".zip"), Directory: true, Size: build.ArtifactSize,
	}, nil
}

func artifactManifestForDownload(build *Build) (artifactManifest, bool, error) {
	var manifest artifactManifest
	raw := strings.TrimSpace(build.ArtifactManifestJSON)
	if raw == "" || raw == "{}" {
		body, err := os.ReadFile(filepath.Join(build.ArtifactPath, artifactManifestFilename))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return manifest, false, nil
			}
			return manifest, false, err
		}
		raw = string(body)
	}
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		return manifest, true, fmt.Errorf("decode artifact manifest: %w", err)
	}
	return manifest, true, nil
}

func confinedDownloadArtifactPath(root, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.IsAbs(name) {
		return "", errors.New("artifact manifest has an invalid primary file")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(absoluteRoot, filepath.Clean(filepath.FromSlash(name))))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(absoluteRoot, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("artifact manifest primary file escapes the artifact directory")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return path, nil
	} else if err != nil {
		return "", err
	}
	evaluatedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return "", err
	}
	evaluatedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	evaluatedRelative, err := filepath.Rel(evaluatedRoot, evaluatedPath)
	if err != nil || evaluatedRelative == ".." || strings.HasPrefix(evaluatedRelative, ".."+string(filepath.Separator)) {
		return "", errors.New("artifact manifest primary file resolves outside the artifact directory")
	}
	return path, nil
}

func artifactDownloadFilename(deployment *Deployment, build *Build, manifest artifactManifest, extension string) string {
	name := "artifact"
	if deployment != nil && strings.TrimSpace(deployment.Name) != "" {
		name = deployment.Name
	}
	parts := []string{sanitizeArtifactFilenamePart(name)}
	if manifest.VersionName != "" {
		parts = append(parts, sanitizeArtifactFilenamePart(manifest.VersionName))
	}
	buildNumber := manifest.BuildNumber
	if buildNumber == "" {
		buildNumber = manifest.VersionCode
	}
	if buildNumber != "" {
		parts = append(parts, sanitizeArtifactFilenamePart(buildNumber))
	}
	if len(parts) == 1 && build != nil {
		parts = append(parts, "build", strconv.FormatInt(build.ID, 10))
	}
	extension = strings.ToLower(extension)
	if extension == "" {
		extension = ".bin"
	}
	return strings.Join(parts, "-") + extension
}

func sanitizeArtifactFilenamePart(value string) string {
	value = strings.TrimSpace(value)
	var out strings.Builder
	lastDash := false
	for _, char := range value {
		allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '_'
		if allowed {
			out.WriteRune(char)
			lastDash = false
			continue
		}
		if !lastDash && out.Len() > 0 {
			out.WriteByte('-')
			lastDash = true
		}
	}
	clean := strings.Trim(out.String(), "-.")
	if clean == "" {
		return "artifact"
	}
	return clean
}

func serveBuildArtifact(w http.ResponseWriter, r *http.Request, artifact buildArtifactDownload) error {
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Filename})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if artifact.Directory {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return nil
		}
		return streamArtifactDirectoryZip(w, artifact.Path)
	}
	file, err := os.Open(artifact.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	http.ServeContent(w, r, artifact.Filename, info.ModTime(), file)
	return nil
}

func streamArtifactDirectoryZip(output io.Writer, root string) error {
	paths := make([]string, 0)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact contains unsupported symlink %q", path)
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		return err
	}

	writer := zip.NewWriter(output)
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			_ = writer.Close()
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			_ = writer.Close()
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			_ = writer.Close()
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.Method = zip.Deflate
		if info.IsDir() {
			header.Name += "/"
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			return err
		}
		if info.IsDir() {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			_ = writer.Close()
			return err
		}
		_, copyErr := io.Copy(entry, file)
		closeErr := file.Close()
		if copyErr != nil {
			_ = writer.Close()
			return copyErr
		}
		if closeErr != nil {
			_ = writer.Close()
			return closeErr
		}
	}
	return writer.Close()
}
