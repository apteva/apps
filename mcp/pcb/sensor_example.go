package main

// sensorNodeExample is a pad-aware acceptance design used by the app examples,
// tests, and release smoke checks. It is deliberately small but electrically
// coherent: USB-C power/data, ESD protection, regulated 3V3, an ESP32-C3,
// SHTC3 I2C sensing, pull-ups, decoupling, CC pulldowns, and a status LED.
func sensorNodeExample() Definition {
	type padSpec struct {
		ID, Pin, Net, Name string
		X, Y, W, H         int64
		Shape              string
	}
	def := emptyDefinition("USB-C Temperature & Humidity Sensor Node")
	def.Board.WidthNM, def.Board.HeightNM = 60_000_000, 36_000_000
	def.Board.Layers = []Layer{{ID: "F.Cu", Kind: "copper", Order: 0}, {ID: "B.Cu", Kind: "copper", Order: 1}, {ID: "F.SilkS", Kind: "silkscreen", Order: 2}, {ID: "Edge.Cuts", Kind: "mechanical", Order: 3}}
	netNodes := map[string][]Node{}
	netNames := map[string]string{"usb5v": "USB_5V", "gnd": "GND", "v3v3": "+3V3", "usb_dp": "USB_D+", "usb_dm": "USB_D-", "cc1": "USB_CC1", "cc2": "USB_CC2", "i2c_sda": "I2C_SDA", "i2c_scl": "I2C_SCL", "led_drive": "STATUS_LED_DRIVE", "led_anode": "STATUS_LED_A"}
	addComponent := func(id, designator, name, value, mpn, footprint string, x, y, bodyW, bodyH int64, specs []padSpec) {
		component := Component{ID: id, Designator: designator, Name: name, Value: value, MPN: mpn, Footprint: footprint, Position: Position{XNM: x, YNM: y, Side: "front"}, Body: &Body{WidthNM: bodyW, HeightNM: bodyH}}
		seenPin := map[string]bool{}
		for _, spec := range specs {
			if !seenPin[spec.Pin] {
				seenPin[spec.Pin] = true
				component.Pins = append(component.Pins, Pin{ID: spec.Pin, Number: spec.ID, Name: spec.Name, ElectricalType: "passive", Pad: spec.ID})
				netNodes[spec.Net] = append(netNodes[spec.Net], Node{ComponentID: id, PinID: spec.Pin})
			}
			shape := spec.Shape
			if shape == "" {
				shape = "roundrect"
			}
			component.Pads = append(component.Pads, Pad{ID: spec.ID, PinID: spec.Pin, Shape: shape, XNM: spec.X, YNM: spec.Y, WidthNM: spec.W, HeightNM: spec.H, Layers: []string{"F.Cu"}})
		}
		def.Components = append(def.Components, component)
	}
	largeW, largeH := int64(600_000), int64(300_000)
	fineW, fineH := int64(300_000), int64(150_000)
	addComponent("j1", "J1", "USB-C receptacle", "USB 2.0 Type-C", "USB4105-GF-A", "USB_C_GCT_USB4105_16P", 5_000_000, 18_000_000, 8_000_000, 14_000_000, []padSpec{
		{ID: "vbus", Pin: "vbus", Net: "usb5v", Name: "VBUS", X: 4_000_000, Y: -5_000_000, W: largeW, H: largeH},
		{ID: "dp", Pin: "dp", Net: "usb_dp", Name: "USB_D+", X: 4_000_000, Y: -350_000, W: fineW, H: fineH},
		{ID: "dm", Pin: "dm", Net: "usb_dm", Name: "USB_D-", X: 4_000_000, Y: 350_000, W: fineW, H: fineH},
		{ID: "cc1", Pin: "cc1", Net: "cc1", Name: "CC1", X: 4_000_000, Y: 2_000_000, W: largeW, H: largeH},
		{ID: "cc2", Pin: "cc2", Net: "cc2", Name: "CC2", X: 4_000_000, Y: 3_500_000, W: largeW, H: largeH},
		{ID: "gnd", Pin: "gnd", Net: "gnd", Name: "GND", X: -4_000_000, Y: 5_000_000, W: largeW, H: largeH},
	})
	addComponent("d1", "D1", "USB ESD protection", "USBLC6-2SC6", "USBLC6-2SC6", "SOT-23-6", 16_000_000, 18_000_000, 4_000_000, 3_000_000, []padSpec{
		{ID: "dp_l", Pin: "dp", Net: "usb_dp", Name: "I/O1", X: -2_000_000, Y: -350_000, W: fineW, H: fineH}, {ID: "dp_r", Pin: "dp", Net: "usb_dp", Name: "I/O1", X: 2_000_000, Y: -350_000, W: fineW, H: fineH},
		{ID: "dm_l", Pin: "dm", Net: "usb_dm", Name: "I/O2", X: -2_000_000, Y: 350_000, W: fineW, H: fineH}, {ID: "dm_r", Pin: "dm", Net: "usb_dm", Name: "I/O2", X: 2_000_000, Y: 350_000, W: fineW, H: fineH},
		{ID: "gnd", Pin: "gnd", Net: "gnd", Name: "GND", X: 0, Y: 1_500_000, W: largeW, H: largeH},
	})
	addComponent("u1", "U1", "Wi-Fi/BLE microcontroller module", "ESP32-C3-MINI-1-N4", "ESP32-C3-MINI-1-N4", "ESP32-C3-MINI-1", 34_000_000, 18_000_000, 16_000_000, 18_000_000, []padSpec{
		{ID: "usb_dp", Pin: "usb_dp", Net: "usb_dp", Name: "USB_D+", X: -8_000_000, Y: -350_000, W: fineW, H: fineH},
		{ID: "usb_dm", Pin: "usb_dm", Net: "usb_dm", Name: "USB_D-", X: -8_000_000, Y: 350_000, W: fineW, H: fineH},
		{ID: "3v3", Pin: "3v3", Net: "v3v3", Name: "3V3", X: -8_000_000, Y: -8_000_000, W: largeW, H: largeH},
		{ID: "gnd", Pin: "gnd", Net: "gnd", Name: "GND", X: -8_000_000, Y: 8_000_000, W: largeW, H: largeH},
		{ID: "sda", Pin: "sda", Net: "i2c_sda", Name: "I2C_SDA", X: 8_000_000, Y: -400_000, W: fineW, H: fineH},
		{ID: "scl", Pin: "scl", Net: "i2c_scl", Name: "I2C_SCL", X: 8_000_000, Y: 400_000, W: fineW, H: fineH},
		{ID: "led", Pin: "led", Net: "led_drive", Name: "STATUS_LED", X: 8_000_000, Y: 2_000_000, W: largeW, H: largeH},
	})
	addComponent("u2", "U2", "Temperature and humidity sensor", "SHTC3", "SHTC3-DIS-B2.5KS", "DFN-4-2x2mm", 52_000_000, 18_000_000, 4_000_000, 4_000_000, []padSpec{
		{ID: "vdd", Pin: "vdd", Net: "v3v3", Name: "VDD", X: 0, Y: -2_000_000, W: largeW, H: largeH},
		{ID: "sda", Pin: "sda", Net: "i2c_sda", Name: "SDA", X: -2_000_000, Y: -400_000, W: fineW, H: fineH},
		{ID: "scl", Pin: "scl", Net: "i2c_scl", Name: "SCL", X: -2_000_000, Y: 400_000, W: fineW, H: fineH},
		{ID: "gnd", Pin: "gnd", Net: "gnd", Name: "GND", X: 0, Y: 2_000_000, W: largeW, H: largeH},
	})
	addComponent("u3", "U3", "3.3 V low-noise LDO", "AP2112K-3.3", "AP2112K-3.3TRG1", "SOT-23-5", 17_000_000, 5_000_000, 4_000_000, 3_000_000, []padSpec{
		{ID: "vin", Pin: "vin", Net: "usb5v", Name: "VIN", X: -2_000_000, Y: -500_000, W: largeW, H: largeH},
		{ID: "en", Pin: "en", Net: "usb5v", Name: "EN", X: -2_000_000, Y: 500_000, W: largeW, H: largeH},
		{ID: "vout", Pin: "vout", Net: "v3v3", Name: "VOUT", X: 2_000_000, Y: -500_000, W: largeW, H: largeH},
		{ID: "gnd", Pin: "gnd", Net: "gnd", Name: "GND", X: 2_000_000, Y: 500_000, W: largeW, H: largeH},
	})
	addTwoPad := func(id, designator, name, value, mpn, footprint, netA, netB string, x, y int64) {
		addComponent(id, designator, name, value, mpn, footprint, x, y, 1_200_000, 2_000_000, []padSpec{{ID: "1", Pin: "1", Net: netA, Y: -1_000_000, W: largeW, H: largeH}, {ID: "2", Pin: "2", Net: netB, Y: 1_000_000, W: largeW, H: largeH}})
	}
	addTwoPad("r1", "R1", "USB-C CC pulldown", "5.1k", "RC0402FR-075K1L", "R_0402", "cc1", "gnd", 12_000_000, 28_000_000)
	addTwoPad("r2", "R2", "USB-C CC pulldown", "5.1k", "RC0402FR-075K1L", "R_0402", "cc2", "gnd", 17_000_000, 30_000_000)
	addTwoPad("r3", "R3", "I2C pullup", "4.7k", "RC0402FR-074K7L", "R_0402", "v3v3", "i2c_sda", 45_000_000, 14_000_000)
	addTwoPad("r4", "R4", "I2C pullup", "4.7k", "RC0402FR-074K7L", "R_0402", "v3v3", "i2c_scl", 55_000_000, 20_000_000)
	addTwoPad("r5", "R5", "LED current resistor", "1k", "RC0402FR-071KL", "R_0402", "led_drive", "led_anode", 47_000_000, 24_000_000)
	addTwoPad("c1", "C1", "LDO input capacitor", "10uF", "CL05A106MQ5NUNC", "C_0402", "usb5v", "gnd", 11_000_000, 5_000_000)
	addTwoPad("c2", "C2", "LDO output capacitor", "10uF", "CL05A106MQ5NUNC", "C_0402", "v3v3", "gnd", 23_000_000, 10_000_000)
	addTwoPad("c3", "C3", "Sensor decoupling capacitor", "100nF", "CL05B104KO5NNNC", "C_0402", "v3v3", "gnd", 56_000_000, 13_000_000)
	addTwoPad("c4", "C4", "MCU decoupling capacitor", "100nF", "CL05B104KO5NNNC", "C_0402", "v3v3", "gnd", 29_000_000, 10_000_000)
	addTwoPad("d2", "D2", "Status LED", "green", "LTST-C190KGKT", "LED_0603", "led_anode", "gnd", 52_000_000, 24_000_000)

	order := []string{"usb5v", "gnd", "v3v3", "usb_dp", "usb_dm", "cc1", "cc2", "i2c_sda", "i2c_scl", "led_drive", "led_anode"}
	for _, netID := range order {
		def.Nets = append(def.Nets, Net{ID: netID, Name: netNames[netID], Nodes: netNodes[netID]})
	}
	def.Traces = []Trace{
		{ID: "usb5v_feed", NetID: "usb5v", Layer: "F.Cu", WidthNM: 500_000, Points: []Point{{XNM: 9_000_000, YNM: 13_000_000}, {XNM: 9_000_000, YNM: 4_000_000}, {XNM: 11_000_000, YNM: 4_000_000}, {XNM: 15_000_000, YNM: 4_500_000}}},
		{ID: "usb5v_en", NetID: "usb5v", Layer: "F.Cu", WidthNM: 300_000, Points: []Point{{XNM: 15_000_000, YNM: 4_500_000}, {XNM: 15_000_000, YNM: 5_500_000}}},
		{ID: "v3v3_main", NetID: "v3v3", Layer: "F.Cu", WidthNM: 400_000, Points: []Point{{XNM: 19_000_000, YNM: 4_500_000}, {XNM: 21_000_000, YNM: 4_500_000}, {XNM: 23_000_000, YNM: 9_000_000}, {XNM: 29_000_000, YNM: 9_000_000}, {XNM: 45_000_000, YNM: 9_000_000}, {XNM: 56_000_000, YNM: 9_000_000}}},
		{ID: "v3v3_mcu", NetID: "v3v3", Layer: "F.Cu", WidthNM: 300_000, Points: []Point{{XNM: 26_000_000, YNM: 10_000_000}, {XNM: 26_000_000, YNM: 9_000_000}}},
		{ID: "v3v3_r3", NetID: "v3v3", Layer: "F.Cu", WidthNM: 300_000, Points: []Point{{XNM: 45_000_000, YNM: 9_000_000}, {XNM: 45_000_000, YNM: 13_000_000}}},
		{ID: "v3v3_r4", NetID: "v3v3", Layer: "F.Cu", WidthNM: 300_000, Points: []Point{{XNM: 56_000_000, YNM: 9_000_000}, {XNM: 55_000_000, YNM: 9_000_000}, {XNM: 55_000_000, YNM: 19_000_000}}},
		{ID: "v3v3_sensor", NetID: "v3v3", Layer: "F.Cu", WidthNM: 300_000, Points: []Point{{XNM: 52_000_000, YNM: 16_000_000}, {XNM: 52_000_000, YNM: 9_000_000}}},
		{ID: "v3v3_c3", NetID: "v3v3", Layer: "F.Cu", WidthNM: 300_000, Points: []Point{{XNM: 56_000_000, YNM: 12_000_000}, {XNM: 56_000_000, YNM: 9_000_000}}},
		{ID: "usb_dp", NetID: "usb_dp", Layer: "F.Cu", WidthNM: 250_000, Points: []Point{{XNM: 9_000_000, YNM: 17_650_000}, {XNM: 14_000_000, YNM: 17_650_000}, {XNM: 18_000_000, YNM: 17_650_000}, {XNM: 26_000_000, YNM: 17_650_000}}},
		{ID: "usb_dm", NetID: "usb_dm", Layer: "F.Cu", WidthNM: 250_000, Points: []Point{{XNM: 9_000_000, YNM: 18_350_000}, {XNM: 14_000_000, YNM: 18_350_000}, {XNM: 18_000_000, YNM: 18_350_000}, {XNM: 26_000_000, YNM: 18_350_000}}},
		{ID: "cc1", NetID: "cc1", Layer: "F.Cu", WidthNM: 250_000, Points: []Point{{XNM: 9_000_000, YNM: 20_000_000}, {XNM: 12_000_000, YNM: 20_000_000}, {XNM: 12_000_000, YNM: 27_000_000}}},
		{ID: "cc2", NetID: "cc2", Layer: "F.Cu", WidthNM: 250_000, Points: []Point{{XNM: 9_000_000, YNM: 21_500_000}, {XNM: 7_000_000, YNM: 21_500_000}, {XNM: 7_000_000, YNM: 31_000_000}, {XNM: 20_000_000, YNM: 33_000_000}, {XNM: 20_000_000, YNM: 29_000_000}, {XNM: 17_000_000, YNM: 29_000_000}}},
		{ID: "sda_main", NetID: "i2c_sda", Layer: "F.Cu", WidthNM: 250_000, Points: []Point{{XNM: 42_000_000, YNM: 17_600_000}, {XNM: 50_000_000, YNM: 17_600_000}}},
		{ID: "sda_pullup", NetID: "i2c_sda", Layer: "F.Cu", WidthNM: 250_000, Points: []Point{{XNM: 45_000_000, YNM: 15_000_000}, {XNM: 45_000_000, YNM: 17_600_000}}},
		{ID: "scl_main", NetID: "i2c_scl", Layer: "F.Cu", WidthNM: 250_000, Points: []Point{{XNM: 42_000_000, YNM: 18_400_000}, {XNM: 50_000_000, YNM: 18_400_000}}},
		{ID: "scl_pullup", NetID: "i2c_scl", Layer: "F.Cu", WidthNM: 250_000, Points: []Point{{XNM: 55_000_000, YNM: 21_000_000}, {XNM: 53_000_000, YNM: 21_000_000}, {XNM: 53_000_000, YNM: 18_400_000}, {XNM: 50_000_000, YNM: 18_400_000}}},
		{ID: "led_drive", NetID: "led_drive", Layer: "F.Cu", WidthNM: 250_000, Points: []Point{{XNM: 42_000_000, YNM: 20_000_000}, {XNM: 47_000_000, YNM: 23_000_000}}},
		{ID: "led_anode", NetID: "led_anode", Layer: "F.Cu", WidthNM: 250_000, Points: []Point{{XNM: 47_000_000, YNM: 25_000_000}, {XNM: 50_000_000, YNM: 25_000_000}, {XNM: 52_000_000, YNM: 23_000_000}}},
	}
	def.Zones = []Zone{
		{ID: "gnd_plane", NetID: "gnd", Layer: "B.Cu", ClearanceNM: 250_000, Polygon: []Point{{XNM: 500_000, YNM: 500_000}, {XNM: 28_500_000, YNM: 500_000}, {XNM: 28_500_000, YNM: 7_500_000}, {XNM: 42_500_000, YNM: 7_500_000}, {XNM: 42_500_000, YNM: 500_000}, {XNM: 59_500_000, YNM: 500_000}, {XNM: 59_500_000, YNM: 35_500_000}, {XNM: 500_000, YNM: 35_500_000}}},
	}
	def.Keepouts = []Keepout{{ID: "esp32_antenna", Kind: "antenna", OwnerID: "u1", Polygon: []Point{{XNM: 29_000_000, YNM: 1_000_000}, {XNM: 42_000_000, YNM: 1_000_000}, {XNM: 42_000_000, YNM: 7_000_000}, {XNM: 29_000_000, YNM: 7_000_000}}}}
	def.DifferentialPairs = []DifferentialPair{{ID: "usb2", PositiveNetID: "usb_dp", NegativeNetID: "usb_dm", TargetOhms: 90, GapNM: 450_000, GapToleranceNM: 50_000, MaxSkewNM: 100_000}}

	// Each F.Cu ground pad gets a short thermal via into the B.Cu plane.
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			key := component.ID + ":" + pad.PinID
			isGround := false
			for _, node := range netNodes["gnd"] {
				if node.ComponentID+":"+node.PinID == key {
					isGround = true
					break
				}
			}
			if !isGround {
				continue
			}
			start := Point{XNM: component.Position.XNM + pad.XNM, YNM: component.Position.YNM + pad.YNM}
			dx := int64(800_000)
			if component.ID == "u2" || component.ID == "u3" || component.ID == "c1" {
				dx = -800_000
			}
			viaPoint := Point{XNM: start.XNM + dx, YNM: start.YNM}
			id := "gnd_" + component.ID + "_" + pad.ID
			def.Traces = append(def.Traces, Trace{ID: id + "_trace", NetID: "gnd", Layer: "F.Cu", WidthNM: 300_000, Points: []Point{start, viaPoint}})
			def.Vias = append(def.Vias, Via{ID: id + "_via", NetID: "gnd", XNM: viaPoint.XNM, YNM: viaPoint.YNM, DiameterNM: 650_000, DrillNM: 300_000, FromLayer: "F.Cu", ToLayer: "B.Cu"})
		}
	}
	return def
}
