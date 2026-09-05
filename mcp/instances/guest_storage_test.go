package main

import (
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestValidateVolumePrepareRequest_UsesSafeDefaults(t *testing.T) {
	req := &VolumePrepareRequest{MountPath: "/srv/media", FormatIfBlank: true}
	if err := validateVolumePrepareRequest(req); err != nil {
		t.Fatal(err)
	}
	if req.Filesystem != "ext4" || req.Owner != "root:root" || req.Mode != "0755" || req.MountOptions != "defaults,nofail" {
		t.Fatalf("defaults = %#v", req)
	}
	for _, invalid := range []*VolumePrepareRequest{
		{MountPath: "/", FormatIfBlank: true},
		{MountPath: "/home/media", FormatIfBlank: true},
		{MountPath: "/srv/media;reboot", FormatIfBlank: true},
		{MountPath: "/srv/media", Filesystem: "zfs", FormatIfBlank: true},
		{MountPath: "/srv/media", Owner: "root;reboot", FormatIfBlank: true},
	} {
		if err := validateVolumePrepareRequest(invalid); err == nil {
			t.Fatalf("unsafe request accepted: %#v", invalid)
		}
	}
}

func TestVolumePrepare_UpdatesVerifiedGuestMetadata(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "media", Provider: "digitalocean", ProviderConnectionID: 7, ProviderID: "123", Region: "ams3",
		Status: "ready", Platform: "linux", SSHUser: "apteva", SSHHost: "host.test", SSHPrivateKey: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	volume, err := dbCreateVolume(ctx.AppDB(), CreateVolumeInput{
		InstanceID: inst.ID, Provider: "digitalocean", ProviderConnectionID: 7, Name: "media-data", SizeGB: 80,
		Tier: "provider-default", DeletePolicy: "retain",
	}, "volume-123", "block", "attached", "{}")
	if err != nil {
		t.Fatal(err)
	}

	previous := runVolumeGuestCommand
	t.Cleanup(func() { runVolumeGuestCommand = previous })
	var command string
	runVolumeGuestCommand = func(_ *Instance, cmd string, _ time.Duration) (string, int, error) {
		command = cmd
		return strings.Join([]string{
			"APTEVA_VOLUME_DEVICE=/dev/sdb",
			"APTEVA_VOLUME_FILESYSTEM=ext4",
			"APTEVA_VOLUME_MOUNT_PATH=/srv/media",
			"APTEVA_VOLUME_UUID=uuid-123",
			"APTEVA_VOLUME_FORMATTED=true",
			"APTEVA_VOLUME_ALREADY_MOUNTED=false",
		}, "\n"), 0, nil
	}

	app := &App{}
	result, err := app.toolVolumePrepare(ctx, map[string]any{
		"id": volume.ID, "filesystem": "ext4", "mount_path": "/srv/media", "owner": "1000:1000", "format_if_blank": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared := result.(map[string]any)["volume"].(*InstanceVolume)
	if prepared.DevicePath != "/dev/sdb" || prepared.Filesystem != "ext4" || prepared.MountPath != "/srv/media" {
		t.Fatalf("prepared volume = %#v", prepared)
	}
	for _, required := range []string{"sudo -n", "device discovery is ambiguous", "blkid -p", "mkfs.ext4", "/etc/fstab", "UUID="} {
		if !strings.Contains(command, required) {
			t.Errorf("guest command missing %q", required)
		}
	}
}

func TestVolumePrepare_FailureLeavesVolumeAttached(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "media", Provider: "digitalocean", ProviderID: "123", Region: "ams3", Status: "ready", Platform: "linux", SSHUser: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	volume, err := dbCreateVolume(ctx.AppDB(), CreateVolumeInput{
		InstanceID: inst.ID, Provider: "digitalocean", Name: "data", SizeGB: 80, Tier: "provider-default", DeletePolicy: "retain",
	}, "volume-123", "block", "attached", "{}")
	if err != nil {
		t.Fatal(err)
	}
	previous := runVolumeGuestCommand
	t.Cleanup(func() { runVolumeGuestCommand = previous })
	runVolumeGuestCommand = func(_ *Instance, _ string, _ time.Duration) (string, int, error) {
		return "guest volume prepare: device discovery is ambiguous", 1, nil
	}
	_, _, err = prepareAttachedVolume(ctx, volume, &VolumePrepareRequest{MountPath: "/srv/media", FormatIfBlank: true})
	if err == nil || !strings.Contains(err.Error(), "remains attached") {
		t.Fatalf("prepare error = %v", err)
	}
	fresh, err := dbGetVolume(ctx.AppDB(), volume.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.InstanceID == nil || fresh.Status != "attached" || !strings.Contains(fresh.ErrorMessage, "ambiguous") {
		t.Fatalf("volume after failed prepare = %#v", fresh)
	}
}

func TestVolumeCreate_AutoPreparesWhenRequested(t *testing.T) {
	platform := &recordingVolumePlatform{slug: "digitalocean"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "media", Provider: "digitalocean", ProviderConnectionID: 7, ProviderID: "123", Region: "ams3",
		Status: "ready", Platform: "linux", SSHUser: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	previous := runVolumeGuestCommand
	t.Cleanup(func() { runVolumeGuestCommand = previous })
	var command string
	runVolumeGuestCommand = func(_ *Instance, cmd string, _ time.Duration) (string, int, error) {
		command = cmd
		return "APTEVA_VOLUME_DEVICE=/dev/sdb\nAPTEVA_VOLUME_FILESYSTEM=ext4\nAPTEVA_VOLUME_MOUNT_PATH=/srv/media\nAPTEVA_VOLUME_UUID=uuid-new\nAPTEVA_VOLUME_FORMATTED=true\nAPTEVA_VOLUME_ALREADY_MOUNTED=false", 0, nil
	}
	app := &App{}
	result, err := app.toolVolumeCreate(ctx, map[string]any{
		"instance_id": inst.ID, "name": "media-data", "size_gb": 80,
		"prepare": map[string]any{"mount_path": "/srv/media", "owner": "1000:1000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	volume := result.(map[string]any)["volume"].(*InstanceVolume)
	if volume.MountPath != "/srv/media" || volume.Filesystem != "ext4" || !strings.Contains(command, "format_if_blank=true") {
		t.Fatalf("auto-prepared volume=%#v command=%s", volume, command)
	}
	if len(platform.tools) != 2 || platform.tools[0] != "volume_create" || platform.tools[1] != "volume_action" {
		t.Fatalf("provider tools = %#v", platform.tools)
	}
}

func TestVolumeDetach_UnmountsGuestBeforeProvider(t *testing.T) {
	platform := &recordingVolumePlatform{slug: "digitalocean"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{
		Name: "media", Provider: "digitalocean", ProviderConnectionID: 7, ProviderID: "123", Region: "ams3",
		Status: "ready", Platform: "linux", SSHUser: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	volume, err := dbCreateVolume(ctx.AppDB(), CreateVolumeInput{
		InstanceID: inst.ID, Provider: "digitalocean", ProviderConnectionID: 7, Name: "data", SizeGB: 80,
		Tier: "provider-default", DeletePolicy: "retain",
	}, "volume-123", "block", "attached", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateVolume(ctx.AppDB(), volume.ID, map[string]any{"filesystem": "ext4", "mount_path": "/srv/media", "device_path": "/dev/sdb"}); err != nil {
		t.Fatal(err)
	}
	previous := runVolumeGuestCommand
	t.Cleanup(func() { runVolumeGuestCommand = previous })
	var guestCommand string
	runVolumeGuestCommand = func(_ *Instance, cmd string, _ time.Duration) (string, int, error) {
		guestCommand = cmd
		return "", 0, nil
	}
	app := &App{}
	if _, err := app.toolVolumeDetach(ctx, map[string]any{"id": volume.ID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(guestCommand, "umount") || !strings.Contains(guestCommand, "apteva-volume:") {
		t.Fatalf("unmount command = %s", guestCommand)
	}
	if len(platform.tools) != 1 || platform.tools[0] != "volume_action" {
		t.Fatalf("provider tools = %#v", platform.tools)
	}
}
