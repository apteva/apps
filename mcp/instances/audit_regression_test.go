package main

// Audit-only regression tests. They express intended behavior and deliberately
// fail against unmodified Instances v0.4.41. No real provider is contacted.
import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"golang.org/x/crypto/ssh"
)

type auditPlatform struct {
	tk.BasePlatformClient
	mu    sync.Mutex
	calls []string
	ids   []int64
	slugs map[int64]string
	fail  bool
	hook  func(int64, string, map[string]any) (*sdk.ExecuteResult, error)
}

func (p *auditPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	ids := []any{}
	for _, id := range []int64{7, 8} {
		if p.slugs[id] != "" {
			ids = append(ids, float64(id))
		}
	}
	return &sdk.InstallIdentity{Bindings: map[string]any{"provider": map[string]any{"ids": ids, "default_id": float64(7)}}}, nil
}
func (p *auditPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: p.slugs[id]}, nil
}
func (p *auditPlatform) ExecuteIntegrationTool(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, tool)
	p.ids = append(p.ids, id)
	p.mu.Unlock()
	if p.hook != nil {
		return p.hook(id, tool, args)
	}
	if p.fail {
		return &sdk.ExecuteResult{Success: false, Status: 503, Data: json.RawMessage(`{"error":"temporary outage"}`)}, nil
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}, nil
}
func auditCtx(t *testing.T, p *auditPlatform) *sdk.AppCtx {
	t.Helper()
	return tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
}

func TestAuditNetworkFailureMustNotCrashProcess(t *testing.T) {
	if os.Getenv("INSTANCES_AUDIT_CRASH_CHILD") == "1" {
		ctx := auditCtx(t, &auditPlatform{slugs: map[int64]string{7: "vultr"}, fail: true})
		inst, err := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: "audit", Provider: "vultr", ProviderConnectionID: 7, ProviderID: "host", Status: "provisioning"})
		if err != nil {
			t.Fatal(err)
		}
		kickAPIProviderReadinessProbe(ctx, inst.ID)
		time.Sleep(300 * time.Millisecond)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuditNetworkFailureMustNotCrashProcess$")
	cmd.Env = append(os.Environ(), "INSTANCES_AUDIT_CRASH_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("network lookup killed the process: %v\n%s", err, out)
	}
}

func TestAuditLinodeAttachCallsProvider(t *testing.T) {
	p := &auditPlatform{slugs: map[int64]string{7: "linode"}}
	ctx := auditCtx(t, p)
	v, err := dbCreateVolume(ctx.AppDB(), CreateVolumeInput{Provider: "linode", ProviderConnectionID: 7, Name: "data", SizeGB: 10, DeletePolicy: "retain"}, "123", "block", "available", "{}")
	if err != nil {
		t.Fatal(err)
	}
	p.hook = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"id":123,"linode_id":456,"status":"active","size":10}`)}, nil
	}
	inst, _ := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: "target", Provider: "linode", ProviderConnectionID: 7, ProviderID: "456", Status: "ready"})
	err = attachProviderVolume(ctx, v, &Instance{ID: inst.ID, Provider: "linode", ProviderConnectionID: 7, ProviderID: "456"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.calls) == 0 {
		t.Fatal("attach reported success without making a provider call")
	}
}

func TestAuditDediboxDeleteUsesRecordedAccount(t *testing.T) {
	p := &auditPlatform{slugs: map[int64]string{7: "scaleway", 8: "scaleway"}}
	ctx := auditCtx(t, p)
	err := scalewayDediboxDestroy(ctx, &Instance{Provider: "scaleway", ProviderConnectionID: 8, ProviderID: "123", Region: "fr-par-1", Size: "dedibox/1", ProviderMetadataJSON: `{"service_id":"456"}`})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ids) != 1 || p.ids[0] != 8 {
		t.Fatalf("delete called accounts %v, wanted recorded account 8", p.ids)
	}
}

func TestAuditInstanceIDsMustNotBeReused(t *testing.T) {
	db := openTestDB(t)
	old, _ := dbCreateInstance(db, CreateInstanceInput{Name: "old", Provider: "external", ProviderID: "old"})
	if err := dbDeleteInstance(db, old.ID); err != nil {
		t.Fatal(err)
	}
	next, err := dbCreateInstance(db, CreateInstanceInput{Name: "new", Provider: "external", ProviderID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if old.ID == next.ID {
		t.Fatalf("host id %d reused for a different machine", old.ID)
	}
}

func TestAuditWaitReadyCannotOverwriteDestroying(t *testing.T) {
	ctx := auditCtx(t, &auditPlatform{slugs: map[int64]string{7: "hetzner"}})
	inst, _ := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: "audit", Provider: "hetzner", ProviderID: "123", Status: "provisioning"})
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = dbTransitionStatus(ctx.AppDB(), inst.ID, []string{"provisioning"}, "destroying", nil)
	}()

	_, err := (&App{}).toolWaitReady(ctx, map[string]any{"id": inst.ID})
	fresh, _ := dbGetInstance(ctx.AppDB(), inst.ID)
	if err == nil || fresh.Status != "destroying" {
		t.Fatalf("wait-ready returned err=%v and changed destroying row to %s", err, fresh.Status)
	}
}

func TestAuditDestroyRecoveryMustHandleDataVolumes(t *testing.T) {
	p := &auditPlatform{slugs: map[int64]string{7: "hetzner"}}
	deleted := false
	p.hook = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "volume_delete" {
			deleted = true
		}
		if tool == "volume_get" {
			if deleted {
				return &sdk.ExecuteResult{Status: 404}, nil
			}
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"volume":{"id":456,"status":"available","size":10,"server":null}}`)}, nil
		}
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}, nil
	}
	ctx := auditCtx(t, p)
	inst, _ := dbCreateInstance(ctx.AppDB(), CreateInstanceInput{Name: "audit", Provider: "hetzner", ProviderConnectionID: 7, ProviderID: "123", Status: "destroying"})
	_, err := dbCreateVolume(ctx.AppDB(), CreateVolumeInput{InstanceID: inst.ID, Provider: "hetzner", ProviderConnectionID: 7, Name: "data", SizeGB: 10, Tier: "provider-default", DeletePolicy: "with_instance"}, "456", "block", "attached", "{}")
	if err != nil {
		t.Fatal(err)
	}
	reconcileDestroying(ctx)
	if !strings.Contains(strings.Join(p.calls, ","), "volume_delete") {
		t.Fatalf("recovery skipped volume cleanup: %v", p.calls)
	}
}

