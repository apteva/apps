package main

import (
	"encoding/json"
	"time"
)

const (
	designSchema  = "apteva-design/v1"
	engineName    = "replicad-opencascade"
	engineVersion = "replicad-0.23.1-ocjs-0.23.0-ds0.4"
)

type ParameterSpec struct {
	Type        string   `json:"type"`
	Default     float64  `json:"default"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Description string   `json:"description,omitempty"`
}

type DesignDefinition struct {
	Schema        string                   `json:"schema"`
	Units         string                   `json:"units"`
	Parameters    map[string]ParameterSpec `json:"parameters,omitempty"`
	Operations    []map[string]any         `json:"operations"`
	Output        string                   `json:"output"`
	Checks        []map[string]any         `json:"checks,omitempty"`
	Provenance    *DesignProvenance        `json:"provenance,omitempty"`
	OpenHardware  *OpenHardwareMetadata    `json:"open_hardware,omitempty"`
	Materials     []MaterialSpec           `json:"materials,omitempty"`
	PrintProfiles []PrintProfile           `json:"print_profiles,omitempty"`
	Parts         []DesignPart             `json:"parts,omitempty"`
	Assembly      *AssemblySpec            `json:"assembly,omitempty"`
	BOM           []BOMItem                `json:"bom,omitempty"`
}

// OpenHardwareMetadata makes a revision redistributable without relying on
// project-side tribal knowledge. License identifiers use SPDX expressions.
type OpenHardwareMetadata struct {
	ProjectName   string   `json:"project_name"`
	Version       string   `json:"version"`
	License       string   `json:"license"`
	Repository    string   `json:"repository,omitempty"`
	Homepage      string   `json:"homepage,omitempty"`
	Authors       []string `json:"authors,omitempty"`
	Attributions  []string `json:"attributions,omitempty"`
	Readme        string   `json:"readme,omitempty"`
	AssemblyGuide string   `json:"assembly_guide,omitempty"`
}

type MaterialSpec struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	DensityGCM3 float64 `json:"density_g_cm3,omitempty"`
	Color       string  `json:"color,omitempty"`
	Recyclable  bool    `json:"recyclable,omitempty"`
	Source      string  `json:"source,omitempty"`
}

type PrintProfile struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Process        string    `json:"process"`
	MaterialID     string    `json:"material_id,omitempty"`
	NozzleMM       float64   `json:"nozzle_mm,omitempty"`
	LayerHeightMM  float64   `json:"layer_height_mm,omitempty"`
	Walls          int       `json:"walls,omitempty"`
	InfillPercent  float64   `json:"infill_percent,omitempty"`
	Supports       string    `json:"supports,omitempty"`
	MaxOverhangDeg float64   `json:"max_overhang_deg,omitempty"`
	BedSizeMM      []float64 `json:"bed_size_mm,omitempty"`
}

type PartManufacturing struct {
	Classification string  `json:"classification"`
	Process        string  `json:"process,omitempty"`
	PrintProfileID string  `json:"print_profile_id,omitempty"`
	Orientation    []any   `json:"orientation,omitempty"`
	MinWallMM      float64 `json:"min_wall_mm,omitempty"`
	MinFeatureMM   float64 `json:"min_feature_mm,omitempty"`
	MaxOverhangDeg float64 `json:"max_overhang_deg,omitempty"`
	Notes          string  `json:"notes,omitempty"`
}

type DesignPart struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Description   string               `json:"description,omitempty"`
	Output        string               `json:"output,omitempty"`
	Source        *PartSourceReference `json:"source,omitempty"`
	Quantity      int                  `json:"quantity,omitempty"`
	MaterialID    string               `json:"material_id,omitempty"`
	Color         string               `json:"color,omitempty"`
	Tags          []string             `json:"tags,omitempty"`
	Manufacturing *PartManufacturing   `json:"manufacturing,omitempty"`
}

// PartSourceReference is the assembly equivalent of a CAD part-file link.
// It always pins an immutable revision and its content hash; following a
// design's moving current revision is an explicit refresh operation.
type PartSourceReference struct {
	DesignID     int64  `json:"design_id"`
	RevisionID   int64  `json:"revision_id"`
	SourceSHA256 string `json:"source_sha256"`
	PartID       string `json:"part_id,omitempty"`
}

// ComponentDependency describes one resolved edge in an assembly's immutable
// revision graph. It is returned by builds and included in release packages.
type ComponentDependency struct {
	OwnerRevisionID      int64  `json:"owner_revision_id"`
	OwnerPartID          string `json:"owner_part_id"`
	SourceDesignID       int64  `json:"source_design_id"`
	SourceDesignName     string `json:"source_design_name"`
	SourceRevisionID     int64  `json:"source_revision_id"`
	SourceRevisionNumber int    `json:"source_revision_number"`
	SourceSHA256         string `json:"source_sha256"`
	SourcePartID         string `json:"source_part_id,omitempty"`
}

type AssemblyTransform struct {
	Translate []any              `json:"translate,omitempty"`
	Rotate    []AssemblyRotation `json:"rotate,omitempty"`
	Scale     any                `json:"scale,omitempty"`
}

type AssemblyRotation struct {
	Angle     any   `json:"angle"`
	Center    []any `json:"center,omitempty"`
	Direction []any `json:"direction,omitempty"`
}

type AssemblyInstance struct {
	ID        string            `json:"id"`
	PartID    string            `json:"part_id"`
	Quantity  int               `json:"quantity,omitempty"`
	Transform AssemblyTransform `json:"transform,omitempty"`
	Visible   *bool             `json:"visible,omitempty"`
}

type MechanicalInterface struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	PartID     string         `json:"part_id"`
	Position   []any          `json:"position"`
	Direction  []any          `json:"direction,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
	Notes      string         `json:"notes,omitempty"`
}

