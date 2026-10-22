package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
)

type nativeTargetInfo struct {
	sdk.RuntimeTargetInfo
	StateAvailableBytes *int64 `json:"state_available_bytes,omitempty"`
}
type nativeProbePathKey struct{}

func (b *nativeBackend) inspectTarget(r nativeRequest) (nativeTargetInfo, error) {
	var out nativeTargetInfo
	inst, err := b.target(r)
	if err != nil {
		return out, err
	}
	var opts nativeOptions
	if json.Unmarshal([]byte(nativeJSON(r.Options)), &opts) != nil {
		return out, errors.New("invalid target options")
	}
	if opts.ControlPort != 0 && (opts.ControlPort < 1024 || opts.ControlPort > 65535) || opts.MinMemoryMB < 0 || opts.MinDiskMB < 0 {
		return out, errors.New("invalid target options")
	}
	if err := nativePublicEndpoints(opts); err != nil {
		return out, err
	}
	gateway := r.GatewayURL
	if gateway == "" {
		gateway = opts.GatewayURL
	}
	if opts.GatewayURL != "" && opts.GatewayURL != gateway {
		return out, errors.New("gateway handoff conflict")
	}
	if err = nativeGateway(gateway); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(b.ctx, 15*time.Second)
	defer cancel()
	p, err := b.transport.Probe(context.WithValue(ctx, nativeProbePathKey{}, fmt.Sprintf("/var/lib/apteva-native/%d", b.installID)), inst, gateway)
	if err != nil {
		return out, errors.New("target SSH/systemd/gateway validation failed")
	}
	if p.HostKey == "" || p.Platform != "linux-amd64" && p.Platform != "linux-arm64" {
		return out, errors.New("unsupported target identity/platform")
	}
	return nativeTargetInfo{RuntimeTargetInfo: sdk.RuntimeTargetInfo{Platform: p.Platform, HostKey: p.HostKey, ABI: p.ABI, SystemLibraries: p.SystemLibraries, Formats: []string{"binary"}}, StateAvailableBytes: p.StateAvailableBytes}, nil
}
func (b *nativeBackend) verifyTargetPackage(r nativeRequest, a nativeArtifact, p nativeProbe) error {
	if metadata := r.Manifest.Runtime.Package; metadata != nil && metadata.AppCapabilities != nil && nativeJSON(metadata.AppCapabilities) != nativeJSON(a.AppCapabilities) {
		return errors.New("artifact lifecycle declaration differs from approved release")
	}
	ref := r.Package
	if ref == nil {
		return nil
	} // Existing manually staged packages remain supported.
	normalized := a
	normalized.ID = ""
	if ref.ID != a.ID || ref.Platform != a.Platform || ref.ExecutableSHA256 != nativeArtifactDigest(a) || !nativeDigest(ref.SHA256) || ref.SHA256 != nativeHash(nativeJSON(normalized)) {
		return errors.New("package reference does not match sealed artifact")
	}
	var descriptor struct {
		ABI       string   `json:"abi"`
		Libraries []string `json:"system_libraries"`
	}
	if ref.ABI != "" {
		listed := false
		for _, file := range a.Files {
			if file.Path == "apteva-runtime-libraries.json" {
				listed = true
				if file.Size > 16384 {
					return errors.New("library descriptor exceeds limit")
				}
				break
			}
		}
		if !listed {
			return errors.New("sealed package lacks runtime library descriptor")
		}
		raw, err := os.ReadFile(filepath.Join(b.artifactDir(a.ID), "apteva-runtime-libraries.json"))
		if err != nil || json.Unmarshal(raw, &descriptor) != nil || descriptor.ABI != ref.ABI || !reflect.DeepEqual(descriptor.Libraries, ref.SystemLibraries) {
			return errors.New("library requirements differ from sealed package")
		}
		if ref.ABI != p.ABI {
			return errors.New("package ABI does not match target")
		}
	}
	for _, lib := range ref.SystemLibraries {
		if !nativeLibraryName(lib) || !slices.Contains(p.SystemLibraries, lib) {
			return errors.New("required package library is absent from target")
		}
	}
	return nil
}

func nativeLibraryName(lib string) bool {
	if lib == "" || len(lib) > 240 || strings.HasPrefix(lib, ".") {
		return false
	}
	for _, c := range lib {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_' || c == '+') {
			return false
		}
	}
	return true
}
