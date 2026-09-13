package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type managedS3Platform struct {
	objectStoragePlatform
	connection *sdk.ManagedConnectionRequest
	calls      []string
	bucket     bool
	cors       string
	fail       string
	purchases  int
	revoked    bool
}

func (p *managedS3Platform) EnsureManagedConnection(req sdk.ManagedConnectionRequest) (*sdk.PlatformConnection, error) {
	p.connection = &req
	p.revoked = false
	return &sdk.PlatformConnection{ID: 99, AppSlug: req.AppSlug}, nil
}
func (p *managedS3Platform) RotateManagedConnection(id int64, req sdk.ManagedConnectionRotation) (*sdk.PlatformConnection, error) {
	p.connection.Fields = req.Fields
	return &sdk.PlatformConnection{ID: id}, nil
}
func (p *managedS3Platform) RevokeManagedConnection(id int64) error {
	if id != 99 {
		return errors.New("wrong connection")
	}
	p.revoked = true
	return nil
}
func (p *managedS3Platform) ExecuteIntegrationTool(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	if id != 99 {
		if tool == "object_storage_create" {
			p.purchases++
		}
		return p.objectStoragePlatform.ExecuteIntegrationTool(id, tool, args)
	}
	p.calls = append(p.calls, tool)
	status := 200
	body := ""
	if p.fail == tool {
		return &sdk.ExecuteResult{Status: 501, Success: false, Data: json.RawMessage(`"<Error><Code>NotImplemented</Code></Error>"`)}, nil
	}
	switch tool {
	case "head_bucket":
		if !p.bucket {
			status = 404
		}
	case "create_bucket":
		p.bucket = true
	case "get_bucket_acl":
		body = `<AccessControlPolicy><Owner><ID>owner</ID></Owner><AccessControlList><Grant><Grantee><ID>owner</ID></Grantee><Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`
	case "get_bucket_policy":
		status = 404
	case "put_bucket_cors":
		p.cors = args["body"].(string)
	case "get_bucket_cors":
		body = p.cors
		if body == "" {
			status = 404
		}
	case "delete_bucket_cors":
		p.cors = ""
	case "get_object":
		body = "Apteva S3 setup verification"
	case "put_bucket_acl", "delete_bucket_policy", "put_object", "delete_object":
	default:
		return nil, errors.New("unknown S3 tool: " + tool)
	}
	data, _ := json.Marshal(body)
	return &sdk.ExecuteResult{Status: status, Success: status < 400, Data: data}, nil
}
func managedSetupArgs() map[string]any {
	return map[string]any{"name": "Media", "provider": "vultr", "provider_connection_id": int64(7), "region": "2", "bucket": "private-media-test", "request_key": "media-1", "setup": map[string]any{"cors_origins": []string{"https://app.example.com"}}}
}

