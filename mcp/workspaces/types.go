package main

import "encoding/json"

const (
	statusProvisioning = "provisioning"
	statusRunning      = "running"
	statusSuspended    = "suspended"
	statusFailed       = "failed"
	statusExpired      = "expired"
	statusDestroying   = "destroying"
	statusDestroyed    = "destroyed"

	activityIdle      = "idle"
	activityExecuting = "executing"
)

type Workspace struct {
	ID                string   `json:"id"`
	ProjectID         string   `json:"project_id"`
	Name              string   `json:"name"`
	Purpose           string   `json:"purpose"`
	Profile           string   `json:"profile"`
	Image             string   `json:"image"`
	WorkloadID        string   `json:"workload_id"`
	LifecycleStatus   string   `json:"lifecycle_status"`
	ActivityStatus    string   `json:"activity_status"`
	DisplayStatus     string   `json:"status"`
	RuntimeStatus     string   `json:"runtime_status"`
	HealthStatus      string   `json:"health_status"`
	HostLabel         string   `json:"host_label"`
	NetworkPolicy     string   `json:"network_policy"`
	CPU               float64  `json:"cpu"`
	MemoryMB          int      `json:"memory_mb"`
	ConsumerApp       string   `json:"consumer_app"`
	ConsumerInstallID int64    `json:"consumer_install_id,omitempty"`
	OwnerAgentID      int64    `json:"owner_agent_id,omitempty"`
	OwnerThreadID     string   `json:"owner_thread_id,omitempty"`
	OwnerLabel        string   `json:"owner_label"`
	ResourceKind      string   `json:"resource_kind,omitempty"`
	ResourceID        string   `json:"resource_id,omitempty"`
	RepoLabel         string   `json:"repo_label,omitempty"`
	BranchLabel       string   `json:"branch_label,omitempty"`
	OriginLabel       string   `json:"origin_label,omitempty"`
	OriginHref        string   `json:"origin_href,omitempty"`
	DirtyState        string   `json:"dirty_state"`
	UnpushedState     string   `json:"unpushed_state"`
	LastError         string   `json:"last_error"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
	LastActivityAt    string   `json:"last_activity_at"`
	ExpiresAt         string   `json:"expires_at"`
	DeleteAt          string   `json:"delete_at"`
	DestroyedAt       string   `json:"destroyed_at,omitempty"`
	CurrentCommand    *Command `json:"current_command,omitempty"`
	StorageBytes      int64    `json:"storage_bytes,omitempty"`
}

func (w *Workspace) deriveStatus() {
	w.DisplayStatus = w.LifecycleStatus
	if w.LifecycleStatus == statusRunning && w.ActivityStatus == activityExecuting {
		w.DisplayStatus = "busy"
	}
}

type Command struct {
	ID               string   `json:"id"`
	WorkspaceID      string   `json:"workspace_id"`
	ProjectID        string   `json:"project_id"`
	ExecutionID      string   `json:"execution_id"`
	DisplayCommand   string   `json:"display_command"`
	Argv             []string `json:"argv"`
	WorkingDirectory string   `json:"working_directory"`
	TimeoutSeconds   int      `json:"timeout_s"`
	ActorKind        string   `json:"actor_kind"`
	ActorID          string   `json:"actor_id"`
	ActorLabel       string   `json:"actor_label"`
	Status           string   `json:"status"`
	ExitCode         *int     `json:"exit_code,omitempty"`
	ErrorCode        string   `json:"error_code,omitempty"`
	Error            string   `json:"error,omitempty"`
	OutputBytes      int      `json:"output_bytes"`
	OutputTruncated  bool     `json:"output_truncated"`
	CreatedAt        string   `json:"created_at"`
	StartedAt        string   `json:"started_at,omitempty"`
	FinishedAt       string   `json:"finished_at,omitempty"`
	UpdatedAt        string   `json:"updated_at"`
}

type Activity struct {
	ID          int64           `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	ProjectID   string          `json:"project_id"`
	EventType   string          `json:"event_type"`
	ActorKind   string          `json:"actor_kind"`
	ActorID     string          `json:"actor_id"`
	ActorLabel  string          `json:"actor_label"`
	Summary     string          `json:"summary"`
	Data        json.RawMessage `json:"data"`
	CreatedAt   string          `json:"created_at"`
}

type Actor struct {
	Kind      string
	ID        string
	Label     string
	ProjectID string
	AgentID   int64
	ThreadID  string
	AppName   string
	InstallID int64
}

type ContainerWorkload struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Image            string            `json:"image"`
	Status           string            `json:"status"`
	DesiredStatus    string            `json:"desired_status"`
	PublicURL        string            `json:"public_url"`
	HealthStatus     string            `json:"health_status"`
	HostID           int64             `json:"host_id"`
	InstanceID       int64             `json:"instance_id"`
	Resources        ContainerResource `json:"resources"`
	Ports            []ContainerPort   `json:"ports"`
	Volumes          []ContainerVolume `json:"volumes"`
	CreatedAt        string            `json:"created_at"`
	UpdatedAt        string            `json:"updated_at"`
	LastError        string            `json:"last_error"`
	WorkingDirectory string            `json:"working_directory"`
}

type ContainerResource struct {
	MemoryMB int     `json:"memory_mb"`
	CPU      float64 `json:"cpu"`
}

type ContainerPort struct {
	Protocol      string `json:"protocol"`
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	BindAddr      string `json:"bind_addr"`
}

type ContainerVolume struct {
	Name             string `json:"name"`
	DockerVolumeName string `json:"docker_volume_name"`
	MountPath        string `json:"mount_path"`
	SizeBytes        int64  `json:"size_bytes"`
}

type ContainerExecution struct {
	ID               string   `json:"id"`
	WorkloadID       string   `json:"workload_id"`
	Argv             []string `json:"argv"`
	WorkingDirectory string   `json:"working_directory"`
	EnvKeys          []string `json:"env_keys"`
	TimeoutSeconds   int      `json:"timeout_s"`
	Status           string   `json:"status"`
	ExitCode         *int     `json:"exit_code,omitempty"`
	ErrorCode        string   `json:"error_code"`
	Error            string   `json:"error"`
	OutputBytes      int      `json:"output_bytes"`
	OutputTruncated  bool     `json:"output_truncated"`
	CreatedAt        string   `json:"created_at"`
	StartedAt        string   `json:"started_at"`
	FinishedAt       string   `json:"finished_at"`
	UpdatedAt        string   `json:"updated_at"`
}

type workloadResponse struct {
	Workload ContainerWorkload `json:"workload"`
}

type executionResponse struct {
	ExecutionID string             `json:"execution_id"`
	Status      string             `json:"status"`
	Execution   ContainerExecution `json:"execution"`
}

type executionLogsResponse struct {
	ExecutionID     string `json:"execution_id"`
	Status          string `json:"status"`
	Logs            string `json:"logs"`
	OutputBytes     int    `json:"output_bytes"`
	OutputTruncated bool   `json:"output_truncated"`
}

type usageMetric struct {
	FeatureKey string            `json:"feature_key"`
	Quantity   int64             `json:"quantity"`
	Unit       string            `json:"unit"`
	Kind       string            `json:"kind"`
	Source     string            `json:"source"`
	Dimensions map[string]string `json:"dimensions,omitempty"`
}

type usageResponse struct {
	Metrics []usageMetric `json:"metrics"`
}
