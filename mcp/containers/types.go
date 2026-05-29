package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
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
}

type ResourceSpec struct {
	MemoryMB int     `json:"memory_mb,omitempty"`
	CPU      float64 `json:"cpu,omitempty"`
}

type RunSpec struct {
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	BlueprintSlug string            `json:"blueprint_slug,omitempty"`
	HostID        int64             `json:"host_id,omitempty"`
	InstanceID    int64             `json:"instance_id,omitempty"`
	Ports         []PortSpec        `json:"ports,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Volumes       []VolumeSpec      `json:"volumes,omitempty"`
	HealthPath    string            `json:"health_path,omitempty"`
	Resources     ResourceSpec      `json:"resources,omitempty"`
	RestartPolicy string            `json:"restart_policy,omitempty"`
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
	if in.HostID != 0 || in.InstanceID != 0 {
		return in, errors.New("remote hosts are not implemented in Containers v0.1; use host_id=0")
	}
	if in.RestartPolicy == "" {
		in.RestartPolicy = "unless-stopped"
	}
	switch in.RestartPolicy {
	case "no", "on-failure", "always", "unless-stopped":
	default:
		return in, fmt.Errorf("unsupported restart_policy %q", in.RestartPolicy)
	}
	if in.HealthPath == "" {
		in.HealthPath = "/"
	}
	if !strings.HasPrefix(in.HealthPath, "/") {
		in.HealthPath = "/" + in.HealthPath
	}
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
			p.BindAddr = "127.0.0.1"
		}
	}
	for i := range in.Volumes {
		v := &in.Volumes[i]
		v.Name = strings.ToLower(strings.TrimSpace(v.Name))
		v.MountPath = strings.TrimSpace(v.MountPath)
		if v.Name == "" {
			return in, fmt.Errorf("volumes[%d].name required", i)
		}
		if !workloadNameRE.MatchString(v.Name) {
			return in, fmt.Errorf("volumes[%d].name must use lowercase letters, numbers, dashes", i)
		}
		if !strings.HasPrefix(v.MountPath, "/") {
			return in, fmt.Errorf("volumes[%d].mount_path must be absolute", i)
		}
	}
	return in, nil
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
