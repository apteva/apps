package main

import (
	"bytes"
	"encoding/csv"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func createDesignFrom(t *testing.T, s *Service, def Definition, name string) *Design {
	t.Helper()
	canonical, _, hash, err := normalizeDefinition(mustJSON(def), name)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.store.CreateDesign(s.project, name, canonical, nil, hash, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func readCSV(t *testing.T, body []byte) [][]string {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// JLCPCB files: CPL in JLC's columns with rotation corrected for the JLC
// library zero, BOM with an LCSC column.
func TestJLCAssemblyFilesUseLCSCAndRotationOffsets(t *testing.T) {
	def := dhtExample(t)
	var sot *Component
	for i := range def.Components {
		if strings.HasPrefix(def.Components[i].Footprint, "SOT-23") {
			sot = &def.Components[i]
			break
		}
	}
	if sot == nil {
		// Force one SOT-23 part so the reference table is exercised.
		sot = &def.Components[0]
		sot.Footprint = "SOT-23-5"
	}
	sot.Position.RotationUdeg = 90_000_000
	sot.LCSC = "C51118"
	override := 45.0
	def.Components[len(def.Components)-1].AssemblyRotationOffsetDeg = &override
	if err := rotationOffsetLimits(&def); err != nil {
		t.Fatal(err)
	}
	cpl := readCSV(t, renderJLCPlacementCSV(&def))
	if strings.Join(cpl[0], ",") != "Designator,Mid X,Mid Y,Layer,Rotation" {
		t.Fatalf("JLC CPL header = %v", cpl[0])
	}
	found := false
	for _, row := range cpl[1:] {
		if row[0] == sot.Designator {
			found = true
			if row[4] != "270" {
				t.Fatalf("SOT-23 at 90 deg should be 270 for JLC, got %s", row[4])
			}
			if !strings.HasSuffix(row[1], "mm") || row[3] != "Top" {
				t.Fatalf("unexpected CPL row %v", row)
			}
		}
	}
	if !found {
		t.Fatalf("%s missing from CPL", sot.Designator)
	}
	bom := readCSV(t, renderJLCBOM(&def))
	if strings.Join(bom[0], ",") != "Comment,Designator,Footprint,LCSC Part #" {
		t.Fatalf("JLC BOM header = %v", bom[0])
	}
	hasLCSC := false
	for _, row := range bom[1:] {
		if strings.Contains(row[1], sot.Designator) && row[3] == "C51118" {
			hasLCSC = true
		}
	}
	if !hasLCSC {
		t.Fatal("LCSC number missing from JLC BOM")
	}
	if c := codes(jlcAssemblyChecks(&def)); c["ASSEMBLY_LCSC_MISSING"] != 1 {
		t.Fatalf("expected one missing-LCSC warning, got %v", c)
	}
	files := assemblyFiles(&def)
	for _, name := range []string{"assembly/jlcpcb-cpl.csv", "assembly/jlcpcb-bom.csv", "assembly/pick-and-place.csv", "assembly/bom.csv"} {
		if len(files[name]) == 0 {
			t.Fatalf("assembly file %s missing", name)
		}
	}
	bad := def
	bad.Components = append([]Component(nil), def.Components...)
	bad.Components[0].LCSC = "2040"
	if rotationOffsetLimits(&bad) == nil {
		t.Fatal("malformed LCSC number accepted")
	}
}

func TestI2CAddressConflictOnSharedSDANet(t *testing.T) {
	def := sensorNodeExample()
	if c := codes(i2cChecks(&def)); c["I2C_ADDRESS_CONFLICT"] != 0 {
		t.Fatalf("clean example reports conflicts: %v", c)
	}
	devices := i2cDevices(&def)
	if len(devices) != 1 || devices[0].BusNetID == "" || devices[0].AddressHex != "0x70" {
		t.Fatalf("SHTC3 not found on its SDA net: %+v", devices)
	}
	// A second sensor at the same address on the same SDA net.
	u2 := def.Components[findComponent(def.Components, "u2")]
	clone := u2
	clone.ID, clone.Designator = "u9", "U9"
	clone.Position.XNM += 5_000_000
	def.Components = append(def.Components, clone)
	for i := range def.Nets {
		for _, node := range def.Nets[i].Nodes {
			if node.ComponentID == "u2" {
				def.Nets[i].Nodes = append(def.Nets[i].Nodes, Node{ComponentID: "u9", PinID: node.PinID})
				break
			}
		}
	}
	checks := i2cChecks(&def)
	if c := codes(checks); c["I2C_ADDRESS_CONFLICT"] != 1 {
		t.Fatalf("expected one conflict, got %v", checks)
	}
	if c := codes(validateDefinition(&def).Checks); c["I2C_ADDRESS_CONFLICT"] != 1 {
		t.Fatal("validation does not report the I2C conflict")
	}
}

func TestI2CBusCheckAcrossBlocks(t *testing.T) {
	store := testStore(t)
	s := &Service{store: store, project: "project-a", artifactRoot: t.TempDir()}
	a := createDesignFrom(t, s, sensorNodeExample(), "Block A")
	b := createDesignFrom(t, s, sensorNodeExample(), "Block B")
	other := sensorNodeExample()
	other.Components[findComponent(other.Components, "u2")].Model.I2CAddress = 0x44
	c := createDesignFrom(t, s, other, "Block C")
	report, err := s.I2CBusCheck([]int64{a.ID, c.ID})
	if err != nil || report.Status != "passed" {
		t.Fatalf("distinct addresses failed: %+v %v", report, err)
	}
	report, err = s.I2CBusCheck([]int64{a.ID, b.ID, c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "failed" || len(report.SharedBus) != 1 || report.SharedBus[0].AddressHex != "0x70" || len(report.SharedBus[0].Devices) != 2 {
		t.Fatalf("expected one shared-bus conflict at 0x70: %+v", report)
	}
}

func TestDesignCopyTemplateApplyAndSync(t *testing.T) {
	store := testStore(t)
	s := &Service{store: store, project: "project-a", artifactRoot: t.TempDir()}
	base := dhtExample(t)
	// The USB connector is the shared block connector.
	usb := -1
	for i, c := range base.Components {
		if strings.HasPrefix(c.Designator, "J") {
			usb = i
			break
		}
	}
	if usb < 0 {
		t.Fatal("example has no connector")
	}
	base.Components[usb].Fixed = true
	tpl := createDesignFrom(t, s, base, "Block template")

	full, sections, err := s.CopyDesign(tpl.ID, 0, "Full copy", nil, false, "", "test")
	if err != nil || len(sections) != len(templateSections) {
		t.Fatalf("full copy: %v %v", sections, err)
	}
	_, _, fullDef, _ := s.revision(full.ID, 0)
	if len(fullDef.Components) != len(base.Components) || len(fullDef.Traces) != len(base.Traces) || fullDef.DerivedFrom == nil || fullDef.DerivedFrom.DesignID != tpl.ID {
		t.Fatal("full copy lost content or provenance")
	}

	block, _, err := s.CopyDesign(tpl.ID, 0, "Block 1", []string{"board", "mechanical", "fixed_components"}, false, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	_, blockRev, blockDef, _ := s.revision(block.ID, 0)
	if len(blockDef.Components) != 1 || !blockDef.Components[0].Fixed || len(blockDef.Traces) != 0 {
		t.Fatalf("template start should carry only the fixed connector, got %d components %d traces", len(blockDef.Components), len(blockDef.Traces))
	}
	if blockDef.Board.WidthNM != base.Board.WidthNM || blockDef.Mechanical == nil {
		t.Fatal("template start lost board or mechanical")
	}

	// Move the connector in the template; sync pushes it to the block.
	_, tplRev, tplDef, _ := s.revision(tpl.ID, 0)
	moved := tplDef.Components[usb]
	moved.Position.XNM += 1_000_000
	if _, _, err := s.commitOperations(tpl.ID, tplRev, tplDef, []Operation{{Type: "component.replace", Component: &moved}}, "move connector", "test"); err != nil {
		t.Fatal(err)
	}
	dry, err := s.TemplateSync(tpl.ID, true, "test")
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[int64]string{}
	for _, r := range dry {
		statuses[r.DesignID] = r.Status
	}
	if statuses[block.ID] != "would_update" || statuses[full.ID] != "would_update" {
		t.Fatalf("dry run statuses %v", statuses)
	}
	results, err := s.TemplateSync(tpl.ID, false, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Status != "updated" {
			t.Fatalf("sync result %+v", r)
		}
	}
	_, newRev, synced, _ := s.revision(block.ID, 0)
	if newRev.ID == blockRev.ID || synced.Components[0].Position.XNM != moved.Position.XNM {
		t.Fatal("template sync did not move the block connector")
	}
	again, _ := s.TemplateSync(tpl.ID, true, "test")
	for _, r := range again {
		if r.Status != "current" {
			t.Fatalf("second sync should be current: %+v", r)
		}
	}

	// Templates list and template.set.
	yes := true
	_, cur, curDef, _ := s.revision(tpl.ID, 0)
	if _, _, err := s.commitOperations(tpl.ID, cur, curDef, []Operation{{Type: "template.set", Template: &yes}}, "mark template", "test"); err != nil {
		t.Fatal(err)
	}
	list, err := s.Templates()
	if err != nil || len(list) != 1 || list[0]["design_id"] != tpl.ID {
		t.Fatalf("templates list %v %v", list, err)
	}
	if _, err := normalizeSections([]string{"bogus"}, nil); err == nil {
		t.Fatal("unknown section accepted")
	}
}

func TestLCSCCatalogParseAndMatch(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("search")
		queries = append(queries, q)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.EqualFold(q, "AP2112K-3.3TRG1"):
			_, _ = w.Write([]byte(`{"components":[{"lcsc":99999,"mfr":"AP2112K-3.3TRG1(MS)","package":"SOT-23-5","stock":10,"price":"1-:0.2"},{"lcsc":51118,"mfr":"AP2112K-3.3TRG1","package":"SOT-23-5","stock":106591,"price":"1-49:0.1089,50-149:0.0877,150-:0.0772","is_basic":false}]}`))
		default:
			_, _ = w.Write([]byte(`{"components":[]}`))
		}
	}))
	defer srv.Close()
	catalog, err := newLCSCCatalog(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := catalog.Search("AP2112K-3.3TRG1", "", 5)
	if err != nil || len(parts) != 2 || parts[0].LCSC != "C51118" {
		t.Fatalf("search parse/sort: %+v %v", parts, err)
	}
	if got := unitPriceAt(parts[0].PriceBreaks, 60); got != 0.0877 {
		t.Fatalf("price at 60 = %v", got)
	}
	if got := unitPriceAt(parts[0].PriceBreaks, 1000); got != 0.0772 {
		t.Fatalf("price at 1000 = %v", got)
	}
	def := Definition{Components: []Component{
		{ID: "u1", Designator: "U1", Name: "LDO", MPN: "AP2112K-3.3TRG1", Footprint: "SOT-23-5", Pads: []Pad{{ID: "1"}}},
		{ID: "u2", Designator: "U2", Name: "Mystery", MPN: "NOPE-1", Pads: []Pad{{ID: "1"}}},
		{ID: "u3", Designator: "U3", Name: "Set", MPN: "X", LCSC: "C1", Pads: []Pad{{ID: "1"}}},
	}}
	matches := lcscMatchBOM(catalog, &def, 5, false)
	status := map[string]LCSCMatch{}
	for _, m := range matches {
		status[m.Designators[0]] = m
	}
	if m := status["U1"]; m.Status != "matched" || !m.Exact || m.Match.LCSC != "C51118" || m.UnitUSD != 0.1089 {
		t.Fatalf("U1 match %+v", m)
	}
	if status["U2"].Status != "no_match" || status["U3"].Status != "already_set" {
		t.Fatalf("U2/U3 %+v %+v", status["U2"], status["U3"])
	}
	if _, err := newLCSCCatalog("off"); err == nil {
		t.Fatal("off should disable the catalog")
	}
	if _, err := newLCSCCatalog("ftp://x"); err == nil {
		t.Fatal("non-http catalog accepted")
	}
}

func TestFirmwareESP32APIsAndI2CReads(t *testing.T) {
	def := Definition{Components: []Component{}}
	src := `
#include <WiFi.h>
#include <Wire.h>
int ch = 0;
void setup() {
  Serial.begin(115200);
  WiFi.mode(WIFI_STA);
  WiFi.begin("lab", "secret");
  if (WiFi.status() == WL_CONNECTED) { Serial.println("wifi ok"); } else { Serial.println("wifi down"); }
  ledcSetup(ch, 5000, 8);
  ledcAttachPin(5, ch);
  ledcWrite(ch, 128);
  Wire.begin();
  Wire.beginTransmission(0x44);
  Wire.write(0x24);
  Wire.endTransmission();
  int n = Wire.requestFrom(0x44, 3);
  int msb = Wire.read();
  int lsb = Wire.read();
  int crc = Wire.read();
  Serial.print("raw=");
  Serial.println(msb * 256 + lsb);
  Serial.print("n=");
  Serial.println(n);
  Serial.print("mv=");
  Serial.println(analogReadMilliVolts(2));
  esp_sleep_enable_timer_wakeup(60000000);
  esp_deep_sleep_start();
  Serial.println("never printed");
}
void loop() { Serial.println("loop never runs"); }
`
	result, err := runFirmwareLab(&def, FirmwareOptions{Source: src, Board: "esp32s3", I2CReadData: map[string][]int{"0x44": {0x66, 0x14, 0x9A}}, AnalogInputs: map[string]float64{"2": 4095}})
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Join(result.SerialOutput, "\n")
	for _, want := range []string{"wifi ok", "raw=26132", "n=3", "mv=3300"} {
		if !strings.Contains(out, want) {
			t.Fatalf("serial missing %q:\n%s\nwarnings %v", want, out, result.Warnings)
		}
	}
	if strings.Contains(out, "never") {
		t.Fatalf("deep sleep did not stop the run:\n%s", out)
	}
	if !result.DeepSleep || result.SleepWakeUS != 60_000_000 {
		t.Fatalf("deep sleep not recorded: %+v", result)
	}
	if result.PinStates["5"] != "pwm:128" {
		t.Fatalf("ledc pin state %v", result.PinStates)
	}
	if result.Unsupported != 0 {
		t.Fatalf("unsupported statements: %v", result.Warnings)
	}
	reads := 0
	for _, tx := range result.I2CTransactions {
		if tx.Direction == "read" && tx.Address == 0x44 && tx.Status == "ack" && len(tx.Bytes) == 3 {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("I2C read not recorded: %+v", result.I2CTransactions)
	}
	down, err := runFirmwareLab(&def, FirmwareOptions{Source: src, Board: "esp32s3", WiFi: "unavailable"})
	if err != nil || !strings.Contains(strings.Join(down.SerialOutput, "\n"), "wifi down") {
		t.Fatalf("wifi unavailable not honoured: %v %v", down.SerialOutput, err)
	}
	if strings.Contains(strings.Join(down.SerialOutput, "\n"), "n=3") {
		t.Fatal("absent I2C device should NACK and return 0 bytes")
	}
}

func TestPassiveValueParsingAndParametricMatch(t *testing.T) {
	for text, want := range map[string]float64{"1k": 1000, "4.7k": 4700, "4k7": 4700, "100R": 100, "10": 10, "1M": 1e6, "5.1k": 5100} {
		if got, ok := parseResistance(text); !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("parseResistance(%q) = %v %v, want %v", text, got, ok, want)
		}
	}
	for text, want := range map[string]float64{"10uF": 10e-6, "100nF": 100e-9, "4.7pF": 4.7e-12, "1u": 1e-6} {
		if got, ok := parseCapacitance(text); !ok || math.Abs(got-want) > want*1e-9 {
			t.Errorf("parseCapacitance(%q) = %v %v, want %v", text, got, ok, want)
		}
	}
	if _, ok := parseCapacitance("10"); ok {
		t.Error("unitless capacitance accepted")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/capacitors/list.json":
			if r.URL.Query().Get("package") != "0402" || r.URL.Query().Get("capacitance") != "1e-05" {
				t.Errorf("capacitor query %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"capacitors":[{"lcsc":1,"mfr":"OUT","in_stock":false,"stock":0,"package":"0402"},{"lcsc":7,"mfr":"UNKNOWN-STOCK","in_stock":false,"stock":null,"package":"0402","is_basic":true},{"lcsc":15525,"mfr":"CL05A106MQ5NUNC","in_stock":true,"stock":null,"price1":0.0199,"package":"0402"},{"lcsc":96446,"mfr":"OTHER","in_stock":true,"stock":500,"price1":0.01,"package":"0402","is_basic":true}]}`))
		default:
			_, _ = w.Write([]byte(`{"components":[]}`))
		}
	}))
	defer srv.Close()
	catalog, _ := newLCSCCatalog(srv.URL)
	def := Definition{Components: []Component{{ID: "c1", Designator: "C1", Value: "10uF", MPN: "UNLISTED-MPN", Footprint: "C_0402_1005Metric", Pads: []Pad{{ID: "1"}}}}}
	matches := lcscMatchBOM(catalog, &def, 5, false)
	if len(matches) != 1 || matches[0].Status != "matched" || matches[0].Basis != "parametric" || matches[0].Exact || matches[0].Match.LCSC != "C96446" {
		t.Fatalf("parametric match %+v", matches)
	}
	parts, _ := catalog.SearchPassive("capacitor", 10e-6, "0402")
	if len(parts) != 3 || parts[0].LCSC != "C96446" || parts[0].StockUnknown || !parts[1].StockUnknown || !parts[2].StockUnknown {
		t.Fatalf("unknown-stock rows should be kept and ranked last: %+v", parts)
	}
}

// A library USB-C receptacle reaches the factory files correctly: its plated
// shell slots are G85 slots in the PTH program, its plastic pegs are NPTH
// holes with no copper flash, and its fine-pitch pads are real shapes.
func TestLibraryUSBCReachesFabOutputs(t *testing.T) {
	base := emptyDefinition("usb test")
	base.Board.WidthNM, base.Board.HeightNM = 20_000_000, 15_000_000
	_, def, _, err := applyOperations(&base, []Operation{{Type: "component.add_from_library", LibraryPartID: "usb-c-gct-usb4105", Position: &Position{XNM: 10_000_000, YNM: 6_000_000}}})
	if err != nil {
		t.Fatal(err)
	}
	files := manufacturingFiles(def)
	var pth, npth []byte
	for name, body := range files {
		switch {
		case strings.HasSuffix(name, "-PTH.drl"):
			pth = body
		case strings.HasSuffix(name, "-NPTH.drl"):
			npth = body
		}
	}
	if got := strings.Count(string(pth), "G85"); got != 4 {
		t.Fatalf("expected 4 plated shell slots, got %d:\n%s", got, pth)
	}
	holes := 0
	for _, line := range strings.Split(string(npth), "\n") {
		if strings.HasPrefix(line, "X") {
			holes++
		}
	}
	if holes != 2 {
		t.Fatalf("expected 2 NPTH pegs, got %d:\n%s", holes, npth)
	}
	report := verifyManufacturingFiles(def, files)
	if report.Errors != 0 {
		t.Fatalf("fabrication verifier: %+v", report.Checks)
	}
}
