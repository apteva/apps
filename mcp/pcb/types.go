package main

import "encoding/json"

const (
	pcbSchema                = "apteva-pcb/v1"
	releaseSchema            = "apteva-pcb-release/v1"
	mechanicalSchema         = "apteva-pcb-mechanical/v1"
	mechanicalEnvelopeSchema = "apteva-mechanical-envelope/v1"
	productionSchema         = "apteva-pcb-production-readiness/v1"
	kicadExchangeSchema      = "apteva-pcb-kicad-exchange/v1"
	engineVersion            = "pcb-native/0.9.0"
	defaultClearance         = int64(200_000)
	defaultTraceWidth        = int64(200_000)
	defaultEdgeClearance     = int64(250_000)
	defaultMinDrill          = int64(200_000)
	defaultAnnularRing       = int64(125_000)
	defaultMaskSliver        = int64(100_000)
	defaultCopperToHole      = int64(250_000)
	defaultCourtyardGap      = int64(250_000)
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
	WidthNM        int64         `json:"width_nm"`
	HeightNM       int64         `json:"height_nm"`
	ThicknessNM    int64         `json:"thickness_nm,omitempty"`
	CornerRadiusNM int64         `json:"corner_radius_nm,omitempty"`
	Outline        []Point       `json:"outline,omitempty"`
	Cutouts        []BoardCutout `json:"cutouts,omitempty"`
	Layers         []Layer       `json:"layers"`
}

// BoardCutout is a native non-copper mechanical void. Circle and slot cutouts
// use centre geometry; polygon cutouts use absolute board coordinates.
type BoardCutout struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"` // circle | slot | polygon
	XNM          int64   `json:"x_nm,omitempty"`
	YNM          int64   `json:"y_nm,omitempty"`
	WidthNM      int64   `json:"width_nm,omitempty"`
	HeightNM     int64   `json:"height_nm,omitempty"`
	DiameterNM   int64   `json:"diameter_nm,omitempty"`
	RotationUdeg int64   `json:"rotation_udeg,omitempty"`
	Polygon      []Point `json:"polygon,omitempty"`
}

