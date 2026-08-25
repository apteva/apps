package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image/png"
	"strings"
	"testing"
)

func TestArduinoLEDWiringExampleIsNativeAndValid(t *testing.T) {
	def := arduinoLEDExample()
	if def.Wiring == nil {
		t.Fatal("missing wiring")
	}
	if got := len(def.Wiring.Parts); got != 4 {
		t.Fatalf("parts=%d", got)
	}
	if got := len(def.Wiring.Wires); got != 5 {
		t.Fatalf("wires=%d", got)
	}
	report := validateWiring(def.Wiring)
	if report.Status != "passed" {
		t.Fatalf("validation=%s checks=%#v", report.Status, report.Checks)
	}
	canonical, decoded, _, err := normalizeDefinition(mustJSON(def), def.Name)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Wiring == nil || !bytes.Contains(canonical, []byte(`"library_id":"arduino.uno.r3"`)) {
		t.Fatal("wiring lost during normalization")
	}
}

func TestWiringValidationRejectsLEDWithoutSeriesResistor(t *testing.T) {
	def := arduinoLEDExample()
	wires := def.Wiring.Wires[:0]
	for _, w := range def.Wiring.Wires {
		if w.ID != "wire-r1-led" {
			wires = append(wires, w)
		}
	}
	def.Wiring.Wires = wires
	report := validateWiring(def.Wiring)
	if report.Status != "failed" {
		t.Fatalf("status=%s", report.Status)
	}
	found := false
	for _, c := range report.Checks {
		if c.Code == "WIRING_LED_RESISTOR_MISSING" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing resistor check")
	}
}

func TestWiringRendersAndExports(t *testing.T) {
	def := arduinoLEDExample()
	svg := renderWiringSVG(&def, map[string]map[string]any{"led1": {"active": true}})
	for _, want := range []string{"ARDUINO", "UNO R3", "Pin-connected native model", "wireShadow"} {
		if !strings.Contains(string(svg), want) {
			t.Fatalf("SVG missing %q", want)
		}
	}
	if _, err := png.Decode(bytes.NewReader(renderWiringPNG(&def))); err != nil {
		t.Fatalf("PNG: %v", err)
	}
	body, err := deterministicZip(wiringTutorialFiles(&def))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 4 {
		t.Fatalf("zip files=%d", len(zr.File))
	}
	var tutorial map[string]any
	if err := json.Unmarshal(wiringTutorialJSON(&def), &tutorial); err != nil {
		t.Fatal(err)
	}
}

func TestWiringFirmwareMapsPinToLED(t *testing.T) {
	def := arduinoLEDExample()
	result, err := simulateWiring(&def, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Firmware.PinStates["9"] != "high" {
		t.Fatalf("pin 9=%q", result.Firmware.PinStates["9"])
	}
	if active, _ := result.PartStates["led1"]["active"].(bool); !active {
		t.Fatal("LED should be active")
	}
}

func TestWiringOperations(t *testing.T) {
	def := arduinoLEDExample()
	wire := def.Wiring.Wires[0]
	wire.ID = "extra"
	wire.NetID = "extra"
	wire.From = WiringEndpoint{PartID: "arduino", PinID: "D8"}
	wire.To = WiringEndpoint{PartID: "breadboard", PinID: "rail+"}
	_, next, _, err := applyOperations(&def, []Operation{{Type: "wiring.wire.add", WiringWire: &wire}})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Wiring.Wires) != 6 {
		t.Fatalf("wires=%d", len(next.Wiring.Wires))
	}
}
