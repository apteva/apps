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
	"time"

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
	IngressHostKey      string                 `json:"ingress_host_key,omitempty"`
	IngressUpstream     string                 `json:"ingress_upstream,omitempty"`
	Health              *nativeExecutionHealth `json:"health,omitempty"`
	ProcessResources    map[string]string      `json:"process_resources,omitempty"`
	StateBytes          *int64                 `json:"state_bytes,omitempty"`
	StateEntries        *int64                 `json:"state_entries,omitempty"`
	StateAvailableBytes *int64                 `json:"state_available_bytes,omitempty"`
	SnapshotSHA256      string                 `json:"snapshot_sha256,omitempty"`
	BrowserEndpoint     string                 `json:"browser_endpoint,omitempty"`
	Endpoints           map[string]string      `json:"endpoints,omitempty"`
	Handle              string                 `json:"handle"`
	State               string                 `json:"state"`
	HostKey             string                 `json:"host_key"`
	ReleaseDigest       string                 `json:"release_digest"`
	Platform            string                 `json:"platform"`
	Endpoint            string                 `json:"control_endpoint,omitempty"`
	StateRef            string                 `json:"state_ref,omitempty"`
	Ports               []nativePort           `json:"ports,omitempty"`
	Ready               bool                   `json:"ready"`
	StoppedConfirmed    bool                   `json:"stopped_confirmed"`
	AppCapabilities     nativeAppCaps          `json:"app_capabilities"`
}
type nativeTarget struct {
	InstallID int64  `json:"app_install_id"`
	Type      string `json:"resource_type"`
	ID        string `json:"resource_id"`
}
type nativeRequest struct {
	Resources        *sdk.ResourcePolicy     `json:"resource_policy,omitempty"`
	StateTransfer    *nativeStatePolicy      `json:"state_transfer,omitempty"`
	TargetInfo       *sdk.RuntimeTargetInfo  `json:"target_info,omitempty"`
	Package          *nativePackageReference `json:"package,omitempty"`
	Contract         string                  `json:"contract"`
	OperationID      string                  `json:"operation_id"`
	IdempotencyKey   string                  `json:"idempotency_key"`
	DeploymentID     string                  `json:"deployment_id"`
	InstallID        int64                   `json:"install_id"`
	Generation       int64                   `json:"generation"`
	ProjectID        string                  `json:"project_id,omitempty"`
	Target           nativeTarget            `json:"target"`
	Options          map[string]any          `json:"options,omitempty"`
	Manifest         sdk.Manifest            `json:"manifest"`
	Environment      map[string]string       `json:"environment,omitempty"`
	Execution        nativeExecution         `json:"execution,omitempty"`
	ArtifactPlatform string                  `json:"artifact_platform,omitempty"`
	StateRef         string                  `json:"state_ref,omitempty"`
	// Forward compatible with a future explicit Server gateway handoff.
	GatewayURL string `json:"gateway_url,omitempty"`
}
type nativePackageReference struct {
	ID               string   `json:"id"`
	Platform         string   `json:"platform"`
	SHA256           string   `json:"sha256"`
	ExecutableSHA256 string   `json:"executable_sha256"`
	ArchiveSHA256    string   `json:"archive_sha256,omitempty"`
	ABI              string   `json:"abi,omitempty"`
	SystemLibraries  []string `json:"system_libraries,omitempty"`
}

