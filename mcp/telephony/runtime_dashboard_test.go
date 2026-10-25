package main

import (
	"errors"
	sdk "github.com/apteva/app-sdk"
	"reflect"
	"testing"
)

type dashboardOriginTestClient struct {
	sdk.DashboardConnectClient
	key     string
	origins []string
	count   int
	err     error
}

func (p *dashboardOriginTestClient) ReplaceDashboardConnectOrigins(key string, origins []string) (*sdk.DashboardConnectOriginRegistration, error) {
	p.key = key
	p.origins = append([]string{}, origins...)
	p.count++
	return &sdk.DashboardConnectOriginRegistration{Key: key, Origins: origins}, p.err
}
func TestRuntimeDashboardExactOriginsAndLocalCleanup(t *testing.T) {
	a := &App{runtime: runtimeTestActive()}
	a.runtime.endpoints = map[string]string{"browser": "https://voice.example", "media": "https://voice.example", "signaling": "https://signal.example:8443", "callbacks": "https://not-browser.example"}
	p := &dashboardOriginTestClient{}
	if e := a.registerRuntimeDashboard(p); e != nil {
		t.Fatal(e)
	}
	if p.key != runtimeDashboardRegistration || !reflect.DeepEqual(p.origins, []string{"https://signal.example:8443", "https://voice.example"}) {
		t.Fatal(p)
	}
	a.runtime = nil
	if e := a.registerRuntimeDashboard(p); e != nil || len(p.origins) != 0 {
		t.Fatalf("local did not clear old origins: %v %+v", e, p)
	}
	if e := a.registerRuntimeDashboard(nil); e != nil {
		t.Fatal(e)
	}
}
func TestRuntimeDashboardFencedAndFailedRegistration(t *testing.T) {
	a := &App{runtime: runtimeTestActive()}
	a.runtime.endpoints["browser"] = "https://voice.example"
	p := &dashboardOriginTestClient{}
	a.runtime.fenced = true
	if e := a.registerRuntimeDashboard(p); e == nil || p.count != 0 {
		t.Fatal("fenced execution registered")
	}
	a.runtime.fenced = false
	p.err = errors.New("unsupported server")
	if e := a.registerRuntimeDashboard(p); e == nil {
		t.Fatal("registration failure hidden")
	}
	a.runtime.endpoints["browser"] = "https://user:secret@voice.example"
	p.err = nil
	if e := a.registerRuntimeDashboard(p); e == nil {
		t.Fatal("credential-bearing origin accepted")
	}
}
