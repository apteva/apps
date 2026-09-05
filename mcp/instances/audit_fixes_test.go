package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"golang.org/x/crypto/ssh"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRotationRetainsFailedRevocationAndRecovers(t *testing.T) {
	p := &objectStoragePlatform{provider: "scaleway"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	item, _, err := createObjectStorage(ctx, CreateObjectStorageInput{Name: "keys", Provider: "scaleway", ProviderConnectionID: 7, Region: "fr-par", Bucket: "rotation-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"access_key_id": "OLD"}); err != nil {
		t.Fatal(err)
	}
	p.failTool = "iam_api_key_delete"
	creds, warnings, err := rotateObjectStorageCredentials(ctx, item)
	if err != nil || len(warnings) == 0 {
		t.Fatalf("creds=%v warnings=%v err=%v", creds, warnings, err)
	}
	var key string
	if err = ctx.AppDB().QueryRow(`SELECT access_key_id FROM object_storage_key_cleanup`).Scan(&key); err != nil || key != "OLD" {
		t.Fatalf("key=%s err=%v", key, err)
	}
	fresh, _ := dbGetObjectStorage(ctx.AppDB(), item.ID)
	if parseObjectStorageMetadata(fresh).KeyExpiresAt != creds.ExpiresAt {
		t.Fatal("rotation expiry was not persisted")
	}
	p.failTool = ""
	if warnings = cleanupObjectStorageKeys(ctx, item.ID); len(warnings) > 0 {
		t.Fatal(warnings)
	}
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM object_storage_key_cleanup`).Scan(&n)
	if n != 0 {
		t.Fatal("revoked key still pending")
	}
}

func TestPendingVultrObjectStorageRetainsIdentity(t *testing.T) {
	p := &auditPlatform{slugs: map[int64]string{7: "vultr"}, hook: func(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
		return &sdk.ExecuteResult{Success: true, Status: 202, Data: json.RawMessage(`{"object_storage":{"id":"new-store","status":"pending"}}`)}, nil
	}}
	ctx := auditCtx(t, p)
	item, creds, err := createVultrObjectStorage(ctx, CreateObjectStorageInput{Name: "pending", Provider: "vultr", ProviderConnectionID: 7, Region: "2"})
	if err != nil || item == nil || creds != nil {
		t.Fatalf("item=%v creds=%v err=%v", item, creds, err)
	}
	stored, err := dbGetObjectStorage(ctx.AppDB(), item.ID)
	if err != nil || stored.ProviderID != "new-store" || stored.Status != "provisioning" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestVolumeAttachRecoveryDoesNotReplayAcceptedMutation(t *testing.T) {
	available := false
	mutations := 0
	p := &auditPlatform{slugs: map[int64]string{7: "digitalocean"}, hook: func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "volume_action" {
			mutations++
			return &sdk.ExecuteResult{Success: true, Status: 202, Data: json.RawMessage(`{}`)}, nil
		}
		if !available {
			return &sdk.ExecuteResult{Status: 503}, nil
		}
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"volume":{"id":"disk","size_gigabytes":20,"droplet_ids":[123]}}`)}, nil
	}}
	ctx := auditCtx(t, p)
	inst, _ := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: "target", Provider: "digitalocean", ProviderConnectionID: 7, ProviderID: "123", Status: "ready"})
	v, _ := dbCreateVolume(ctx.AppDB(), CreateVolumeInput{Name: "disk", Provider: "digitalocean", ProviderConnectionID: 7, SizeGB: 20, DeletePolicy: "retain"}, "disk", "block", "available", "{}")
	if err := attachProviderVolume(ctx, v, inst); err == nil {
		t.Fatal("verification failure was ignored")
	}
	fresh, _ := dbGetVolume(ctx.AppDB(), v.ID)
	if fresh.InstanceID != nil || fresh.Status != "attaching" {
		t.Fatalf("premature success: %+v", fresh)
	}
	available = true
	if err := attachProviderVolume(ctx, fresh, inst); err != nil {
		t.Fatal(err)
	}
	if mutations != 1 {
		t.Fatalf("replayed mutation %d times", mutations)
	}
	fresh, _ = dbGetVolume(ctx.AppDB(), v.ID)
	if fresh.InstanceID == nil || *fresh.InstanceID != inst.ID || fresh.Status != "attached" {
		t.Fatalf("recovery failed: %+v", fresh)
	}
}

