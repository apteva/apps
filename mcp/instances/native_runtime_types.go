package main

// Server-owned HTTP wire types. No Server implementation packages are imported.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const nativeContract = "apteva.runtime-backend/v1"
const nativePrefix = "/_apteva/runtime/backends/native"
const nativeArtifactBudget int64 = 512 << 20
const nativeMaxFile int64 = 256 << 20

type nativeAppCaps struct {
	PassiveStart       bool   `json:"passive_start"`
	Drain              bool   `json:"drain"`
	ReplicaSafe        bool   `json:"replica_safe"`
	WorkerCoordination bool   `json:"worker_coordination"`
	Routing            string `json:"routing"`
}
type nativePort struct {
	Protocol  string `json:"protocol"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Exclusive bool   `json:"exclusive"`
}
type nativeExecution struct {
	Handle           string        `json:"handle"`
	State            string        `json:"state"`
	HostKey          string        `json:"host_key"`
	ReleaseDigest    string        `json:"release_digest"`
	Platform         string        `json:"platform"`
	Endpoint         string        `json:"control_endpoint,omitempty"`
	StateRef         string        `json:"state_ref,omitempty"`
	Ports            []nativePort  `json:"ports,omitempty"`
	Ready            bool          `json:"ready"`
	StoppedConfirmed bool          `json:"stopped_confirmed"`
	AppCapabilities  nativeAppCaps `json:"app_capabilities"`
}
type nativeTarget struct {
	InstallID int64  `json:"app_install_id"`
	Type      string `json:"resource_type"`
	ID        string `json:"resource_id"`
}
type nativeRequest struct {
	Contract         string            `json:"contract"`
	OperationID      string            `json:"operation_id"`
	IdempotencyKey   string            `json:"idempotency_key"`
	DeploymentID     string            `json:"deployment_id"`
	InstallID        int64             `json:"install_id"`
	Generation       int64             `json:"generation"`
	ProjectID        string            `json:"project_id,omitempty"`
	Target           nativeTarget      `json:"target"`
	Options          map[string]any    `json:"options,omitempty"`
	Manifest         sdk.Manifest      `json:"manifest"`
	Environment      map[string]string `json:"environment,omitempty"`
	Execution        nativeExecution   `json:"execution,omitempty"`
	ArtifactPlatform string            `json:"artifact_platform,omitempty"`
	StateRef         string            `json:"state_ref,omitempty"`
	// Forward compatible with a future explicit Server gateway handoff.
	GatewayURL string `json:"gateway_url,omitempty"`
}
type nativeOptions struct {
	ArtifactID  string `json:"artifact_id"`
	GatewayURL  string `json:"gateway_url"`
	ControlPort int    `json:"control_port,omitempty"`
	MinMemoryMB int64  `json:"min_memory_mb,omitempty"`
	MinDiskMB   int64  `json:"min_disk_mb,omitempty"`
}
type nativeArtifactFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type nativeArtifact struct {
	ID              string               `json:"id"`
	AppName         string               `json:"app_name"`
	AppVersion      string               `json:"app_version"`
	Platform        string               `json:"platform"`
	Executable      string               `json:"executable"`
	Files           []nativeArtifactFile `json:"files"`
	AppCapabilities nativeAppCaps        `json:"app_capabilities"`
}
type nativeRecord struct {
	ID          string          `json:"id"`
	InstallID   int64           `json:"install_id"`
	Generation  int64           `json:"generation"`
	InstanceID  int64           `json:"instance_id"`
	Fingerprint string          `json:"fingerprint"`
	Options     nativeOptions   `json:"options"`
	Artifact    nativeArtifact  `json:"artifact"`
	ReleaseDir  string          `json:"release_dir"`
	DataDir     string          `json:"data_dir"`
	Unit        string          `json:"unit"`
	LocalPort   int             `json:"local_port"`
	ControlPort int             `json:"control_port"`
	Started     bool            `json:"started"`
	EnvDigest   string          `json:"env_digest,omitempty"`
	HealthPath  string          `json:"health_path"`
	Execution   nativeExecution `json:"execution"`
}
type nativeProbe struct {
	Platform string
	HostKey  string
	MemoryMB int64
	DiskMB   int64
}
type nativeObservation struct {
	State    string
	Prepared bool
	Stopped  bool
}

func nativeJSON(v any) string    { b, _ := json.Marshal(v); return string(b) }
func nativeHash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func nativeDigest(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && v == strings.ToLower(v)
}
func nativeID(v string) bool {
	if len(v) == 0 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return v != "." && v != ".."
}
func nativeFilePath(v string) bool {
	if v == "" || len(v) > 240 || path.IsAbs(v) || path.Clean(v) != v {
		return false
	}
	for _, part := range strings.Split(v, "/") {
		if strings.HasPrefix(part, ".") || !nativeID(part) {
			return false
		}
	}
	return true
}
func nativeGateway(v string) error {
	u, e := url.Parse(v)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("valid gateway_url required")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return fmt.Errorf("gateway must use HTTPS or a private loopback tunnel")
	}
	return nil
}
func nativeCapabilities() any {
	return map[string]any{"contract": nativeContract, "id": "native", "runtime_kinds": []string{"source", "service"}, "platforms": []string{"linux-amd64", "linux-arm64"}, "formats": []string{"binary"}, "protocols": []string{"tcp", "udp"}, "confirmed_stop": true, "snapshots": false, "replicas": false, "fencing": false, "resource_controls": false,
		"target_schema":  map[string]any{"type": "object", "required": []string{"app_install_id", "resource_type", "resource_id"}, "properties": map[string]any{"app_install_id": map[string]any{"type": "integer"}, "resource_type": map[string]any{"const": "instances.instance"}, "resource_id": map[string]any{"type": "string"}}},
		"options_schema": map[string]any{"type": "object", "required": []string{"artifact_id", "gateway_url"}, "properties": map[string]any{"artifact_id": map[string]any{"type": "string"}, "gateway_url": map[string]any{"type": "string"}, "control_port": map[string]any{"type": "integer", "minimum": 1024, "maximum": 65535}}}}
}
