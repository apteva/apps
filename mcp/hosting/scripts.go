package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
)

// Machine scripts ship inside the Hosting binary. The access script is
// uploaded for every step; the macOS reset scripts are installed once per
// machine by inspect and pinned by hash in the machine profile.
//
//go:embed scripts/macos/*.sh scripts/linux/*.sh
var scriptFS embed.FS

// resetScriptNames maps a reset step to its installed helper name.
var resetScriptNames = map[string]string{
	"wipe":         "wipe",
	"reinstall":    "reinstall",
	"verify_clean": "verify-clean",
}

const resetScriptDir = "/usr/local/libexec/apteva-hosting"

func accessScript(platform string) ([]byte, error) {
	switch platform {
	case "macos", "linux":
		return scriptFS.ReadFile("scripts/" + platform + "/customer-access.sh")
	default:
		return nil, fmt.Errorf("no access scripts for platform %q", platform)
	}
}

// resetScript returns a macOS reset helper by installed name (wipe,
// reinstall, verify-clean).
func resetScript(name string) ([]byte, error) {
	return scriptFS.ReadFile("scripts/macos/" + name + ".sh")
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// shippedResetHashes is what inspect installs and pins on macOS machines.
func shippedResetHashes() map[string]string {
	out := map[string]string{}
	for _, name := range resetScriptNames {
		if b, err := resetScript(name); err == nil {
			out[name] = sha256Hex(b)
		}
	}
	return out
}
