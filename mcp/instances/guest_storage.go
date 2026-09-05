package main

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// VolumePrepareRequest describes the guest-side activation of an attached
// block volume. Formatting is deliberately opt-in except when the caller has
// just created a brand-new managed volume.
type VolumePrepareRequest struct {
	Filesystem    string `json:"filesystem,omitempty"`
	MountPath     string `json:"mount_path"`
	Owner         string `json:"owner,omitempty"`
	Mode          string `json:"mode,omitempty"`
	MountOptions  string `json:"mount_options,omitempty"`
	FormatIfBlank bool   `json:"format_if_blank,omitempty"`
}

type VolumePrepareResult struct {
	DevicePath     string `json:"device_path"`
	Filesystem     string `json:"filesystem"`
	MountPath      string `json:"mount_path"`
	UUID           string `json:"uuid"`
	Formatted      bool   `json:"formatted"`
	AlreadyMounted bool   `json:"already_mounted"`
}

var (
	guestPathPattern    = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)
	guestOwnerPattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?::[A-Za-z0-9_.-]+)?$`)
	guestModePattern    = regexp.MustCompile(`^0?[0-7]{3}$`)
	guestOptionsPattern = regexp.MustCompile(`^[A-Za-z0-9_=.,:-]+$`)

	// Overridable in unit tests; production always uses the authenticated,
	// host-key-pinned SSH path already owned by Instances.
	runVolumeGuestCommand = runSSH
)

func validateVolumePrepareRequest(req *VolumePrepareRequest) error {
	if req == nil {
		return errors.New("prepare configuration is required")
	}
	req.MountPath = path.Clean(strings.TrimSpace(req.MountPath))
	if req.MountPath == "." || !strings.HasPrefix(req.MountPath, "/") || !guestPathPattern.MatchString(req.MountPath) {
		return errors.New("mount_path must be a clean absolute path without spaces")
	}
	allowedRoot := req.MountPath == "/data" || strings.HasPrefix(req.MountPath, "/data/") ||
		strings.HasPrefix(req.MountPath, "/mnt/") || strings.HasPrefix(req.MountPath, "/srv/") ||
		strings.HasPrefix(req.MountPath, "/opt/") || strings.HasPrefix(req.MountPath, "/var/lib/")
	if !allowedRoot {
		return fmt.Errorf("mount_path %q is outside the safe data roots /data, /mnt, /srv, /opt, and /var/lib", req.MountPath)
	}
	req.Filesystem = strings.ToLower(strings.TrimSpace(req.Filesystem))
	if req.Filesystem != "" && req.Filesystem != "ext4" && req.Filesystem != "xfs" {
		return errors.New("filesystem must be ext4 or xfs")
	}
	if req.Filesystem == "" && req.FormatIfBlank {
		req.Filesystem = "ext4"
	}
	if req.Owner == "" {
		req.Owner = "root:root"
	}
	if !guestOwnerPattern.MatchString(req.Owner) {
		return errors.New("owner must be a user, uid, user:group, or uid:gid without spaces")
	}
	if req.Mode == "" {
		req.Mode = "0755"
	}
	if !guestModePattern.MatchString(req.Mode) {
		return errors.New("mode must be an octal directory mode such as 0755 or 0770")
	}
	if !strings.HasPrefix(req.Mode, "0") {
		req.Mode = "0" + req.Mode
	}
	if req.MountOptions == "" {
		req.MountOptions = "defaults,nofail"
	}
	if !guestOptionsPattern.MatchString(req.MountOptions) {
		return errors.New("mount_options contains unsupported characters")
	}
	return nil
}

func volumePrepareRequestArg(args map[string]any, key string, defaultFormat bool) (*VolumePrepareRequest, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	req := &VolumePrepareRequest{
		Filesystem:    strArg(values, "filesystem"),
		MountPath:     strArg(values, "mount_path"),
		Owner:         strArg(values, "owner"),
		Mode:          strArg(values, "mode"),
		MountOptions:  strArg(values, "mount_options"),
		FormatIfBlank: boolArg(values, "format_if_blank", defaultFormat),
	}
	return req, validateVolumePrepareRequest(req)
}

func flatVolumePrepareRequest(args map[string]any) (*VolumePrepareRequest, error) {
	req := &VolumePrepareRequest{
		Filesystem:    strArg(args, "filesystem"),
		MountPath:     strArg(args, "mount_path"),
		Owner:         strArg(args, "owner"),
		Mode:          strArg(args, "mode"),
		MountOptions:  strArg(args, "mount_options"),
		FormatIfBlank: boolArg(args, "format_if_blank", false),
	}
	return req, validateVolumePrepareRequest(req)
}

func prepareAttachedVolume(ctx *sdk.AppCtx, v *InstanceVolume, req *VolumePrepareRequest) (*InstanceVolume, *VolumePrepareResult, error) {
	if v == nil || v.InstanceID == nil {
		return nil, nil, errors.New("volume must be attached before it can be prepared")
	}
	if v.Role != "data" || v.StorageClass != "block" {
		return nil, nil, fmt.Errorf("guest preparation supports attached data block volumes, got role=%s class=%s", v.Role, v.StorageClass)
	}
	if err := validateVolumePrepareRequest(req); err != nil {
		return nil, nil, err
	}
	inst, err := dbGetInstance(ctx.AppDB(), *v.InstanceID)
	if err != nil {
		return nil, nil, err
	}
	if inst.IsLocal() {
		return nil, nil, errors.New("provider volumes cannot be prepared on the local instance")
	}
	if inst.Platform != "" && !strings.EqualFold(inst.Platform, "linux") {
		return nil, nil, fmt.Errorf("guest volume preparation currently requires Linux, instance platform is %q", inst.Platform)
	}
	if inst.Status != "ready" {
		return nil, nil, fmt.Errorf("instance must be ready for guest preparation, current status is %q", inst.Status)
	}

	observed, verifyErr := readProviderVolume(ctx, v)
	if verifyErr != nil || !volumeConverged(observed, "attach", volumeIntent{ProviderInstanceID: inst.ProviderID}) {
		return nil, nil, fmt.Errorf("provider attachment must be verified before guest preparation: %v", verifyErr)
	}
	script := buildVolumePrepareScript(v, req)
	command := rootGuestCommand(inst, script)
	output, exitCode, runErr := runVolumeGuestCommand(inst, command, 2*time.Minute)
	if runErr != nil || exitCode != 0 {
		message := strings.TrimSpace(output)
		if message == "" && runErr != nil {
			message = runErr.Error()
		}
		_ = dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"error_message": "guest prepare failed: " + message})
		return nil, nil, fmt.Errorf("volume %d remains attached but guest preparation failed: %s", v.ID, message)
	}
	result, err := parseVolumePrepareOutput(output)
	if err != nil {
		_ = dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"error_message": "guest prepare response: " + err.Error()})
		return nil, nil, err
	}
	if err := dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{
		"filesystem": result.Filesystem, "mount_path": result.MountPath, "device_path": result.DevicePath, "error_message": "",
	}); err != nil {
		return nil, nil, err
	}
	fresh, err := dbGetVolume(ctx.AppDB(), v.ID)
	return fresh, result, err
}

func unmountPreparedVolume(ctx *sdk.AppCtx, v *InstanceVolume) error {
	if v == nil || v.InstanceID == nil || v.MountPath == "" {
		return nil
	}
	inst, err := dbGetInstance(ctx.AppDB(), *v.InstanceID)
	if err != nil {
		return err
	}
	if inst.Platform != "" && !strings.EqualFold(inst.Platform, "linux") {
		return fmt.Errorf("cannot safely unmount prepared volume %d on non-Linux instance", v.ID)
	}
	script := buildVolumeUnmountScript(v)
	output, exitCode, runErr := runVolumeGuestCommand(inst, rootGuestCommand(inst, script), time.Minute)
	if runErr != nil || exitCode != 0 {
		message := strings.TrimSpace(output)
		if message == "" && runErr != nil {
			message = runErr.Error()
		}
		return fmt.Errorf("unmount volume %d before provider detach: %s", v.ID, message)
	}
	return nil
}

func rootGuestCommand(inst *Instance, script string) string {
	prefix := "/bin/sh -c "
	if inst != nil && inst.SSHUser != "" && inst.SSHUser != "root" {
		prefix = "sudo -n /bin/sh -c "
	}
	return prefix + shellSingleQuote(script)
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func buildVolumePrepareScript(v *InstanceVolume, req *VolumePrepareRequest) string {
	format := "false"
	if req.FormatIfBlank {
		format = "true"
	}
	return fmt.Sprintf(`set -eu