type Rules struct {
	MinClearanceNM          int64 `json:"min_clearance_nm"`
	MinTraceWidthNM         int64 `json:"min_trace_width_nm"`
	MinEdgeClearanceNM      int64 `json:"min_edge_clearance_nm"`
	MinDrillNM              int64 `json:"min_drill_nm,omitempty"`
	MinAnnularRingNM        int64 `json:"min_annular_ring_nm,omitempty"`
	MinSolderMaskSliverNM   int64 `json:"min_solder_mask_sliver_nm,omitempty"`
	MinCopperToHoleNM       int64 `json:"min_copper_to_hole_nm,omitempty"`
	MinCourtyardClearanceNM int64 `json:"min_courtyard_clearance_nm,omitempty"`
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

// Body is the physical component body used by the native renderer and
// placement/keepout checks. Coordinates are centred on Component.Position.
type Body struct {
	WidthNM  int64 `json:"width_nm"`
	HeightNM int64 `json:"height_nm"`
}

// Pad is native footprint copper geometry. X/Y are relative to the component
// origin before Component.Position rotation is applied. PinID links the pad to
// the electrical graph; Layers normally contains F.Cu or B.Cu and may contain
// both for through-hole pads.
type Pad struct {
	ID                    string   `json:"id"`
	PinID                 string   `json:"pin_id"`
	Shape                 string   `json:"shape"` // rect | roundrect | circle | oval
	XNM                   int64    `json:"x_nm"`
	YNM                   int64    `json:"y_nm"`
	WidthNM               int64    `json:"width_nm"`
	HeightNM              int64    `json:"height_nm"`
	DrillNM               int64    `json:"drill_nm,omitempty"`
	DrillWidthNM          int64    `json:"drill_width_nm,omitempty"`
	DrillHeightNM         int64    `json:"drill_height_nm,omitempty"`
	Plated                bool     `json:"plated,omitempty"`
	SolderMaskExpansionNM int64    `json:"solder_mask_expansion_nm,omitempty"`
	PasteExpansionNM      int64    `json:"paste_expansion_nm,omitempty"`
	Layers                []string `json:"layers"`
}

// FootprintSpec makes the land pattern an auditable contract rather than a
// free-form display string. Geometry remains embedded in Pads/Courtyard so a
// revision is reproducible even if a library changes later.
type FootprintSpec struct {
	LibraryID        string   `json:"library_id"`
	Revision         string   `json:"revision"`
	Standard         string   `json:"standard,omitempty"`
	Source           string   `json:"source,omitempty"`
	SourceSHA256     string   `json:"source_sha256,omitempty"`
	Verified         bool     `json:"verified"`
	PinCount         int      `json:"pin_count"`
	Courtyard        []Point  `json:"courtyard,omitempty"`
	AssemblyHeightNM int64    `json:"assembly_height_nm,omitempty"`
	CompatibleMPNs   []string `json:"compatible_mpns,omitempty"`
}

// Model3DSpec identifies the exact native mechanical model revision associated
// with a component. STEP is exported from this immutable geometry contract.
type Model3DSpec struct {
	LibraryID    string `json:"library_id"`
	Revision     string `json:"revision"`
	SourceSHA256 string `json:"source_sha256,omitempty"`
	WidthNM      int64  `json:"width_nm"`
	DepthNM      int64  `json:"depth_nm"`
	HeightNM     int64  `json:"height_nm"`
	Verified     bool   `json:"verified"`
}

type Component struct {
	ID            string           `json:"id"`
	Designator    string           `json:"designator"`
	Name          string           `json:"name"`
	Value         string           `json:"value,omitempty"`
	MPN           string           `json:"mpn,omitempty"`
	Footprint     string           `json:"footprint,omitempty"`
	FootprintSpec *FootprintSpec   `json:"footprint_spec,omitempty"`
	Model3D       *Model3DSpec     `json:"model_3d,omitempty"`
	Position      Position         `json:"position"`
	Pins          []Pin            `json:"pins,omitempty"`
	Body          *Body            `json:"body,omitempty"`
	Pads          []Pad            `json:"pads,omitempty"`
	Model         *ElectricalModel `json:"electrical_model,omitempty"`
}

// MechanicalSpec is PCB Studio's engine-independent 3D handoff model. All
// coordinates are integer nanometres in the same XY frame as the PCB graph;
// Z=0 is the board's bottom face and +Z points toward the front side.
type MechanicalSpec struct {
	Schema             string                `json:"schema,omitempty"`
	Datum              MechanicalDatum       `json:"datum"`
	Tolerances         MechanicalTolerances  `json:"tolerances"`
	MountingHoles      []MechanicalHole      `json:"mounting_holes,omitempty"`
	ComponentEnvelopes []ComponentEnvelope   `json:"component_envelopes,omitempty"`
	PanelOpenings      []PanelOpening        `json:"panel_openings,omitempty"`
	ClearanceZones     []MechanicalClearance `json:"clearance_zones,omitempty"`
}

// MechanicalTolerances records manufacturing and enclosure-fit intent without
// baking one fabrication process into the board geometry.
type MechanicalTolerances struct {
	XYNM              int64 `json:"xy_nm"`
	ZNM               int64 `json:"z_nm"`
	PanelOpeningFitNM int64 `json:"panel_opening_fit_nm"`
	FastenerFitNM     int64 `json:"fastener_fit_nm"`
}

type MechanicalDatum struct {
	Origin     string `json:"origin"`
	Handedness string `json:"handedness"`
	XAxis      string `json:"x_axis"`
	YAxis      string `json:"y_axis"`
	ZAxis      string `json:"z_axis"`
}

type MechanicalHole struct {
	ID              string `json:"id"`
	XNM             int64  `json:"x_nm"`
	YNM             int64  `json:"y_nm"`
	DiameterNM      int64  `json:"diameter_nm"`
	Plated          bool   `json:"plated"`
	Fastener        string `json:"fastener,omitempty"`
	HeadClearanceNM int64  `json:"head_clearance_nm,omitempty"`
	PositionTolNM   int64  `json:"position_tolerance_nm,omitempty"`
	DiameterTolNM   int64  `json:"diameter_tolerance_nm,omitempty"`
}

// ComponentEnvelope is a conservative oriented solid. X/Y are absolute PCB
// coordinates; z_min/z_max include the board-side transformation. ModelRef is
// an optional Apteva-owned visual primitive, never an external engine handle.
type ComponentEnvelope struct {
	ID           string `json:"id"`
	ComponentID  string `json:"component_id,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Label        string `json:"label,omitempty"`
	XNM          int64  `json:"x_nm"`
	YNM          int64  `json:"y_nm"`
	ZMinNM       int64  `json:"z_min_nm"`
	ZMaxNM       int64  `json:"z_max_nm"`
	WidthNM      int64  `json:"width_nm"`
	DepthNM      int64  `json:"depth_nm"`
	RotationUdeg int64  `json:"rotation_udeg,omitempty"`
	Shape        string `json:"shape,omitempty"` // box | cylinder | header | connector
	ModelRef     string `json:"model_ref,omitempty"`
	ToleranceNM  int64  `json:"tolerance_nm,omitempty"`
}

// PanelOpening describes a required enclosure aperture in an interface plane.
// Face is north/south/east/west/top/bottom in the PCB datum.
type PanelOpening struct {
	ID          string `json:"id"`
	ComponentID string `json:"component_id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Face        string `json:"face"`
	XNM         int64  `json:"x_nm"`
	YNM         int64  `json:"y_nm"`
	ZNM         int64  `json:"z_nm"`
	WidthNM     int64  `json:"width_nm"`
	HeightNM    int64  `json:"height_nm"`
	RadiusNM    int64  `json:"radius_nm,omitempty"`
	DepthNM     int64  `json:"depth_nm,omitempty"`
	ToleranceNM int64  `json:"tolerance_nm,omitempty"`
}

type MechanicalClearance struct {
	ID           string `json:"id"`
	OwnerID      string `json:"owner_id,omitempty"`
	Kind         string `json:"kind"` // cable_insertion | service | airflow | thermal | antenna
	XNM          int64  `json:"x_nm"`
	YNM          int64  `json:"y_nm"`
	ZMinNM       int64  `json:"z_min_nm"`
	ZMaxNM       int64  `json:"z_max_nm"`
	WidthNM      int64  `json:"width_nm"`
	DepthNM      int64  `json:"depth_nm"`
	RotationUdeg int64  `json:"rotation_udeg,omitempty"`
}

// ElectricalModel is a small, provider-neutral behavioral model owned by PCB
// Studio. It intentionally describes electrical intent instead of referencing
// a SPICE or vendor engine. Pin roles map semantic names (in, out, ground,
// anode, cathode, sda, scl, gpio...) to the component's stable pin IDs.
type ElectricalModel struct {
	Kind            string            `json:"kind"` // resistor | capacitor | voltage_source | regulator | led | switch | mcu | sensor | digital
	PinRoles        map[string]string `json:"pin_roles,omitempty"`
	ResistanceOhms  float64           `json:"resistance_ohms,omitempty"`
	CapacitanceF    float64           `json:"capacitance_f,omitempty"`
	VoltageV        float64           `json:"voltage_v,omitempty"`
	ForwardVoltageV float64           `json:"forward_voltage_v,omitempty"`
	DropoutVoltageV float64           `json:"dropout_voltage_v,omitempty"`
	LogicHighV      float64           `json:"logic_high_v,omitempty"`
	I2CAddress      int               `json:"i2c_address,omitempty"`
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

// Zone is a filled copper polygon tied to a net. The native writer emits it as
// a Gerber region; the validator uses it for pad connectivity and keepouts.
type Zone struct {
	ID          string  `json:"id"`
	NetID       string  `json:"net_id"`
	Layer       string  `json:"layer"`
	ClearanceNM int64   `json:"clearance_nm,omitempty"`
	Polygon     []Point `json:"polygon"`
}

// Keepout reserves a polygon for antennas, copper, components, or all three.
// An antenna keepout always excludes copper, vias, zones, and other components.
type Keepout struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"` // antenna | copper | component | all
	Layer   string  `json:"layer,omitempty"`
	OwnerID string  `json:"owner_id,omitempty"`
	Polygon []Point `json:"polygon"`
}

// DifferentialPair records routing intent independently from any vendor or
// external ECAD engine. Gap is edge-to-edge and MaxSkew is the allowed length
// difference between the two routed nets.
type DifferentialPair struct {
	ID             string `json:"id"`
	PositiveNetID  string `json:"positive_net_id"`
	NegativeNetID  string `json:"negative_net_id"`
	TargetOhms     int    `json:"target_ohms,omitempty"`
	GapNM          int64  `json:"gap_nm"`
	GapToleranceNM int64  `json:"gap_tolerance_nm,omitempty"`
	MaxSkewNM      int64  `json:"max_skew_nm"`
}

// NetClass captures native routing policy. A net can be listed in at most one
// class; unlisted nets inherit Rules and the router's default via geometry.
type NetClass struct {
	ID            string   `json:"id"`
	NetIDs        []string `json:"net_ids"`
	TraceWidthNM  int64    `json:"trace_width_nm,omitempty"`
	ClearanceNM   int64    `json:"clearance_nm,omitempty"`
	ViaDiameterNM int64    `json:"via_diameter_nm,omitempty"`
	ViaDrillNM    int64    `json:"via_drill_nm,omitempty"`
	Priority      int      `json:"priority,omitempty"`
}

// SimulationSpec stores revisioned stimuli and probes. Tool calls may add
// temporary overrides, but saved tutorial designs remain fully reproducible.
type SimulationSpec struct {
	DurationUS int64              `json:"duration_us,omitempty"`
	StepUS     int64              `json:"step_us,omitempty"`
	Sources    []SimulationSource `json:"sources,omitempty"`
	Probes     []SimulationProbe  `json:"probes,omitempty"`
	Devices    []SimulationDevice `json:"devices,omitempty"`
}

type SimulationSource struct {
	ID        string  `json:"id"`
	NetID     string  `json:"net_id"`
	Kind      string  `json:"kind"` // dc | digital | clock
	Value     float64 `json:"value,omitempty"`
	StartUS   int64   `json:"start_us,omitempty"`
	PeriodUS  int64   `json:"period_us,omitempty"`
	DutyCycle float64 `json:"duty_cycle,omitempty"`
}

type SimulationProbe struct {
	ID    string `json:"id"`
	NetID string `json:"net_id"`
	Kind  string `json:"kind,omitempty"` // voltage | digital
}

type SimulationDevice struct {
	ID          string             `json:"id"`
	ComponentID string             `json:"component_id,omitempty"`
	Kind        string             `json:"kind"` // i2c_sensor | uart | gpio
	Nets        map[string]string  `json:"nets,omitempty"`
	Address     int                `json:"address,omitempty"`
	Values      map[string]float64 `json:"values,omitempty"`
}

// WiringSpec is PCB Studio's engine-independent physical prototyping model.
// It records real part/pin connectivity and tutorial intent; the SVG/PNG
// illustration is a deterministic view of this data rather than the source.
type WiringSpec struct {
	Schema      string             `json:"schema,omitempty"`
	Canvas      WiringCanvas       `json:"canvas"`
	Parts       []WiringPart       `json:"parts"`
	Wires       []WiringWire       `json:"wires"`
	Annotations []WiringAnnotation `json:"annotations,omitempty"`
	Steps       []WiringStep       `json:"steps,omitempty"`
	Firmware    *WiringFirmware    `json:"firmware,omitempty"`
}

type WiringCanvas struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	Grid   int `json:"grid,omitempty"`
}