func TestManagedObjectSetupRetryAndRotation(t *testing.T) {
	p := &managedS3Platform{objectStoragePlatform: objectStoragePlatform{provider: "vultr"}, fail: "put_bucket_cors"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	a := &App{}
	result, err := a.toolObjectStorageCreate(ctx, managedSetupArgs())
	if err != nil {
		t.Fatal(err)
	}
	item := result.(map[string]any)["object_storage"].(*ObjectStorage)
	if item.Status != "error" || item.Setup.Stage != "cors" || p.connection.AppSlug != "s3-compatible" {
		t.Fatalf("item=%+v connection=%+v", item, p.connection)
	}
	if result.(map[string]any)["credentials"] != (*ObjectStorageCredentials)(nil) {
		t.Fatal("managed setup leaked credentials")
	}
	if p.connection.Fields["region"] != "us-east-1" || p.connection.Fields["endpoint"] != "https://ewr1.vultrobjects.com" {
		t.Fatal("wrong S3 endpoint/signing region")
	}
	p.fail = ""
	result, err = a.toolObjectStorageCreate(ctx, managedSetupArgs())
	if err != nil {
		t.Fatal(err)
	}
	item = result.(map[string]any)["object_storage"].(*ObjectStorage)
	if item.Status != "ready" || !item.Setup.Capabilities["private"] || !item.Setup.Capabilities["read_write_delete"] || p.purchases != 1 {
		t.Fatalf("item=%+v purchases=%d", item, p.purchases)
	}
	var setup, metadata string
	if err = ctx.AppDB().QueryRow(`SELECT setup_json,provider_metadata_json FROM object_storages WHERE id=?`, item.ID).Scan(&setup, &metadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(setup+metadata, "vultr-secret") {
		t.Fatal("secret persisted in Instances")
	}
	if _, err = a.toolObjectStorageRotateCredentials(ctx, map[string]any{"id": item.ID}); err != nil {
		t.Fatal(err)
	}
	if p.connection.Fields["secret_access_key"] != "vultr-new-secret" {
		t.Fatal("rotation did not update vault")
	}
	if _, err = a.toolObjectStorageDestroy(ctx, map[string]any{"id": item.ID, "confirm": true}); err != nil {
		t.Fatal(err)
	}
	if !p.revoked {
		t.Fatal("managed connection not revoked")
	}
}
func TestGenericObjectSetupAcrossProviders(t *testing.T) {
	for _, provider := range []string{"scaleway", "vultr"} {
		t.Run(provider, func(t *testing.T) {
			p := &managedS3Platform{objectStoragePlatform: objectStoragePlatform{provider: provider}, bucket: provider == "scaleway"}
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
			args := managedSetupArgs()
			args["provider"] = provider
			if provider == "scaleway" {
				args["region"] = "fr-par"
			}
			r, err := (&App{}).toolObjectStorageCreate(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			item := r.(map[string]any)["object_storage"].(*ObjectStorage)
			if item.Status != "ready" {
				t.Fatalf("setup=%+v", item.Setup)
			}
			for _, tool := range []string{"put_bucket_acl", "put_bucket_cors", "put_object", "get_object", "delete_object"} {
				if !containsString(p.calls, tool) {
					t.Fatal("missing generic step " + tool)
				}
			}
		})
	}
}
func TestObjectSetupRejectsUnsafeInputBeforePurchase(t *testing.T) {
	for _, setup := range []any{map[string]any{"private": false}, map[string]any{"cors_origins": []string{"*"}}, map[string]any{"connection_id": 7}, map[string]any{"cors_origins": []string{"https://app.example.com/path"}}, nil} {
		p := &managedS3Platform{objectStoragePlatform: objectStoragePlatform{provider: "vultr"}}
		ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
		args := managedSetupArgs()
		args["setup"] = setup
		if _, err := (&App{}).toolObjectStorageCreate(ctx, args); err == nil {
			t.Fatalf("accepted %+v", setup)
		}
		if p.purchases != 0 {
			t.Fatal("purchased before validation")
		}
	}
}
func TestObjectSetupDoesNotAdoptExistingBucket(t *testing.T) {
	p := &managedS3Platform{objectStoragePlatform: objectStoragePlatform{provider: "vultr"}, bucket: true}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	r, err := (&App{}).toolObjectStorageCreate(ctx, managedSetupArgs())
	if err != nil {
		t.Fatal(err)
	}
	if r.(map[string]any)["object_storage"].(*ObjectStorage).Status != "error" || containsString(p.calls, "put_bucket_acl") {
		t.Fatal("changed unrelated bucket")
	}
}
func TestObjectSetupTemporaryConnectionExport(t *testing.T) {
	p := &managedS3Platform{objectStoragePlatform: objectStoragePlatform{provider: "vultr"}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	args := managedSetupArgs()
	args["setup"] = map[string]any{"create_connection": false}
	r, err := (&App{}).toolObjectStorageCreate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	item := r.(map[string]any)["object_storage"].(*ObjectStorage)
	if item.Status != "ready" || item.Setup.ConnectionID != 0 || !p.revoked || r.(map[string]any)["credentials"].(*ObjectStorageCredentials).SecretAccessKey == "" {
		t.Fatalf("unexpected export: %+v", item)
	}
}

func TestConfigureExistingSubscriptionWithoutRepurchasing(t *testing.T) {
	p := &managedS3Platform{objectStoragePlatform: objectStoragePlatform{provider: "vultr"}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	a := &App{}
	item, _, err := createObjectStorage(ctx, CreateObjectStorageInput{Name: "Existing", Provider: "vultr", ProviderConnectionID: 7, Region: "2"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.toolObjectStorageCreate(ctx, map[string]any{"id": item.ID, "bucket": "existing-private-bucket", "setup": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	updated := r.(map[string]any)["object_storage"].(*ObjectStorage)
	if updated.Status != "ready" || p.purchases != 1 || updated.Bucket != "existing-private-bucket" {
		t.Fatalf("item=%+v purchases=%d", updated, p.purchases)
	}
}

func TestConfigureOlderScalewayBucketWithRecordedIAMOwnership(t *testing.T) {
	p := &managedS3Platform{objectStoragePlatform: objectStoragePlatform{provider: "scaleway"}, bucket: true}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	item, _, err := createObjectStorage(ctx, CreateObjectStorageInput{Name: "Older", Provider: "scaleway", ProviderConnectionID: 7, Region: "fr-par", Bucket: "older-private-bucket"})
	if err != nil {
		t.Fatal(err)
	}
	meta := parseObjectStorageMetadata(item)
	meta.BucketCreated = false
	b, _ := json.Marshal(meta)
	if err = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"provider_metadata_json": string(b)}); err != nil {
		t.Fatal(err)
	}
	r, err := (&App{}).toolObjectStorageCreate(ctx, map[string]any{"id": item.ID, "setup": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(map[string]any)["object_storage"].(*ObjectStorage); got.Status != "ready" {
		t.Fatalf("setup=%+v", got.Setup)
	}
	if containsString(p.calls, "create_bucket") {
		t.Fatal("tried to recreate tracked bucket")
	}
}