provider_id=%s
volume_name=%s
recorded_device=%s
size_gb=%d
requested_fs=%s
mount_path=%s
mount_owner=%s
mount_mode=%s
mount_options=%s
format_if_blank=%s
marker=%s
expected_links=%s
die() { echo "guest volume prepare: $*" >&2; exit 1; }
for tool in lsblk findmnt blkid wipefs mount readlink awk grep tr find flock; do command -v "$tool" >/dev/null 2>&1 || die "required command $tool is not installed"; done
[ "$(uname -s)" = "Linux" ] || die "Linux is required"
root_source=$(findmnt -nr -o SOURCE / | head -n 1)
root_parent=$(lsblk -ndo PKNAME "$root_source" 2>/dev/null | head -n 1 || true)
if [ -n "$root_parent" ]; then root_device="/dev/$root_parent"; else root_device=$(readlink -f "$root_source" 2>/dev/null || true); fi
candidate_file=$(mktemp)
trap 'rm -f "$candidate_file"' EXIT HUP INT TERM
add_candidate() {
  candidate=$(readlink -f "$1" 2>/dev/null || true)
  [ -n "$candidate" ] && [ -b "$candidate" ] || return 0
  [ "$candidate" != "$root_device" ] || return 0
  grep -Fxq "$candidate" "$candidate_file" 2>/dev/null || echo "$candidate" >> "$candidate_file"
}
id_token=$(printf '%%s' "$provider_id" | tr '[:upper:]' '[:lower:]' | tr -cd '[:alnum:]')
attempt=0
while [ "$attempt" -lt 30 ]; do
  : > "$candidate_file"
  printf '%%s\n' "$expected_links" | while IFS= read -r link; do
    [ -n "$link" ] && [ -e "$link" ] && add_candidate "$link"
  done
  # Match the complete hardware serial to the provider ID. Sizes and mutable
  # /dev/sdX names never establish identity for formatting.
  lsblk -bdnpo NAME,SERIAL | while read -r dev serial; do
    serial_token=$(printf '%%s' "$serial" | tr '[:upper:]' '[:lower:]' | tr -cd '[:alnum:]')
    [ -n "$id_token" ] && [ "$serial_token" = "$id_token" ] && add_candidate "$dev"
  done
  count=$(awk 'NF {n++} END {print n+0}' "$candidate_file")
  [ "$count" -gt 0 ] && break
  attempt=$((attempt + 1))
  sleep 2