type WiringPart struct {
	ID         string         `json:"id"`
	LibraryID  string         `json:"library_id"`
	Label      string         `json:"label,omitempty"`
	X          float64        `json:"x"`
	Y          float64        `json:"y"`
	Rotation   float64        `json:"rotation_deg,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

type WiringEndpoint struct {
	PartID string `json:"part_id"`
	PinID  string `json:"pin_id"`
}

type WiringPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type WiringWire struct {
	ID     string         `json:"id"`
	NetID  string         `json:"net_id"`
	Color  string         `json:"color,omitempty"`
	From   WiringEndpoint `json:"from"`
	To     WiringEndpoint `json:"to"`
	Points []WiringPoint  `json:"points,omitempty"`
}

type WiringAnnotation struct {
	ID   string  `json:"id"`
	Text string  `json:"text"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}

type WiringStep struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Instruction string   `json:"instruction"`
	PartIDs     []string `json:"part_ids,omitempty"`
	WireIDs     []string `json:"wire_ids,omitempty"`
}

type WiringFirmware struct {
	Board  string            `json:"board"`
	Source string            `json:"source"`
	PinMap map[string]string `json:"pin_map,omitempty"`
}

type Definition struct {
	Schema            string             `json:"schema"`
	Name              string             `json:"name,omitempty"`
	Board             Board              `json:"board"`
	Rules             Rules              `json:"rules"`
	Components        []Component        `json:"components"`
	Nets              []Net              `json:"nets"`
	Traces            []Trace            `json:"traces"`
	Vias              []Via              `json:"vias"`
	Zones             []Zone             `json:"zones,omitempty"`
	Keepouts          []Keepout          `json:"keepouts,omitempty"`
	DifferentialPairs []DifferentialPair `json:"differential_pairs,omitempty"`
	NetClasses        []NetClass         `json:"net_classes,omitempty"`
	Simulation        *SimulationSpec    `json:"simulation,omitempty"`
	Wiring            *WiringSpec        `json:"wiring,omitempty"`
	Mechanical        *MechanicalSpec    `json:"mechanical,omitempty"`
}

