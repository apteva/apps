package main

import "encoding/json"

const (
	pcbSchema            = "apteva-pcb/v1"
	releaseSchema        = "apteva-pcb-release/v1"
	engineVersion        = "pcb-native/0.1.0"
	defaultClearance     = int64(200_000)
	defaultTraceWidth    = int64(200_000)
	defaultEdgeClearance = int64(250_000)
)

type Point struct {
	XNM int64 `json:"x_nm"`
	YNM int64 `json:"y_nm"`
}

type Layer struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Order int    `json:"order"`
}

type Board struct {
	WidthNM  int64   `json:"width_nm"`
	HeightNM int64   `json:"height_nm"`
	Layers   []Layer `json:"layers"`
}

type Rules struct {
	MinClearanceNM     int64 `json:"min_clearance_nm"`
	MinTraceWidthNM    int64 `json:"min_trace_width_nm"`
	MinEdgeClearanceNM int64 `json:"min_edge_clearance_nm"`
}

type Position struct {
	XNM          int64  `json:"x_nm"`
	YNM          int64  `json:"y_nm"`
	RotationUdeg int64  `json:"rotation_udeg"`
	Side         string `json:"side"`
}

type Pin struct {
	ID             string `json:"id"`
	Number         string `json:"number"`
	Name           string `json:"name,omitempty"`
	ElectricalType string `json:"electrical_type,omitempty"`
	Pad            string `json:"pad,omitempty"`
}

type Component struct {
	ID         string   `json:"id"`
	Designator string   `json:"designator"`
	Name       string   `json:"name"`
	Value      string   `json:"value,omitempty"`
	MPN        string   `json:"mpn,omitempty"`
	Footprint  string   `json:"footprint,omitempty"`
	Position   Position `json:"position"`
	Pins       []Pin    `json:"pins,omitempty"`
}

type Node struct {
	ComponentID string `json:"component_id"`
	PinID       string `json:"pin_id"`
}

type Net struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Nodes []Node `json:"nodes,omitempty"`
}

type Trace struct {
	ID      string  `json:"id"`
	NetID   string  `json:"net_id"`
	Layer   string  `json:"layer"`
	WidthNM int64   `json:"width_nm"`
	Points  []Point `json:"points"`
}

type Via struct {
	ID         string `json:"id"`
	NetID      string `json:"net_id"`
	XNM        int64  `json:"x_nm"`
	YNM        int64  `json:"y_nm"`
	DiameterNM int64  `json:"diameter_nm"`
	DrillNM    int64  `json:"drill_nm"`
	FromLayer  string `json:"from_layer"`
	ToLayer    string `json:"to_layer"`
}

type Definition struct {
	Schema     string      `json:"schema"`
	Name       string      `json:"name,omitempty"`
	Board      Board       `json:"board"`
	Rules      Rules       `json:"rules"`
	Components []Component `json:"components"`
	Nets       []Net       `json:"nets"`
	Traces     []Trace     `json:"traces"`
	Vias       []Via       `json:"vias"`
}

type Operation struct {
	Type        string     `json:"type"`
	Board       *Board     `json:"board,omitempty"`
	Rules       *Rules     `json:"rules,omitempty"`
	Component   *Component `json:"component,omitempty"`
	ComponentID string     `json:"component_id,omitempty"`
	Position    *Position  `json:"position,omitempty"`
	Net         *Net       `json:"net,omitempty"`
	NetID       string     `json:"net_id,omitempty"`
	Node        *Node      `json:"node,omitempty"`
	Trace       *Trace     `json:"trace,omitempty"`
	TraceID     string     `json:"trace_id,omitempty"`
	Via         *Via       `json:"via,omitempty"`
	ViaID       string     `json:"via_id,omitempty"`
}

type Check struct {
	Code      string   `json:"code"`
	Severity  string   `json:"severity"`
	Message   string   `json:"message"`
	ObjectIDs []string `json:"object_ids,omitempty"`
}

type Metrics struct {
	Components   int   `json:"components"`
	Pins         int   `json:"pins"`
	Nets         int   `json:"nets"`
	Traces       int   `json:"traces"`
	Vias         int   `json:"vias"`
	BoardAreaNM2 int64 `json:"board_area_nm2"`
}

type ValidationReport struct {
	Schema   string  `json:"schema"`
	Engine   string  `json:"engine"`
	Status   string  `json:"status"`
	Errors   int     `json:"errors"`
	Warnings int     `json:"warnings"`
	Checks   []Check `json:"checks"`
	Metrics  Metrics `json:"metrics"`
}

type Design struct {
	ID                int64          `json:"id"`
	ProjectID         string         `json:"project_id,omitempty"`
	Name              string         `json:"name"`
	Status            string         `json:"status"`
	CurrentRevisionID int64          `json:"current_revision_id"`
	Archived          bool           `json:"archived"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
	CurrentRevision   *Revision      `json:"current_revision,omitempty"`
	Validation        *ValidationRun `json:"validation,omitempty"`
	Artifacts         []Artifact     `json:"artifacts,omitempty"`
}

type Revision struct {
	ID            int64           `json:"id"`
	DesignID      int64           `json:"design_id"`
	ProjectID     string          `json:"project_id,omitempty"`
	ParentID      *int64          `json:"parent_id,omitempty"`
	Number        int             `json:"number"`
	SchemaVersion string          `json:"schema_version"`
	Definition    json.RawMessage `json:"definition"`
	Operations    json.RawMessage `json:"operations,omitempty"`
	SourceSHA256  string          `json:"source_sha256"`
	Note          string          `json:"note,omitempty"`
	Author        string          `json:"author,omitempty"`
	CreatedAt     string          `json:"created_at"`
}

type ValidationRun struct {
	ID         int64            `json:"id"`
	DesignID   int64            `json:"design_id"`
	RevisionID int64            `json:"revision_id"`
	Status     string           `json:"status"`
	Errors     int              `json:"errors"`
	Warnings   int              `json:"warnings"`
	Report     ValidationReport `json:"report"`
	CreatedAt  string           `json:"created_at"`
}

type Artifact struct {
	ID            int64           `json:"id"`
	DesignID      int64           `json:"design_id"`
	RevisionID    int64           `json:"revision_id"`
	Kind          string          `json:"kind"`
	Format        string          `json:"format"`
	Name          string          `json:"name"`
	ContentType   string          `json:"content_type"`
	LocalPath     string          `json:"-"`
	StorageFileID string          `json:"storage_file_id,omitempty"`
	SHA256        string          `json:"sha256"`
	SizeBytes     int64           `json:"size_bytes"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     string          `json:"created_at"`
}

type RevisionDiff struct {
	FromRevisionID int64               `json:"from_revision_id"`
	ToRevisionID   int64               `json:"to_revision_id"`
	SourceChanged  bool                `json:"source_changed"`
	BoardChanged   bool                `json:"board_changed"`
	RulesChanged   bool                `json:"rules_changed"`
	Components     map[string][]string `json:"components"`
	Nets           map[string][]string `json:"nets"`
	Traces         map[string][]string `json:"traces"`
	Vias           map[string][]string `json:"vias"`
}
