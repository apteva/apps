package main

import (
	sdk "github.com/apteva/app-sdk"
	"os/exec"
	"strings"
	"testing"
)

func TestNativeResourceUnitAndFencing(t *testing.T) {
	p := &sdk.ResourcePolicy{Process: sdk.ProcessResources{CPUWeight: 200, CPUQuotaPercent: 125, IOWeight: 150, MemoryHighBytes: 256 << 20, MemoryMaxBytes: 512 << 20}}
	d := nativeRecord{ID: "deployment", InstallID: 17, Generation: 3, Options: nativeOptions{Resources: p}}
	unit := nativeUnitText(d)
	for _, property := range []string{"CPUWeight=200", "CPUQuota=125%", "IOWeight=150", "MemoryHigh=268435456", "MemoryMax=536870912"} {
		if !strings.Contains(unit, property) {
			t.Fatal(property, unit)
		}
	}
	if strings.Index(unit, "CPUWeight") < strings.Index(unit, "[Service]") {
		t.Fatal("wrong unit section")
	}
	e := sdk.ResourceEnvelope{InstallID: "17", DeploymentID: "deployment", Generation: "3", Revision: 1, Policy: *p}
	env := map[string]string{"APTEVA_RESOURCE_POLICY": nativeJSON(e)}
	if err := validateNativeResourceEnvironment(d, env); err != nil {
		t.Fatal(err)
	}
	e.Generation = "2"
	env["APTEVA_RESOURCE_POLICY"] = nativeJSON(e)
	if validateNativeResourceEnvironment(d, env) == nil {
		t.Fatal("stale generation accepted")
	}
	e.Generation = "3"
	e.Policy.Process.CPUWeight = 300
	env["APTEVA_RESOURCE_POLICY"] = nativeJSON(e)
	if validateNativeResourceEnvironment(d, env) == nil {
		t.Fatal("unprepared policy accepted")
	}
}
func TestNativeResourceInspectionShellSyntax(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-n")
	cmd.Stdin = strings.NewReader(nativeResourceInspectionScript())
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatal(e, string(out))
	}
	d := nativeRecord{ID: "d"}
	if strings.Contains(nativeUnitText(d), "CPUWeight=") {
		t.Fatal("default CPU behavior changed")
	}
}

func TestNativeResourcePolicyFullLifecycle(t *testing.T) {
	b, f, req := newNativeTest(t)
	req.Resources = &sdk.ResourcePolicy{Process: sdk.ProcessResources{CPUWeight: 200}}
	req.Execution = prepareNativeTest(t, b, req)
	for _, action := range []string{"prepare", "inspect"} {
		if _, w := nativeCall(t, b, action, req); w.Code != 200 {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
	}
	changed := req
	changed.Resources = &sdk.ResourcePolicy{Process: sdk.ProcessResources{CPUWeight: 201}}
	for _, action := range []string{"prepare", "inspect", "start", "stop", "delete"} {
		if _, w := nativeCall(t, b, action, changed); w.Code != 409 {
			t.Fatalf("changed policy accepted by %s: %d", action, w.Code)
		}
	}
	start := startNativeRequest(req)
	start.Environment["APTEVA_RESOURCE_POLICY"] = nativeJSON(sdk.ResourceEnvelope{InstallID: "52", DeploymentID: req.DeploymentID, Generation: "1", Policy: *req.Resources})
	if _, w := nativeCall(t, b, "start", start); w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	if out, w := nativeCall(t, b, "inspect", req); w.Code != 200 || !out.Ready {
		t.Fatalf("running inspect: %d %s", w.Code, w.Body.String())
	}
	if _, w := nativeCall(t, b, "start", start); w.Code != 200 || f.starts != 1 {
		t.Fatal("start retry failed or duplicated process")
	}
	for _, action := range []string{"stop", "prepare", "inspect", "delete", "delete", "inspect"} {
		if _, w := nativeCall(t, b, action, req); w.Code != 200 {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
	}
}

func TestNativeLegacyDeploymentAcceptsEmptyResourcePolicy(t *testing.T) {
	b, _, req := newNativeTest(t)
	req.Execution = prepareNativeTest(t, b, req)
	req.Resources = &sdk.ResourcePolicy{Workloads: map[string]sdk.WorkloadPolicy{}, Network: map[string]sdk.SocketPolicy{}}
	for _, action := range []string{"inspect", "prepare", "stop", "delete"} {
		if _, w := nativeCall(t, b, action, req); w.Code != 200 {
			t.Fatalf("legacy %s: %d %s", action, w.Code, w.Body.String())
		}
	}
}
