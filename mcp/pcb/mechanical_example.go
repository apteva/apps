package main

// raspberryPi4MechanicalExample is a deterministic mechanical-reference
// fixture based on the Raspberry Pi 4 Model B's 85 x 56 mm board and standard
// four-hole mounting pattern. Component solids are conservative envelopes for
// enclosure generation, not a vendor component library or cosmetic mesh.
func raspberryPi4MechanicalExample() Definition {
	def := emptyDefinition("Raspberry Pi 4 Model B · mechanical reference")
	def.Board.WidthNM = 85_000_000
	def.Board.HeightNM = 56_000_000
	def.Board.ThicknessNM = 1_600_000
	def.Board.CornerRadiusNM = 3_000_000
	def.Board.Outline = []Point{{XNM: 3_000_000, YNM: 0}, {XNM: 82_000_000, YNM: 0}, {XNM: 84_120_000, YNM: 880_000}, {XNM: 85_000_000, YNM: 3_000_000}, {XNM: 85_000_000, YNM: 53_000_000}, {XNM: 84_120_000, YNM: 55_120_000}, {XNM: 82_000_000, YNM: 56_000_000}, {XNM: 3_000_000, YNM: 56_000_000}, {XNM: 880_000, YNM: 55_120_000}, {XNM: 0, YNM: 53_000_000}, {XNM: 0, YNM: 3_000_000}, {XNM: 880_000, YNM: 880_000}}
	def.Board.Cutouts = []BoardCutout{}
	def.Components = []Component{
		mechanicalComponent("j1", "J1", "USB-C power", "USB-C", 8_000_000, 10_500_000, 9_000_000, 8_000_000),
		mechanicalComponent("j2", "J2", "Micro HDMI 0", "Micro HDMI", 22_000_000, 4_000_000, 7_000_000, 8_000_000),
		mechanicalComponent("j3", "J3", "Micro HDMI 1", "Micro HDMI", 35_500_000, 4_000_000, 7_000_000, 8_000_000),
		mechanicalComponent("j4", "J4", "TRRS audio/video", "3.5 mm", 50_500_000, 4_000_000, 7_500_000, 12_000_000),
		mechanicalComponent("j5", "J5", "Gigabit Ethernet", "RJ45", 77_000_000, 45_000_000, 16_000_000, 21_000_000),
		mechanicalComponent("j6", "J6", "Dual USB 3", "2× USB-A", 77_000_000, 26_500_000, 17_000_000, 15_000_000),
		mechanicalComponent("j7", "J7", "Dual USB 2", "2× USB-A", 77_000_000, 10_500_000, 17_000_000, 15_000_000),
		mechanicalComponent("j8", "J8", "40-pin GPIO", "2×20", 32_500_000, 51_500_000, 51_000_000, 5_000_000),
		mechanicalComponent("j9", "J9", "microSD", "microSD", 2_000_000, 28_000_000, 12_000_000, 14_000_000),
		mechanicalComponent("u1", "U1", "Broadcom BCM2711", "SoC", 45_000_000, 31_500_000, 15_000_000, 15_000_000),
		mechanicalComponent("u2", "U2", "LPDDR4 memory", "RAM", 61_000_000, 32_000_000, 10_000_000, 14_000_000),
		mechanicalComponent("j10", "J10", "CSI camera", "FFC", 17_000_000, 31_000_000, 22_000_000, 4_000_000),
		mechanicalComponent("j11", "J11", "DSI display", "FFC", 50_000_000, 18_000_000, 22_000_000, 4_000_000),
	}
	def.Nets, def.Traces, def.Vias, def.Zones, def.Keepouts = []Net{}, []Trace{}, []Via{}, []Zone{}, []Keepout{}
	def.Mechanical = &MechanicalSpec{
		Schema:     mechanicalSchema,
		Datum:      defaultMechanicalDatum(),
		Tolerances: MechanicalTolerances{XYNM: 200_000, ZNM: 200_000, PanelOpeningFitNM: 300_000, FastenerFitNM: 150_000},
		MountingHoles: []MechanicalHole{
			{ID: "mh-sw", XNM: 3_500_000, YNM: 3_500_000, DiameterNM: 2_700_000, Fastener: "M2.5", HeadClearanceNM: 5_000_000, PositionTolNM: 100_000, DiameterTolNM: 100_000},
			{ID: "mh-se", XNM: 61_500_000, YNM: 3_500_000, DiameterNM: 2_700_000, Fastener: "M2.5", HeadClearanceNM: 5_000_000, PositionTolNM: 100_000, DiameterTolNM: 100_000},
			{ID: "mh-nw", XNM: 3_500_000, YNM: 52_500_000, DiameterNM: 2_700_000, Fastener: "M2.5", HeadClearanceNM: 5_000_000, PositionTolNM: 100_000, DiameterTolNM: 100_000},
			{ID: "mh-ne", XNM: 61_500_000, YNM: 52_500_000, DiameterNM: 2_700_000, Fastener: "M2.5", HeadClearanceNM: 5_000_000, PositionTolNM: 100_000, DiameterTolNM: 100_000},
		},
		ComponentEnvelopes: []ComponentEnvelope{
			modelEnvelope("env-usbc", "j1", "connector", "USB-C", 8, 10.5, 1.6, 5.0, 9, 8, "usb-c-receptacle"),
			modelEnvelope("env-hdmi0", "j2", "connector", "HDMI 0", 22, 4, 1.6, 4.9, 7, 8, "micro-hdmi"), modelEnvelope("env-hdmi1", "j3", "connector", "HDMI 1", 35.5, 4, 1.6, 4.9, 7, 8, "micro-hdmi"),
			modelEnvelope("env-audio", "j4", "connector", "A/V", 50.5, 4, 1.6, 7.5, 7.5, 12, "trrs-jack"),
			modelEnvelope("env-rj45", "j5", "connector", "Ethernet", 77, 45, 1.6, 15.5, 16, 21, "rj45"),
			modelEnvelope("env-usb3", "j6", "connector", "USB 3", 77, 26.5, 1.6, 17.2, 17, 15, "dual-usb-a-blue"), modelEnvelope("env-usb2", "j7", "connector", "USB 2", 77, 10.5, 1.6, 17.2, 17, 15, "dual-usb-a-black"),
			modelEnvelope("env-gpio", "j8", "header", "GPIO", 32.5, 51.5, 1.6, 10.5, 51, 5, "gpio-2x20"), modelEnvelope("env-sd", "j9", "connector", "microSD", 2, 28, -2.2, 0, 12, 14, "microsd-socket"),
			modelEnvelope("env-soc", "u1", "processor", "BCM2711", 45, 31.5, 1.6, 3.2, 15, 15, "bga-processor"), modelEnvelope("env-ram", "u2", "memory", "LPDDR4", 61, 32, 1.6, 2.9, 10, 14, "memory-bga"),
			modelEnvelope("env-csi", "j10", "connector", "CSI", 17, 31, 1.6, 6.5, 22, 4, "ffc-connector"), modelEnvelope("env-dsi", "j11", "connector", "DSI", 50, 18, 1.6, 6.5, 22, 4, "ffc-connector"),
		},
		PanelOpenings: []PanelOpening{
			{ID: "opening-usbc", ComponentID: "j1", Kind: "usb-c", Face: "south", XNM: 8_000_000, YNM: 0, ZNM: 3_200_000, WidthNM: 10_000_000, HeightNM: 4_000_000, RadiusNM: 1_500_000, DepthNM: 3_000_000},
			{ID: "opening-hdmi0", ComponentID: "j2", Kind: "micro-hdmi", Face: "south", XNM: 22_000_000, YNM: 0, ZNM: 3_200_000, WidthNM: 8_000_000, HeightNM: 4_000_000, RadiusNM: 800_000, DepthNM: 3_000_000},
			{ID: "opening-hdmi1", ComponentID: "j3", Kind: "micro-hdmi", Face: "south", XNM: 35_500_000, YNM: 0, ZNM: 3_200_000, WidthNM: 8_000_000, HeightNM: 4_000_000, RadiusNM: 800_000, DepthNM: 3_000_000},
			{ID: "opening-audio", ComponentID: "j4", Kind: "round", Face: "south", XNM: 50_500_000, YNM: 0, ZNM: 5_000_000, WidthNM: 7_500_000, HeightNM: 7_500_000, RadiusNM: 3_750_000, DepthNM: 3_000_000},
			{ID: "opening-rj45", ComponentID: "j5", Kind: "rj45", Face: "east", XNM: 85_000_000, YNM: 45_000_000, ZNM: 8_500_000, WidthNM: 18_000_000, HeightNM: 15_000_000, RadiusNM: 1_000_000, DepthNM: 3_000_000},
			{ID: "opening-usb3", ComponentID: "j6", Kind: "dual-usb-a", Face: "east", XNM: 85_000_000, YNM: 26_500_000, ZNM: 9_000_000, WidthNM: 17_000_000, HeightNM: 16_000_000, RadiusNM: 1_000_000, DepthNM: 3_000_000},
			{ID: "opening-usb2", ComponentID: "j7", Kind: "dual-usb-a", Face: "east", XNM: 85_000_000, YNM: 10_500_000, ZNM: 9_000_000, WidthNM: 17_000_000, HeightNM: 16_000_000, RadiusNM: 1_000_000, DepthNM: 3_000_000},
			{ID: "opening-microsd", ComponentID: "j9", Kind: "card-slot", Face: "west", XNM: 0, YNM: 28_000_000, ZNM: -1_000_000, WidthNM: 14_000_000, HeightNM: 3_500_000, RadiusNM: 500_000, DepthNM: 3_000_000},
		},
		ClearanceZones: []MechanicalClearance{
			clearance("clear-usbc", "j1", "cable_insertion", 8, -13, 1, 8, 12, 24), clearance("clear-hdmi0", "j2", "cable_insertion", 22, -12, 1, 7, 10, 22), clearance("clear-hdmi1", "j3", "cable_insertion", 35.5, -12, 1, 7, 10, 22),
			clearance("clear-rj45", "j5", "cable_insertion", 98, 45, 1, 18, 26, 18), clearance("clear-usb3", "j6", "cable_insertion", 98, 26.5, 1, 19, 26, 15), clearance("clear-usb2", "j7", "cable_insertion", 98, 10.5, 1, 19, 26, 15),
			clearance("clear-sd", "j9", "service", -12, 28, -3, 2, 24, 16), clearance("clear-gpio", "j8", "service", 32.5, 52, 1.6, 28, 54, 12), clearance("clear-soc", "u1", "airflow", 45, 31.5, 3.2, 25, 28, 28),
		},
	}
	return def
}

