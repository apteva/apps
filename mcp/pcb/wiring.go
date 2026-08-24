package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	wiringSchema           = "apteva-pcb-wiring/v1"
	wiringLibrarySchema    = "apteva-pcb-wiring-library/v1"
	wiringSimulationSchema = "apteva-pcb-wiring-simulation/v1"
)

type WiringLibraryPin struct {
	ID             string  `json:"id"`
	Label          string  `json:"label"`
	X              float64 `json:"x"`
	Y              float64 `json:"y"`
	ElectricalType string  `json:"electrical_type,omitempty"`
	VoltageV       float64 `json:"voltage_v,omitempty"`
}

type WiringLibraryPart struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Kind       string             `json:"kind"`
	Width      float64            `json:"width"`
	Height     float64            `json:"height"`
	Pins       []WiringLibraryPin `json:"pins"`
	Properties map[string]any     `json:"properties,omitempty"`
}

type WiringValidation struct {
	Schema   string  `json:"schema"`
	Engine   string  `json:"engine"`
	Status   string  `json:"status"`
	Errors   int     `json:"errors"`
	Warnings int     `json:"warnings"`
	Checks   []Check `json:"checks"`
}

type WiringSimulation struct {
	Schema     string                    `json:"schema"`
	Engine     string                    `json:"engine"`
	Status     string                    `json:"status"`
	Board      string                    `json:"board"`
	Firmware   *FirmwareRunResult        `json:"firmware"`
	PartStates map[string]map[string]any `json:"part_states"`
}

func wiringCatalog() map[string]WiringLibraryPart {
	pins := []WiringLibraryPin{
		{ID: "D13", Label: "13", X: 282, Y: 25, ElectricalType: "digital"}, {ID: "D12", Label: "12", X: 264, Y: 25, ElectricalType: "digital"}, {ID: "D11", Label: "~11", X: 246, Y: 25, ElectricalType: "digital"}, {ID: "D10", Label: "~10", X: 228, Y: 25, ElectricalType: "digital"}, {ID: "D9", Label: "~9", X: 210, Y: 25, ElectricalType: "digital"}, {ID: "D8", Label: "8", X: 192, Y: 25, ElectricalType: "digital"},
		{ID: "D7", Label: "7", X: 174, Y: 25, ElectricalType: "digital"}, {ID: "D6", Label: "~6", X: 156, Y: 25, ElectricalType: "digital"}, {ID: "D5", Label: "~5", X: 138, Y: 25, ElectricalType: "digital"}, {ID: "D4", Label: "4", X: 120, Y: 25, ElectricalType: "digital"}, {ID: "D3", Label: "~3", X: 102, Y: 25, ElectricalType: "digital"}, {ID: "D2", Label: "2", X: 84, Y: 25, ElectricalType: "digital"},
		{ID: "5V", Label: "5V", X: 110, Y: 385, ElectricalType: "power", VoltageV: 5}, {ID: "3V3", Label: "3.3V", X: 92, Y: 385, ElectricalType: "power", VoltageV: 3.3}, {ID: "GND", Label: "GND", X: 128, Y: 385, ElectricalType: "ground"}, {ID: "A0", Label: "A0", X: 190, Y: 385, ElectricalType: "analog"}, {ID: "A1", Label: "A1", X: 208, Y: 385, ElectricalType: "analog"}, {ID: "A2", Label: "A2", X: 226, Y: 385, ElectricalType: "analog"}, {ID: "A3", Label: "A3", X: 244, Y: 385, ElectricalType: "analog"}, {ID: "A4", Label: "A4", X: 262, Y: 385, ElectricalType: "analog"}, {ID: "A5", Label: "A5", X: 280, Y: 385, ElectricalType: "analog"},
	}
	return map[string]WiringLibraryPart{
		"arduino.uno.r3":  {ID: "arduino.uno.r3", Name: "Arduino Uno R3", Kind: "microcontroller", Width: 330, Height: 410, Pins: pins, Properties: map[string]any{"platform": "arduino", "logic_voltage_v": 5}},
		"breadboard.half": {ID: "breadboard.half", Name: "Half-size breadboard", Kind: "breadboard", Width: 610, Height: 390, Pins: []WiringLibraryPin{{ID: "rail+", Label: "+", X: 25, Y: 45, ElectricalType: "power"}, {ID: "rail-", Label: "−", X: 25, Y: 345, ElectricalType: "ground"}}},
		"resistor.axial":  {ID: "resistor.axial", Name: "Axial resistor", Kind: "resistor", Width: 130, Height: 32, Pins: []WiringLibraryPin{{ID: "1", Label: "1", X: 0, Y: 16, ElectricalType: "passive"}, {ID: "2", Label: "2", X: 130, Y: 16, ElectricalType: "passive"}}, Properties: map[string]any{"resistance_ohms": 220}},
		"led.5mm.red":     {ID: "led.5mm.red", Name: "5 mm red LED", Kind: "led", Width: 54, Height: 88, Pins: []WiringLibraryPin{{ID: "anode", Label: "A (+)", X: 17, Y: 88, ElectricalType: "anode"}, {ID: "cathode", Label: "K (−)", X: 37, Y: 88, ElectricalType: "cathode"}}, Properties: map[string]any{"forward_voltage_v": 2, "color": "red"}},
	}
}

