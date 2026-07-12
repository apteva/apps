package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxInjectedFiles      = 32
	maxInjectedFileBytes  = 1 << 20
	maxInjectedTotalBytes = 4 << 20
	maxRunRequestBytes    = 6 << 20
	maxEnvironmentBytes   = 1 << 20
	redactedValue         = "<redacted>"
)

const (
	StatusCreating  = "creating"
	StatusRunning   = "running"
	StatusStopped   = "stopped"
	StatusUnhealthy = "unhealthy"
	StatusError     = "error"
	StatusDestroyed = "destroyed"
)

type Workload struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	BlueprintSlug string            `json:"blueprint_slug"`
	HostID        int64             `json:"host_id"`
	InstanceID    int64             `json:"instance_id"`
	Kind          string            `json:"kind"`
	Image         string            `json:"image"`
	Status        string            `json:"status"`
	DesiredStatus string            `json:"desired_status"`
	ContainerID   string            `json:"container_id"`
	ContainerName string            `json:"container_name"`
	NetworkName   string            `json:"network_name"`
	PublicURL     string            `json:"public_url"`
	HealthStatus  string            `json:"health_status"`
	HealthPath    string            `json:"health_path"`
	HealthURL     string            `json:"health_url"`
	ConfigJSON    string            `json:"config_json"`
	Env           map[string]string `json:"env"`
	EnvJSON       string            `json:"-"`
	Resources     ResourceSpec      `json:"resources"`
	ResourcesJSON string            `json:"-"`
	RestartPolicy string            `json:"restart_policy"`
	LastHealthAt  string            `json:"last_health_at,omitempty"`
	LastError     string            `json:"last_error"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
	Ports         []PortSpec        `json:"ports,omitempty"`
	Volumes       []VolumeSpec      `json:"volumes,omitempty"`
}

type PortSpec struct {
	Protocol      string `json:"protocol"`
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	BindAddr      string `json:"bind_addr"`
}

type VolumeSpec struct {
	Name             string `json:"name"`
	DockerVolumeName string `json:"docker_volume_name,omitempty"`
	MountPath        string `json:"mount_path"`
	SizeBytes        int64  `json:"size_bytes,omitempty"`
}

type FileSpec struct {
	Path          string `json:"path"`
	Content       string `json:"content,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	Mode          string `json:"mode,omitempty"`
	Secret        bool   `json:"secret,omitempty"`
}

type ResourceSpec struct {
	MemoryMB int     `json:"memory_mb,omitempty"`
	CPU      float64 `json:"cpu,omitempty"`
}

type UsageMetric struct {
	FeatureKey string            `json:"feature_key"`
	Quantity   int64             `json:"quantity"`
	Unit       string            `json:"unit"`
	Kind       string            `json:"kind"`
	Source     string            `json:"source"`
	Dimensions map[string]string `json:"dimensions,omitempty"`
}

type WorkloadUsage struct {
	WorkloadID string        `json:"workload_id"`
	Metrics    []UsageMetric `json:"metrics"`
	UpdatedAt  string        `json:"updated_at"`
}