done
[ "$count" -ne 0 ] || die "attached device did not appear within 60 seconds"
[ "$count" -eq 1 ] || die "device discovery is ambiguous: $(tr '\n' ' ' < "$candidate_file")"
device=$(head -n 1 "$candidate_file")
children=$(lsblk -nrpo NAME "$device" | tail -n +2 | awk 'NF {n++} END {print n+0}')
[ "$children" -eq 0 ] || die "device $device has partitions or child mappings; refusing to modify it"
exec 9>/etc/fstab.apteva.lock
flock -w 90 9 || die "fstab is busy"
probe_status=0
probe_output=$(blkid -p -o export "$device" 2>/dev/null) || probe_status=$?
case "$probe_status" in
  0) existing_fs=$(printf '%%s\n' "$probe_output" | sed -n 's/^TYPE=//p'); [ -n "$existing_fs" ] || die "unknown disk signature; refusing to format" ;;
  2) existing_fs=""; signatures=$(wipefs -n --noheadings "$device") || die "cannot inspect disk signatures"; [ -z "$signatures" ] || die "disk has signatures; refusing to format"; dd if="$device" of=/dev/null bs=4096 count=1 2>/dev/null || die "disk is unreadable" ;;
  *) die "ambiguous or failed signature probe ($probe_status); refusing to format" ;;
