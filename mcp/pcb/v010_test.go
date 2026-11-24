package main

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
)

var (
	dhtOnce sync.Once
	dhtJSON []byte
)

// dhtExample returns a fresh copy of the DHT22 node; building it is slow.
func dhtExample(t *testing.T) Definition {
	t.Helper()
	dhtOnce.Do(func() { dhtJSON = mustJSON(dht22SensorNodeExample()) })
	var def Definition
	if err := json.Unmarshal(dhtJSON, &def); err != nil {
		t.Fatal(err)
	}
	return def
}

func codes(checks []Check) map[string]int {
	out := map[string]int{}
	for _, c := range checks {
		out[c.Code]++
	}
	return out
}

// Part reasons survive a revision and appear in the BOM.
func TestAnnotationSurvivesRevisionAndReachesBOM(t *testing.T) {
	base := testDefinition()
	_, def, _, err := applyOperations(&base, []Operation{
		{Type: "annotation.set", ComponentID: "r1", Annotation: &Annotation{Reason: "Sets LED current to 1.5 mA", DatasheetRef: "https://example.com/rc0805.pdf#page=3"}},
		{Type: "annotation.set", NetID: base.Nets[0].ID, Annotation: &Annotation{Reason: "Signal path"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(def)
	round, err := decodeDefinition(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if round.Components[findComponent(round.Components, "r1")].Reason != "Sets LED current to 1.5 mA" || round.Nets[0].Reason != "Signal path" {
		t.Fatal("annotation lost across revision")
	}
	bom := string(renderBOM(round))
	if !strings.Contains(bom, "reason,datasheet_ref") || !strings.Contains(bom, "Sets LED current to 1.5 mA") || !strings.Contains(bom, "rc0805.pdf#page=3") {
		t.Fatalf("BOM lacks rationale: %s", bom)
	}
	if _, _, _, err := applyOperations(&base, []Operation{{Type: "annotation.set", ComponentID: "nope", Annotation: &Annotation{Reason: "x"}}}); err == nil {
		t.Fatal("annotation on a missing component should fail")
	}
}

// The centroid file lists every placed part on the board and re-verifies.
func TestPickAndPlaceCoversPlacedComponents(t *testing.T) {
	def := dhtExample(t)
	body := renderPlacementCSV(&def)
	if checks := verifyPlacementCSV(&def, body); len(checks) != 0 {
		t.Fatalf("verification: %v", checks)
	}
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(records[0], ",") != "Designator,Val,Package,Mid X,Mid Y,Rotation,Layer,Type" {
		t.Fatalf("header %v", records[0])
	}
	placed := 0
	for _, c := range def.Components {
		// Test points with a part number (S1751-46R) are placed parts too.
		if len(c.Pads) > 0 && (c.MPN != "" || !strings.HasPrefix(c.Designator, "TP")) {
			placed++
		}
	}
	if len(records)-1 != placed {
		t.Fatalf("got %d placements, want %d", len(records)-1, placed)
	}
	if !bytes.Equal(body, renderPlacementCSV(&def)) {
		t.Fatal("centroid file is not deterministic")
	}
	tampered := bytes.Replace(body, []byte("U4,"), []byte("U99,"), 1)
	if c := codes(verifyPlacementCSV(&def, tampered)); c["CPL_UNKNOWN"] != 1 || c["CPL_MISSING"] != 1 {
		t.Fatalf("tampered file not caught: %v", c)
	}
}

// Switching fab profile changes the rule-check result.
func TestFabProfileSwitchChangesRuleCheck(t *testing.T) {
	def := testDefinition()
	for i := range def.Traces {
		def.Traces[i].WidthNM = 140_000 // 0.14 mm: inside JLCPCB, below OSH Park
	}
	def.Rules.MinTraceWidthNM = 100_000
	def.Rules.MinClearanceNM = 100_000
	traceWidthErrors := func(d *Definition) int {
		n := 0
		for _, c := range validateDefinition(d).Checks {
			if c.Severity == "error" && strings.Contains(c.Code, "WIDTH") {
				n++
			}
		}
		return n
	}
	if n := traceWidthErrors(&def); n != 0 {
		t.Fatalf("baseline has %d width errors", n)
	}
	for _, step := range []struct {
		profile string
		errors  bool
	}{{"jlcpcb-standard-2l", false}, {"oshpark-2l", true}, {"", false}} {
		_, next, _, err := applyOperations(&def, []Operation{{Type: "fab_profile.select", FabProfileID: step.profile}})
		if err != nil {
			t.Fatal(err)
		}
		if got := traceWidthErrors(next) > 0; got != step.errors {
			t.Fatalf("profile %q: width errors=%v want %v", step.profile, got, step.errors)
		}
		if step.profile != "" && len(next.FabProfiles) == 0 {
			t.Fatal("selected built-in profile was not copied into the design")
		}
		def = *next
	}
	if _, _, _, err := applyOperations(&def, []Operation{{Type: "fab_profile.select", FabProfileID: "no-such-fab"}}); err == nil {
		t.Fatal("unknown profile should be rejected")
	}
	// A profile never loosens a stricter design rule.
	def.Rules.MinTraceWidthNM = 500_000
	def.ActiveFabProfileID = "jlcpcb-standard-2l"
	if effectiveRules(&def).MinTraceWidthNM != 500_000 {
		t.Fatal("profile loosened a stricter design rule")
	}
}

func TestFirmwareControlFlowInputsAndTime(t *testing.T) {
	def := emptyDefinition("fw")
	source := `
#define LED 9
const int BUTTON = 2;
int count = 0;
int blink(int times) {
  int n = 0;
  for (int i = 0; i < times; i++) { n += 2; }
  return n;
}
void setup() {
  Serial.begin(9600);
  pinMode(LED, OUTPUT);
  pinMode(BUTTON, INPUT);
}
void loop() {
  if (digitalRead(BUTTON) == HIGH && analogRead(A0) > 600) {
    digitalWrite(LED, HIGH);
    count++;
  } else {
    digitalWrite(LED, LOW);
  }
  while (millis() < 50) { delay(10); }
  int level = map(analogRead(A0), 0, 1023, 0, 100);
  Serial.print("count=");
  Serial.println(count);
  Serial.print("level=");
  Serial.println(level);
  Serial.print("blink=");
  Serial.println(blink(3));
}`
	result, err := runFirmwareLab(&def, FirmwareOptions{Source: source, Iterations: 2, DigitalInputs: map[string]int{"2": 1}, AnalogInputs: map[string]float64{"A0": 900}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Unsupported != 0 || result.Status != "passed" {
		t.Fatalf("unsupported=%d status=%s warnings=%v", result.Unsupported, result.Status, result.Warnings)
	}
	want := []string{"count=1", "level=87", "blink=6", "count=2", "level=87", "blink=6"}
	if strings.Join(result.SerialOutput, "|") != strings.Join(want, "|") {
		t.Fatalf("serial %v", result.SerialOutput)
	}
	if result.PinStates["9"] != "high" || result.DurationUS != 50_000 || result.InputReads == 0 {
		t.Fatalf("pins=%v duration=%d reads=%d", result.PinStates, result.DurationUS, result.InputReads)
	}
	// Button released: the else branch runs.
	result, err = runFirmwareLab(&def, FirmwareOptions{Source: source, DigitalInputs: map[string]int{"2": 0}})
	if err != nil || result.PinStates["9"] != "low" {
		t.Fatalf("else branch: %v %v", err, result.PinStates)
	}
	// Unsupported code is counted, and runaway loops stop.
	result, _ = runFirmwareLab(&def, FirmwareOptions{Source: `void loop(){ Serial.printf("%d", 1); }`})
	if result.Unsupported == 0 {
		t.Fatal("unsupported call not counted")
	}
	result, _ = runFirmwareLab(&def, FirmwareOptions{Source: `void loop(){ while (1) { } }`})
	if result.Status != "failed" {
		t.Fatal("infinite loop was not stopped")
	}
}

// The DHT22 example sketch runs in simulation, reads a value, and passes the
// firmware readiness gate.
func TestDHT22FirmwareGateAndPinHeader(t *testing.T) {
	def := dhtExample(t)
	pins := firmwareBoardPins(&def)
	if pins["PIN_DHT_DATA"] != 4 {
		t.Fatalf("pins %v", pins)
	}
	if !strings.Contains(string(renderFirmwareHeader(&def)), "#define PIN_DHT_DATA 4") {
		t.Fatal("header lacks DHT pin")
	}
	result, checks := firmwareGate(&def)
	if len(checks) != 0 {
		t.Fatalf("gate checks %v (warnings %v)", checks, result.Warnings)
	}
	if result.SensorReads != 4 || result.SerialOutput[0] != "T=23.5" || result.SerialOutput[1] != "H=45.0" || result.PinModes["4"] != "INPUT_PULLUP" {
		t.Fatalf("run %+v", result)
	}
	report := productionReadiness(&def)
	if report.Firmware.Status != "passed" || report.Errors != 0 {
		t.Fatalf("readiness firmware=%+v errors=%d %v", report.Firmware, report.Errors, report.Checks)
	}
	def.Firmware.ExpectSerial = []string{"T=99"}
	if _, checks := firmwareGate(&def); codes(checks)["FIRMWARE_EXPECTATION"] != 1 {
		t.Fatal("expectation mismatch not caught")
	}
}

// Nexar-shaped data becomes stock and price; short stock blocks readiness.
func TestSourcingSummaryAndGate(t *testing.T) {
	def := dhtExample(t)
	byMPN, _ := bomMPNs(&def)
	parts := []any{}
	short := ""
	for mpn := range byMPN {
		stock := 5000.0
		if mpn == "AM2302" {
			stock, short = 0, mpn
		}
		parts = append(parts, map[string]any{"part": map[string]any{"mpn": mpn, "manufacturer": map[string]any{"name": "Maker"}, "totalAvail": stock, "sellers": []any{map[string]any{"offers": []any{map[string]any{"inventoryLevel": stock, "prices": []any{map[string]any{"quantity": 1.0, "price": 0.5, "currency": "USD"}, map[string]any{"quantity": 1000.0, "price": 0.1, "currency": "USD"}}}}}}}})
	}
	raw := []any{map[string]any{"provider": "nexar", "data": map[string]any{"supMultiMatch": []any{map[string]any{"parts": parts}}}}}
	summary := summarizeSourcing("nexar", raw, &def, 2, "USD")
	if summary.ShortParts != 1 || len(summary.Parts) != len(byMPN) {
		t.Fatalf("summary %+v", summary)
	}
	for _, p := range summary.Parts {
		if p.MPN == short && p.Status != "out_of_stock" {
			t.Fatalf("%s status %s", p.MPN, p.Status)
		}
		if p.MPN != short && (p.Status != "in_stock" || p.UnitPrice != 0.5 || p.Sellers != 1) {
			t.Fatalf("%s %+v", p.MPN, p)
		}
	}
	if !strings.Contains(string(renderPricedBOM(summary)), "out_of_stock") {
		t.Fatal("priced BOM lacks status")
	}
	report := productionReadinessWith(&def, ReadinessInputs{Sourcing: summary})
	if report.Sourcing.Status != "failed" || codes(report.Checks)["SOURCING_OUT_OF_STOCK"] != 1 || report.Status != "blocked" {
		t.Fatalf("sourcing gate %+v %s", report.Sourcing, report.Status)
	}
	if productionReadiness(&def).Sourcing.Status != "pending" {
		t.Fatal("no stock check should leave the gate pending")
	}
}

// A load step on the 3V3 rail dips and recovers.
func TestRegulatorLoadStep(t *testing.T) {
	def := dhtExample(t)
	result, err := simulateDefinition(&def, SimulationOptions{DurationUS: 400, StepUS: 2, Sources: []SimulationSource{{ID: "usb", NetID: "usb5v", Kind: "dc", Value: 5}, {ID: "radio", NetID: "v3v3", Kind: "load", Value: 0.3, StartUS: 100}}, Probes: []SimulationProbe{{ID: "rail", NetID: "v3v3"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.LoadSteps) != 1 {
		t.Fatalf("load steps %v warnings %v", result.LoadSteps, result.Warnings)
	}
	step := result.LoadSteps[0]
	wantPeak := 0.3 * 20e-6 / (step.OutputCapacitanceF * math.E)
	if step.OutputCapacitanceF < 10e-6 || math.Abs(step.PeakDipV-wantPeak) > 1e-6 || step.RecoveryUS <= step.ResponseTimeUS || step.MinV >= 3.3 {
		t.Fatalf("report %+v want peak %.4f", step, wantPeak)
	}
	minSeen, final := 10.0, 0.0
	for _, p := range result.Waveforms[0].Points {
		if p.TimeUS < 100 && math.Abs(p.Value-3.3) > 1e-9 {
			t.Fatalf("rail moved before the step: %v", p)
		}
		minSeen = math.Min(minSeen, p.Value)
		final = p.Value
	}
	if math.Abs(minSeen-step.MinV) > 0.01 || math.Abs(final-3.3) > 0.033 {
		t.Fatalf("waveform min %.4f (report %.4f) final %.4f", minSeen, step.MinV, final)
	}
	// Exceeding the current limit is flagged.
	for i := range def.Components {
		if def.Components[i].ID == "u3" {
			def.Components[i].Model.MaxCurrentA = 0.25
		}
	}
	result, _ = simulateDefinition(&def, SimulationOptions{DurationUS: 200, StepUS: 2, Sources: []SimulationSource{{ID: "usb", NetID: "usb5v", Kind: "dc", Value: 5}, {ID: "radio", NetID: "v3v3", Kind: "load", Value: 0.3, StartUS: 100}}})
	if !result.LoadSteps[0].CurrentLimited {
		t.Fatal("current limit not reported")
	}
}

// Measurements close the physical validation gate; a failing one blocks it.
func TestBringUpCaptureClosesPhysicalValidation(t *testing.T) {
	store := testStore(t)
	def := dhtExample(t)
	canonical, _, hash, err := normalizeDefinition(mustJSON(def), def.Name)
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.CreateDesign("p", def.Name, canonical, nil, hash, "", "")
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{store: store, project: "p", artifactRoot: t.TempDir()}
	status, err := s.BringUpStatus(d.ID, 0)
	if err != nil || status.Status != "pending" {
		t.Fatalf("%v %+v", err, status)
	}
	ids := map[string]bool{}
	inputs := []BringUpInput{}
	yes := true
	for _, check := range status.Checks {
		ids[check.ID] = true
		switch check.Kind {
		case "inspection", "firmware":
			inputs = append(inputs, BringUpInput{CheckID: check.ID, Passed: &yes})
		case "resistance":
			v := 12_000.0
			inputs = append(inputs, BringUpInput{CheckID: check.ID, Value: &v})
		case "voltage":
			v := (*check.ExpectedMin + *check.ExpectedMax) / 2
			inputs = append(inputs, BringUpInput{CheckID: check.ID, Value: &v})
		}
	}
	for _, want := range []string{"visual", "short-usb5v", "short-v3v3", "rail-usb5v", "rail-v3v3", "firmware"} {
		if !ids[want] {
			t.Fatalf("plan lacks %s: %v", want, ids)
		}
	}
	design, revision, _, _ := s.revision(d.ID, 0)
	if r := productionReadinessWith(&def, s.readinessInputs(design, revision, &def)); r.PhysicalValidation != "pending" {
		t.Fatalf("before measurements: %s", r.PhysicalValidation)
	}
	// Board A fails the 3V3 rail.
	low := 2.9
	if _, _, err := s.BringUpRecord(d.ID, 0, "A", []BringUpInput{{CheckID: "rail-v3v3", Value: &low}}, "tester"); err != nil {
		t.Fatal(err)
	}
	r := productionReadinessWith(&def, s.readinessInputs(design, revision, &def))
	if r.PhysicalValidation != "failed" || r.Status != "blocked" {
		t.Fatalf("failing board: %s %s", r.PhysicalValidation, r.Status)
	}
	// Board B passes everything.
	if status, _, err = s.BringUpRecord(d.ID, 0, "B", inputs, "tester"); err != nil || status.Status != "passed" {
		t.Fatalf("%v %+v", err, status)
	}
	r = productionReadinessWith(&def, s.readinessInputs(design, revision, &def))
	if r.PhysicalValidation != "passed" || codes(r.Checks)["PHYSICAL_VALIDATION_PASSED"] != 1 {
		t.Fatalf("passing board: %s %v", r.PhysicalValidation, r.Checks)
	}
	if _, _, err := s.BringUpRecord(d.ID, 0, "B", []BringUpInput{{CheckID: "rail-v3v3"}}, ""); err == nil {
		t.Fatal("numeric check without a value should be rejected")
	}
	if _, _, err := s.BringUpRecord(d.ID, 0, "B", []BringUpInput{{CheckID: "bogus", Passed: &yes}}, ""); err == nil {
		t.Fatal("unknown check should be rejected")
	}
}

func TestOrderPackageAndStatusFlow(t *testing.T) {
	store := testStore(t)
	def := dhtExample(t)
	canonical, _, hash, err := normalizeDefinition(mustJSON(def), def.Name)
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.CreateDesign("p", def.Name, canonical, nil, hash, "", "")
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{store: store, project: "p", artifactRoot: t.TempDir()}
	order, artifact, err := s.OrderPrepare(d.ID, 0, OrderOptions{Fabricator: "JLCPCB", Quantity: 5, Assembly: true})
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != "prepared" || order.Fabricator != "jlcpcb" || order.PackageArtifactID == nil || *order.PackageArtifactID != artifact.ID {
		t.Fatalf("order %+v", order)
	}
	body, _ := os.ReadFile(artifact.LocalPath)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"order.json", "fabrication/drill/board.drl", "fabrication/gerbers/Edge_Cuts.gbr", "assembly/pick-and-place.csv", "assembly/bom.csv"} {
		if !names[want] {
			t.Fatalf("package lacks %s: %v", want, names)
		}
	}
	quote := 18.4
	if order, err = s.OrderUpdate(order.ID, OrderUpdate{Status: "quoted", QuoteAmount: &quote, QuoteCurrency: "usd"}, "agent"); err != nil || order.QuoteCurrency != "USD" || *order.QuoteAmount != quote {
		t.Fatalf("%v %+v", err, order)
	}
	if _, err = s.OrderUpdate(order.ID, OrderUpdate{Status: "delivered"}, ""); err == nil {
		t.Fatal("quoted -> delivered should be refused")
	}
	if order, err = s.OrderUpdate(order.ID, OrderUpdate{Status: "placed", ExternalOrderID: "Y123"}, ""); err != nil || order.ExternalOrderID != "Y123" {
		t.Fatalf("%v %+v", err, order)
	}
	var history []map[string]any
	_ = json.Unmarshal(order.History, &history)
	if len(history) != 3 {
		t.Fatalf("history %v", history)
	}
	orders, err := s.Orders(d.ID)
	if err != nil || len(orders) != 1 {
		t.Fatalf("%v %v", err, orders)
	}
	overview, err := s.ReleaseOverview(d.ID, 0)
	if err != nil || len(overview["orders"].([]Order)) != 1 {
		t.Fatalf("overview %v", err)
	}
	if _, _, err := s.OrderPrepare(d.ID, 0, OrderOptions{Fabricator: "jlcpcb", Quantity: 0}); err == nil {
		t.Fatal("zero quantity should be refused")
	}
}
