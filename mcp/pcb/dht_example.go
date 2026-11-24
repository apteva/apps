package main

// dht22SensorNodeExample extends the routed USB-C ESP32-C3 sensor-node design
// with a real through-hole DHT22 and a 4-pin JST service connector. It remains
// one native apteva-pcb/v1 revision: copper, pads, simulation, and mechanical
// geometry all refer to the same component IDs.
func dht22SensorNodeExample() Definition {
	def := sensorNodeExample()
	def.Name = "ESP32-C3 DHT22 Environmental Sensor Node"
	def.Board.WidthNM, def.Board.HeightNM, def.Board.ThicknessNM = 72_000_000, 44_000_000, 1_600_000
	def.Board.CornerRadiusNM = 3_000_000
	def.Board.Outline = []Point{{XNM: 3_000_000, YNM: 0}, {XNM: 69_000_000, YNM: 0}, {XNM: 71_120_000, YNM: 880_000}, {XNM: 72_000_000, YNM: 3_000_000}, {XNM: 72_000_000, YNM: 41_000_000}, {XNM: 71_120_000, YNM: 43_120_000}, {XNM: 69_000_000, YNM: 44_000_000}, {XNM: 3_000_000, YNM: 44_000_000}, {XNM: 880_000, YNM: 43_120_000}, {XNM: 0, YNM: 41_000_000}, {XNM: 0, YNM: 3_000_000}, {XNM: 880_000, YNM: 880_000}}

	// Add one GPIO pad to the ESP32 module for the one-wire DHT signal.
	for i := range def.Components {
		if def.Components[i].ID != "u1" {
			continue
		}
		def.Components[i].Pins = append(def.Components[i].Pins, Pin{ID: "dht", Number: "GPIO4", Name: "DHT_DATA", ElectricalType: "bidirectional", Pad: "dht"})
		def.Components[i].Pads = append(def.Components[i].Pads, Pad{ID: "dht", PinID: "dht", Shape: "roundrect", XNM: 8_000_000, YNM: 4_000_000, WidthNM: 600_000, HeightNM: 300_000, Layers: []string{"F.Cu"}})
	}

	dht := Component{ID: "u4", Designator: "U4", Name: "DHT22 temperature and humidity sensor", Value: "DHT22", MPN: "AM2302", Footprint: "DHT22_4PIN_THT", Position: Position{XNM: 64_000_000, YNM: 30_000_000, Side: "front"}, Body: &Body{WidthNM: 12_000_000, HeightNM: 18_000_000}, Model: &ElectricalModel{Kind: "sensor", PinRoles: map[string]string{"vdd": "vcc", "data": "data", "ground": "gnd"}}}
	for i, pin := range []struct{ id, name string }{{"vcc", "VCC"}, {"data", "DATA"}, {"nc", "NC"}, {"gnd", "GND"}} {
		x := int64(i-1)*2_540_000 - 1_270_000
		dht.Pins = append(dht.Pins, Pin{ID: pin.id, Number: string(rune('1' + i)), Name: pin.name, ElectricalType: "passive", Pad: pin.id})
		dht.Pads = append(dht.Pads, Pad{ID: pin.id, PinID: pin.id, Shape: "circle", XNM: x, YNM: -7_000_000, WidthNM: 1_500_000, HeightNM: 1_500_000, DrillNM: 800_000, Layers: []string{"F.Cu", "B.Cu"}})
	}

	jst := Component{ID: "j2", Designator: "J2", Name: "JST-XH service connector", Value: "3V3 / DATA / AUX / GND", MPN: "B4B-XH-A", Footprint: "JST_XH_B4B-XH-A_1x04_P2.50mm", Position: Position{XNM: 64_000_000, YNM: 7_000_000, Side: "front"}, Body: &Body{WidthNM: 12_000_000, HeightNM: 8_000_000}}
	for i, pin := range []struct{ id, name string }{{"vcc", "3V3"}, {"data", "DHT_DATA"}, {"aux", "I2C_SDA"}, {"gnd", "GND"}} {
		x := int64(i)*2_500_000 - 3_750_000
		jst.Pins = append(jst.Pins, Pin{ID: pin.id, Number: string(rune('1' + i)), Name: pin.name, ElectricalType: "passive", Pad: pin.id})
		jst.Pads = append(jst.Pads, Pad{ID: pin.id, PinID: pin.id, Shape: "circle", XNM: x, WidthNM: 1_500_000, HeightNM: 1_500_000, DrillNM: 800_000, Layers: []string{"F.Cu", "B.Cu"}})
	}
	def.Components = append(def.Components, dht, jst)
	// Dedicated accessible production-test pads make power, ground, and the
	// primary sensor bus probeable without touching fine-pitch component pads.
	for _, fixture := range []struct {
		id, designator, net, label string
		x, y                       int64
	}{
		{id: "tp1", designator: "TP1", net: "usb5v", label: "USB_5V", x: 10_000_000, y: 8_000_000},
		{id: "tp2", designator: "TP2", net: "v3v3", label: "+3V3", x: 23_000_000, y: 5_000_000},
		{id: "tp3", designator: "TP3", net: "gnd", label: "GND", x: 41_000_000, y: 39_000_000},
		{id: "tp4", designator: "TP4", net: "dht_data", label: "DHT_DATA", x: 52_000_000, y: 39_000_000},
	} {
		def.Components = append(def.Components, Component{ID: fixture.id, Designator: fixture.designator, Name: fixture.label + " production test point", Value: fixture.label, MPN: "S1751-46R", Footprint: "TestPoint_Pad_D1.2mm", Position: Position{XNM: fixture.x, YNM: fixture.y, Side: "front"}, Body: &Body{WidthNM: 1_600_000, HeightNM: 1_600_000}, Pins: []Pin{{ID: "1", Number: "1", Name: fixture.label, ElectricalType: "passive", Pad: "1"}}, Pads: []Pad{{ID: "1", PinID: "1", Shape: "circle", WidthNM: 1_200_000, HeightNM: 1_200_000, Layers: []string{"F.Cu"}}}})
	}

	appendNode := func(netID string, nodes ...Node) {
		for i := range def.Nets {
			if def.Nets[i].ID == netID {
				def.Nets[i].Nodes = append(def.Nets[i].Nodes, nodes...)
				return
			}
		}
	}
	appendNode("v3v3", Node{ComponentID: "u4", PinID: "vcc"}, Node{ComponentID: "j2", PinID: "vcc"})
	appendNode("gnd", Node{ComponentID: "u4", PinID: "gnd"}, Node{ComponentID: "j2", PinID: "gnd"})
	appendNode("i2c_sda", Node{ComponentID: "j2", PinID: "aux"})
	def.Nets = append(def.Nets, Net{ID: "dht_data", Name: "DHT_DATA", Nodes: []Node{{ComponentID: "u1", PinID: "dht"}, {ComponentID: "u4", PinID: "data"}, {ComponentID: "j2", PinID: "data"}}})
	appendNode("usb5v", Node{ComponentID: "tp1", PinID: "1"})
	appendNode("v3v3", Node{ComponentID: "tp2", PinID: "1"})
	appendNode("gnd", Node{ComponentID: "tp3", PinID: "1"})
	for index := range def.Nets {
		if def.Nets[index].ID == "dht_data" {
			def.Nets[index].Nodes = append(def.Nets[index].Nodes, Node{ComponentID: "tp4", PinID: "1"})
		}
	}
	def.NetClasses = append(def.NetClasses, NetClass{ID: "sensor-one-wire", NetIDs: []string{"dht_data"}, TraceWidthNM: 250_000, ClearanceNM: 200_000, ViaDiameterNM: 650_000, ViaDrillNM: 300_000, Priority: 50})
	if plan, err := suggestRoutes(&def, RouteOptions{NetIDs: []string{"dht_data", "usb5v", "v3v3", "gnd", "i2c_sda"}, Layers: []string{"F.Cu", "B.Cu"}, GridNM: 250_000, MaxVisited: 500_000}); err == nil && len(plan.Failures) == 0 {
		for _, operation := range plan.Operations {
			_ = applyOperation(&def, operation)
		}
	}

	def.Mechanical = &MechanicalSpec{
		Schema: mechanicalSchema, Datum: defaultMechanicalDatum(),
		Tolerances: MechanicalTolerances{XYNM: 200_000, ZNM: 200_000, PanelOpeningFitNM: 300_000, FastenerFitNM: 150_000},
		MountingHoles: []MechanicalHole{
			{ID: "mh-sw", XNM: 3_500_000, YNM: 3_500_000, DiameterNM: 2_700_000, Fastener: "M2.5", HeadClearanceNM: 5_000_000, PositionTolNM: 100_000, DiameterTolNM: 100_000},
			{ID: "mh-se", XNM: 68_500_000, YNM: 3_500_000, DiameterNM: 2_700_000, Fastener: "M2.5", HeadClearanceNM: 5_000_000, PositionTolNM: 100_000, DiameterTolNM: 100_000},
			{ID: "mh-nw", XNM: 3_500_000, YNM: 40_500_000, DiameterNM: 2_700_000, Fastener: "M2.5", HeadClearanceNM: 5_000_000, PositionTolNM: 100_000, DiameterTolNM: 100_000},
			{ID: "mh-ne", XNM: 68_500_000, YNM: 40_500_000, DiameterNM: 2_700_000, Fastener: "M2.5", HeadClearanceNM: 5_000_000, PositionTolNM: 100_000, DiameterTolNM: 100_000},
		},
		ComponentEnvelopes: []ComponentEnvelope{
			modelEnvelope("env-usbc", "j1", "connector", "USB-C", 5, 18, 1.6, 5.2, 8, 14, "usb-c-receptacle"),
			modelEnvelope("env-esp32", "u1", "module", "ESP32-C3", 34, 18, 1.6, 4.8, 16, 18, "esp32-c3-module"),
			modelEnvelope("env-shtc3", "u2", "sensor", "SHTC3", 52, 18, 1.6, 2.5, 4, 4, "shtc3-dfn"),
			modelEnvelope("env-ldo", "u3", "ic", "3V3 LDO", 17, 5, 1.6, 3.2, 4, 3, "sot23-5"),
			modelEnvelope("env-dht22", "u4", "sensor", "DHT22", 64, 30, 1.6, 25.6, 12, 18, "dht22-vented"),
			modelEnvelope("env-jst", "j2", "connector", "SERVICE", 64, 7, 1.6, 9.0, 12, 8, "jst-xh-4"),
			modelEnvelope("env-esd", "d1", "ic", "ESD", 16, 18, 1.6, 3.0, 4, 3, "sot23-6"),
			modelEnvelope("env-led", "d2", "led", "STATUS", 52, 24, 1.6, 2.5, 2, 1.2, "led-0603-green"),
			modelEnvelope("env-tp1", "tp1", "testpoint", "TP1", 10, 8, 1.6, 2.2, 1.6, 1.6, "testpoint-smd"),
			modelEnvelope("env-tp2", "tp2", "testpoint", "TP2", 23, 5, 1.6, 2.2, 1.6, 1.6, "testpoint-smd"),
			modelEnvelope("env-tp3", "tp3", "testpoint", "TP3", 41, 39, 1.6, 2.2, 1.6, 1.6, "testpoint-smd"),
			modelEnvelope("env-tp4", "tp4", "testpoint", "TP4", 52, 39, 1.6, 2.2, 1.6, 1.6, "testpoint-smd"),
		},
		PanelOpenings: []PanelOpening{
			{ID: "opening-usbc", ComponentID: "j1", Kind: "usb-c", Face: "west", XNM: 0, YNM: 18_000_000, ZNM: 3_300_000, WidthNM: 10_000_000, HeightNM: 4_000_000, RadiusNM: 1_500_000, DepthNM: 3_000_000, ToleranceNM: 300_000},
			{ID: "opening-jst", ComponentID: "j2", Kind: "jst-xh", Face: "south", XNM: 64_000_000, YNM: 0, ZNM: 5_000_000, WidthNM: 14_000_000, HeightNM: 9_000_000, RadiusNM: 800_000, DepthNM: 3_000_000, ToleranceNM: 300_000},
			{ID: "opening-dht-vent", ComponentID: "u4", Kind: "sensor-vent", Face: "east", XNM: 72_000_000, YNM: 30_000_000, ZNM: 14_000_000, WidthNM: 20_000_000, HeightNM: 25_000_000, RadiusNM: 2_000_000, DepthNM: 3_000_000, ToleranceNM: 500_000},
		},
		ClearanceZones: []MechanicalClearance{
			clearance("clear-usbc", "j1", "cable_insertion", -11, 18, 1, 9, 22, 14),
			clearance("clear-jst", "j2", "cable_insertion", 64, -10, 1, 14, 16, 20),
			clearance("clear-dht", "u4", "airflow", 64, 30, 25.6, 45, 24, 28),
			clearance("clear-antenna", "u1", "antenna", 34, 8, 1.6, 18, 18, 12),
		},
	}
	// Bring-up firmware: reads the DHT22 through the generated board pin
	// header and reports over USB serial. The readiness gate runs it in the
	// behavioral runtime and checks the expected serial lines.
	def.Firmware = &FirmwareSpec{Board: "esp32c3", Iterations: 2, ExpectSerial: []string{"T=23.5", "H=45.0"}, Source: dht22NodeFirmware}
	attachNativeContracts(&def)
	return def
}

const dht22NodeFirmware = `#include <DHT.h>
#include "board_pins.h"

DHT dht(PIN_DHT_DATA, DHT22);
unsigned long readings = 0;

void setup() {
  Serial.begin(115200);
  dht.begin();
  pinMode(PIN_DHT_DATA, INPUT_PULLUP);
}

void loop() {
  float t = dht.readTemperature();
  float h = dht.readHumidity();
  if (isnan(t) || isnan(h)) {
    Serial.println("DHT22 read failed");
    return;
  }
  readings++;
  Serial.print("T=");
  Serial.println(t, 1);
  Serial.print("H=");
  Serial.println(h, 1);
  delay(2000);
}
`