func TestVolumeRejectedAttachDoesNotLeaveUnresolvableOperation(t *testing.T) {
	p := &auditPlatform{slugs: map[int64]string{7: "linode"}, hook: func(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
		return &sdk.ExecuteResult{Status: 400}, nil
	}}
	ctx := auditCtx(t, p)
	v, _ := dbCreateVolume(ctx.AppDB(), CreateVolumeInput{Name: "disk", Provider: "linode", ProviderConnectionID: 7, SizeGB: 20, DeletePolicy: "retain"}, "123", "block", "available", "{}")
	if err := attachProviderVolume(ctx, v, &Instance{ID: 1, ProviderID: "456"}); err == nil {
		t.Fatal("rejection reported success")
	}
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM resource_operations`).Scan(&n)
	if n != 0 {
		t.Fatal("definitively rejected operation remains pending")
	}
	fresh, _ := dbGetVolume(ctx.AppDB(), v.ID)
	if fresh.Status != "available" || fresh.InstanceID != nil {
		t.Fatalf("wrong state %+v", fresh)
	}
}

func TestCatalogSingleFlightIsScopedByConnection(t *testing.T) {
	var calls atomic.Int32
	p := &auditPlatform{slugs: map[int64]string{7: "hetzner", 8: "hetzner"}, hook: func(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"server_types":[]}`)}, nil
	}}
	ctx := auditCtx(t, p)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := executeProviderToolOnConnection(ctx, 7, "hetzner", "server_types_list", map[string]any{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("expected one upstream call, got %d", calls.Load())
	}
	_, err := executeProviderToolOnConnection(ctx, 8, "hetzner", "server_types_list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("cache crossed provider accounts")
	}
}

func TestCatalogPaginationMergesAllPages(t *testing.T) {
	p := &auditPlatform{slugs: map[int64]string{7: "hetzner"}, hook: func(_ int64, _ string, args map[string]any) (*sdk.ExecuteResult, error) {
		data := `{"items":[{"id":1}],"meta":{"pagination":{"next_page":2}}}`
		if anyToInt(args["page"]) == 2 {
			data = `{"items":[{"id":2}],"meta":{"pagination":{"next_page":null}}}`
		}
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(data)}, nil
	}}
	data, err := executePagedProviderTool(auditCtx(t, p), 7, "hetzner", "server_list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Items []any `json:"items"`
	}
	if err = json.Unmarshal(data, &body); err != nil || len(body.Items) != 2 {
		t.Fatalf("data=%s err=%v", data, err)
	}
}

func TestImageCompatibilityIncludesArchitectureAndZone(t *testing.T) {
	server := &ServerType{Name: "arm-type", Architecture: "arm64", Platform: "linux", ResourceClass: "virtual"}
	if imageMatchesType(Image{Architecture: "x86_64"}, server, "zone") {
		t.Fatal("x86 image accepted for ARM")
	}
	if imageMatchesType(Image{Architecture: "arm", AvailableIn: []string{"elsewhere"}}, server, "zone") {
		t.Fatal("image from another zone accepted")
	}
	if !imageMatchesType(Image{Architecture: "aarch64", AvailableIn: []string{"zone"}}, server, "zone") {
		t.Fatal("compatible image rejected")
	}
}

