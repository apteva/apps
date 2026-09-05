package main

import (
	"context"
	"errors"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"strings"
	"testing"
	"time"
)

type rotationPlatform struct {
	tk.BasePlatformClient
	fields map[string]string
}

func (p *rotationPlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	return &sdk.ConnectionCredentials{ConnectionID: id, Slug: "aws-s3", Fields: p.fields}, nil
}
func TestCredentialRefreshPreservesLocation(t *testing.T) {
	platform := &rotationPlatform{fields: map[string]string{"access_key_id": "new-key", "secret_access_key": "new-secret", "region": "us-east-1"}}
	app := auditCtx(t, tk.WithPlatform(platform))
	raw, _ := platform.GetConnectionCredentials(1)
	resolved, err := resolveS3Connection(raw)
	if err != nil {
		t.Fatal(err)
	}
	provider := &refreshingS3Credentials{app: app, connectionID: 1, location: *resolved}
	value, err := provider.Retrieve()
	if err != nil || value.AccessKeyID != "new-key" {
		t.Fatal(value.AccessKeyID, err)
	}
	provider.expires = time.Time{}
	platform.fields["region"] = "eu-west-1"
	if _, err = provider.Retrieve(); err == nil {
		t.Fatal("credential refresh switched object location")
	}
}
func TestBackendPinAndVerifiedMigration(t *testing.T) {
	ctx := auditCtx(t)
	f := mustUpload(t, ctx, "x", "/", "data")
	if err := verifyBackendIdentity(ctx, backend()); err != nil {
		t.Fatal(err)
	}
	target := newFakeS3()
	if err := verifyBackendIdentity(ctx, target); err == nil {
		t.Fatal("backend switch ignored pin")
	}
	if err := verifyMigratedBackend(ctx, target, "s3:test"); err == nil {
		t.Fatal("migration accepted missing objects")
	}
	var pin string
	_ = ctx.AppDB().QueryRow(`SELECT value FROM storage_state WHERE key='backend'`).Scan(&pin)
	if pin != "disk" {
		t.Fatal(pin)
	}
	target.objects[objectKey(f.SHA256, f.StorageKey)] = []byte("evil")
	if err := verifyMigratedBackend(ctx, target, "s3:test"); err == nil {
		t.Fatal("migration accepted checksum mismatch")
	}
	target.objects[objectKey(f.SHA256, f.StorageKey)] = []byte("data")
	if err := verifyMigratedBackend(ctx, target, "s3:test"); err != nil {
		t.Fatal(err)
	}
}
func TestAllUploadModesRejectMetadataConflict(t *testing.T) {
	ctx := auditCtx(t)
	f := mustUpload(t, ctx, "test.txt", "/", "data")
	_, _, err := saveBytes(ctx, "test-proj", uploadInput{Name: f.Name, Folder: f.Folder, Visibility: "public"}, []byte("data"))
	if err == nil {
		t.Fatal("upload silently changed/discarded visibility")
	}
	_, err = (&App{}).toolUploadInitCtx(context.Background(), ctx, map[string]any{"name": f.Name, "size_bytes": 4, "sha256": f.SHA256, "visibility": "public"})
	if err == nil {
		t.Fatal("init discarded visibility")
	}
}
func TestLegacyCommittedPendingCannotBeSwept(t *testing.T) {
	ctx := auditCtx(t)
	f := mustUpload(t, ctx, "x", "/", "data")
	id := newDirectUploadID()
	_, err := ctx.AppDB().Exec(`INSERT INTO pending_uploads(upload_id,project_id,storage_key,name,folder,size_bytes,declared_sha256,visibility,expires_at) VALUES(?,?,?,?,?,?,?,?,?)`, id, f.ProjectID, f.StorageKey, f.Name, f.Folder, f.SizeBytes, f.SHA256, f.Visibility, time.Now().Add(-time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err = recoverStorageState(ctx); err != nil {
		t.Fatal(err)
	}
	sweepStalePendingUploads(ctx)
	if _, err = backend().Stat(context.Background(), objectKey(f.SHA256, f.StorageKey)); err != nil {
		t.Fatal(err)
	}
	completed, _, err := completedUpload(ctx, id, f.ProjectID)
	if err != nil || completed == nil || completed.ID != f.ID {
		t.Fatal(completed, err)
	}
}
func TestUploadReadCancellation(t *testing.T) {
	ctx := auditCtx(t)
	c, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := saveStream(c, ctx, "test-proj", uploadInput{Name: "cancelled", Folder: "/"}, strings.NewReader("body"))
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCompletionRetryChecksCurrentFolder(t *testing.T) {
	ctx := auditCtx(t)
	id := agentSession(t, ctx, "/allowed/", 4)
	if err := writeUploadPartBytes(ctx, id, 1, []byte("safe")); err != nil {
		t.Fatal(err)
	}
	out, err := completeUploadSessionForTool(ctx, context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	f := out.(map[string]any)["file"].(*File)
	if _, err = dbUpdate(ctx.AppDB(), "test-proj", f.ID, map[string]any{"folder": "/secret/"}); err != nil {
		t.Fatal(err)
	}
	c := withCaller(sdk.Grant{Effect: "allow", Permission: "files.write", Resource: "folder/allowed/**"})
	if _, err = (&App{}).toolUploadCompleteCtx(c, ctx, map[string]any{"upload_id": id}); err == nil {
		t.Fatal("completion receipt bypassed moved folder permissions")
	}
}
