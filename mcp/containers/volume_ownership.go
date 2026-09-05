package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type ownedFileWriter interface {
	WriteOwnedVolumeFile(context.Context, string, string, []byte, string, string) error
	FileOwner(context.Context, RunSpec) (string, error)
}

func (d LocalDocker) FileOwner(ctx context.Context, s RunSpec) (string, error) {
	user := s.User
	if user == "" {
		out, err := docker(ctx, "image", "inspect", "--format", "{{.Config.User}}", s.Image)
		if err != nil {
			if s.PullPolicy == "never" {
				return "", err
			}
			if _, err = docker(ctx, "pull", s.Image); err != nil {
				return "", err
			}
			out, err = docker(ctx, "image", "inspect", "--format", "{{.Config.User}}", s.Image)
			if err != nil {
				return "", err
			}
		}
		user = strings.TrimSpace(out)
	}
	if user == "" || user == "root" {
		return "0:0", nil
	}
	if regexp.MustCompile(`^[0-9]+(:[0-9]+)?$`).MatchString(user) {
		if !strings.Contains(user, ":") {
			user += ":0"
		}
		return user, nil
	}
	// Resolve names against the workload image, not Alpine's passwd database.
	out, err := helperContainer(ctx, nil, 128, "--user", user, "--entrypoint", "/bin/sh", s.Image, "-c", `printf '%s:%s' "$(id -u)" "$(id -g)"`)
	if err != nil {
		return "", fmt.Errorf("resolve file ownership; set files[].owner to numeric uid:gid: %w", err)
	}
	owner := strings.TrimSpace(out)
	if !regexp.MustCompile(`^[0-9]+:[0-9]+$`).MatchString(owner) {
		return "", errors.New("invalid resolved file owner")
	}
	return owner, nil
}
func (d LocalDocker) WriteOwnedVolumeFile(ctx context.Context, volume, rel string, content []byte, mode, owner string) error {

	_, err := helperContainer(ctx, content, 1024, "-i", "-v", volume+":/target", "alpine:3.20", "sh", "-c", ownedVolumeWriteScript, "sh", rel, mode, owner)
	return err
}
func (d *RemoteDocker) FileOwner(ctx context.Context, s RunSpec) (string, error) {
	user := s.User
	if regexp.MustCompile(`^[0-9]+(:[0-9]+)?$`).MatchString(user) {
		if !strings.Contains(user, ":") {
			user += ":0"
		}
		return user, nil
	}
	args := []string{"run", "--rm", "--network", "none", "--memory", "64m", "--pids-limit", "32"}
	if user != "" {
		args = append(args, "--user", user)
	}
	args = append(args, "--entrypoint", "/bin/sh", s.Image, "-c", `printf '%s:%s' "$(id -u)" "$(id -g)"`)
	out, err := d.remoteDocker(ctx, 120, args...)
	if err != nil {
		return "", err
	}
	owner := strings.TrimSpace(out)
	if !regexp.MustCompile(`^[0-9]+:[0-9]+$`).MatchString(owner) {
		return "", errors.New("set files[].owner to numeric uid:gid")
	}
	return owner, nil
}
func (d *RemoteDocker) WriteOwnedVolumeFile(ctx context.Context, volume, rel string, content []byte, mode, owner string) error {
	return d.writeOwnedRemoteFile(ctx, volume, rel, content, mode, owner)
}
func retainedVolume(db *sql.DB, sourceID, name string, owner ownerIdentity, targetID int64) (string, error) {
	var source *Workload
	var err error
	if owner.InstallID == 0 {
		source, err = getWorkload(db, sourceID)
		if err == nil && (source == nil || source.ProjectID != owner.ProjectID) {
			err = errWorkloadNotFound
		}
	} else {
		source, err = requireOwnedWorkload(db, sourceID, owner)
	}
	if err != nil {
		return "", err
	}
	if source.Status != StatusDestroyed || workloadTargetID(source) != targetID {
		return "", fmt.Errorf("%w: retained volume must belong to a destroyed workload on the same host", errConflict)
	}
	v, err := findWorkloadVolume(source, name)
	if err != nil {
		return "", err
	}
	return v.DockerVolumeName, nil
}

const ownedVolumeWriteScript = `set -eu
rel="$1"; mode="$2"; owner="$3"
case "$rel" in /*|*../*|..|*:/?*) exit 1;; esac
parent=/target
set -f; oldifs="$IFS"; IFS=/; set -- $rel; IFS="$oldifs"
while [ "$#" -gt 1 ]; do parent="$parent/$1"; [ ! -L "$parent" ] || exit 73; mkdir -p "$parent"; shift; done
dest="$parent/$1"; [ ! -L "$dest" ] || exit 73
umask 077
tmp="$parent/.apteva-file-$$"; trap 'rm -f "$tmp"' EXIT
cat > "$tmp"; chmod "$mode" "$tmp"; chown "$owner" "$tmp"; mv "$tmp" "$dest"`