type JointSpec struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	ParentInstance string    `json:"parent_instance"`
	ChildInstance  string    `json:"child_instance"`
	Origin         []any     `json:"origin"`
	Axis           []any     `json:"axis,omitempty"`
	Limits         []float64 `json:"limits,omitempty"`
}

type AssemblySpec struct {
	Name       string                `json:"name"`
	Instances  []AssemblyInstance    `json:"instances"`
	Interfaces []MechanicalInterface `json:"interfaces,omitempty"`
	Joints     []JointSpec           `json:"joints,omitempty"`
}

type BOMItem struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Quantity       int    `json:"quantity"`
	Classification string `json:"classification"`
	PartID         string `json:"part_id,omitempty"`
	Manufacturer   string `json:"manufacturer,omitempty"`
	PartNumber     string `json:"part_number,omitempty"`
	Source         string `json:"source,omitempty"`
	Notes          string `json:"notes,omitempty"`
}

// DesignProvenance links generated geometry to an immutable upstream source.
// The source remains authoritative; refreshing always creates a new Design
// Studio revision and never mutates an existing enclosure silently.
type DesignProvenance struct {
	Kind                 string           `json:"kind"`
	SourceSchema         string           `json:"source_schema"`
	SourceApp            string           `json:"source_app"`
	SourceDesignID       int64            `json:"source_design_id"`
	SourceRevisionID     int64            `json:"source_revision_id"`
	SourceRevisionNumber int              `json:"source_revision_number"`
	SourceSHA256         string           `json:"source_sha256"`
	Generator            string           `json:"generator"`
	GeneratorVersion     string           `json:"generator_version"`
	Options              EnclosureOptions `json:"options"`
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
	Valid          bool                 `json:"valid"`
	Representation string               `json:"representation"`
	Units          string               `json:"units"`
	Bounds         Bounds               `json:"bounds"`
	VolumeMM3      float64              `json:"volume_mm3"`
	SurfaceAreaMM2 float64              `json:"surface_area_mm2"`
	BodyCount      int                  `json:"body_count"`
	FaceCount      int                  `json:"face_count"`
	EdgeCount      int                  `json:"edge_count"`
	VertexCount    int                  `json:"vertex_count"`
	TriangleCount  int                  `json:"triangle_count"`
	MassG          float64              `json:"mass_g,omitempty"`
	CenterOfMass   [3]float64           `json:"center_of_mass,omitempty"`
	Parts          []PartGeometryReport `json:"parts,omitempty"`
}

type PartGeometryReport struct {
	PartID       string     `json:"part_id"`
	InstanceID   string     `json:"instance_id,omitempty"`
	Name         string     `json:"name"`
	MaterialID   string     `json:"material_id,omitempty"`
	VolumeMM3    float64    `json:"volume_mm3"`
	MassG        float64    `json:"mass_g,omitempty"`
	CenterOfMass [3]float64 `json:"center_of_mass"`
	Bounds       Bounds     `json:"bounds"`
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
	PartID      string `json:"part_id,omitempty"`
	PartName    string `json:"part_name,omitempty"`
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
	Run          BuildRun              `json:"build"`
	Report       GeometryReport        `json:"report"`
	Checks       []CheckResult         `json:"checks"`
	Artifacts    []Artifact            `json:"artifacts"`
	Parameters   map[string]float64    `json:"parameters"`
	Dependencies []ComponentDependency `json:"dependencies,omitempty"`
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
