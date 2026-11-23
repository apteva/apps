package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// maxRemoteArchiveBytes is the Instances file transfer limit. Remote volume
// copies refuse a larger configured archive limit up front rather than
// failing partway through a transfer.
const maxRemoteArchiveBytes = 16 << 20

func remoteArchiveLimitError(maxCompressed int) error {
	if maxCompressed > maxRemoteArchiveBytes {
		return fmt.Errorf("max_archive_bytes is %d, but remote hosts transfer at most %d bytes per archive; lower max_archive_bytes to copy volumes on remote workloads", maxCompressed, maxRemoteArchiveBytes)
	}
	return nil
}

// remoteHelperRun runs a short-lived alpine container on the host with the
// same limits as local helper containers, removing it on any exit.
func remoteHelperRun(name string, extra []string, script string, scriptArgs ...string) string {
	args := []string{"docker", "run", "--rm", "--name", name, "--network", "none", "--memory", "256m", "--cpus", "1", "--pids-limit", "64"}
	args = append(args, extra...)
	args = append(args, "alpine:3.20", "sh", "-c", script, "sh")
	args = append(args, scriptArgs...)
	cleanup := "docker rm -f " + shellQuote(name) + " >/dev/null 2>&1 || true"
	return "trap " + shellQuote(cleanup) + " EXIT\n" + shellJoin(args...)
}

// ImportVolumeArchive uploads the archive to a private host folder and runs
// the shared import script with it mounted read-only.
func (d *RemoteDocker) ImportVolumeArchive(ctx context.Context, volumeName, relativePath string, archive []byte, owner string) error {
	if len(archive) > maxRemoteArchiveBytes {
		return remoteArchiveLimitError(len(archive))
	}
	if d.app == nil || d.app.PlatformAPI() == nil {
		return errors.New("platform API unavailable")
	}
	if err := d.ensureDocker(ctx); err != nil {
		return err
	}
	hostDir, cleanup, err := d.remoteTempDir(ctx, "archive")
	if err != nil {
		return err
	}
	defer cleanup()
	if err := d.uploadFile(hostDir+"/archive.tgz", archive); err != nil {
		return fmt.Errorf("stage remote archive: %w", err)
	}
	name := "containers-helper-" + newWorkloadID()[4:]
	cmd := remoteHelperRun(name,
		[]string{"--tmpfs", "/tmp:rw,nosuid,nodev,size=160m", "-v", volumeName + ":/volume", "-v", hostDir + "/archive.tgz:/archive.tgz:ro"},
		"exec 0</archive.tgz\n"+volumeImportScript, relativePath, owner)
	if out, _, err := d.runRemote(ctx, cmd, 300); err != nil {
		return fmt.Errorf("import remote volume archive: %s", strings.TrimSpace(firstNonEmpty(out, err.Error())))
	}
	return nil
}

func (d *RemoteDocker) ExportVolumeArchive(ctx context.Context, volumeName, relativePath string, maxBytes int) ([]byte, error) {
	return d.exportVolumeArchiveLimited(ctx, volumeName, relativePath, maxBytes, defaultMaxExpandedBytes, defaultMaxArchiveFiles)
}

// exportVolumeArchiveLimited writes the archive to a private host file, checks
// its size there, then fetches it with instance_download_file.
func (d *RemoteDocker) exportVolumeArchiveLimited(ctx context.Context, volumeName, relativePath string, maxBytes, maxExpanded, maxFiles int) ([]byte, error) {
	if maxBytes > maxRemoteArchiveBytes {
		return nil, remoteArchiveLimitError(maxBytes)
	}
	if d.app == nil || d.app.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	if err := d.ensureDocker(ctx); err != nil {
		return nil, err
	}
	hostDir, cleanup, err := d.remoteTempDir(ctx, "archive")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	file := hostDir + "/archive.tgz"
	name := "containers-helper-" + newWorkloadID()[4:]
	cmd := remoteHelperRun(name, []string{"-v", volumeName + ":/volume:ro"}, volumeExportScript, relativePath, strconv.Itoa(maxExpanded), strconv.Itoa(maxFiles)) +
		" > " + shellQuote(file) + "\n" +
		`size=$(wc -c < ` + shellQuote(file) + `); if [ "$size" -gt ` + strconv.Itoa(maxBytes) + ` ]; then echo "archive exceeds ` + strconv.Itoa(maxBytes) + ` bytes" >&2; exit 74; fi`
	if out, _, err := d.runRemote(ctx, cmd, 300); err != nil {
		return nil, fmt.Errorf("export remote volume archive: %s", strings.TrimSpace(firstNonEmpty(out, err.Error())))
	}
	var got struct {
		ContentB64 string `json:"content_b64"`
	}
	if err := d.app.PlatformAPI().CallAppResult("instances", "instance_download_file", map[string]any{"id": d.instanceID, "path": file}, &got); err != nil {
		return nil, fmt.Errorf("fetch remote volume archive: %w", err)
	}
	archive, err := base64.StdEncoding.DecodeString(got.ContentB64)
	if err != nil {
		return nil, fmt.Errorf("decode remote volume archive: %w", err)
	}
	if len(archive) > maxBytes {
		return nil, fmt.Errorf("archive exceeds compressed size limit of %d bytes", maxBytes)
	}
	return archive, nil
}
