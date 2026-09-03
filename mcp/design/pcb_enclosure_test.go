package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func pcbEnvelopeFixture() PCBMechanicalResponse {
	return PCBMechanicalResponse{
		Validation: PCBMechanicalValidation{Status: "passed"},
		Envelope: PCBMechanicalEnvelope{
			Schema: pcbMechanicalEnvelopeSchema, SourceApp: "pcb", SourceDesignID: 42,
			SourceRevisionID: 7, SourceRevisionNumber: 3, SourceSHA256: strings.Repeat("a", 64),
			Datum:      PCBMechanicalDatum{Origin: "board-outline-lower-left-bottom-face", Handedness: "right", XAxis: "board-right", YAxis: "board-up-in-top-view", ZAxis: "front-side"},
			Tolerances: PCBMechanicalTolerances{XYNM: 200_000, ZNM: 200_000, PanelOpeningFitNM: 300_000, FastenerFitNM: 150_000},
			Board:      PCBMechanicalBoard{WidthNM: 85_000_000, HeightNM: 56_000_000, ThicknessNM: 1_600_000, Outline: []PCBPoint{{0, 0}, {85_000_000, 0}, {85_000_000, 56_000_000}, {0, 56_000_000}}},
			MountingHoles: []PCBMechanicalHole{
				{ID: "mh1", XNM: 3_500_000, YNM: 3_500_000, DiameterNM: 2_700_000, HeadClearanceNM: 5_000_000},
				{ID: "mh2", XNM: 61_500_000, YNM: 3_500_000, DiameterNM: 2_700_000, HeadClearanceNM: 5_000_000},
				{ID: "mh3", XNM: 3_500_000, YNM: 52_500_000, DiameterNM: 2_700_000, HeadClearanceNM: 5_000_000},
				{ID: "mh4", XNM: 61_500_000, YNM: 52_500_000, DiameterNM: 2_700_000, HeadClearanceNM: 5_000_000},
			},
			Components: []PCBComponentEnvelope{
				{ID: "usb", ComponentID: "j1", XNM: 8_000_000, YNM: 3_000_000, ZMinNM: 1_600_000, ZMaxNM: 5_000_000, WidthNM: 9_000_000, DepthNM: 8_000_000, ToleranceNM: 200_000},
				{ID: "soc", ComponentID: "u1", XNM: 45_000_000, YNM: 31_500_000, ZMinNM: 1_600_000, ZMaxNM: 3_200_000, WidthNM: 15_000_000, DepthNM: 15_000_000, ToleranceNM: 200_000},
				{ID: "sd", ComponentID: "j2", XNM: 2_000_000, YNM: 28_000_000, ZMinNM: -2_200_000, ZMaxNM: 0, WidthNM: 12_000_000, DepthNM: 14_000_000, ToleranceNM: 200_000},
			},
			PanelOpenings: []PCBPanelOpening{
				{ID: "usb-open", ComponentID: "j1", Face: "south", XNM: 8_000_000, YNM: 0, ZNM: 3_200_000, WidthNM: 10_000_000, HeightNM: 4_000_000, DepthNM: 3_000_000},
				{ID: "sd-open", ComponentID: "j2", Face: "west", XNM: 0, YNM: 28_000_000, ZNM: -1_000_000, WidthNM: 14_000_000, HeightNM: 3_500_000, DepthNM: 3_000_000},
			},
			ClearanceZones: []PCBMechanicalClearance{
				{ID: "usb-cable", OwnerID: "j1", Kind: "cable_insertion", XNM: 8_000_000, YNM: -12_000_000, ZMinNM: 1_000_000, ZMaxNM: 8_000_000, WidthNM: 12_000_000, DepthNM: 24_000_000},
				{ID: "gpio-service", OwnerID: "gpio", Kind: "service", XNM: 32_500_000, YNM: 52_000_000, ZMinNM: 1_600_000, ZMaxNM: 20_000_000, WidthNM: 51_000_000, DepthNM: 5_000_000},
				{ID: "soc-airflow", OwnerID: "u1", Kind: "airflow", XNM: 45_000_000, YNM: 31_500_000, ZMinNM: 3_200_000, ZMaxNM: 12_000_000, WidthNM: 28_000_000, DepthNM: 28_000_000},
			},
		},
	}
}

