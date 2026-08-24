package main

import (
	"errors"
	"reflect"
	"sort"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type browserOriginStub struct {
	tk.BasePlatformClient
	registrations map[string][]string
	replaced      []string
	deleted       []string
	replaceErr    error
	listErr       error
}

var _ sdk.BrowserOriginClient = (*browserOriginStub)(nil)

func (s *browserOriginStub) ReplaceBrowserOrigins(key string, origins []string) (*sdk.BrowserOriginRegistration, error) {
	s.replaced = append(s.replaced, key)
	if s.replaceErr != nil {
		return nil, s.replaceErr
	}
	if s.registrations == nil {
		s.registrations = map[string][]string{}
	}
	if len(origins) == 0 {
		delete(s.registrations, key)
	} else {
		s.registrations[key] = append([]string{}, origins...)
	}
	return &sdk.BrowserOriginRegistration{
		Key: key, Origins: append([]string{}, origins...),
		Preflight: sdk.BrowserPreflightPlatform, Credentials: true,
	}, nil
}

func (s *browserOriginStub) DeleteBrowserOrigins(key string) error {
	s.deleted = append(s.deleted, key)
	delete(s.registrations, key)
	return nil
}

func (s *browserOriginStub) ListBrowserOriginRegistrations() ([]sdk.BrowserOriginRegistration, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	keys := make([]string, 0, len(s.registrations))
	for key := range s.registrations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]sdk.BrowserOriginRegistration, 0, len(keys))
	for _, key := range keys {
		out = append(out, sdk.BrowserOriginRegistration{
			Key: key, Origins: append([]string{}, s.registrations[key]...),
			Preflight: sdk.BrowserPreflightPlatform, Credentials: true,
		})
	}
	return out, nil
}

func newBrowserOriginCtx(t *testing.T, projectID string, platform *browserOriginStub) *sdk.AppCtx {
	t.Helper()
	return tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID(projectID),
		tk.WithPlatform(platform),
	)
}

func TestClientBrowserOriginsCreateUpdateDisableLifecycle(t *testing.T) {
	platform := &browserOriginStub{}
	ctx := newBrowserOriginCtx(t, "cors-lifecycle", platform)
	app := &App{}

	createdAny, err := app.toolClientsCreate(ctx, map[string]any{
		"name":            "browser app",
		"type":            "spa",
		"allowed_origins": []any{"https://app.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	created := createdAny.(map[string]any)
	clientID := created["client_id"].(string)
	key := browserOriginRegistrationKey(clientID)
	if created["browser_origins_synced"] != true {
		t.Fatalf("create sync status=%v", created)
	}
	if got := platform.registrations[key]; !reflect.DeepEqual(got, []string{"https://app.example"}) {
		t.Fatalf("create origins=%v", got)
	}

	updatedAny, err := app.toolClientsUpdate(ctx, map[string]any{
		"client_id":              clientID,
		"add_allowed_origins":    []any{"http://localhost:3000"},
		"remove_allowed_origins": []any{"https://app.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated := updatedAny.(map[string]any)
	if updated["browser_origins_synced"] != true {
		t.Fatalf("update sync status=%v", updated)
	}
	wantOrigins := []string{"http://localhost:3000"}
	if got := platform.registrations[key]; !reflect.DeepEqual(got, wantOrigins) {
		t.Fatalf("updated registration=%v want=%v", got, wantOrigins)
	}

	disabledAny, err := app.toolClientsDisable(ctx, map[string]any{"client_id": clientID})
	if err != nil {
		t.Fatal(err)
	}
	disabled := disabledAny.(map[string]any)
	if disabled["browser_origins_synced"] != true {
		t.Fatalf("disable sync status=%v", disabled)
	}
	if _, ok := platform.registrations[key]; ok {
		t.Fatalf("registration %q remains after disable", key)
	}
}

func TestClientCreatePreservesSecretWhenBrowserOriginSyncFails(t *testing.T) {
	platform := &browserOriginStub{replaceErr: errors.New("server upgrade required")}
	ctx := newBrowserOriginCtx(t, "cors-create-failure", platform)

	createdAny, err := (&App{}).toolClientsCreate(ctx, map[string]any{
		"name":            "web app",
		"type":            "web",
		"allowed_origins": []any{"https://app.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	created := createdAny.(map[string]any)
	if created["client_secret"] == "" {
		t.Fatalf("one-time secret missing after sync failure: %v", created)
	}
	if created["browser_origins_synced"] != false || created["browser_origins_error"] == "" {
		t.Fatalf("sync failure not surfaced: %v", created)
	}
}

func TestClientCreateRejectsOriginsPlatformCORSCannotRegister(t *testing.T) {
	ctx := newBrowserOriginCtx(t, "cors-create-validation", &browserOriginStub{})
	for _, origin := range []string{"*", "https://example.com/path"} {
		if _, err := (&App{}).toolClientsCreate(ctx, map[string]any{
			"name": "invalid browser app", "type": "spa",
			"allowed_origins": []any{origin},
		}); err == nil {
			t.Fatalf("origin %q was accepted", origin)
		}
	}
}

func TestReconcileBrowserOriginsReplacesActiveAndDeletesOnlyStaleAuthKeys(t *testing.T) {
	platform := &browserOriginStub{registrations: map[string][]string{
		"oauth-client-stale":    {"https://stale.example"},
		"oauth-client-disabled": {"https://disabled.example"},
		"future-client-keep":    {"https://keep.example"},
	}}
	ctx := newBrowserOriginCtx(t, "cors-reconcile", platform)
	active := Client{ClientID: "active", Name: "Active", Type: "spa", AllowedOrigins: []string{"https://active.example"}}
	if _, err := dbCreateClient(ctx.AppDB(), "cors-reconcile", 0, active, ""); err != nil {
		t.Fatal(err)
	}
	disabled := Client{ClientID: "disabled", Name: "Disabled", Type: "spa", AllowedOrigins: []string{"https://disabled.example"}}
	if _, err := dbCreateClient(ctx.AppDB(), "cors-reconcile", 0, disabled, ""); err != nil {
		t.Fatal(err)
	}
	if err := dbDisableClient(ctx.AppDB(), "cors-reconcile", disabled.ClientID); err != nil {
		t.Fatal(err)
	}

	if err := reconcileBrowserOrigins(ctx, "cors-reconcile"); err != nil {
		t.Fatal(err)
	}
	if got := platform.registrations["oauth-client-active"]; !reflect.DeepEqual(got, active.AllowedOrigins) {
		t.Fatalf("active registration=%v", got)
	}
	if _, ok := platform.registrations["oauth-client-stale"]; ok {
		t.Fatal("stale Auth registration remains")
	}
	if _, ok := platform.registrations["oauth-client-disabled"]; ok {
		t.Fatal("disabled-client registration remains")
	}
	if _, ok := platform.registrations["future-client-keep"]; !ok {
		t.Fatal("non-Auth registration was deleted")
	}
}

func TestReconcileBrowserOriginsDoesNotCleanupAfterListFailure(t *testing.T) {
	platform := &browserOriginStub{
		registrations: map[string][]string{"oauth-client-stale": {"https://stale.example"}},
		listErr:       errors.New("list unavailable"),
	}
	ctx := newBrowserOriginCtx(t, "cors-list-failure", platform)
	if err := reconcileBrowserOrigins(ctx, "cors-list-failure"); err == nil {
		t.Fatal("expected list failure")
	}
	if len(platform.deleted) != 0 {
		t.Fatalf("cleanup ran with incomplete list: %v", platform.deleted)
	}
}
