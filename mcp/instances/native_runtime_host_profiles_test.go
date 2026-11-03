package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func chromeReadyProbe() nativeProbe {
	p := nativeProbe{ABI: "gnu", OSID: "debian", Fonts: true, Platform: "linux-amd64", HostKey: "physical-host-a", MemoryMB: 2048, DiskMB: 2048}
	p.SystemLibraries = append([]string{"libc.so.6"}, nativeHostProfiles[sdk.HostProfileChromeHeadless].Libraries...)
	return p
}

func TestNativeTargetProbeParsesHostSetupFacts(t *testing.T) {
	raw := "x86_64\n" + strings.Repeat("a", 32) + "\nssh-ed25519 identity\n2048\n4096\nABI=gnu\nOS=raspbian:debian\nFONTS=1\nLIB=libc.so.6\nSTATE_AVAILABLE=100"
	p, err := parseNativeTargetProbe(raw)
	if err != nil || p.OSID != "raspbian" || p.OSLike != "debian" || !p.Fonts || !p.aptHost() {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = parseNativeTargetProbe(strings.Replace(raw, "OS=raspbian:debian", "OS=$(reboot):", 1))
	if err != nil || p.OSID != "" || p.aptHost() {
		t.Fatalf("unsafe os-release value kept: %+v %v", p, err)
	}
}

func TestNativeHostProfileStates(t *testing.T) {
	missing := nativeProbe{ABI: "gnu", OSID: "debian", SystemLibraries: []string{"libc.so.6"}}
	noFonts := chromeReadyProbe()
	noFonts.Fonts = false
	ubuntuLike := missing
	ubuntuLike.OSID, ubuntuLike.OSLike = "pop", "ubuntu debian"
	alpine := nativeProbe{ABI: "musl", OSID: "alpine"}
	for name, tc := range map[string]struct {
		probe nativeProbe
		want  string
	}{
		"ready":       {chromeReadyProbe(), sdk.HostProfilePresent},
		"debian":      {missing, sdk.HostProfileInstallable},
		"fonts only":  {noFonts, sdk.HostProfileInstallable},
		"ubuntu like": {ubuntuLike, sdk.HostProfileInstallable},
		"alpine":      {alpine, sdk.HostProfileUnsupported},
	} {
		if got := nativeHostProfileStates(tc.probe)[sdk.HostProfileChromeHeadless]; got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

func TestNativeHostProfilePlanRequiresApproval(t *testing.T) {
	missing := nativeProbe{ABI: "gnu", OSID: "debian"}
	req := func(approved []string, profiles ...string) nativeRequest {
		var r nativeRequest
		r.Manifest.Runtime.Package = &sdk.RuntimePackage{HostProfiles: profiles}
		r.ApprovedHostProfiles = approved
		return r
	}
	if got, err := nativeHostProfilePlan(nativeRequest{}, missing); err != nil || got != nil {
		t.Fatalf("no declaration: %v %v", got, err)
	}
	if _, err := nativeHostProfilePlan(req(nil, "chrome-headless"), missing); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("unapproved install allowed: %v", err)
	}
	if got, err := nativeHostProfilePlan(req([]string{"chrome-headless"}, "chrome-headless"), missing); err != nil || !reflect.DeepEqual(got, []string{"chrome-headless"}) {
		t.Fatalf("approved plan: %v %v", got, err)
	}
	if got, err := nativeHostProfilePlan(req(nil, "chrome-headless"), chromeReadyProbe()); err != nil || got != nil {
		t.Fatalf("present profile needs no approval: %v %v", got, err)
	}
	if _, err := nativeHostProfilePlan(req([]string{"gpu-drivers"}, "gpu-drivers"), missing); err == nil || !strings.Contains(err.Error(), "not provided") {
		t.Fatalf("unknown profile accepted: %v", err)
	}
	if _, err := nativeHostProfilePlan(req([]string{"chrome-headless"}, "chrome-headless"), nativeProbe{ABI: "musl", OSID: "alpine"}); err == nil || !strings.Contains(err.Error(), "cannot be installed") {
		t.Fatalf("unsupported target accepted: %v", err)
	}
}

func TestNativeHostProfileScriptPicksDistributionPackages(t *testing.T) {
	script := nativeHostProfileScript([]string{sdk.HostProfileChromeHeadless})
	for _, want := range []string{"--no-install-recommends", "DPkg::Lock::Timeout", `"$(pick 'libasound2t64' 'libasound2')"`, `"$(pick 'fontconfig')"`, nativeHostSetupOK} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}
}

type hostSetupTransport struct {
	*nativeFake
	probe nativeProbe
}

func (t *hostSetupTransport) Probe(context.Context, *Instance, string) (nativeProbe, error) {
	return t.probe, nil
}

func TestNativeInstallHostProfilesVerifiesTheTarget(t *testing.T) {
	d := nativeRecord{ID: "d1", HostProfiles: []string{sdk.HostProfileChromeHeadless}}
	transport := &hostSetupTransport{nativeFake: &nativeFake{}, probe: nativeProbe{ABI: "gnu", OSID: "debian"}}
	var scripts []string
	b := &nativeBackend{transport: transport, hostRun: func(_ context.Context, _ *Instance, script string) (string, error) {
		scripts = append(scripts, script)
		if len(scripts) == 1 {
			return "E: Unable to locate package libgbm1\n", nil
		}
		if len(scripts) == 2 {
			return "", errors.New("remote operation failed")
		}
		if len(scripts) == 3 {
			transport.probe = chromeReadyProbe()
		}
		return nativeHostSetupOK + "\n", nil
	}}
	if err := b.installHostProfiles(context.Background(), &Instance{}, d); err == nil {
		t.Fatal("apt failure accepted")
	}
	if err := b.installHostProfiles(context.Background(), &Instance{}, d); err == nil {
		t.Fatal("ssh failure accepted")
	}
	if err := b.installHostProfiles(context.Background(), &Instance{}, d); err != nil {
		t.Fatal(err)
	}
	transport.probe.Fonts = false
	if err := b.installHostProfiles(context.Background(), &Instance{}, d); err == nil || !strings.Contains(err.Error(), "still missing") {
		t.Fatalf("unverified install accepted: %v", err)
	}
}

func TestNativeCapabilitiesAdvertiseHostProfiles(t *testing.T) {
	caps := nativeCapabilities().(map[string]any)
	if !reflect.DeepEqual(caps["host_profiles"], []string{sdk.HostProfileChromeHeadless}) {
		t.Fatalf("host_profiles = %v", caps["host_profiles"])
	}
}