func wiringLibraryResponse() map[string]any {
	catalog := wiringCatalog()
	parts := make([]WiringLibraryPart, 0, len(catalog))
	for _, p := range catalog {
		parts = append(parts, p)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].ID < parts[j].ID })
	return map[string]any{"schema": wiringLibrarySchema, "engine": engineVersion, "parts": parts, "formats": []string{"svg", "png", "tutorial-json", "tutorial-zip"}}
}

func arduinoLEDExample() Definition {
	def := emptyDefinition("Arduino Uno LED breadboard tutorial")
	def.Wiring = &WiringSpec{Schema: wiringSchema, Canvas: WiringCanvas{Width: 1280, Height: 760, Grid: 10},
		Parts: []WiringPart{
			{ID: "arduino", LibraryID: "arduino.uno.r3", Label: "Arduino Uno R3", X: 70, Y: 170},
			{ID: "breadboard", LibraryID: "breadboard.half", Label: "Half breadboard", X: 565, Y: 170},
			{ID: "r1", LibraryID: "resistor.axial", Label: "R1 · 220 Ω", X: 700, Y: 330, Properties: map[string]any{"resistance_ohms": 220}},
			{ID: "led1", LibraryID: "led.5mm.red", Label: "LED1 · red", X: 930, Y: 300},
		},
		Wires: []WiringWire{
			{ID: "wire-d9-r1", NetID: "drive", Color: "#e16b32", From: WiringEndpoint{PartID: "arduino", PinID: "D9"}, To: WiringEndpoint{PartID: "r1", PinID: "1"}, Points: []WiringPoint{{X: 410, Y: 95}, {X: 520, Y: 95}, {X: 620, Y: 346}}},
			{ID: "wire-r1-led", NetID: "led-anode", Color: "#d79a24", From: WiringEndpoint{PartID: "r1", PinID: "2"}, To: WiringEndpoint{PartID: "led1", PinID: "anode"}, Points: []WiringPoint{{X: 865, Y: 346}, {X: 900, Y: 388}}},
			{ID: "wire-led-gnd", NetID: "ground", Color: "#26282b", From: WiringEndpoint{PartID: "led1", PinID: "cathode"}, To: WiringEndpoint{PartID: "arduino", PinID: "GND"}, Points: []WiringPoint{{X: 967, Y: 438}, {X: 1010, Y: 625}, {X: 470, Y: 625}, {X: 198, Y: 555}}},
			{ID: "wire-5v-rail", NetID: "power-5v", Color: "#c9433d", From: WiringEndpoint{PartID: "arduino", PinID: "5V"}, To: WiringEndpoint{PartID: "breadboard", PinID: "rail+"}, Points: []WiringPoint{{X: 180, Y: 620}, {X: 520, Y: 650}, {X: 590, Y: 215}}},
			{ID: "wire-gnd-rail", NetID: "ground", Color: "#34373b", From: WiringEndpoint{PartID: "arduino", PinID: "GND"}, To: WiringEndpoint{PartID: "breadboard", PinID: "rail-"}, Points: []WiringPoint{{X: 198, Y: 646}, {X: 540, Y: 680}, {X: 590, Y: 515}}},
		},
		Annotations: []WiringAnnotation{{ID: "note-safety", Text: "220 Ω limits LED current", X: 725, Y: 290}, {ID: "note-pin", Text: "Arduino digital pin 9", X: 355, Y: 65}},
		Steps: []WiringStep{
			{ID: "step-1", Title: "Place the parts", Instruction: "Place the Arduino Uno beside the breadboard. Insert the 220 Ω resistor and red LED as shown.", PartIDs: []string{"arduino", "breadboard", "r1", "led1"}},
			{ID: "step-2", Title: "Connect the rails", Instruction: "Connect Arduino 5V to the red rail and GND to the ground rail.", WireIDs: []string{"wire-5v-rail", "wire-gnd-rail"}},
			{ID: "step-3", Title: "Wire the LED", Instruction: "Connect D9 through the 220 Ω resistor to the LED anode. Connect the cathode to GND.", WireIDs: []string{"wire-d9-r1", "wire-r1-led", "wire-led-gnd"}},
			{ID: "step-4", Title: "Run the sketch", Instruction: "Upload the included Arduino sketch. Pin 9 goes HIGH and the LED turns on.", PartIDs: []string{"arduino", "led1"}},
		},
		Firmware: &WiringFirmware{Board: "arduino:avr:uno", PinMap: map[string]string{"9": "arduino:D9"}, Source: "void setup() {\n  pinMode(9, OUTPUT);\n}\n\nvoid loop() {\n  digitalWrite(9, HIGH);\n}\n"},
	}
	return def
}

