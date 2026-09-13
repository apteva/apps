package main

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type vultrPlatform struct {
	tk.BasePlatformClient
	fields map[string]string
}

func (p *vultrPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"backend": float64(42)}}, nil
}

func (p *vultrPlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	return &sdk.ConnectionCredentials{ConnectionID: id, Slug: "vultr-object-storage", Fields: p.fields}, nil
}

func TestVultrBindingSelectsS3AndSignsRegionalURLs(t *testing.T) {
	var selectable bool
	for _, dep := range (&App{}).Manifest().Requires.Integrations {
		if dep.Role == "backend" && dep.Kind == "integration" {
			selectable = slices.Contains(dep.CompatibleSlugs, "vultr-object-storage")
		}
	}
	if !selectable {
		t.Fatal("Vultr is not selectable as Storage's backend")
	}
	for _, region := range []string{"", "us-east-1", "custom-region"} {
		t.Run(region, func(t *testing.T) {
			platform := &vultrPlatform{fields: map[string]string{
				"access_key_id": "test-access", "secret_access_key": "test-secret", "region": region, "s3_hostname": "ewr1.vultrobjects.com",
			}}
			app := auditCtx(t, tk.WithPlatform(platform), tk.WithConfig(map[string]string{"s3_bucket": "test-bucket"}))
			be, err := initBackend(app)
			if err != nil {
				t.Fatal(err)
			}
			s3, ok := be.(*s3Backend)
			if !ok {
				t.Fatalf("Vultr binding selected %T instead of S3", be)
			}
			for _, method := range []string{"PUT", "GET"} {
				var raw string
				if method == "PUT" {
					raw, err = s3.PresignPut(context.Background(), "ab/test.txt", "text/plain", time.Minute)
				} else {
					raw, err = s3.PresignGet(context.Background(), "ab/test.txt", GetObjectOptions{}, time.Minute)
				}
				if err != nil {
					t.Fatal(err)
				}
				u, err := url.Parse(raw)
				if err != nil {
					t.Fatal(err)
				}
				if u.Scheme != "https" || u.Host != "ewr1.vultrobjects.com" || u.Path != "/test-bucket/ab/test.txt" {
					t.Fatalf("%s uses incorrect bucket endpoint: %s", method, u.Redacted())
				}
				if region == "" {
					region = "us-east-1"
				}
				q := u.Query()
				if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || q.Get("X-Amz-Signature") == "" ||
					!strings.HasPrefix(q.Get("X-Amz-Credential"), "test-access/") ||
					!strings.HasSuffix(q.Get("X-Amz-Credential"), "/"+region+"/s3/aws4_request") {
					t.Fatalf("%s did not sign with the bound credentials and region", method)
				}
			}
		})
	}
}

func TestVultrRejectsIncompleteConnection(t *testing.T) {
	for _, field := range []string{"s3_hostname", "access_key_id", "secret_access_key"} {
		t.Run(field, func(t *testing.T) {
			fields := map[string]string{"s3_hostname": "ewr1.vultrobjects.com", "region": "us-east-1", "access_key_id": "test-access", "secret_access_key": "test-secret"}
			delete(fields, field)
			_, err := resolveS3Connection(&sdk.ConnectionCredentials{Slug: "vultr-object-storage", Fields: fields})
			if err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("missing %s must have an actionable error: %v", field, err)
			}
		})
	}
}

func TestVultrCredentialRotationPreservesLocation(t *testing.T) {
	p := &vultrPlatform{fields: map[string]string{"s3_hostname": "ewr1.vultrobjects.com", "region": "us-east-1", "access_key_id": "old-key", "secret_access_key": "old-secret"}}
	app := auditCtx(t, tk.WithPlatform(p))
	raw, _ := p.GetConnectionCredentials(42)
	resolved, err := resolveS3Connection(raw)
	if err != nil {
		t.Fatal(err)
	}
	provider := &refreshingS3Credentials{app: app, connectionID: 42, location: *resolved}
	p.fields["access_key_id"], p.fields["secret_access_key"] = "new-key", "new-secret"
	value, err := provider.Retrieve()
	if err != nil || value.AccessKeyID != "new-key" || value.SecretAccessKey != "new-secret" {
		t.Fatalf("bound credential rotation failed: %v", err)
	}
	provider.expires = time.Time{}
	p.fields["s3_hostname"] = "ams1.vultrobjects.com"
	if _, err := provider.Retrieve(); err == nil {
		t.Fatal("credential refresh silently changed the bucket location")
	}
}

func TestVultrBindingRequiresBucket(t *testing.T) {
	p := &vultrPlatform{fields: map[string]string{"s3_hostname": "ewr1.vultrobjects.com", "region": "us-east-1", "access_key_id": "test-access", "secret_access_key": "test-secret"}}
	app := auditCtx(t, tk.WithPlatform(p))
	if be, err := initBackend(app); err == nil || !strings.Contains(err.Error(), "s3_bucket") || be != nil {
		t.Fatalf("missing bucket must fail instead of selecting disk: backend=%T, err=%v", be, err)
	}
}

func TestVultrRejectsInvalidHostname(t *testing.T) {
	for _, host := range []string{"https://ewr1.vultrobjects.com", "ewr1.vultrobjects.com/bucket", "ewr1.vultrobjects.com:443", "user@ewr1.vultrobjects.com", "-invalid.com", "invalid..com"} {
		_, err := resolveS3Connection(&sdk.ConnectionCredentials{Slug: "vultr-object-storage", Fields: map[string]string{
			"s3_hostname": host, "access_key_id": "test-access", "secret_access_key": "test-secret",
		}})
		if err == nil || !strings.Contains(err.Error(), "invalid s3_hostname") {
			t.Fatalf("hostname %q should be rejected: %v", host, err)
		}
	}
}
