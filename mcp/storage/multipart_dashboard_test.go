package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type dashboardPlatform struct {
	tk.BasePlatformClient
	mu    sync.Mutex
	calls []sdk.DashboardConnectOriginRegistration
	err   error
}

func (p *dashboardPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{}, nil
}
func (p *dashboardPlatform) ReplaceDashboardConnectOrigins(key string, origins []string) (*sdk.DashboardConnectOriginRegistration, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := sdk.DashboardConnectOriginRegistration{Key: key, Origins: append([]string{}, origins...)}
	p.calls = append(p.calls, out)
	return &out, p.err
}
func (p *dashboardPlatform) GetDashboardConnectOrigins(string) (*sdk.DashboardConnectOriginRegistration, error) {
	return nil, errors.New("unused")
}
func (p *dashboardPlatform) ListDashboardConnectOriginRegistrations() ([]sdk.DashboardConnectOriginRegistration, error) {
	return nil, errors.New("unused")
}
func (p *dashboardPlatform) DeleteDashboardConnectOrigins(string) error { return errors.New("unused") }
func (p *dashboardPlatform) snapshot() []sdk.DashboardConnectOriginRegistration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]sdk.DashboardConnectOriginRegistration{}, p.calls...)
}
func dashboardS3(t *testing.T, endpoint, bucket string, lookup minio.BucketLookupType, secure bool) *s3Backend {
	t.Helper()
	client, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4("dummy-key", "dummy-secret", ""), Region: "us-east-1", Secure: secure, BucketLookup: lookup})
	if err != nil {
		t.Fatal(err)
	}
	return &s3Backend{client: client, bucket: bucket}
}
func TestDashboardUploadOriginMatchesSignedTarget(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, bucket, want string
		lookup                       minio.BucketLookupType
	}{
		{"Hetzner virtual host", "fsn1.your-objectstorage.com", "apteva", "https://apteva.fsn1.your-objectstorage.com", minio.BucketLookupDNS},
		{"path style", "s3.fr-par.scw.cloud", "apteva", "https://s3.fr-par.scw.cloud", minio.BucketLookupPath},
		{"custom HTTPS port", "storage.example:9443", "videos", "https://storage.example:9443", minio.BucketLookupPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be := dashboardS3(t, tc.endpoint, tc.bucket, tc.lookup, true)
			got, err := be.browserUploadOrigin(context.Background())
			if err != nil || got != tc.want {
				t.Fatalf("origin=%s err=%v", got, err)
			}
			if strings.ContainsAny(got, "?#") || strings.Contains(got, "dummy-") {
				t.Fatal("leaked signing data")
			}
		})
	}
	be := dashboardS3(t, "127.0.0.1:9000", "videos", minio.BucketLookupPath, false)
	if _, err := be.browserUploadOrigin(context.Background()); err == nil {
		t.Fatal("accepted HTTP destination")
	}
}
func TestDashboardRegistrationReconcilesBackendAndClearsDisk(t *testing.T) {
	platform := &dashboardPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	globalBackend = dashboardS3(t, "fsn1.your-objectstorage.com", "apteva", minio.BucketLookupDNS, true)
	c := context.Background()
	for range 2 {
		if err := reconcileDashboardUploadOrigin(c, ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(platform.snapshot()) != 1 {
		t.Fatal("repeated registration on upload")
	}
	// Configuration changes replace the backend at mount. The stable key replaces
	// the previous origin instead of accumulating access to old buckets.
	globalBackend = dashboardS3(t, "s3.example", "new-bucket", minio.BucketLookupPath, true)
	if err := reconcileDashboardUploadOrigin(c, ctx); err != nil {
		t.Fatal(err)
	}
	globalBackend = newDiskBackend(ctx)
	if err := reconcileDashboardUploadOrigin(c, ctx); err != nil {
		t.Fatal(err)
	}
	calls := platform.snapshot()
	want := []sdk.DashboardConnectOriginRegistration{
		{Key: "storage-backend", Origins: []string{"https://apteva.fsn1.your-objectstorage.com"}},
		{Key: "storage-backend", Origins: []string{"https://s3.example"}},
		{Key: "storage-backend", Origins: []string{}},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("registrations=%+v", calls)
	}
}
func TestDashboardRegistrationFailureUsesRelayAndRetries(t *testing.T) {
	for _, failure := range []string{"unsupported SDK client", "HTTP 404 older server", "HTTP 403 permission not approved"} {
		t.Run(failure, func(t *testing.T) {
			platform := &dashboardPlatform{err: errors.New(failure)}
			opts := []tk.Option{tk.WithEnv("APTEVA_PUBLIC_URL", "https://dashboard.example")}
			if failure != "unsupported SDK client" {
				opts = append(opts, tk.WithPlatform(platform))
			}
			ctx := newTestCtx(t, opts...)
			be := dashboardS3(t, "fsn1.your-objectstorage.com", "apteva", minio.BucketLookupDNS, true)
			be.corsOrigin = "https://dashboard.example"
			be.corsUntil = time.Now().Add(time.Hour)
			globalBackend = be
			if prepareBrowserUpload(context.Background(), ctx, "") {
				t.Fatal("enabled direct without registration")
			}
			if prepareBrowserUpload(context.Background(), ctx, "") {
				t.Fatal("cached failure enabled direct")
			}
			if failure == "unsupported SDK client" {
				return
			}
			if len(platform.snapshot()) != 1 {
				t.Fatal("registration failure not cached")
			}
			platform.err = nil
			be.dashboardUntil = time.Time{}
			if !prepareBrowserUpload(context.Background(), ctx, "") {
				t.Fatal("failed to recover after approval/server upgrade")
			}
			if len(platform.snapshot()) != 2 {
				t.Fatal("did not retry registration")
			}
		})
	}
}
func TestDashboardDiskMountClearsRegistration(t *testing.T) {
	platform := &dashboardPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(platform), tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	defer app.OnUnmount(ctx)
	deadline := time.Now().Add(time.Second)
	for len(platform.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	calls := platform.snapshot()
	if len(calls) != 1 || calls[0].Key != "storage-backend" || len(calls[0].Origins) != 0 {
		t.Fatalf("mount did not clear registration: %+v", calls)
	}
}
