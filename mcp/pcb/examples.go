package main

func pcbExamples() map[string]any {
	def := emptyDefinition("Sensor breakout")
	def.Board.WidthNM, def.Board.HeightNM = 40_000_000, 24_000_000
	def.Components = []Component{
		{ID: "r1", Designator: "R1", Name: "Resistor", Value: "10k", MPN: "RC0805FR-0710KL", Footprint: "R_0805_2012Metric", Position: Position{XNM: 12_000_000, YNM: 12_000_000, Side: "front"}, Pins: []Pin{{ID: "1", Number: "1", ElectricalType: "passive", Pad: "1"}, {ID: "2", Number: "2", ElectricalType: "passive", Pad: "2"}}},
		{ID: "led1", Designator: "D1", Name: "LED", Value: "green", Footprint: "LED_0805_2012Metric", Position: Position{XNM: 28_000_000, YNM: 12_000_000, Side: "front"}, Pins: []Pin{{ID: "a", Number: "1", ElectricalType: "passive", Pad: "1"}, {ID: "k", Number: "2", ElectricalType: "passive", Pad: "2"}}},
	}
	def.Nets = []Net{{ID: "signal", Name: "SIGNAL", Nodes: []Node{{ComponentID: "r1", PinID: "2"}, {ComponentID: "led1", PinID: "a"}}}}
	def.Traces = []Trace{{ID: "trace-signal", NetID: "signal", Layer: "F.Cu", WidthNM: 300_000, Points: []Point{{XNM: 12_000_000, YNM: 12_000_000}, {XNM: 28_000_000, YNM: 12_000_000}}}}
	return map[string]any{
		"schema": pcbSchema, "engine": engineVersion, "units": map[string]string{"geometry": "integer nanometres", "rotation": "integer microdegrees"}, "example": def,
		"operations":    []string{"board.set", "rules.set", "component.add", "component.replace", "component.remove", "placement.set", "net.add", "net.replace", "net.remove", "net.connect", "net.disconnect", "trace.add", "trace.remove", "via.add", "via.remove"},
		"compatibility": map[string]any{"native": "apteva-pcb/v1 JSON", "v0_1_export": []string{"SVG", "BOM CSV", "release ZIP"}, "adapter_contract": "Future import/export adapters are deterministic Apteva-owned parsers and writers; no external ECAD engine is required."},
	}
}