func wiringChecks(spec *WiringSpec) []Check {
	if spec == nil {
		return nil
	}
	checks := []Check{}
	add := func(code, severity, msg string, ids ...string) {
		checks = append(checks, Check{Code: code, Severity: severity, Message: msg, ObjectIDs: ids})
	}
	if spec.Schema != wiringSchema {
		add("WIRING_SCHEMA_UNSUPPORTED", "error", "Unsupported wiring schema")
	}
	if spec.Canvas.Width < 320 || spec.Canvas.Height < 240 {
		add("WIRING_CANVAS_INVALID", "error", "Wiring canvas must be at least 320 × 240")
	}
	catalog := wiringCatalog()
	parts := map[string]WiringPart{}
	pins := map[string]map[string]bool{}
	for _, p := range spec.Parts {
		if !idValid(p.ID) {
			add("WIRING_PART_ID_INVALID", "error", "Wiring part has an invalid stable ID", p.ID)
		}
		if _, ok := parts[p.ID]; ok {
			add("WIRING_PART_ID_DUPLICATE", "error", "Wiring part ID is duplicated", p.ID)
		}
		parts[p.ID] = p
		lib, ok := catalog[p.LibraryID]
		if !ok {
			add("WIRING_LIBRARY_PART_UNKNOWN", "error", "Wiring part is not in the native reference library", p.ID, p.LibraryID)
			continue
		}
		pins[p.ID] = map[string]bool{}
		for _, pin := range lib.Pins {
			pins[p.ID][pin.ID] = true
		}
		if p.X < 0 || p.Y < 0 || p.X+lib.Width > float64(spec.Canvas.Width) || p.Y+lib.Height > float64(spec.Canvas.Height) {
			add("WIRING_PART_OUTSIDE_CANVAS", "error", "Wiring part lies outside the illustration canvas", p.ID)
		}
		if lib.Kind == "resistor" {
			if value, ok := numberProperty(p, "resistance_ohms", lib); !ok || value <= 0 {
				add("WIRING_RESISTOR_VALUE_INVALID", "error", "Resistor must have a positive resistance_ohms value", p.ID)
			}
		}
	}
	wireIDs := map[string]bool{}
	endpointNets := map[string]string{}
	connected := map[string]bool{}
	for _, w := range spec.Wires {
		if !idValid(w.ID) || !idValid(w.NetID) {
			add("WIRING_WIRE_ID_INVALID", "error", "Wire and net IDs must be stable IDs", w.ID)
		}
		if wireIDs[w.ID] {
			add("WIRING_WIRE_ID_DUPLICATE", "error", "Wiring wire ID is duplicated", w.ID)
		}
		wireIDs[w.ID] = true
		for _, ep := range []WiringEndpoint{w.From, w.To} {
			key := ep.PartID + ":" + ep.PinID
			if _, ok := parts[ep.PartID]; !ok {
				add("WIRING_ENDPOINT_PART_MISSING", "error", "Wire endpoint references a missing part", w.ID, ep.PartID)
			} else if !pins[ep.PartID][ep.PinID] {
				add("WIRING_ENDPOINT_PIN_MISSING", "error", "Wire endpoint references a missing library pin", w.ID, key)
			}
			if prior, ok := endpointNets[key]; ok && prior != w.NetID {
				add("WIRING_ENDPOINT_NET_CONFLICT", "error", "A physical pin cannot belong to two nets", w.ID, key)
			}
			endpointNets[key] = w.NetID
			connected[key] = true
		}
	}
	for _, p := range spec.Parts {
		if catalog[p.LibraryID].Kind == "led" {
			if !connected[p.ID+":anode"] || !connected[p.ID+":cathode"] {
				add("WIRING_LED_INCOMPLETE", "error", "LED anode and cathode must both be connected", p.ID)
			}
			if !ledHasSeriesResistor(spec, p.ID, catalog) {
				add("WIRING_LED_RESISTOR_MISSING", "error", "LED requires a series resistor on its anode path", p.ID)
			}
		}
	}
	for _, step := range spec.Steps {
		for _, id := range step.PartIDs {
			if _, ok := parts[id]; !ok {
				add("WIRING_STEP_PART_MISSING", "error", "Tutorial step references a missing part", step.ID, id)
			}
		}
		for _, id := range step.WireIDs {
			if !wireIDs[id] {
				add("WIRING_STEP_WIRE_MISSING", "error", "Tutorial step references a missing wire", step.ID, id)
			}
		}
	}
	if spec.Firmware != nil {
		if strings.TrimSpace(spec.Firmware.Source) == "" {
			add("WIRING_FIRMWARE_EMPTY", "error", "Saved firmware source is empty")
		}
		for _, target := range spec.Firmware.PinMap {
			bits := strings.SplitN(target, ":", 2)
			if len(bits) != 2 || !pins[bits[0]][bits[1]] {
				add("WIRING_FIRMWARE_PIN_MISSING", "error", "Firmware pin map references a missing physical pin", target)
			}
		}
	}
	if len(checks) == 0 {
		add("WIRING_READY", "info", "Wiring is pin-connected, electrically compatible, and tutorial-ready")
	}
	return checks
}