type Operation struct {
	Type               string            `json:"type"`
	Board              *Board            `json:"board,omitempty"`
	Rules              *Rules            `json:"rules,omitempty"`
	Component          *Component        `json:"component,omitempty"`
	ComponentID        string            `json:"component_id,omitempty"`
	Position           *Position         `json:"position,omitempty"`
	Net                *Net              `json:"net,omitempty"`
	NetID              string            `json:"net_id,omitempty"`
	Node               *Node             `json:"node,omitempty"`
	Trace              *Trace            `json:"trace,omitempty"`
	TraceID            string            `json:"trace_id,omitempty"`
	Via                *Via              `json:"via,omitempty"`
	ViaID              string            `json:"via_id,omitempty"`
	Zone               *Zone             `json:"zone,omitempty"`
	ZoneID             string            `json:"zone_id,omitempty"`
	Keepout            *Keepout          `json:"keepout,omitempty"`
	KeepoutID          string            `json:"keepout_id,omitempty"`
	DifferentialPair   *DifferentialPair `json:"differential_pair,omitempty"`
	DifferentialPairID string            `json:"differential_pair_id,omitempty"`
	NetClass           *NetClass         `json:"net_class,omitempty"`
	NetClassID         string            `json:"net_class_id,omitempty"`
	Simulation         *SimulationSpec   `json:"simulation,omitempty"`
	Wiring             *WiringSpec       `json:"wiring,omitempty"`
	WiringPart         *WiringPart       `json:"wiring_part,omitempty"`
	WiringPartID       string            `json:"wiring_part_id,omitempty"`
	WiringWire         *WiringWire       `json:"wiring_wire,omitempty"`
	WiringWireID       string            `json:"wiring_wire_id,omitempty"`
	Mechanical         *MechanicalSpec   `json:"mechanical,omitempty"`
}

