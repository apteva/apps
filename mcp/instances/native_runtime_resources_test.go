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