func TestAuditMissingRowUpdateMustFail(t *testing.T) {
	if err := dbUpdateInstance(openTestDB(t), 999, map[string]any{"provider_id": "created-upstream"}); err == nil {
		t.Fatal("identity update succeeded although the instance row no longer exists")
	}
}

func TestAuditLocalAbsolutePathPreserved(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "file.txt")
	got, err := resolveLocalPath(root, want)
	if err != nil || got != want {
		t.Fatalf("absolute path resolved to %q, want %q (err=%v)", got, want, err)
	}
}

func TestAuditLocalTimeoutBoundsDescendantWait(t *testing.T) {
	started := time.Now()
	_, _, _ = runLocal("sleep 0.6; printf completed", 30*time.Millisecond)
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("30ms command timeout returned after %v", elapsed)
	}
}

func TestAuditAWSDeviceMustNotCollideAfterDetach(t *testing.T) {
	db := openTestDB(t)
	inst, _ := dbCreateInstance(db, CreateInstanceInput{Name: "audit", Provider: "aws-ec2", ProviderID: "i-test"})
	a, _ := dbCreateVolume(db, CreateVolumeInput{InstanceID: inst.ID, Provider: "aws-ec2", Name: "a", SizeGB: 10, DeletePolicy: "retain"}, "vol-a", "block", "attached", "{}")
	b, _ := dbCreateVolume(db, CreateVolumeInput{InstanceID: inst.ID, Provider: "aws-ec2", Name: "b", SizeGB: 10, DeletePolicy: "retain"}, "vol-b", "block", "attached", "{}")
	_ = dbUpdateVolume(db, a.ID, map[string]any{"device_path": "/dev/sdf"})
	_ = dbUpdateVolume(db, b.ID, map[string]any{"device_path": "/dev/sdg"})
	_ = dbUpdateVolume(db, a.ID, map[string]any{"instance_id": nil})
	_, _ = dbCreateVolume(db, CreateVolumeInput{InstanceID: inst.ID, Provider: "aws-ec2", Name: "c", SizeGB: 10, DeletePolicy: "retain"}, "vol-c", "block", "available", "{}")
	if got := nextAWSVolumeDevice(db, inst.ID); got == "/dev/sdg" {
		t.Fatal("allocator reused occupied /dev/sdg after /dev/sdf was detached")
	}
}

func TestAuditUnmountMarkerMustMatchWholeID(t *testing.T) {
	script := buildVolumeUnmountScript(&InstanceVolume{ID: 1})
	input := "UUID=a /srv/one ext4 defaults 0 2 # apteva-volume:1\nUUID=b /srv/ten ext4 defaults 0 2 # apteva-volume:10\n"
	fstab := filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(fstab, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	// Redirect only the fixed system file to a disposable fixture.
	script = strings.ReplaceAll(script, "/etc/fstab", quoteShellArg(fstab))
	script = strings.ReplaceAll(script, "/etc/.apteva-fstab.XXXXXX", quoteShellArg(filepath.Join(filepath.Dir(fstab), "fstab.XXXXXX")))
	// Native macOS lacks flock; locking is orthogonal to the exact-marker fixture.
	script = "flock() { return 0; }; " + script
	_, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(fstab)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "apteva-volume:10") {
		t.Fatal("unmounting volume 1 also removes volume 10's fstab entry")
	}
}

