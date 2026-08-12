package main

import (
	"encoding/json"
	"time"
)

const (
	designSchema  = "apteva-design/v1"
	engineName    = "replicad-opencascade"
	engineVersion = "replicad-0.23.1-ocjs-0.23.0"
)

type ParameterSpec struct {
	Type        string   `json:"type"`
	Default     float64  `json:"default"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Description string   `json:"description,omitempty"`
}

type DesignDefinition struct {
	Schema     string                   `json:"schema"`
	Units      string                   `json:"units"`
	Parameters map[string]ParameterSpec `json:"parameters,omitempty"`
	Operations []map[string]any         `json:"operations"`
	Output     string                   `json:"output"`
	Checks     []map[string]any         `json:"checks,omitempty"`
}

type Design struct {
	ID                int64           `json:"id"`
	ProjectID         string          `json:"project_id,omitempty"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Kind              string          `json:"kind"`
	Status            string          `json:"status"`
	Tags              json.RawMessage `json:"tags"`
	CurrentRevisionID int64           `json:"current_revision_id"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
	CurrentRevision   *Revision       `json:"current_revision,omitempty"`
	LatestBuild       *BuildRun       `json:"latest_build,omitempty"`
	Artifacts         []Artifact      `json:"artifacts,omitempty"`
}

type Revision struct {
	ID               int64           `json:"id"`
	DesignID         int64           `json:"design_id"`
	ParentRevisionID *int64          `json:"parent_revision_id,omitempty"`
	RevisionNumber   int             `json:"revision_number"`
	Definition       json.RawMessage `json:"definition"`
	Parameters       json.RawMessage `json:"parameters"`
	SourceSHA256     string          `json:"source_sha256"`
	Note             string          `json:"note"`
	Author           string          `json:"author"`
	CreatedAt        string          `json:"created_at"`
}

type BuildRun struct {
	ID            int64           `json:"id"`
	DesignID      int64           `json:"design_id"`
	RevisionID    int64           `json:"revision_id"`
	Status        string          `json:"status"`
	Engine        string          `json:"engine"`
	EngineVersion string          `json:"engine_version"`
	Report        json.RawMessage `json:"report"`
	Checks        json.RawMessage `json:"checks"`
	ErrorText     string          `json:"error,omitempty"`
	DurationMS    int64           `json:"duration_ms"`
	CreatedAt     string          `json:"created_at"`
	CompletedAt   string          `json:"completed_at,omitempty"`
}

type Artifact struct {
	ID            int64           `json:"id"`
	DesignID      int64           `json:"design_id"`
	RevisionID    int64           `json:"revision_id"`
	BuildRunID    *int64          `json:"build_run_id,omitempty"`
	Kind          string          `json:"kind"`
	Format        string          `json:"format"`
	Name          string          `json:"name"`
	ContentType   string          `json:"content_type"`
	SHA256        string          `json:"sha256"`
	SizeBytes     int64           `json:"size_bytes"`
	StorageFileID *int64          `json:"storage_file_id,omitempty"`
	LocalPath     string          `json:"-"`
	Metadata      json.RawMessage `json:"metadata"`
	CreatedAt     string          `json:"created_at"`
	DownloadURL   string          `json:"download_url,omitempty"`
}

type GeometryReport struct {
	Valid          bool    `json:"valid"`
	Representation string  `json:"representation"`
	Units          string  `json:"units"`
	Bounds         Bounds  `json:"bounds"`
	VolumeMM3      float64 `json:"volume_mm3"`
	SurfaceAreaMM2 float64 `json:"surface_area_mm2"`
	BodyCount      int     `json:"body_count"`
	FaceCount      int     `json:"face_count"`
	EdgeCount      int     `json:"edge_count"`
	VertexCount    int     `json:"vertex_count"`
	TriangleCount  int     `json:"triangle_count"`
}

type Bounds struct {
	Min    [3]float64 `json:"min"`
	Max    [3]float64 `json:"max"`
	Size   [3]float64 `json:"size"`
	Center [3]float64 `json:"center"`
}

type CheckResult struct {
	Type     string `json:"type"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Expected any    `json:"expected,omitempty"`
	Actual   any    `json:"actual,omitempty"`
}

type EngineArtifact struct {
	Format      string `json:"format"`
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
}

type EngineResult struct {
	OK         bool               `json:"ok"`
	Error      string             `json:"error,omitempty"`
	Details    any                `json:"details,omitempty"`
	Parameters map[string]float64 `json:"parameters"`
	Report     GeometryReport     `json:"report"`
	Artifacts  []EngineArtifact   `json:"artifacts"`
	Duration   time.Duration      `json:"-"`
}

type BuildResult struct {
	Run        BuildRun           `json:"build"`
	Report     GeometryReport     `json:"report"`
	Checks     []CheckResult      `json:"checks"`
	Artifacts  []Artifact         `json:"artifacts"`
	Parameters map[string]float64 `json:"parameters"`
}

type CreateDesignInput struct {
	Name        string
	Description string
	Kind        string
	Tags        []string
	Definition  []byte
	Parameters  []byte
	Note        string
	Author      string
}

type CreateRevisionInput struct {
	DesignID       int64
	ExpectedParent int64
	Definition     []byte
	Parameters     []byte
	Note           string
	Author         string
}