func TestBenchmarkPreservesExistingFileAndLiteralDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "space ' $(touch injected)")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, ".apteva-storage-benchmark")
	_ = os.WriteFile(sentinel, []byte("keep"), 0600)
	bin := filepath.Join(root, "bin")
	_ = os.Mkdir(bin, 0700)
	_ = os.WriteFile(filepath.Join(bin, "dd"), []byte("#!/bin/sh\nexit 0\n"), 0700)
	_ = os.WriteFile(filepath.Join(bin, "uname"), []byte("#!/bin/sh\necho Linux\n"), 0700)
	cmd := exec.Command("sh", "-c", storageBenchmarkCommand(target))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	data, _ := os.ReadFile(sentinel)
	if string(data) != "keep" {
		t.Fatal("benchmark overwrote an existing file")
	}
	if _, err := os.Stat(filepath.Join(root, "injected")); !os.IsNotExist(err) {
		t.Fatal("shell substitution executed")
	}
	files, _ := os.ReadDir(target)
	if len(files) != 1 {
		t.Fatal("benchmark temporary file leaked")
	}
}

func TestSSHLostResponseDoesNotReplayCommand(t *testing.T) {
	dir := t.TempDir()
	client := auditSSHClient(t, dir, true)
	old := globalSSHPool
	globalSSHPool = &sshPool{clients: map[int64]*ssh.Client{991: client}}
	t.Cleanup(func() { globalSSHPool = old })
	_, _, err := runSSH(&Instance{ID: 991}, "echo once >> counter", time.Second)
	if err == nil {
		t.Fatal("lost command result should be unknown")
	}
	if strings.Contains(err.Error(), "redial") {
		t.Fatalf("command was dispatched and then retried: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "counter"))
	if string(data) != "once\n" {
		t.Fatalf("command execution count: %q", data)
	}
}

func TestObjectCreateFailureRetainsAcquiredIAMIdentity(t *testing.T) {
	p := &objectStoragePlatform{provider: "scaleway", failTool: "iam_policy_create"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	_, _, err := createObjectStorage(ctx, CreateObjectStorageInput{Name: "interrupted", Provider: "scaleway", ProviderConnectionID: 7, Region: "fr-par", Bucket: "partial-create"})
	if err == nil {
		t.Fatal("injected failure should fail creation")
	}
	items, err := dbListObjectStorages(ctx.AppDB(), "")
	if err != nil || len(items) != 1 {
		t.Fatalf("pending inventory lost: %+v %v", items, err)
	}
	if meta := parseObjectStorageMetadata(items[0]); meta.ApplicationID != "application-1" || !meta.BucketCreated || meta.PendingStep != "IAM policy create" {
		t.Fatalf("identity/progress missing %+v", meta)
	}
	p.failTool = ""
	reconcileObjectStorage(ctx)
	items, _ = dbListObjectStorages(ctx.AppDB(), "")
	if len(items) != 1 {
		t.Fatal("unknown policy creation outcome was forgotten")
	}
}

func TestVolumeCreateAttachmentIntentSurvivesRestartBoundary(t *testing.T) {
	p := &recordingVolumePlatform{slug: "digitalocean"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	inst, _ := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: "target", Provider: "digitalocean", ProviderConnectionID: 7, ProviderID: "123", Status: "ready"})
	v, _ := dbCreateVolume(ctx.AppDB(), CreateVolumeInput{Name: "disk", Provider: "digitalocean", ProviderConnectionID: 7, SizeGB: 80, DeletePolicy: "retain"}, "volume-1", "block", "creating", "{}")
	intent := volumeIntent{InstanceID: inst.ID, SizeGB: 80}
	if err := persistVolumeIntent(ctx.AppDB(), v.ID, "create", intent); err != nil {
		t.Fatal(err)
	}
	if err := completeVolumeOperation(ctx, v, "create", intent); err != nil {
		t.Fatal(err)
	}
	var op string
	_ = ctx.AppDB().QueryRow(`SELECT operation FROM resource_operations WHERE resource_id=?`, v.ID).Scan(&op)
	if op != "await_attach" {
		t.Fatal("creation discarded pending attachment")
	}
	reconcileVolumeOperations(ctx)
	fresh, _ := dbGetVolume(ctx.AppDB(), v.ID)
	if fresh.InstanceID == nil || *fresh.InstanceID != inst.ID || fresh.Status != "attached" {
		t.Fatalf("attachment not recovered: %+v", fresh)
	}
}