func TestAuditRunPodBootSizeOnlyRequest(t *testing.T) {
	err := validateStorageRequest("runpod", InstanceStorageRequest{Boot: &BootStorageRequest{SizeGB: 80}})
	if err != nil {
		t.Fatalf("advertised configurable boot size rejected: %v", err)
	}
}

func TestAuditRetainedBootVolumeCanBeDeleted(t *testing.T) {
	p := &auditPlatform{slugs: map[int64]string{7: "scaleway"}}
	p.hook = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "volume_get" {
			return &sdk.ExecuteResult{Status: 404}, nil
		}
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}, nil
	}
	ctx := auditCtx(t, p)
	v, err := dbCreateVolume(ctx.AppDB(), CreateVolumeInput{Provider: "scaleway", ProviderConnectionID: 7, Name: "retained-boot", SizeGB: 10, DeletePolicy: "retain"}, "vol-1", "block", "available", "{}")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = ctx.AppDB().Exec("UPDATE instance_volumes SET role='boot' WHERE id=?", v.ID)
	_, err = (&App{}).toolVolumeDelete(ctx, map[string]any{"id": v.ID, "confirm": true})
	if err != nil {
		t.Fatalf("retained detached boot volume cannot be deleted: %v", err)
	}
}

func auditSSHClient(t *testing.T, dir string, loseResponse ...bool) *ssh.Client {
	t.Helper()
	return auditSSHClientWithForwardDelay(t, dir, 0, loseResponse...)
}

func auditSSHClientWithForwardDelay(t *testing.T, dir string, delay time.Duration, loseResponse ...bool) *ssh.Client {
	t.Helper()
	priv, _, err := generateSSHKeypair()
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.ParsePrivateKey([]byte(priv))
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		server, chs, reqs, err := ssh.NewServerConn(raw, cfg)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(reqs)
		for next := range chs {
			if next.ChannelType() != "session" {
				go func(ch ssh.NewChannel) {
					time.Sleep(delay)
					_ = ch.Reject(ssh.ConnectionFailed, "connect failed: Connection refused")
				}(next)
				continue
			}
			ch, requests, err := next.Accept()
			if err != nil {
				continue
			}
			go func() {
				defer ch.Close()
				for req := range requests {
					if req.Type != "exec" {
						req.Reply(false, nil)
						continue
					}
					var msg struct{ Command string }
					_ = ssh.Unmarshal(req.Payload, &msg)
					req.Reply(true, nil)
					c := exec.Command("sh", "-c", msg.Command)
					c.Dir = dir
					c.Stdin = ch
					c.Stdout = ch
					if len(loseResponse) > 0 && loseResponse[0] {
						c.Stdout = io.Discard
					}
					c.Stderr = ch.Stderr()
					err := c.Run()
					if len(loseResponse) > 0 && loseResponse[0] {
						_ = server.Close()
						return
					}
					status := uint32(0)
					if err != nil {
						status = 1
					}
					ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
					return
				}
			}()
		}
	}()
	client, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "audit", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestAuditSSHUploadTreatsPathLiterally(t *testing.T) {
	dir := t.TempDir()
	client := auditSSHClient(t, dir)
	_, err := uploadSSHOnce(client, "output-$(touch injected).txt", "aGk=", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "injected")); err == nil {
		t.Fatal("upload filename executed touch injected on the SSH host")
	}
}

func TestAuditSSHDownloadTreatsPathLiterally(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0600)
	client := auditSSHClient(t, dir)
	_, _ = downloadSSHOnce(client, "file$(touch injected).txt")
	if _, err := os.Stat(filepath.Join(dir, "injected")); err == nil {
		t.Fatal("download filename executed touch injected on the SSH host")
	}
}

func TestAuditSSHUploadSupportsSpacedDirectory(t *testing.T) {
	dir := t.TempDir()
	client := auditSSHClient(t, dir)
	_, err := uploadSSHOnce(client, "dir with spaces/file.txt", "aGk=", 2)
	if err != nil {
		t.Fatalf("valid path upload failed: %v", err)
	}
}

func TestAuditSSHHandshakeRespectsTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(350 * time.Millisecond)
	}()
	priv, _, _ := generateSSHKeypair()
	started := time.Now()
	_, _ = dialSSH(&Instance{SSHPrivateKey: priv, SSHHost: "127.0.0.1", SSHPort: ln.Addr().(*net.TCPAddr).Port}, 20*time.Millisecond)
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("20ms SSH timeout blocked %s during handshake", elapsed)
	}
}

