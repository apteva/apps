package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	defaultMaxArchiveBytes  = 8 * 1024 * 1024
	defaultMaxExpandedBytes = 128 * 1024 * 1024
	defaultMaxArchiveFiles  = 20000
)

type volumeArchiveBackend interface {
	ImportVolumeArchive(context.Context, string, string, []byte) error
	ExportVolumeArchive(context.Context, string, string, int) ([]byte, error)
}

func (d LocalDocker) ImportVolumeArchive(ctx context.Context, volumeName, relativePath string, archive []byte) error {
	script := `set -eu
umask 077
mkdir /tmp/stage
tar -xzf - -C /tmp/stage
# No pre-existing links may redirect this import. The caller pauses the workload
# while committing so its processes cannot replace checked components.
dest=/volume
rel="$1"; set -f; oldifs="$IFS"; IFS=/; set -- $rel; IFS="$oldifs"
for part in "$@"; do [ "$part" = . ] && continue; dest="$dest/$part"; [ ! -L "$dest" ] || { echo 'destination symlink rejected' >&2; exit 73; }; done
if [ -d "$dest" ] && [ -n "$(find "$dest" -type l -print -quit)" ]; then echo 'destination contains symlinks' >&2; exit 73; fi
mkdir -p "$dest"
cp -a /tmp/stage/. "$dest/"`
	_, err := helperContainer(ctx, archive, 1024, "-i", "--tmpfs", "/tmp:rw,nosuid,nodev,size=160m", "-v", volumeName+":/volume", "alpine:3.20", "sh", "-c", script, "sh", relativePath)
	return err
}
func (d LocalDocker) ExportVolumeArchive(ctx context.Context, volumeName, relativePath string, maxBytes int) ([]byte, error) {
	return d.exportVolumeArchiveLimited(ctx, volumeName, relativePath, maxBytes, defaultMaxExpandedBytes, defaultMaxArchiveFiles)
}
func (d LocalDocker) exportVolumeArchiveLimited(ctx context.Context, volumeName, relativePath string, maxBytes, maxExpanded, maxFiles int) ([]byte, error) {
	script := `set -eu
cd /volume
item="./$1"
rel="$1"; expanded="$2"; files="$3"; current=.; set -f; oldifs="$IFS"; IFS=/; set -- $rel; IFS="$oldifs"
for part in "$@"; do current="$current/$part"; [ ! -L "$current" ] || { echo 'source symlink rejected' >&2; exit 73; }; done
[ -e "$item" ] || exit 1
find "$item" -exec stat -c '%s' '{}' + | awk -v bytes="$expanded" -v files="$files" '{ total+=$1; count++; if(total>bytes || count>files) exit 73; }'
tar -czf - "$item"`
	out, err := helperContainer(ctx, nil, maxBytes, "-v", volumeName+":/volume:ro", "alpine:3.20", "sh", "-c", script, "sh", relativePath, strconv.Itoa(maxExpanded), strconv.Itoa(maxFiles))
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

func normalizeVolumePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		return ".", nil
	}
	if strings.HasPrefix(value, "/") || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("path must be relative to the named volume")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", errors.New("path must not contain '..'")
		}
	}
	clean := path.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path escapes the named volume")
	}
	return clean, nil
}

