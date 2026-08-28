package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type objectStoragePlatform struct {
	tk.BasePlatformClient
	provider string
	tools    []string
	args     []map[string]any
	failTool string
}

func (p *objectStoragePlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"provider": float64(7)}}, nil
}

func (p *objectStoragePlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: p.provider}, nil
}

func (p *objectStoragePlatform) GetConnectionPublicConfig(id int64) (*sdk.ConnectionPublicConfig, error) {
	return &sdk.ConnectionPublicConfig{ConnectionID: id, Slug: p.provider, Fields: map[string]string{
		"access_key": "SCWPARENT", "project_id": "project-1",
	}}, nil
}

func (p *objectStoragePlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.tools = append(p.tools, tool)
	p.args = append(p.args, input)
	if tool == p.failTool {
		return &sdk.ExecuteResult{Success: false, Status: 500, Data: json.RawMessage(`{"error":"injected cleanup failure"}`)}, nil
	}
	data := json.RawMessage(`{}`)
	status := 200
	switch tool {
	case "object_bucket_create", "object_bucket_delete", "iam_api_key_delete", "iam_policy_delete", "iam_application_delete", "object_storage_delete":
		status, data = 204, json.RawMessage(`null`)
	case "project_get":
		data = json.RawMessage(`{"id":"project-1","organization_id":"organization-1"}`)
	case "iam_security_settings_get":
		data = json.RawMessage(`{"max_api_key_expiration_duration":"31536000s"}`)
	case "iam_application_create":
		data = json.RawMessage(`{"id":"application-1"}`)
	case "iam_policy_create":
		data = json.RawMessage(`{"id":"policy-1"}`)
	case "iam_api_key_create":
		data = json.RawMessage(`{"access_key":"SCWCHILD","secret_key":"one-time-secret","application_id":"application-1","default_project_id":"project-1"}`)
	case "object_storage_create":
		data = json.RawMessage(`{"object_storage":{"id":"vultr-store-1","status":"active","region":"ewr","s3_hostname":"ewr1.vultrobjects.com","s3_access_key":"VULTRACCESS","s3_secret_key":"vultr-secret"}}`)
	case "object_storage_rotate_credentials":
		data = json.RawMessage(`{"s3_credentials":{"s3_hostname":"ewr1.vultrobjects.com","s3_access_key":"VULTRNEW","s3_secret_key":"vultr-new-secret"}}`)
	case "object_storage_clusters_list":
		data = json.RawMessage(`{"clusters":[{"id":2,"region":"ewr","hostname":"ewr1.vultrobjects.com"}]}`)
	case "object_storage_tiers_list":
		data = json.RawMessage(`{"tiers":[{"id":1,"name":"Performance"}]}`)
	default:
		return &sdk.ExecuteResult{Success: false, Status: 404, Data: json.RawMessage(`{"error":"unexpected tool"}`)}, nil
	}
	return &sdk.ExecuteResult{Success: true, Status: status, Data: data}, nil
}

func TestScalewayObjectStorageLifecycleDoesNotPersistSecret(t *testing.T) {
	platform := &objectStoragePlatform{provider: "scaleway"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))

	item, credentials, err := createObjectStorage(ctx, CreateObjectStorageInput{
		Name: "Media", Provider: "scaleway", ProviderConnectionID: 7,
		Region: "fr-par-1", Bucket: "apteva-media-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Bucket != "apteva-media-test" || item.Region != "fr-par" || item.AccessKeyID != "SCWCHILD" {
		t.Fatalf("item=%#v", item)
	}
	if credentials.SecretAccessKey != "one-time-secret" || !credentials.ShownOnce {
		t.Fatalf("credentials=%#v", credentials)
	}
	if _, err := time.Parse(time.RFC3339, credentials.ExpiresAt); err != nil {
		t.Fatalf("credentials expiry=%q: %v", credentials.ExpiresAt, err)
	}
	stored, err := dbGetObjectStorage(ctx.AppDB(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(stored)
	if strings.Contains(string(encoded), "one-time-secret") || strings.Contains(stored.ProviderMetadataJSON, "one-time-secret") {
		t.Fatalf("secret persisted: row=%s metadata=%s", encoded, stored.ProviderMetadataJSON)
	}
	if _, err := destroyObjectStorage(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := dbGetObjectStorage(ctx.AppDB(), item.ID); err != errObjectStorageNotFound {
		t.Fatalf("destroy left row: %v", err)
	}
	for _, required := range []string{"object_bucket_create", "project_get", "iam_security_settings_get", "iam_application_create", "iam_policy_create", "iam_api_key_create", "object_bucket_delete", "iam_api_key_delete", "iam_policy_delete", "iam_application_delete"} {
		if !containsString(platform.tools, required) {
			t.Errorf("missing provider call %s in %#v", required, platform.tools)
		}
	}
}

func TestVultrObjectStorageCreateAndRotate(t *testing.T) {
	platform := &objectStoragePlatform{provider: "vultr"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	item, credentials, err := createObjectStorage(ctx, CreateObjectStorageInput{
		Name: "Backups", Provider: "vultr", ProviderConnectionID: 7, Region: "2", Plan: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.ProviderID != "vultr-store-1" || item.Endpoint != "https://ewr1.vultrobjects.com" || credentials.SecretAccessKey != "vultr-secret" {
		t.Fatalf("item=%#v credentials=%#v", item, credentials)
	}
	rotated, warnings, err := rotateObjectStorageCredentials(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.AccessKeyID != "VULTRNEW" || rotated.SecretAccessKey != "vultr-new-secret" || len(warnings) != 0 {
		t.Fatalf("rotated=%#v warnings=%#v", rotated, warnings)
	}
	stored, _ := dbGetObjectStorage(ctx.AppDB(), item.ID)
	if stored.AccessKeyID != "VULTRNEW" || strings.Contains(stored.ProviderMetadataJSON, "vultr-new-secret") {
		t.Fatalf("stored=%#v", stored)
	}
}

func TestScalewayDestroyRetainsRecordWhenCredentialCleanupFails(t *testing.T) {
	platform := &objectStoragePlatform{provider: "scaleway"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	item, _, err := createObjectStorage(ctx, CreateObjectStorageInput{
		Name: "Retry cleanup", Provider: "scaleway", ProviderConnectionID: 7,
		Region: "fr-par", Bucket: "apteva-cleanup-retry-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	platform.failTool = "iam_api_key_delete"
	warnings, err := destroyObjectStorage(ctx, item)
	if err == nil || len(warnings) == 0 {
		t.Fatalf("expected retained cleanup error, warnings=%#v err=%v", warnings, err)
	}
	if _, getErr := dbGetObjectStorage(ctx.AppDB(), item.ID); getErr != nil {
		t.Fatalf("record should remain retryable: %v", getErr)
	}
}

func TestObjectStorageCatalogNormalizesProviders(t *testing.T) {
	platform := &objectStoragePlatform{provider: "vultr"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	catalog, err := objectStorageCatalog(ctx, "vultr", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog["locations"].([]any)) != 1 || len(catalog["plans"].([]any)) != 1 {
		t.Fatalf("catalog=%#v", catalog)
	}
}