type RunSpec struct {
	Name              string            `json:"name"`
	Image             string            `json:"image"`
	BlueprintSlug     string            `json:"blueprint_slug,omitempty"`
	HostID            int64             `json:"host_id,omitempty"`
	InstanceID        int64             `json:"instance_id,omitempty"`
	UseLocal          bool              `json:"use_local,omitempty"`
	Ports             []PortSpec        `json:"ports,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Volumes           []VolumeSpec      `json:"volumes,omitempty"`
	Files             []FileSpec        `json:"files,omitempty"`
	PullPolicy        string            `json:"pull_policy,omitempty"`
	HealthPath        string            `json:"health_path,omitempty"`
	Resources         ResourceSpec      `json:"resources,omitempty"`
	RestartPolicy     string            `json:"restart_policy,omitempty"`
	runtimeWorkloadID string            `json:"-"`
}

type Blueprint struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Spec        RunSpec `json:"spec"`
}

var workloadNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func normalizeRunSpec(in RunSpec) (RunSpec, error) {
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	in.Image = strings.TrimSpace(in.Image)
	if !workloadNameRE.MatchString(in.Name) {
		return in, errors.New("name must be 1-63 chars: lowercase letters, numbers, dashes, starting with a letter/number")
	}
	if in.Image == "" {
		return in, errors.New("image is required")
	}
	if len(in.Image) > 512 {
		return in, errors.New("image must be at most 512 characters")
	}
	if in.Resources.MemoryMB < 0 || in.Resources.CPU < 0 {
		return in, errors.New("resources.memory_mb and resources.cpu must be >= 0")
	}
	if len(in.Ports) > 64 || len(in.Volumes) > 32 || len(in.Env) > 256 {
		return in, errors.New("run spec exceeds the maximum of 64 ports, 32 volumes, or 256 environment variables")
	}
	envBytes := 0
	for key, value := range in.Env {
		if !validEnvKey(key) {
			return in, fmt.Errorf("invalid env key %q", key)
		}
		envBytes += len(key) + len(value)
		if envBytes > maxEnvironmentBytes {
			return in, fmt.Errorf("environment exceeds %d bytes total", maxEnvironmentBytes)
		}
	}
	if in.HostID < 0 || in.InstanceID < 0 {
		return in, errors.New("host_id and instance_id must be >= 0")
	}
	if in.UseLocal && (in.HostID != 0 || in.InstanceID != 0) {
		return in, errors.New("use_local cannot be combined with host_id or instance_id")
	}
	targetID := in.InstanceID
	if targetID == 0 {
		targetID = in.HostID
	}
	if in.HostID != 0 && in.InstanceID != 0 && in.HostID != in.InstanceID {
		return in, errors.New("host_id and instance_id must match when both are provided")
	}
	if targetID != 0 {
		in.HostID = targetID
		in.InstanceID = targetID
	}
	if in.RestartPolicy == "" {
		in.RestartPolicy = "unless-stopped"
	}
	switch in.RestartPolicy {
	case "no", "on-failure", "always", "unless-stopped":
	default:
		return in, fmt.Errorf("unsupported restart_policy %q", in.RestartPolicy)
	}
	in.PullPolicy = strings.ToLower(strings.TrimSpace(in.PullPolicy))
	if in.PullPolicy == "" {
		in.PullPolicy = "missing"
	}
	switch in.PullPolicy {
	case "missing", "always", "never":
	default:
		return in, fmt.Errorf("unsupported pull_policy %q", in.PullPolicy)
	}
	if in.HealthPath == "" {
		in.HealthPath = "/"
	}
	if len(in.HealthPath) > 2048 {
		return in, errors.New("health_path must be at most 2048 characters")
	}
	if !strings.HasPrefix(in.HealthPath, "/") {
		in.HealthPath = "/" + in.HealthPath
	}
	seenPorts := map[string]struct{}{}
	for i := range in.Ports {
		p := &in.Ports[i]
		if p.Protocol == "" {
			p.Protocol = "tcp"
		}
		if p.Protocol != "tcp" && p.Protocol != "udp" {
			return in, fmt.Errorf("ports[%d].protocol must be tcp or udp", i)
		}
		if p.ContainerPort <= 0 || p.ContainerPort > 65535 {
			return in, fmt.Errorf("ports[%d].container_port invalid", i)
		}
		if p.HostPort < 0 || p.HostPort > 65535 {
			return in, fmt.Errorf("ports[%d].host_port invalid", i)
		}
		if p.BindAddr == "" {
			if targetID != 0 {
				p.BindAddr = "0.0.0.0"
			} else {
				p.BindAddr = "127.0.0.1"
			}
		}
		bindIP := net.ParseIP(strings.Trim(p.BindAddr, "[]"))
		if bindIP == nil {
			return in, fmt.Errorf("ports[%d].bind_addr must be an IP address", i)
		}
		if bindIP.To4() == nil {
			p.BindAddr = "[" + strings.Trim(p.BindAddr, "[]") + "]"
		}
		portKey := fmt.Sprintf("%d/%s", p.ContainerPort, p.Protocol)
		if _, exists := seenPorts[portKey]; exists {
			return in, fmt.Errorf("ports[%d] duplicates container port %s", i, portKey)
		}
		seenPorts[portKey] = struct{}{}
	}
	seenVolumeNames := map[string]struct{}{}
	seenMountPaths := map[string]struct{}{}
	for i := range in.Volumes {
		v := &in.Volumes[i]
		v.Name = strings.ToLower(strings.TrimSpace(v.Name))
		v.MountPath = path.Clean(strings.TrimSpace(v.MountPath))
		if v.Name == "" {
			return in, fmt.Errorf("volumes[%d].name required", i)
		}
		if !workloadNameRE.MatchString(v.Name) {
			return in, fmt.Errorf("volumes[%d].name must use lowercase letters, numbers, dashes", i)
		}
		if !strings.HasPrefix(v.MountPath, "/") {
			return in, fmt.Errorf("volumes[%d].mount_path must be absolute", i)
		}
		if _, exists := seenVolumeNames[v.Name]; exists {
			return in, fmt.Errorf("volumes[%d].name duplicates %q", i, v.Name)
		}
		if _, exists := seenMountPaths[v.MountPath]; exists {
			return in, fmt.Errorf("volumes[%d].mount_path duplicates %q", i, v.MountPath)
		}
		seenVolumeNames[v.Name] = struct{}{}
		seenMountPaths[v.MountPath] = struct{}{}
	}
	if len(in.Files) > maxInjectedFiles {
		return in, fmt.Errorf("files supports at most %d entries", maxInjectedFiles)
	}
	seenFilePaths := map[string]struct{}{}
	totalFileBytes := 0
	for i := range in.Files {
		size, err := normalizeFileSpec(&in.Files[i], i, in.Volumes)
		if err != nil {
			return in, err
		}
		if _, exists := seenFilePaths[in.Files[i].Path]; exists {
			return in, fmt.Errorf("files[%d].path duplicates %q", i, in.Files[i].Path)
		}
		seenFilePaths[in.Files[i].Path] = struct{}{}
		totalFileBytes += size
		if totalFileBytes > maxInjectedTotalBytes {
			return in, fmt.Errorf("files content exceeds %d bytes total", maxInjectedTotalBytes)
		}
	}
	return in, nil
}

func normalizeFileSpec(f *FileSpec, i int, volumes []VolumeSpec) (int, error) {
	f.Path = path.Clean(strings.TrimSpace(f.Path))
	if f.Path == "." || f.Path == "" {
		return 0, fmt.Errorf("files[%d].path required", i)
	}
	if !strings.HasPrefix(f.Path, "/") {
		return 0, fmt.Errorf("files[%d].path must be absolute", i)
	}
	if f.Path == "/" {
		return 0, fmt.Errorf("files[%d].path must target a file", i)
	}
	if f.Content != "" && f.ContentBase64 != "" {
		return 0, fmt.Errorf("files[%d] must set only one of content or content_base64", i)
	}
	if f.Content == "" && f.ContentBase64 == "" {
		return 0, fmt.Errorf("files[%d] content or content_base64 required", i)
	}
	contentSize := len(f.Content)
	if f.ContentBase64 != "" {
		if len(f.ContentBase64) > base64.StdEncoding.EncodedLen(maxInjectedFileBytes) {
			return 0, fmt.Errorf("files[%d] content exceeds %d bytes", i, maxInjectedFileBytes)
		}
		decoded, err := base64.StdEncoding.DecodeString(f.ContentBase64)
		if err != nil {
			return 0, fmt.Errorf("files[%d].content_base64 invalid: %w", i, err)
		}
		contentSize = len(decoded)
	}
	if contentSize > maxInjectedFileBytes {
		return 0, fmt.Errorf("files[%d] content exceeds %d bytes", i, maxInjectedFileBytes)
	}
	mode := strings.TrimSpace(f.Mode)
	if mode == "" {
		mode = "0600"
	}
	n, err := strconv.ParseUint(mode, 8, 32)
	if err != nil || n > 0o777 {
		return 0, fmt.Errorf("files[%d].mode must be an octal permission from 0000 to 0777", i)
	}
	f.Mode = fmt.Sprintf("%04o", n)
	if _, _, err := resolveVolumeFileTarget(volumes, f.Path); err != nil {
		return 0, fmt.Errorf("files[%d].path: %w", i, err)
	}
	return contentSize, nil
}

type VolumeFileWrite struct {
	Path       string
	VolumeName string
	RelPath    string
	Content    []byte
	Mode       string
	Secret     bool
}

func resolveFileWrites(spec RunSpec) ([]VolumeFileWrite, error) {
	if len(spec.Files) == 0 {
		return nil, nil
	}
	out := make([]VolumeFileWrite, 0, len(spec.Files))
	for i, f := range spec.Files {
		volume, relPath, err := resolveVolumeFileTarget(spec.Volumes, f.Path)
		if err != nil {
			return nil, fmt.Errorf("files[%d].path: %w", i, err)
		}
		if strings.TrimSpace(volume.DockerVolumeName) == "" {
			return nil, fmt.Errorf("files[%d].path: target volume %q has no docker volume name", i, volume.Name)
		}
		content, err := fileContentBytes(f)
		if err != nil {
			return nil, fmt.Errorf("files[%d]: %w", i, err)
		}
		out = append(out, VolumeFileWrite{
			Path:       f.Path,
			VolumeName: volume.DockerVolumeName,
			RelPath:    relPath,
			Content:    content,
			Mode:       f.Mode,
			Secret:     f.Secret,
		})
	}
	return out, nil
}

func resolveVolumeFileTarget(volumes []VolumeSpec, filePath string) (VolumeSpec, string, error) {
	cleanPath := path.Clean(filePath)
	var best *VolumeSpec
	bestLen := -1
	for i := range volumes {
		mount := path.Clean(strings.TrimSpace(volumes[i].MountPath))
		if mount == "." || mount == "" || !strings.HasPrefix(mount, "/") {
			continue
		}
		matches := false
		switch {
		case mount == "/":
			matches = strings.HasPrefix(cleanPath, "/")
		case cleanPath == mount:
			return VolumeSpec{}, "", errors.New("must target a file below a declared volume mount")
		case strings.HasPrefix(cleanPath, mount+"/"):
			matches = true
		}
		if matches && len(mount) > bestLen {
			best = &volumes[i]
			bestLen = len(mount)
		}
	}
	if best == nil {
		return VolumeSpec{}, "", errors.New("must be under a declared volume mount")
	}
	mount := path.Clean(best.MountPath)
	rel := ""
	if mount == "/" {
		rel = strings.TrimPrefix(cleanPath, "/")
	} else {
		rel = strings.TrimPrefix(cleanPath, mount+"/")
	}
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if rel == "" || rel == "." {
		return VolumeSpec{}, "", errors.New("must target a file below a declared volume mount")
	}
	return *best, rel, nil
}

func fileContentBytes(f FileSpec) ([]byte, error) {
	if f.ContentBase64 != "" {
		return base64.StdEncoding.DecodeString(f.ContentBase64)
	}
	return []byte(f.Content), nil
}

func sanitizeRunSpecForStorage(spec RunSpec) RunSpec {
	out := spec
	out.Env = redactEnvironment(out.Env)
	out.Files = append([]FileSpec(nil), out.Files...)
	for i := range out.Files {
		out.Files[i].Content = ""
		out.Files[i].ContentBase64 = ""
	}
	return out
}

func redactEnvironment(env map[string]string) map[string]string {
	if len(env) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(env))
	for key := range env {
		out[key] = redactedValue
	}
	return out
}

func newWorkloadID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "wrk_" + strconv.FormatInt(nowUnixNano(), 16)
	}
	return "wrk_" + hex.EncodeToString(b[:])
}

func dockerSafeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "workload"
	}
	if len(out) > 54 {
		out = out[:54]
	}
	return out
}

func encodeJSON(v any) string {
	b, _ := json.Marshal(v)
	if len(b) == 0 {
		return "{}"
	}
	return string(b)
}

func decodeMap(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return map[string]string{}
	}
	return out
}

func decodeResources(raw string) ResourceSpec {
	var out ResourceSpec
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}