func validateTarGzip(archive []byte, maxCompressed, maxExpanded, maxFiles int) error {
	if len(archive) == 0 {
		return errors.New("archive is empty")
	}
	if len(archive) > maxCompressed {
		return fmt.Errorf("archive exceeds compressed size limit of %d bytes", maxCompressed)
	}
	compressed := bytes.NewReader(archive)
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return errors.New("archive must be a valid tar.gz stream")
	}
	defer gz.Close()
	gz.Multistream(false)
	expandedStream := &io.LimitedReader{R: gz, N: int64(maxExpanded) + int64(maxFiles)*1024 + 2*1024*1024}
	tr := tar.NewReader(expandedStream)
	var expanded int64
	files := 0
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar.gz: %w", err)
		}
		files++
		if files > maxFiles {
			return fmt.Errorf("archive exceeds file limit of %d", maxFiles)
		}
		name, err := safeArchiveEntry(header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 {
				return fmt.Errorf("archive entry %q has invalid size", header.Name)
			}
			expanded += header.Size
		case tar.TypeDir:
		case tar.TypeSymlink:
			if err := validateArchiveLink(name, header.Linkname, true); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := validateArchiveLink(name, header.Linkname, false); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive entry %q has unsupported type %d", header.Name, header.Typeflag)
		}
		if expanded > int64(maxExpanded) {
			return fmt.Errorf("archive exceeds expanded size limit of %d bytes", maxExpanded)
		}
	}
	// Finish gzip validation, including CRC, and bound padding/extension headers.
	padding := make([]byte, 32768)
	for {
		n, err := expandedStream.Read(padding)
		for _, b := range padding[:n] {
			if b != 0 {
				return errors.New("archive contains data after tar end")
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("gzip checksum: %w", err)
		}
	}
	if expandedStream.N == 0 {
		return errors.New("archive decompressed stream exceeds limit")
	}
	if compressed.Len() != 0 {
		return errors.New("archive contains concatenated gzip streams or trailing data")
	}
	return nil
}

func safeArchiveEntry(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.IndexByte(name, 0) >= 0 {
		return "", fmt.Errorf("unsafe archive entry %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("archive entry %q escapes its destination", name)
		}
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("archive entry %q escapes its destination", name)
	}
	return clean, nil
}

func validateArchiveLink(entry, target string, symbolic bool) error {
	if target == "" || strings.HasPrefix(target, "/") || strings.IndexByte(target, 0) >= 0 {
		return fmt.Errorf("archive link %q has unsafe target %q", entry, target)
	}
	for _, part := range strings.Split(target, "/") {
		if part == ".." {
			return fmt.Errorf("archive link %q may not contain parent traversal", entry)
		}
	}
	resolved := target
	if symbolic {
		resolved = path.Join(path.Dir(entry), target)
	}
	clean := path.Clean(resolved)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("archive link %q escapes its destination", entry)
	}
	return nil
}

func findWorkloadVolume(workload *Workload, name string) (*VolumeSpec, error) {
	name = strings.TrimSpace(name)
	for i := range workload.Volumes {
		if workload.Volumes[i].Name == name {
			return &workload.Volumes[i], nil
		}
	}
	return nil, fmt.Errorf("volume %q is not attached to workload", name)
}

func (a *App) importVolumeArchive(app *sdk.AppCtx, workload *Workload, volumeName, relativePath, encoded string) (result map[string]any, resultErr error) {
	if workload.HostID != 0 || workload.InstanceID != 0 {
		return nil, errors.New("volume archive transfer currently supports local workloads only")
	}
	volume, err := findWorkloadVolume(workload, volumeName)
	if err != nil {
		return nil, err
	}
	relativePath, err = normalizeVolumePath(relativePath)
	if err != nil {
		return nil, err
	}
	maxCompressed := configInt(app, "max_archive_bytes", defaultMaxArchiveBytes)
	if maxCompressed <= 0 {
		maxCompressed = defaultMaxArchiveBytes
	}
	if len(encoded) > ((maxCompressed+2)/3)*4+4 {
		return nil, fmt.Errorf("archive exceeds compressed size limit of %d bytes", maxCompressed)
	}
	archive, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("archive_base64 is not valid base64")
	}
	maxExpanded := configInt(app, "max_archive_expanded_bytes", defaultMaxExpandedBytes)
	maxFiles := configInt(app, "max_archive_files", defaultMaxArchiveFiles)
	if maxExpanded <= 0 {
		maxExpanded = defaultMaxExpandedBytes
	}
	if maxFiles <= 0 {
		maxFiles = defaultMaxArchiveFiles
	}
	if err := validateTarGzip(archive, maxCompressed, maxExpanded, maxFiles); err != nil {
		return nil, err
	}
	backend, err := a.backendForWorkload(app, workload)
	if err != nil {
		return nil, err
	}
	archiveBackend, ok := backend.(volumeArchiveBackend)
	if !ok {
		return nil, errors.New("volume archive transfer is not supported by this container backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ctx, unlock, lockErr := a.lockWorkload(ctx, workload.ID, false)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	fresh, err := requireWorkload(app.AppDB(), workload.ID)
	if err != nil {
		return nil, err
	}
	volume, err = findWorkloadVolume(fresh, volumeName)
	if err != nil {
		return nil, err
	}
	workload = fresh
	resume, err := a.pauseArchive(ctx, app, workload)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resume(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("archive completed but workload resume is pending: %w", err))
		}
	}()
	if err := archiveBackend.ImportVolumeArchive(ctx, volume.DockerVolumeName, relativePath, archive); err != nil {
		return nil, err
	}
	return map[string]any{"workload_id": workload.ID, "volume": volume.Name, "path": relativePath, "compressed_bytes": len(archive)}, nil
}