func numberProperty(p WiringPart, key string, lib WiringLibraryPart) (float64, bool) {
	v, ok := p.Properties[key]
	if !ok {
		v, ok = lib.Properties[key]
	}
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, _ := n.Float64()
		return f, true
	}
	return 0, false
}

func ledHasSeriesResistor(spec *WiringSpec, ledID string, catalog map[string]WiringLibraryPart) bool {
	for _, w := range spec.Wires {
		var other string
		if w.From.PartID == ledID && w.From.PinID == "anode" {
			other = w.To.PartID
		} else if w.To.PartID == ledID && w.To.PinID == "anode" {
			other = w.From.PartID
		}
		if other != "" {
			for _, p := range spec.Parts {
				if p.ID == other && catalog[p.LibraryID].Kind == "resistor" {
					return true
				}
			}
		}
	}
	return false
}

func validateWiring(spec *WiringSpec) WiringValidation {
	out := WiringValidation{Schema: "apteva-pcb-wiring-validation/v1", Engine: engineVersion, Status: "passed", Checks: wiringChecks(spec)}
	for _, c := range out.Checks {
		if c.Severity == "error" {
			out.Errors++
		} else if c.Severity == "warning" {
			out.Warnings++
		}
	}
	if out.Errors > 0 {
		out.Status = "failed"
	} else if out.Warnings > 0 {
		out.Status = "warning"
	}
	return out
}

func simulateWiring(def *Definition, source string, iterations int) (*WiringSimulation, error) {
	if def.Wiring == nil {
		return nil, fmt.Errorf("revision has no wiring workspace")
	}
	validation := validateWiring(def.Wiring)
	if validation.Status == "failed" {
		return nil, fmt.Errorf("wiring failed validation with %d errors", validation.Errors)
	}
	if strings.TrimSpace(source) == "" && def.Wiring.Firmware != nil {
		source = def.Wiring.Firmware.Source
	}
	board := "arduino:avr:uno"
	if def.Wiring.Firmware != nil && def.Wiring.Firmware.Board != "" {
		board = def.Wiring.Firmware.Board
	}
	firmware, err := runFirmwareLab(def, FirmwareOptions{Source: source, Language: "arduino", Board: board, Iterations: iterations})
	if err != nil {
		return nil, err
	}
	states := map[string]map[string]any{}
	for _, p := range def.Wiring.Parts {
		states[p.ID] = map[string]any{"active": false}
	}
	if def.Wiring.Firmware != nil {
		for pin, target := range def.Wiring.Firmware.PinMap {
			if strings.EqualFold(firmware.PinStates[pin], "high") {
				bits := strings.SplitN(target, ":", 2)
				if len(bits) == 2 {
					states[bits[0]]["pin_"+bits[1]] = "high"
					for _, p := range def.Wiring.Parts {
						if wiringCatalog()[p.LibraryID].Kind == "led" && ledHasSeriesResistor(def.Wiring, p.ID, wiringCatalog()) {
							states[p.ID] = map[string]any{"active": true, "brightness": 1, "color": "red"}
						}
					}
				}
			}
		}
	}
	return &WiringSimulation{Schema: wiringSimulationSchema, Engine: engineVersion, Status: "passed", Board: board, Firmware: firmware, PartStates: states}, nil
}