func TestAuditProviderParserIgnoresUnrelatedIDs(t *testing.T) {
	id, _, _ := parseProviderResource("scaleway", json.RawMessage(`{"id":"request-id","server":{"id":"real-server","commercial_type":"DEV1-S","public_ip":{"address":"203.0.113.1"}}}`))
	if id != "real-server" {
		t.Fatalf("stored unrelated ID %q instead of server identity", id)
	}
}

func TestAuditGuestDiscoveryDoesNotInferIdentityFromSize(t *testing.T) {
	script := buildVolumePrepareScript(&InstanceVolume{Provider: "aws-ec2", ProviderVolumeID: "vol-123abc", SizeGB: 80}, &VolumePrepareRequest{})
	if strings.Contains(script, "target_bytes=") || strings.Contains(script, "min_bytes=") {
		t.Fatal("size is still used as disk identity")
	}
	if !strings.Contains(script, "Amazon_Elastic_Block_Store_vol123abc") {
		t.Fatal("provider identity not used")
	}
}

func TestAuditFormatProbeRejectsAmbiguousSignature(t *testing.T) {
	// Execute the generated signature/format stage, replacing external programs
	// with harmless stubs. Never access a real block device or run mkfs.
	script := buildVolumePrepareScript(&InstanceVolume{SizeGB: 80}, &VolumePrepareRequest{FormatIfBlank: true, Filesystem: "ext4"})
	start := strings.Index(script, "probe_status=0")
	end := strings.Index(script, "\nuuid=$(blkid")
	if start < 0 || end < start {
		t.Fatal("signature stage unavailable")
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "mkfs.ext4"), []byte("#!/bin/sh\nprintf formatted > \"$AUDIT_FORMAT_SENTINEL\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(binDir, "formatted")
	c := exec.Command("sh", "-c", `set -eu; device=/dev/audit; format_if_blank=true; requested_fs=ext4; blkid() { return 8; }; die() { exit 1; }; `+script[start:end])
	c.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"), "AUDIT_FORMAT_SENTINEL="+sentinel)
	out, err := c.Output()
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Fatal("ambiguous blkid signatures enter the format-if-blank branch")
	}
	if err == nil {
		t.Fatalf("ambiguous signature was not rejected: %s", out)
	}
}

func TestAuditHetznerVolumeIDMustNotBeActionID(t *testing.T) {
	data := json.RawMessage(`{"volume":{"id":123,"name":"data"},"action":{"id":999,"status":"running"},"next_actions":[]}`)
	for i := 0; i < 500; i++ {
		if id := providerVolumeID("hetzner", data); id != "123" {
			t.Fatalf("stored operation ID %s instead of volume ID 123 (iteration %d)", id, i)
		}
	}
}

func TestAuditTunnelPreservesResponseAfterClientHalfClose(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.ReadAll(conn)
		time.Sleep(20 * time.Millisecond)
		_, _ = conn.Write([]byte("response"))
	}()
	r := newTunnelRegistry(func(_ *Instance, _ string) (net.Conn, error) {
		return net.Dial("tcp", backend.Addr().String())
	})
	defer r.closeAll()
	tunnel, err := r.open(&Instance{ID: 7, Provider: "hetzner", Status: "ready"}, 7200)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", tunnel.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = conn.Write([]byte("request"))
	_ = conn.(*net.TCPConn).CloseWrite()
	out, err := io.ReadAll(conn)
	<-done
	if err != nil || string(out) != "response" {
		t.Fatalf("half-close lost backend response: got %q, error=%v", out, err)
	}
}

func TestAuditDestroyDuringCreateCannotLoseUpstreamHost(t *testing.T) {
	p := &auditPlatform{slugs: map[int64]string{7: "hetzner"}}
	ctx := auditCtx(t, p)
	p.hook = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "server_create" {
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}, nil
		}
		rows, _ := dbListInstances(ctx.AppDB(), "hetzner", "")
		if len(rows) != 1 {
			return nil, fmt.Errorf("unexpected rows")
		}
		if err := destroyManagedInstance(ctx, rows[0]); err != nil {
			return nil, err
		}
		return &sdk.ExecuteResult{Success: true, Status: 201, Data: json.RawMessage(`{"server":{"id":123,"public_net":{"ipv4":{"ip":"203.0.113.1"}}}}`)}, nil
	}
	_, err := provisionInstance(ctx, CreateInstanceInput{Name: "audit", Provider: "hetzner"})
	time.Sleep(10 * time.Millisecond)
	if !strings.Contains(strings.Join(p.calls, ","), "server_delete") {
		t.Fatalf("create returned %v after concurrent destroy; created server 123 was neither tracked nor deleted; calls=%v", err, p.calls)
	}
}