func (a *App) exportVolumeArchive(app *sdk.AppCtx, workload *Workload, volumeName, relativePath string) (result map[string]any, resultErr error) {
	if workload.HostID != 0 || workload.InstanceID != 0 {
		return nil, errors.New("volume archive transfer currently supports local workloads only")
	}
	volume, err := findWorkloadVolume(workload, volumeName)
	if err != nil {
		return nil, err
	}
	relativePath, err = normalizeVolumePath(relativePath)
	if err != nil {
		return nil, err
	}
	backend, err := a.backendForWorkload(app, workload)
	if err != nil {
		return nil, err
	}
	archiveBackend, ok := backend.(volumeArchiveBackend)
	if !ok {
		return nil, errors.New("volume archive transfer is not supported by this container backend")
	}
	maxCompressed := configInt(app, "max_archive_bytes", defaultMaxArchiveBytes)
	if maxCompressed <= 0 {
		maxCompressed = defaultMaxArchiveBytes
	}
	maxExpanded := configInt(app, "max_archive_expanded_bytes", defaultMaxExpandedBytes)
	if maxExpanded <= 0 {
		maxExpanded = defaultMaxExpandedBytes
	}
	maxFiles := configInt(app, "max_archive_files", defaultMaxArchiveFiles)
	if maxFiles <= 0 {
		maxFiles = defaultMaxArchiveFiles
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ctx, unlock, lockErr := a.lockWorkload(ctx, workload.ID, false)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	fresh, err := requireWorkload(app.AppDB(), workload.ID)
	if err != nil {
		return nil, err
	}
	volume, err = findWorkloadVolume(fresh, volumeName)
	if err != nil {
		return nil, err
	}
	workload = fresh
	resume, err := a.pauseArchive(ctx, app, workload)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resume(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("archive completed but workload resume is pending: %w", err))
		}
	}()
	var archive []byte
	if local, ok := backend.(LocalDocker); ok {
		archive, err = local.exportVolumeArchiveLimited(ctx, volume.DockerVolumeName, relativePath, maxCompressed, maxExpanded, maxFiles)
	} else {
		archive, err = archiveBackend.ExportVolumeArchive(ctx, volume.DockerVolumeName, relativePath, maxCompressed)
	}
	if err != nil {
		return nil, err
	}
	if err := validateTarGzip(archive, maxCompressed, maxExpanded, maxFiles); err != nil {
		return nil, err
	}
	return map[string]any{
		"workload_id": workload.ID, "volume": volume.Name, "path": relativePath,
		"format": "tar.gz", "archive_base64": base64.StdEncoding.EncodeToString(archive),
		"compressed_bytes": len(archive),
	}, nil
}