esac
formatted=false
if [ -z "$existing_fs" ]; then
  [ "$format_if_blank" = "true" ] || die "device $device is blank; set format_if_blank=true to create a filesystem"
  [ -n "$requested_fs" ] || requested_fs=ext4
  command -v "mkfs.$requested_fs" >/dev/null 2>&1 || die "mkfs.$requested_fs is not installed"
  case "$requested_fs" in
    ext4) mkfs.ext4 "$device" >/dev/null ;;
    xfs) mkfs.xfs "$device" >/dev/null ;;
    *) die "unsupported filesystem $requested_fs" ;;
  esac
  existing_fs=$requested_fs
  formatted=true
else
  case "$existing_fs" in ext4|xfs) ;; *) die "existing filesystem/signature $existing_fs is not supported";; esac
  [ -z "$requested_fs" ] || [ "$requested_fs" = "$existing_fs" ] || die "device contains $existing_fs, requested $requested_fs"
fi
uuid=$(blkid -s UUID -o value "$device" 2>/dev/null || true)
[ -n "$uuid" ] || die "filesystem UUID is unavailable"
current_target=$(findmnt -nr -S "$device" -o TARGET | head -n 1 || true)
already_mounted=false
if [ -n "$current_target" ] && [ "$current_target" != "$mount_path" ]; then die "device is already mounted at $current_target"; fi
path_source=$(findmnt -nr -M "$mount_path" -o SOURCE 2>/dev/null | head -n 1 || true)
if [ -n "$path_source" ] && [ "$current_target" != "$mount_path" ]; then die "mount path is already used by $path_source"; fi
if [ -z "$current_target" ] && [ -d "$mount_path" ] && [ -n "$(find "$mount_path" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]; then
  die "mount path $mount_path is not empty; refusing to hide existing data"
fi
mkdir -p "$mount_path"
fstab_source=$(awk -v p="$mount_path" '$1 !~ /^#/ && $2 == p {print $1; exit}' /etc/fstab)
if [ -n "$fstab_source" ] && [ "$fstab_source" != "UUID=$uuid" ]; then die "fstab already assigns $mount_path to $fstab_source"; fi
if [ -z "$fstab_source" ]; then
  fstab_tmp=$(mktemp /etc/.apteva-fstab.XXXXXX)
  trap 'rm -f "$fstab_tmp"' EXIT
  cp -p /etc/fstab "$fstab_tmp"
  pass=0; [ "$existing_fs" = "ext4" ] && pass=2
  printf 'UUID=%%s\t%%s\t%%s\t%%s\t0\t%%s\t# %%s\n' "$uuid" "$mount_path" "$existing_fs" "$mount_options" "$pass" "$marker" >> "$fstab_tmp"
  mv -f "$fstab_tmp" /etc/fstab
  trap - EXIT
fi
if [ "$current_target" = "$mount_path" ]; then already_mounted=true; else mount "$mount_path"; fi
verified=$(findmnt -nr -M "$mount_path" -o SOURCE | head -n 1 || true)
[ -n "$verified" ] || die "mount verification failed"
chown "$mount_owner" "$mount_path"
chmod "$mount_mode" "$mount_path"
echo "APTEVA_VOLUME_DEVICE=$device"
echo "APTEVA_VOLUME_FILESYSTEM=$existing_fs"
echo "APTEVA_VOLUME_MOUNT_PATH=$mount_path"
echo "APTEVA_VOLUME_UUID=$uuid"
echo "APTEVA_VOLUME_FORMATTED=$formatted"
echo "APTEVA_VOLUME_ALREADY_MOUNTED=$already_mounted"`,
		shellSingleQuote(v.ProviderVolumeID), shellSingleQuote(v.Name), shellSingleQuote(v.DevicePath), v.SizeGB,
		shellSingleQuote(req.Filesystem), shellSingleQuote(req.MountPath), shellSingleQuote(req.Owner), shellSingleQuote(req.Mode),
		shellSingleQuote(req.MountOptions), format, shellSingleQuote("apteva-volume:"+strconv.FormatInt(v.ID, 10)), shellSingleQuote(strings.Join(expectedGuestVolumePaths(v), "\n")))
}

func expectedGuestVolumePaths(v *InstanceVolume) []string {
	switch v.Provider {
	case "hetzner":
		return []string{"/dev/disk/by-id/scsi-0HC_Volume_" + v.ProviderVolumeID}
	case "digitalocean":
		return []string{"/dev/disk/by-id/scsi-0DO_Volume_" + v.Name}
	case "linode":
		return []string{"/dev/disk/by-id/scsi-0Linode_Volume_" + v.Name}
	case "aws-ec2":
		return []string{"/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_" + strings.ReplaceAll(v.ProviderVolumeID, "-", "")}
	}
	return nil
}

func buildVolumeUnmountScript(v *InstanceVolume) string {
	return fmt.Sprintf(`set -eu
mount_path=%s
marker=%s
expected_device=%s
exec 9>/etc/fstab.apteva.lock
flock -w 30 9 || { echo "fstab is busy" >&2; exit 1; }
if [ -n "$mount_path" ]; then
  current_target=$(findmnt -nr -M "$mount_path" -o TARGET 2>/dev/null | head -n 1 || true)
  if [ "$current_target" = "$mount_path" ]; then
    expected_source=$(awk -v marker="$marker" '$NF == marker {print $1; exit}' /etc/fstab)
    actual_uuid=$(findmnt -nr -M "$mount_path" -o UUID | head -n 1)
    [ -n "$actual_uuid" ] && [ "$expected_source" = "UUID=$actual_uuid" ] || { echo "mounted filesystem identity changed" >&2; exit 1; }
    umount "$mount_path"
  fi
fi
if [ -f /etc/fstab ]; then
  tmp=$(mktemp /etc/.apteva-fstab.XXXXXX)
  trap 'rm -f "$tmp"' EXIT HUP INT TERM
  cp -p /etc/fstab "$tmp"
  awk -v marker="$marker" '$NF != marker {print}' /etc/fstab > "$tmp"
  mv -f "$tmp" /etc/fstab
fi`, shellSingleQuote(v.MountPath), shellSingleQuote("apteva-volume:"+strconv.FormatInt(v.ID, 10)), shellSingleQuote(v.DevicePath))
}

func parseVolumePrepareOutput(output string) (*VolumePrepareResult, error) {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "APTEVA_VOLUME_") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = strings.TrimSpace(value)
		}
	}
	result := &VolumePrepareResult{
		DevicePath: values["APTEVA_VOLUME_DEVICE"], Filesystem: values["APTEVA_VOLUME_FILESYSTEM"],
		MountPath: values["APTEVA_VOLUME_MOUNT_PATH"], UUID: values["APTEVA_VOLUME_UUID"],
		Formatted: values["APTEVA_VOLUME_FORMATTED"] == "true", AlreadyMounted: values["APTEVA_VOLUME_ALREADY_MOUNTED"] == "true",
	}
	if result.DevicePath == "" || result.Filesystem == "" || result.MountPath == "" || result.UUID == "" {
		return nil, errors.New("guest prepare completed without required verification markers")
	}
	return result, nil
}

func (a *App) toolVolumePrepare(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	unlock, err := lockResource(ctx.AppDB(), "volume", int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	v, err := dbGetVolume(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	req, err := flatVolumePrepareRequest(args)
	if err != nil {
		return nil, err
	}
	v, result, err := prepareAttachedVolume(ctx, v, req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"volume": v, "prepared": result}, nil
}