func TestGeneratePCBEnclosurePreservesSourceAndParametricFit(t *testing.T) {
	definition, report, err := generatePCBEnclosure(pcbEnvelopeFixture(), EnclosureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ready" || report.MountingHoles != 4 || report.PanelOpenings != 2 || report.GeneratedServiceOpenings != 1 {
		t.Fatalf("generation report=%#v", report)
	}
	if definition.Provenance == nil || definition.Provenance.SourceRevisionID != 7 || definition.Provenance.SourceSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("source provenance=%#v", definition.Provenance)
	}
	if definition.Parameters["standoff"].Default < 2.6 {
		t.Fatalf("standoff did not account for underside geometry: %#v", definition.Parameters["standoff"])
	}
	if definition.Output != "enclosure" || len(definition.Operations) < 18 {
		t.Fatalf("generated graph is incomplete: output=%q operations=%d", definition.Output, len(definition.Operations))
	}
	body, _ := json.Marshal(definition)
	canonical, normalized, err := normalizeDefinition(body, 256)
	if err != nil || !json.Valid(canonical) || normalized.Provenance == nil {
		t.Fatalf("generated definition did not normalize: %v\n%s", err, canonical)
	}
}

func TestGeneratePCBEnclosureRejectsFailedOrUntraceableSource(t *testing.T) {
	fixture := pcbEnvelopeFixture()
	fixture.Validation.Status, fixture.Validation.Errors = "failed", 2
	if _, _, err := generatePCBEnclosure(fixture, EnclosureOptions{}); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("expected failed validation rejection, got %v", err)
	}
	fixture = pcbEnvelopeFixture()
	fixture.Envelope.SourceSHA256 = ""
	if _, _, err := generatePCBEnclosure(fixture, EnclosureOptions{}); err == nil || !strings.Contains(err.Error(), "provenance") {
		t.Fatalf("expected provenance rejection, got %v", err)
	}
}

func TestGeneratedPCBEnclosureBuildsAsExactGeometry(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	definition, _, err := generatePCBEnclosure(pcbEnvelopeFixture(), EnclosureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(definition)
	canonical, normalized, err := normalizeDefinition(body, 256)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := normalizeParameters([]byte(`{}`), normalized)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(t.TempDir(), bun, 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Build(context.Background(), "PCB enclosure", canonical, parameters, []string{"mesh-json", "step", "stl"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Report.Valid || result.Report.Bounds.Size[0] < 89 || result.Report.Bounds.Size[1] < 60 || result.Report.Bounds.Size[2] < 13 {
		t.Fatalf("unexpected enclosure geometry: %#v", result.Report)
	}
}

type pcbSourcePlatform struct {
	tk.BasePlatformClient
	response PCBMechanicalResponse
	app      string
	tool     string
	input    map[string]any
}

func (p *pcbSourcePlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{AppName: "design", InstallID: 20, ProjectID: "project-a", Bindings: map[string]any{"storage": float64(41), "pcb_source": float64(77)}}, nil
}

func (p *pcbSourcePlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return &sdk.PlatformInstance{ID: id, Name: "pcb", Status: "running", ProjectID: "project-a"}, nil
}

func (p *pcbSourcePlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.app, p.tool, p.input = app, tool, input
	body, _ := json.Marshal(p.response)
	return json.Unmarshal(body, out)
}

func TestPCBEnclosureToolCallsBoundSourceAndRefreshesImmutably(t *testing.T) {
	platform := &pcbSourcePlatform{response: pcbEnvelopeFixture()}
	store := testStore(t)
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, store.db, sdk.Config{}, platform, nil).WithProject("project-a")
	app := &App{ctx: ctx, store: store, engine: &Engine{}, artifactRoot: t.TempDir(), maxOperations: 256}

	createdRaw, err := app.toolEnclosureFromPCB(context.Background(), ctx, map[string]any{"pcb_design_id": int64(42), "name": "Pi enclosure"})
	if err != nil {
		t.Fatal(err)
	}
	created := createdRaw.(map[string]any)["design"].(*Design)
	if created.Name != "Pi enclosure" || platform.app != "pcb" || platform.tool != "pcb_mechanical_get" || platform.input["_project_id"] != "project-a" {
		t.Fatalf("handoff call/design mismatch: design=%#v app=%q tool=%q input=%#v", created, platform.app, platform.tool, platform.input)
	}
	platform.response.Envelope.SourceRevisionID = 8
	platform.response.Envelope.SourceRevisionNumber = 4
	platform.response.Envelope.SourceSHA256 = strings.Repeat("b", 64)
	refreshedRaw, err := app.toolEnclosureRefreshFromPCB(context.Background(), ctx, map[string]any{
		"design_id": created.ID, "expected_parent_id": created.CurrentRevisionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	refreshed := refreshedRaw.(map[string]any)["revision"].(*Revision)
	if refreshed.ParentRevisionID == nil || *refreshed.ParentRevisionID != created.CurrentRevisionID || refreshed.RevisionNumber != 2 {
		t.Fatalf("refresh was not immutable: %#v", refreshed)
	}
	_, definition, err := normalizeDefinition(refreshed.Definition, 256)
	if err != nil || definition.Provenance.SourceRevisionID != 8 || definition.Provenance.SourceSHA256 != strings.Repeat("b", 64) {
		t.Fatalf("refreshed provenance=%#v err=%v", definition.Provenance, err)
	}
}
