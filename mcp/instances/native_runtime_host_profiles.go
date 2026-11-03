package main

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Host profiles are OS package sets the native backend installs on a target
// before preparing a release that declares them (runtime.package.host_profiles),
// and only when Server forwards the placement's approval. Packages are never
// removed afterwards: the host is shared by every deployment on it.
type nativeHostProfile struct {
	Libraries []string   // sonames that must be in the target's ldconfig cache
	Fonts     bool       // at least one font file must be installed
	Packages  [][]string // Debian/Ubuntu packages; the first available alternative wins
}

var nativeHostProfiles = map[string]nativeHostProfile{
	sdk.HostProfileChromeHeadless: {
		Libraries: []string{"libX11.so.6", "libXcomposite.so.1", "libXdamage.so.1", "libXext.so.6", "libXfixes.so.3", "libXrandr.so.2", "libasound.so.2", "libatk-1.0.so.0", "libatk-bridge-2.0.so.0", "libatspi.so.0", "libdbus-1.so.3", "libdrm.so.2", "libexpat.so.1", "libgbm.so.1", "libglib-2.0.so.0", "libnspr4.so", "libnss3.so", "libnssutil3.so", "libxcb.so.1", "libxkbcommon.so.0"},
		Fonts:     true,
		Packages: [][]string{
			{"libasound2t64", "libasound2"}, {"libatk1.0-0t64", "libatk1.0-0"}, {"libatk-bridge2.0-0t64", "libatk-bridge2.0-0"}, {"libatspi2.0-0t64", "libatspi2.0-0"}, {"libglib2.0-0t64", "libglib2.0-0"},
			{"libdbus-1-3"}, {"libdrm2"}, {"libexpat1"}, {"libgbm1"}, {"libnspr4"}, {"libnss3"}, {"libx11-6"}, {"libxcb1"}, {"libxcomposite1"}, {"libxdamage1"}, {"libxext6"}, {"libxfixes3"}, {"libxkbcommon0"}, {"libxrandr2"},
			{"fontconfig"}, {"fonts-dejavu-core"}, {"fonts-liberation2", "fonts-liberation"},
		},
	},
}

func nativeHostProfileNames() []string {
	names := make([]string, 0, len(nativeHostProfiles))
	for name := range nativeHostProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// apt is the only installer so far; other distributions report unsupported.
func (p nativeProbe) aptHost() bool {
	if p.ABI != "gnu" {
		return false
	}
	if p.OSID == "debian" || p.OSID == "ubuntu" {
		return true
	}
	for _, like := range strings.Fields(p.OSLike) {
		if like == "debian" || like == "ubuntu" {
			return true
		}
	}
	return false
}
func nativeHostProfileSatisfied(profile nativeHostProfile, p nativeProbe) bool {
	if profile.Fonts && !p.Fonts {
		return false
	}
	for _, lib := range profile.Libraries {
		if !slices.Contains(p.SystemLibraries, lib) {
			return false
		}
	}
	return true
}
func nativeHostProfileStates(p nativeProbe) map[string]string {
	states := make(map[string]string, len(nativeHostProfiles))
	for name, profile := range nativeHostProfiles {
		switch {
		case nativeHostProfileSatisfied(profile, p):
			states[name] = sdk.HostProfilePresent
		case p.aptHost():
			states[name] = sdk.HostProfileInstallable
		default:
			states[name] = sdk.HostProfileUnsupported
		}
	}
	return states
}

// nativeHostProfilePlan returns the declared profiles to install before
// prepare, refusing any that are unknown, uninstallable or not approved.
func nativeHostProfilePlan(r nativeRequest, p nativeProbe) ([]string, error) {
	if r.Manifest.Runtime.Package == nil {
		return nil, nil
	}
	states := nativeHostProfileStates(p)
	var install []string
	for _, name := range r.Manifest.Runtime.Package.HostProfiles {
		switch states[name] {
		case sdk.HostProfilePresent:
		case sdk.HostProfileInstallable:
			if !slices.Contains(r.ApprovedHostProfiles, name) {
				return nil, fmt.Errorf("host profile %s is missing on this target and its installation was not approved", name)
			}
			install = append(install, name)
		case sdk.HostProfileUnsupported:
			return nil, fmt.Errorf("host profile %s cannot be installed on this target", name)
		default:
			return nil, fmt.Errorf("host profile %s is not provided by this backend", name)
		}
	}
	return install, nil
}

const nativeHostSetupOK = "APTEVA_HOST_SETUP_OK"

// The script always exits 0 so the caller sees apt's tail on failure.
func nativeHostProfileScript(names []string) string {
	var pkgs []string
	for _, name := range names {
		for _, alternatives := range nativeHostProfiles[name].Packages {
			quoted := make([]string, len(alternatives))
			for i, pkg := range alternatives {
				quoted[i] = quoteShellArg(pkg)
			}
			pkgs = append(pkgs, `"$(pick `+strings.Join(quoted, " ")+`)"`)
		}
	}
	return `set -u
 export DEBIAN_FRONTEND=noninteractive
 log=/var/log/apteva-host-setup.log
 pick() { for p in "$@"; do if apt-cache show "$p" >/dev/null 2>&1; then printf '%s' "$p"; return 0; fi; done; printf '%s' "$1"; }
 if { apt-get -o DPkg::Lock::Timeout=300 update -qq && apt-get -o DPkg::Lock::Timeout=300 install -y -qq --no-install-recommends ` + strings.Join(pkgs, " ") + `; } >"$log" 2>&1; then printf '%s\n' ` + nativeHostSetupOK + `; else tail -n 20 "$log"; fi
`
}

// installHostProfiles installs a record's approved profiles, then re-probes
// the target and fails unless every one is satisfied.
func (b *nativeBackend) installHostProfiles(c context.Context, inst *Instance, d nativeRecord) error {
	run := b.hostRun
	if run == nil {
		run = nativeRun
	}
	out, err := run(c, inst, nativeHostProfileScript(d.HostProfiles))
	if err != nil || !strings.Contains(out, nativeHostSetupOK) {
		if b.appCtx != nil {
			b.appCtx.Logger().Warn("native host setup failed", "deployment", d.ID, "profiles", d.HostProfiles, "output", strings.TrimSpace(out))
		}
		return fmt.Errorf("host setup %s failed", strings.Join(d.HostProfiles, ", "))
	}
	p, err := b.transport.Probe(c, inst, d.Options.GatewayURL)
	if err != nil {
		return fmt.Errorf("host setup could not be verified: %w", err)
	}
	for _, name := range d.HostProfiles {
		if !nativeHostProfileSatisfied(nativeHostProfiles[name], p) {
			return fmt.Errorf("host profile %s is still missing after installation", name)
		}
	}
	return nil
}
