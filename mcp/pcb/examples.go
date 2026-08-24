package main

func pcbExamples() map[string]any {
	def := emptyDefinition("Sensor breakout")
	def.Board.WidthNM, def.Board.HeightNM = 40_000_000, 24_000_000
	def.Components = []Component{
		{ID: "r1", Designator: "R1", Name: "Resistor", Value: "10k", MPN: "RC0805FR-0710KL", Footprint: "R_0805_2012Metric", Position: Position{XNM: 12_000_000, YNM: 12_000_000, Side: "front"}, Body: &Body{WidthNM: 2_000_000, HeightNM: 1_200_000}, Pins: []Pin{{ID: "1", Number: "1", ElectricalType: "passive", Pad: "1"}, {ID: "2", Number: "2", ElectricalType: "passive", Pad: "2"}}, Pads: []Pad{{ID: "1", PinID: "1", Shape: "roundrect", XNM: -1_000_000, WidthNM: 1_000_000, HeightNM: 1_200_000, Layers: []string{"F.Cu"}}, {ID: "2", PinID: "2", Shape: "roundrect", XNM: 1_000_000, WidthNM: 1_000_000, HeightNM: 1_200_000, Layers: []string{"F.Cu"}}}},
		{ID: "led1", Designator: "D1", Name: "LED", Value: "green", Footprint: "LED_0805_2012Metric", Position: Position{XNM: 28_000_000, YNM: 12_000_000, Side: "front"}, Body: &Body{WidthNM: 2_000_000, HeightNM: 1_200_000}, Pins: []Pin{{ID: "a", Number: "1", ElectricalType: "passive", Pad: "1"}, {ID: "k", Number: "2", ElectricalType: "passive", Pad: "2"}}, Pads: []Pad{{ID: "1", PinID: "a", Shape: "roundrect", XNM: -1_000_000, WidthNM: 1_000_000, HeightNM: 1_200_000, Layers: []string{"F.Cu"}}, {ID: "2", PinID: "k", Shape: "roundrect", XNM: 1_000_000, WidthNM: 1_000_000, HeightNM: 1_200_000, Layers: []string{"F.Cu"}}}},
	}
	def.Nets = []Net{{ID: "signal", Name: "SIGNAL", Nodes: []Node{{ComponentID: "r1", PinID: "2"}, {ComponentID: "led1", PinID: "a"}}}}
	def.Traces = []Trace{{ID: "trace-signal", NetID: "signal", Layer: "F.Cu", WidthNM: 300_000, Points: []Point{{XNM: 13_000_000, YNM: 12_000_000}, {XNM: 27_000_000, YNM: 12_000_000}}}}
	return map[string]any{
		"schema": pcbSchema, "engine": engineVersion, "units": map[string]string{"geometry": "integer nanometres", "rotation": "integer microdegrees"}, "example": def, "manufacturable_sensor_node": sensorNodeExample(),
		"operations":    []string{"board.set", "rules.set", "component.add", "component.replace", "component.remove", "placement.set", "net.add", "net.replace", "net.remove", "net.connect", "net.disconnect", "trace.add", "trace.remove", "via.add", "via.remove", "zone.add", "zone.remove", "keepout.add", "keepout.remove", "differential_pair.add", "differential_pair.remove", "net_class.add", "net_class.replace", "net_class.remove", "simulation.set"},
		"compatibility": map[string]any{"native": "apteva-pcb/v1 JSON", "v0_4_export": []string{"interactive SVG", "BOM CSV", "Gerber X2", "Excellon", "Gerber job JSON", "fabrication-verification JSON", "release ZIP", "simulation JSON", "firmware-run JSON"}, "adapter_contract": "Import/export adapters are deterministic Apteva-owned parsers and writers; no external ECAD engine is required."},
	}
}