func mechanicalComponent(id, designator, name, value string, x, y, width, depth int64) Component {
	return Component{ID: id, Designator: designator, Name: name, Value: value, Position: Position{XNM: x, YNM: y, Side: "front"}, Body: &Body{WidthNM: width, HeightNM: depth}, Pins: []Pin{}, Pads: []Pad{}}
}
func envelope(id, componentID, kind, label string, x, y, z0, z1, w, d float64) ComponentEnvelope {
	return ComponentEnvelope{ID: id, ComponentID: componentID, Kind: kind, Label: label, XNM: int64(x * 1e6), YNM: int64(y * 1e6), ZMinNM: int64(z0 * 1e6), ZMaxNM: int64(z1 * 1e6), WidthNM: int64(w * 1e6), DepthNM: int64(d * 1e6)}
}
func modelEnvelope(id, componentID, kind, label string, x, y, z0, z1, w, d float64, model string) ComponentEnvelope {
	e := envelope(id, componentID, kind, label, x, y, z0, z1, w, d)
	e.Shape, e.ModelRef, e.ToleranceNM = "box", "apteva://mechanical/"+model+"/v1", 200_000
	if kind == "header" {
		e.Shape = "header"
	} else if kind == "connector" {
		e.Shape = "connector"
	}
	return e
}
func clearance(id, owner, kind string, x, y, z0, z1, w, d float64) MechanicalClearance {
	return MechanicalClearance{ID: id, OwnerID: owner, Kind: kind, XNM: int64(x * 1e6), YNM: int64(y * 1e6), ZMinNM: int64(z0 * 1e6), ZMaxNM: int64(z1 * 1e6), WidthNM: int64(w * 1e6), DepthNM: int64(d * 1e6)}
}
