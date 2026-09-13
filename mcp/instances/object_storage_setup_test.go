package main

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type directSetupPlatform struct {
	objectStoragePlatform
	purchases int
}

func (p *directSetupPlatform) ExecuteIntegrationTool(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	if id != 7 {
		return nil, fmt.Errorf("unexpected integration connection %d", id)
	}
	if tool == "object_storage_create" {
		p.purchases++
	}
	return p.objectStoragePlatform.ExecuteIntegrationTool(id, tool, args)
}
func (p *directSetupPlatform) EnsureManagedConnection(sdk.ManagedConnectionRequest) (*sdk.PlatformConnection, error) {
	panic("Instances must not create connections")
}
func (p *directSetupPlatform) RotateManagedConnection(int64, sdk.ManagedConnectionRotation) (*sdk.PlatformConnection, error) {
	panic("Instances must not manage connections")
}
func (p *directSetupPlatform) RevokeManagedConnection(int64) error {
	panic("Instances must not manage connections")
}

type directFixture struct {
	bucket bool
	cors   string
	fail   string
	calls  []string
}
type s3FixtureTransport func(*http.Request) (*http.Response, error)

func (f s3FixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
func newDirectFixture(t *testing.T, existing bool) *directFixture {
	t.Helper()
	f := &directFixture{bucket: existing}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("request not signed")
		}
		op := r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery
		f.calls = append(f.calls, op)
		if r.URL.Query().Has(f.fail) && f.fail != "" {
			w.WriteHeader(501)
			io.WriteString(w, `<Error><Code>NotImplemented</Code></Error>`)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Has("acl") {
			if r.Method == "PUT" {
				if r.Header.Get("x-amz-acl") != "private" {
					t.Error("missing private ACL")
				}
				return
			}
			io.WriteString(w, `<AccessControlPolicy><Owner><ID>owner</ID></Owner><AccessControlList><Grant><Grantee><ID>owner</ID></Grantee><Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`)
			return
		}
		if r.URL.Query().Has("policy") {
			if r.Method == "GET" {
				w.WriteHeader(404)
			}
			return
		}
		if r.URL.Query().Has("cors") {
			switch r.Method {
			case "PUT":
				b, _ := io.ReadAll(r.Body)
				f.cors = string(b)
				sum := md5.Sum(b)
				if r.Header.Get("Content-MD5") != base64.StdEncoding.EncodeToString(sum[:]) {
					t.Error("wrong CORS checksum")
				}
			case "DELETE":
				f.cors = ""
			case "GET":
				if f.cors == "" {
					w.WriteHeader(404)
				} else {
					io.WriteString(w, f.cors)
				}
			}
			return
		}
		if strings.Contains(r.URL.Path, "/.apteva-setup-") {
			if r.Method == "GET" {
				w.Header().Set("Content-Type", "application/octet-stream")
				io.WriteString(w, "Apteva S3 setup verification")
			}
			return
		}
		if r.Method == "HEAD" && !f.bucket {
			w.WriteHeader(404)
		}
		if r.Method == "PUT" {
			f.bucket = true
		}
	}))
	t.Cleanup(server.Close)
	original := directS3HTTPClient
	// Route signed requests to TLS fixture while preserving their original Host.
	transport := server.Client().Transport
	directS3HTTPClient = &http.Client{CheckRedirect: original.CheckRedirect, Transport: s3FixtureTransport(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		u := *r.URL
		copy.Host = u.Host
		u.Host = strings.TrimPrefix(server.URL, "https://")
		copy.URL = &u
		return transport.RoundTrip(copy)
	})}
	t.Cleanup(func() { directS3HTTPClient = original })
	return f
}
func directSetupArgs() map[string]any {
	return map[string]any{"name": "Media", "provider": "vultr", "provider_connection_id": int64(7), "region": "2", "bucket": "private-media-test", "request_key": "media-1", "setup": map[string]any{"cors_origins": []string{"https://app.example.com"}}}
}
func retryCredentials(creds *ObjectStorageCredentials) map[string]any {
	return map[string]any{"access_key_id": creds.AccessKeyID, "secret_access_key": creds.SecretAccessKey}
}
func TestDirectObjectSetupRetryReturnsCredentialsWithoutConnections(t *testing.T) {
	f := newDirectFixture(t, false)
	f.fail = "cors"
	p := &directSetupPlatform{objectStoragePlatform: objectStoragePlatform{provider: "vultr"}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	a := &App{}
	r, err := a.toolObjectStorageCreate(ctx, directSetupArgs())
	if err != nil {
		t.Fatal(err)
	}
	item := r.(map[string]any)["object_storage"].(*ObjectStorage)
	creds := r.(map[string]any)["credentials"].(*ObjectStorageCredentials)
	if item.Status != "error" || item.Setup.Stage != "cors" || creds.SecretAccessKey != "vultr-secret" {
		t.Fatalf("item=%+v", item)
	}
	f.fail = ""
	if _, err = a.toolObjectStorageCreate(ctx, map[string]any{"id": item.ID}); err == nil {
		t.Fatal("resumed without credentials or consent to rotate")
	}
	if containsString(p.tools, "object_storage_rotate_credentials") {
		t.Fatal("implicitly rotated credentials")
	}
	r, err = a.toolObjectStorageCreate(ctx, map[string]any{"id": item.ID, "credentials": retryCredentials(creds)})
	if err != nil {
		t.Fatal(err)
	}
	item = r.(map[string]any)["object_storage"].(*ObjectStorage)
	if item.Status != "ready" || !item.Setup.Capabilities["private"] || !item.Setup.Capabilities["read_write_delete"] || p.purchases != 1 {
		t.Fatalf("setup=%+v", item.Setup)
	}
	var setup, metadata string
	if err = ctx.AppDB().QueryRow(`SELECT setup_json,provider_metadata_json FROM object_storages WHERE id=?`, item.ID).Scan(&setup, &metadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(setup+metadata, "vultr-secret") || strings.Contains(setup, "connection_id") {
		t.Fatal("credentials/connection persisted")
	}
	r, err = a.toolObjectStorageRotateCredentials(ctx, map[string]any{"id": item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if r.(map[string]any)["credentials"].(*ObjectStorageCredentials).SecretAccessKey != "vultr-new-secret" {
		t.Fatal("rotation did not return credentials")
	}
	if _, err = a.toolObjectStorageDestroy(ctx, map[string]any{"id": item.ID, "confirm": true}); err != nil {
		t.Fatal(err)
	}
}
func TestDirectObjectSetupAcrossProviders(t *testing.T) {
	for _, provider := range []string{"scaleway", "vultr"} {
		t.Run(provider, func(t *testing.T) {
			newDirectFixture(t, provider == "scaleway")
			p := &directSetupPlatform{objectStoragePlatform: objectStoragePlatform{provider: provider}}
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
			args := directSetupArgs()
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
			encoded, _ := json.Marshal(item)
			if strings.Contains(string(encoded), "secret") {
				t.Fatal("secret on public resource")
			}
		})
	}
}
func TestDirectObjectSetupExistingSubscription(t *testing.T) {
	newDirectFixture(t, false)
	p := &directSetupPlatform{objectStoragePlatform: objectStoragePlatform{provider: "vultr"}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	item, creds, err := createObjectStorage(ctx, CreateObjectStorageInput{Name: "Existing", Provider: "vultr", ProviderConnectionID: 7, Region: "2"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := (&App{}).toolObjectStorageCreate(ctx, map[string]any{"id": item.ID, "bucket": "existing-private-bucket", "setup": map[string]any{}, "credentials": retryCredentials(creds)})
	if err != nil {
		t.Fatal(err)
	}
	if r.(map[string]any)["object_storage"].(*ObjectStorage).Status != "ready" || p.purchases != 1 {
		t.Fatal("existing setup failed")
	}
}
func TestDirectSetupRejectsUnsafeInputBeforePurchase(t *testing.T) {
	for _, setup := range []any{map[string]any{"private": false}, map[string]any{"cors_origins": []string{"*"}}, map[string]any{"create_connection": true}, map[string]any{"connection_id": 99}, nil} {
		p := &directSetupPlatform{objectStoragePlatform: objectStoragePlatform{provider: "vultr"}}
		ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
		args := directSetupArgs()
		args["setup"] = setup
		if _, err := (&App{}).toolObjectStorageCreate(ctx, args); err == nil {
			t.Fatalf("accepted %+v", setup)
		}
		if p.purchases != 0 {
			t.Fatal("purchased before validation")
		}
	}
}
func TestDirectSetupDoesNotAdoptExistingBucket(t *testing.T) {
	f := newDirectFixture(t, true)
	p := &directSetupPlatform{objectStoragePlatform: objectStoragePlatform{provider: "vultr"}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	r, err := (&App{}).toolObjectStorageCreate(ctx, directSetupArgs())
	if err != nil {
		t.Fatal(err)
	}
	if r.(map[string]any)["object_storage"].(*ObjectStorage).Status != "error" {
		t.Fatal("adopted bucket")
	}
	for _, call := range f.calls {
		if strings.Contains(call, "acl") {
			t.Fatal("changed unrelated bucket")
		}
	}
}

func TestDirectSetupExplicitRotationOnReadyResource(t *testing.T) {
	newDirectFixture(t, false)
	p := &directSetupPlatform{objectStoragePlatform: objectStoragePlatform{provider: "vultr"}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	a := &App{}
	result, err := a.toolObjectStorageCreate(ctx, directSetupArgs())
	if err != nil {
		t.Fatal(err)
	}
	item := result.(map[string]any)["object_storage"].(*ObjectStorage)
	result, err = a.toolObjectStorageCreate(ctx, map[string]any{"id": item.ID, "rotate_credentials": true})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["credentials"].(*ObjectStorageCredentials).SecretAccessKey != "vultr-new-secret" {
		t.Fatal("explicit credential recovery failed")
	}
	if p.purchases != 1 {
		t.Fatal("repurchased subscription")
	}
}