// Public endpoints describe separately provisioned ingress; control stays tunneled.
type nativeOptions struct {
	Resources       *sdk.ResourcePolicy `json:"resource_policy,omitempty"`
	Firewall        *FirewallPolicy     `json:"firewall,omitempty"`
	Ingress         *nativeIngress      `json:"ingress,omitempty"`
	BrowserEndpoint string              `json:"browser_endpoint,omitempty"`
	Endpoints       map[string]string   `json:"endpoints,omitempty"`
	ArtifactID      string              `json:"artifact_id"`
	GatewayURL      string              `json:"gateway_url"`
	ControlPort     int                 `json:"control_port,omitempty"`
	MinMemoryMB     int64               `json:"min_memory_mb,omitempty"`
	MinDiskMB       int64               `json:"min_disk_mb,omitempty"`
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
	Snapshot     *nativeSnapshotReceipt    `json:"snapshot,omitempty"`
	Restore      *nativeRestore            `json:"restore,omitempty"`
	PublicRoutes []sdk.RuntimeIngressRoute `json:"public_routes,omitempty"`
	ID           string                    `json:"id"`
	InstallID    int64                     `json:"install_id"`
	Generation   int64                     `json:"generation"`
	InstanceID   int64                     `json:"instance_id"`
	Fingerprint  string                    `json:"fingerprint"`
	Options      nativeOptions             `json:"options"`
	Artifact     nativeArtifact            `json:"artifact"`
	ReleaseDir   string                    `json:"release_dir"`
	DataDir      string                    `json:"data_dir"`
	Unit         string                    `json:"unit"`
	LocalPort    int                       `json:"local_port"`
	ControlPort  int                       `json:"control_port"`
	Started      bool                      `json:"started"`
	EnvDigest    string                    `json:"env_digest,omitempty"`
	HealthPath   string                    `json:"health_path"`
	Execution    nativeExecution           `json:"execution"`
}
type nativeProbe struct {
	StateAvailableBytes *int64
	ABI                 string
	SystemLibraries     []string
	Platform            string
	HostKey             string
	MemoryMB            int64
	DiskMB              int64
}
type nativeObservation struct {
	ProcessResources    map[string]string `json:"process_resources,omitempty"`
	StateBytes          *int64            `json:"state_bytes,omitempty"`
	StateEntries        *int64            `json:"state_entries,omitempty"`
	StateAvailableBytes *int64            `json:"state_available_bytes,omitempty"`
	RestorePending      bool              `json:"restore_pending,omitempty"`
	SnapshotSHA256      string            `json:"snapshot_sha256,omitempty"`
	State               string            `json:"state"`
	Prepared            bool              `json:"-"`
	Stopped             bool              `json:"-"`
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
	return nativeCapabilitiesFor(defaultNativeSnapshotLimits(), 2*time.Hour)
}
func nativeCapabilitiesFor(limits nativeSnapshotLimits, timeout time.Duration) any {
	return map[string]any{"ingress_upstream": true, "state_transfer": nativeStatePolicy{Format: "tar.gz", MaxArchiveBytes: limits.ArchiveBytes, MaxExpandedBytes: limits.StateBytes, MaxEntries: nativeEntryLimit(limits), TimeoutSeconds: int64(timeout / time.Second)}, "snapshot_limits": map[string]any{"max_archive_bytes": limits.ArchiveBytes, "max_expanded_bytes": limits.StateBytes, "max_file_bytes": limits.StateBytes, "max_entries": nativeStateMaxEntries, "tar_overhead_bytes": nativeStateTarOverhead, "hard_max_bytes": nativeStateHardLimit, "disk_reserve_bytes": nativeStateReserve, "timeout_seconds": int64(timeout / time.Second)}, "snapshot_progress": map[string]any{"endpoint": nativePrefix + "/state-progress", "poll_interval_ms": 1000}, "firewall_policies": true, "firewall_capabilities_endpoint": "/api/instances-firewall-capabilities", "target_listing": true, "managed_ingress": false, "ingress_provider": map[string]any{"id": "caddy"}, "target_inspection": true, "package_delivery": map[string]any{"protocol": sdk.RuntimePackageProtocol, "formats": []string{"binary"}, "verified_archives": true}, "contract": nativeContract, "id": "native", "runtime_kinds": []string{"source", "service"}, "platforms": []string{"linux-amd64", "linux-arm64"}, "formats": []string{"binary"}, "protocols": []string{"tcp", "udp"}, "confirmed_stop": true, "snapshots": nativeSnapshotsSupported, "replicas": false, "fencing": false, "resource_controls": true, "resource_capabilities": nativeResourceCapabilities(),
		"target_schema":  map[string]any{"type": "object", "required": []string{"app_install_id", "resource_type", "resource_id"}, "properties": map[string]any{"app_install_id": map[string]any{"type": "integer"}, "resource_type": map[string]any{"const": "instances.instance"}, "resource_id": map[string]any{"type": "string"}}},
		"options_schema": map[string]any{"type": "object", "required": []string{"artifact_id", "gateway_url"}, "properties": map[string]any{"firewall": firewallPolicySchema(), "artifact_id": map[string]any{"type": "string"}, "gateway_url": map[string]any{"type": "string"}, "browser_endpoint": map[string]any{"type": "string"}, "endpoints": map[string]any{"type": "object", "maxProperties": 16, "additionalProperties": map[string]any{"type": "string"}}, "control_port": map[string]any{"type": "integer", "minimum": 1024, "maximum": 65535}}}}
}

func nativePublicEndpoints(o nativeOptions) error {
	if len(o.Endpoints) > 16 {
		return fmt.Errorf("too many public endpoints")
	}
	validate := func(raw string) error {
		u, e := url.Parse(raw)
		if e != nil || len(raw) > 2048 || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.Contains(u.Path, "..") || strings.ContainsAny(raw, "\\\x00\r\n") || (u.Scheme != "https" && u.Scheme != "wss") {
			return fmt.Errorf("public endpoints require credential-free HTTPS or WSS URLs")
		}
		return nil
	}
	if o.BrowserEndpoint != "" {
		if e := validate(o.BrowserEndpoint); e != nil {
			return e
		}
		if !strings.HasPrefix(o.BrowserEndpoint, "https://") {
			return fmt.Errorf("browser endpoint requires HTTPS")
		}
	}
	for name, raw := range o.Endpoints {
		if !nativeID(name) {
			return fmt.Errorf("invalid endpoint name")
		}
		if e := validate(raw); e != nil {
			return e
		}
	}
	return nil
}

type nativeExecutionHealth struct {
	ObservedAt        time.Time `json:"observed_at"`
	HostReachable     bool      `json:"host_reachable"`
	AdministrativeSSH bool      `json:"administrative_ssh"`
	ControlForwarding bool      `json:"control_forwarding"`
	ProcessRunning    bool      `json:"process_running"`
	// nil means unknown, never healthy by implication from SSH or process state.
	Admitting *bool          `json:"admitting"`
	Tunnel    map[string]any `json:"tunnel,omitempty"`
}