type Check struct {
	Code      string   `json:"code"`
	Severity  string   `json:"severity"`
	Message   string   `json:"message"`
	ObjectIDs []string `json:"object_ids,omitempty"`
}

type Metrics struct {
	Components        int   `json:"components"`
	Pins              int   `json:"pins"`
	Nets              int   `json:"nets"`
	Traces            int   `json:"traces"`
	Vias              int   `json:"vias"`
	Pads              int   `json:"pads"`
	Zones             int   `json:"zones"`
	Keepouts          int   `json:"keepouts"`
	DifferentialPairs int   `json:"differential_pairs"`
	BoardAreaNM2      int64 `json:"board_area_nm2"`
	WiringParts       int   `json:"wiring_parts,omitempty"`
	WiringWires       int   `json:"wiring_wires,omitempty"`
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
	FromRevisionID    int64               `json:"from_revision_id"`
	ToRevisionID      int64               `json:"to_revision_id"`
	SourceChanged     bool                `json:"source_changed"`
	BoardChanged      bool                `json:"board_changed"`
	RulesChanged      bool                `json:"rules_changed"`
	Components        map[string][]string `json:"components"`
	Nets              map[string][]string `json:"nets"`
	Traces            map[string][]string `json:"traces"`
	Vias              map[string][]string `json:"vias"`
	Zones             map[string][]string `json:"zones"`
	Keepouts          map[string][]string `json:"keepouts"`
	DifferentialPairs map[string][]string `json:"differential_pairs"`
	NetClasses        map[string][]string `json:"net_classes"`
	SimulationChanged bool                `json:"simulation_changed"`
	WiringChanged     bool                `json:"wiring_changed"`
	MechanicalChanged bool                `json:"mechanical_changed"`
}
