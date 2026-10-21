package sdk

import (
	"fmt"
	"path"
	"strings"
)

// RuntimePackage declares release-owned exceptions to the standard Go/SDK
// package builder. Installation settings and credentials never belong here.
type RuntimePackage struct {
	AppCapabilities *RuntimeAppCapabilities `yaml:"app_capabilities,omitempty" json:"app_capabilities,omitempty"`
	Files           []string                `yaml:"files,omitempty" json:"files,omitempty"`
	Build           []RuntimeBuildCommand   `yaml:"build,omitempty" json:"build,omitempty"`
	CGO             bool                    `yaml:"cgo,omitempty" json:"cgo,omitempty"`
	ABI             string                  `yaml:"abi,omitempty" json:"abi,omitempty"`
	SystemLibraries []string                `yaml:"system_libraries,omitempty" json:"system_libraries,omitempty"`
}
type RuntimeBuildCommand struct {
	Dir  string   `yaml:"dir,omitempty" json:"dir,omitempty"`
	Args []string `yaml:"args" json:"args"`
}

// RuntimeAppCapabilities describes the exact release, not a previous running
// installation. Server verifies the declaration against the authenticated app.
type RuntimeAppCapabilities struct {
	PassiveStart       bool   `yaml:"passive_start" json:"passive_start"`
	Drain              bool   `yaml:"drain" json:"drain"`
	ReplicaSafe        bool   `yaml:"replica_safe" json:"replica_safe"`
	WorkerCoordination bool   `yaml:"worker_coordination" json:"worker_coordination"`
	Routing            string `yaml:"routing" json:"routing"`
}

const RuntimePackageProtocol = "apteva.runtime-package/files-v1"

// RuntimeTargetCheck is a bounded diagnostic category, not provider log text.
type RuntimeTargetCheck struct {
	Name   string `json:"name"`   // ssh, systemd, gateway, capacity
	Status string `json:"status"` // passed, blocked, unknown
}

// RuntimeTargetInfo is returned by an artifact-independent inspect-target.
// HostKey identifies the physical reservation domain, never a credential.
type RuntimeTargetInfo struct {
	Checks          []RuntimeTargetCheck `json:"checks,omitempty"`
	Platform        string               `json:"platform"`
	HostKey         string               `json:"host_key"`
	ABI             string               `json:"abi,omitempty"`
	SystemLibraries []string             `json:"system_libraries,omitempty"`
	Formats         []string             `json:"formats"`
}

func runtimeReleasePath(p string) bool {
	if p == "" || len(p) > 240 || path.IsAbs(p) || path.Clean(p) != p || strings.Contains(p, "\\") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return false
			}
		}
	}
	return true
}
func ValidateRuntimePackage(p *RuntimePackage) error {
	if p == nil {
		return nil
	}
	if p.ABI != "" && p.ABI != "gnu" && p.ABI != "musl" {
		return fmt.Errorf("runtime.package: unsupported ABI")
	}
	if len(p.Files) > 128 || len(p.Build) > 16 || len(p.SystemLibraries) > 32 {
		return fmt.Errorf("runtime.package exceeds limits")
	}
	for _, f := range p.Files {
		if !runtimeReleasePath(f) {
			return fmt.Errorf("runtime.package: unsafe file path")
		}
	}
	for _, f := range p.SystemLibraries {
		if !runtimeReleasePath(f) || strings.Contains(f, "/") {
			return fmt.Errorf("runtime.package: invalid library name")
		}
	}
	for _, step := range p.Build {
		if step.Dir != "" && step.Dir != "." && !runtimeReleasePath(step.Dir) || len(step.Args) == 0 || len(step.Args) > 32 {
			return fmt.Errorf("runtime.package: invalid build command")
		}
		if step.Args[0] != "bun" && step.Args[0] != "go" {
			return fmt.Errorf("runtime.package: unsupported build tool")
		}
		for _, arg := range step.Args {
			if len(arg) > 1024 || strings.ContainsRune(arg, 0) {
				return fmt.Errorf("runtime.package: invalid build argument")
			}
		}
	}
	if c := p.AppCapabilities; c != nil && c.Routing != "" && c.Routing != "key" && c.Routing != "stateless" {
		return fmt.Errorf("runtime.package: invalid routing capability")
	}
	return nil
}
