package main

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// Built-in parts. Every land pattern below is transcribed from the cited
// upstream footprint (KiCad standard library v10 or Espressif's official
// KiCad library) by the .kicad_mod importer in library.go, then frozen here
// as millimetre literals so the geometry is reviewable against the source.
// Paste-only apertures are not copper and are omitted; the generator's
// upstream file hash is kept in each part's provenance reference.
//
// Coordinates are KiCad footprint coordinates: origin at the footprint
// anchor, +X right, +Y down, millimetres.

const kicadAttribution = "KiCad standard footprint library (CC-BY-SA 4.0 with design exception), https://gitlab.com/kicad/libraries/kicad-footprints"
const espressifAttribution = "Espressif KiCad library (Apache-2.0 / CC-BY-SA 4.0), https://github.com/espressif/kicad-libraries"

func mm(v float64) int64 { return mmNM(v) }

func fpSMD(id, pin, shape string, x, y, w, h, ratio float64) Pad {
	return Pad{ID: id, PinID: pin, Shape: shape, XNM: mm(x), YNM: mm(y), WidthNM: mm(w), HeightNM: mm(h), RoundrectRatio: ratio, Layers: []string{"F.Cu"}}
}

func fpTHT(id, pin, shape string, x, y, w, h, drill float64) Pad {
	return Pad{ID: id, PinID: pin, Shape: shape, XNM: mm(x), YNM: mm(y), WidthNM: mm(w), HeightNM: mm(h), DrillNM: mm(drill), Layers: []string{"F.Cu", "B.Cu"}}
}

// fpSlot is a plated pad with an oval (slotted) drill. DrillNM carries the
// slot's minor dimension (the routing tool diameter).
func fpSlot(id, pin, shape string, x, y, w, h, drillW, drillH float64) Pad {
	p := Pad{ID: id, PinID: pin, Shape: shape, XNM: mm(x), YNM: mm(y), WidthNM: mm(w), HeightNM: mm(h), DrillWidthNM: mm(drillW), DrillHeightNM: mm(drillH), Layers: []string{"F.Cu", "B.Cu"}}
	p.DrillNM = min64(p.DrillWidthNM, p.DrillHeightNM)
	return p
}

// fpNPTH is a non-plated hole (KiCad np_thru_hole) of the given diameter.
func fpNPTH(id string, x, y, d float64) Pad {
	return Pad{ID: id, PinID: "NPTH", Shape: "circle", XNM: mm(x), YNM: mm(y), WidthNM: mm(d), HeightNM: mm(d), DrillNM: mm(d), NonPlated: true, Layers: []string{"F.Cu", "B.Cu"}}
}

func fpFootprint(libraryID, mount string, cx1, cy1, cx2, cy2, bodyW, bodyH float64, model string, pads ...Pad) LibraryFootprint {
	return LibraryFootprint{LibraryID: libraryID, Mount: mount, Courtyard: rectPoints(mm(cx1), mm(cy1), mm(cx2), mm(cy2)), BodyWidth: mm(bodyW), BodyHeight: mm(bodyH), Model3DRef: model, Pads: pads}
}

func lp(number, name, kind string, functions ...string) LibraryPin {
	return LibraryPin{Number: number, Name: name, ElectricalType: kind, Functions: functions}
}

// numberedPins returns passive pins "1".."n".
func numberedPins(n int) []LibraryPin {
	out := make([]LibraryPin, 0, n)
	for i := 1; i <= n; i++ {
		number := string(rune('0' + i))
		if i >= 10 {
			number = string(rune('0'+i/10)) + string(rune('0'+i%10))
		}
		out = append(out, lp(number, number, "passive"))
	}
	return out
}

var (
	builtinOnce  sync.Once
	builtinParts []*LibraryPart
	builtinByID  map[string]*LibraryPart
)

func builtinLibrary() []*LibraryPart {
	builtinOnce.Do(func() {
		builtinParts = builtinLibraryParts()
		builtinByID = map[string]*LibraryPart{}
		for _, part := range builtinParts {
			part.Schema = librarySchema
			if part.Provenance.Source == "" {
				part.Provenance.Source = "builtin"
			}
			part.Provenance.SourceSHA256 = part.geometryHash()
			part.Designator = designatorPrefixFor(part)
			if err := validateLibraryPart(part); err != nil {
				panic("invalid built-in library part " + part.ID + ": " + err.Error())
			}
			builtinByID[part.ID] = part
		}
	})
	return builtinParts
}

// builtinLibraryPart returns a deep copy of a built-in part.
func builtinLibraryPart(id string) (*LibraryPart, bool) {
	builtinLibrary()
	part, ok := builtinByID[strings.TrimSpace(id)]
	if !ok {
		return nil, false
	}
	raw, _ := json.Marshal(part)
	var out LibraryPart
	_ = json.Unmarshal(raw, &out)
	return &out, true
}

func libraryCategories() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, part := range builtinLibrary() {
		if !seen[part.Category] {
			seen[part.Category] = true
			out = append(out, part.Category)
		}
	}
	sort.Strings(out)
	return out
}

func twoTerminal(id, name, category, libraryID string, fp LibraryFootprint, description string, keywords ...string) *LibraryPart {
	return &LibraryPart{ID: id, Name: name, Category: category, Description: description, Keywords: keywords, Footprint: fp, Pins: numberedPins(2),
		Provenance: LibraryProvenance{Reference: "KiCad " + libraryID, Attribution: kicadAttribution},
		Notes:      []string{"Generic land pattern: set value and MPN when placing; IPC-7351 nominal density per the KiCad footprint."}}
}

func builtinLibraryParts() []*LibraryPart {
	const ipcSMD = "https://www.pcb-3d.com/wordpress/wp-content/uploads/ipc-sm-782a_amendment_1_and_2.pdf"
	parts := []*LibraryPart{
		twoTerminal("r-0402", "Resistor 0402 (1005 metric)", "resistor", "Resistor_SMD:R_0402_1005Metric",
			fpFootprint("R_0402_1005Metric", "smd", -0.93, -0.47, 0.93, 0.47, 1.05, 0.54, "${KICAD10_3DMODEL_DIR}/Resistor_SMD.3dshapes/R_0402_1005Metric.step",
				fpSMD("1", "1", "roundrect", -0.51, 0, 0.54, 0.64, 0.25),
				fpSMD("2", "2", "roundrect", 0.51, 0, 0.54, 0.64, 0.25)),
			"Chip resistor, 0402 / 1005 metric", "resistor", "0402", "smd"),
		twoTerminal("r-0603", "Resistor 0603 (1608 metric)", "resistor", "Resistor_SMD:R_0603_1608Metric",
			fpFootprint("R_0603_1608Metric", "smd", -1.48, -0.73, 1.48, 0.73, 1.6, 0.825, "${KICAD10_3DMODEL_DIR}/Resistor_SMD.3dshapes/R_0603_1608Metric.step",
				fpSMD("1", "1", "roundrect", -0.825, 0, 0.8, 0.95, 0.25),
				fpSMD("2", "2", "roundrect", 0.825, 0, 0.8, 0.95, 0.25)),
			"Chip resistor, 0603 / 1608 metric", "resistor", "0603", "smd"),
		twoTerminal("r-0805", "Resistor 0805 (2012 metric)", "resistor", "Resistor_SMD:R_0805_2012Metric",
			fpFootprint("R_0805_2012Metric", "smd", -1.68, -0.95, 1.68, 0.95, 2, 1.25, "${KICAD10_3DMODEL_DIR}/Resistor_SMD.3dshapes/R_0805_2012Metric.step",
				fpSMD("1", "1", "roundrect", -0.9125, 0, 1.025, 1.4, 0.243902),
				fpSMD("2", "2", "roundrect", 0.9125, 0, 1.025, 1.4, 0.243902)),
			"Chip resistor, 0805 / 2012 metric", "resistor", "0805", "smd"),
		twoTerminal("c-0402", "Capacitor 0402 (1005 metric)", "capacitor", "Capacitor_SMD:C_0402_1005Metric",
			fpFootprint("C_0402_1005Metric", "smd", -0.91, -0.46, 0.91, 0.46, 1, 0.5, "${KICAD10_3DMODEL_DIR}/Capacitor_SMD.3dshapes/C_0402_1005Metric.step",
				fpSMD("1", "1", "roundrect", -0.48, 0, 0.56, 0.62, 0.25),
				fpSMD("2", "2", "roundrect", 0.48, 0, 0.56, 0.62, 0.25)),
			"MLCC, 0402 / 1005 metric", "capacitor", "mlcc", "0402", "smd"),
		twoTerminal("c-0603", "Capacitor 0603 (1608 metric)", "capacitor", "Capacitor_SMD:C_0603_1608Metric",
			fpFootprint("C_0603_1608Metric", "smd", -1.48, -0.73, 1.48, 0.73, 1.6, 0.8, "${KICAD10_3DMODEL_DIR}/Capacitor_SMD.3dshapes/C_0603_1608Metric.step",
				fpSMD("1", "1", "roundrect", -0.775, 0, 0.9, 0.95, 0.25),
				fpSMD("2", "2", "roundrect", 0.775, 0, 0.9, 0.95, 0.25)),
			"MLCC, 0603 / 1608 metric", "capacitor", "mlcc", "0603", "smd"),
		twoTerminal("c-0805", "Capacitor 0805 (2012 metric)", "capacitor", "Capacitor_SMD:C_0805_2012Metric",
			fpFootprint("C_0805_2012Metric", "smd", -1.7, -0.98, 1.7, 0.98, 2, 1.25, "${KICAD10_3DMODEL_DIR}/Capacitor_SMD.3dshapes/C_0805_2012Metric.step",
				fpSMD("1", "1", "roundrect", -0.95, 0, 1, 1.45, 0.25),
				fpSMD("2", "2", "roundrect", 0.95, 0, 1, 1.45, 0.25)),
			"MLCC, 0805 / 2012 metric", "capacitor", "mlcc", "0805", "smd"),
	}
	for _, part := range parts {
		part.DatasheetURL = ipcSMD
	}

	led := &LibraryPart{ID: "led-0603", Name: "LED 0603 (1608 metric)", Category: "led", Description: "Chip LED, 0603 / 1608 metric. Pad 1 is the cathode (KiCad LED convention).", Keywords: []string{"led", "0603", "indicator"},
		Footprint: fpFootprint("LED_0603_1608Metric", "smd", -1.48, -0.73, 1.48, 0.73, 1.6, 0.8, "${KICAD10_3DMODEL_DIR}/LED_SMD.3dshapes/LED_0603_1608Metric.step",
			fpSMD("1", "1", "roundrect", -0.7875, 0, 0.875, 0.95, 0.25),
			fpSMD("2", "2", "roundrect", 0.7875, 0, 0.875, 0.95, 0.25)),
		Pins:       []LibraryPin{lp("1", "K", "passive", "cathode"), lp("2", "A", "passive", "anode")},
		Model:      &ElectricalModel{Kind: "led", ForwardVoltageV: 2.0, PinRoles: map[string]string{"anode": "2", "cathode": "1"}},
		Provenance: LibraryProvenance{Reference: "KiCad LED_SMD:LED_0603_1608Metric", Attribution: kicadAttribution},
		Notes:      []string{"Forward voltage hint 2.0 V is typical for green/red; set the actual LED's value when placing."}}

	sot23 := &LibraryPart{ID: "sot-23", Name: "SOT-23 3-pin (generic)", Category: "transistor", Description: "Generic SOT-23 (TO-236AB) 3-lead package for small-signal MOSFETs/BJTs/diodes. Pin functions depend on the device.", Keywords: []string{"sot23", "mosfet", "bjt", "transistor"},
		Footprint: fpFootprint("SOT-23", "smd", -1.93, -1.7, 1.93, 1.7, 1.3, 2.9, "${KICAD10_3DMODEL_DIR}/Package_TO_SOT_SMD.3dshapes/SOT-23.step",
			fpSMD("1", "1", "roundrect", -0.9375, -0.95, 1.475, 0.6, 0.25),
			fpSMD("2", "2", "roundrect", -0.9375, 0.95, 1.475, 0.6, 0.25),
			fpSMD("3", "3", "roundrect", 0.9375, 0, 1.475, 0.6, 0.25)),
		Pins: numberedPins(3), DatasheetURL: "https://www.jedec.org/document_search?search_api_views_fulltext=TO-236",
		Provenance: LibraryProvenance{Reference: "KiCad Package_TO_SOT_SMD:SOT-23 (JEDEC TO-236 var AB)", Attribution: kicadAttribution}}

	sot23x5 := func(model string, pads ...Pad) LibraryFootprint {
		return fpFootprint("SOT-23-5", "smd", -2.05, -1.7, 2.05, 1.7, 1.6, 2.9, model, pads...)
	}
	sot23_5Pads := []Pad{
		fpSMD("1", "1", "roundrect", -1.1375, -0.95, 1.325, 0.6, 0.25),
		fpSMD("2", "2", "roundrect", -1.1375, 0, 1.325, 0.6, 0.25),
		fpSMD("3", "3", "roundrect", -1.1375, 0.95, 1.325, 0.6, 0.25),
		fpSMD("4", "4", "roundrect", 1.1375, 0.95, 1.325, 0.6, 0.25),
		fpSMD("5", "5", "roundrect", 1.1375, -0.95, 1.325, 0.6, 0.25),
	}
	sot23Model := "${KICAD10_3DMODEL_DIR}/Package_TO_SOT_SMD.3dshapes/SOT-23-5.step"
	clonePads := func(pads []Pad) []Pad {
		out := make([]Pad, len(pads))
		for i, pad := range pads {
			pad.Layers = append([]string(nil), pad.Layers...)
			out[i] = pad
		}
		return out
	}

	ap2112 := &LibraryPart{ID: "ap2112k-3.3", Name: "AP2112K-3.3 600 mA LDO (SOT-23-5)", Category: "regulator", Description: "3.3 V, 600 mA CMOS LDO with enable. Pinout per Diodes AP2112 datasheet: 1 VIN, 2 GND, 3 EN, 4 NC, 5 VOUT.", Keywords: []string{"ldo", "3v3", "regulator", "sot-23-5"},
		Value: "AP2112K-3.3", Manufacturer: "Diodes Incorporated", MPN: "AP2112K-3.3TRG1", LCSC: "C51118", DatasheetURL: "https://www.diodes.com/assets/Datasheets/AP2112.pdf",
		Footprint:  sot23x5(sot23Model, clonePads(sot23_5Pads)...),
		Pins:       []LibraryPin{lp("1", "VIN", "power_in"), lp("2", "GND", "power_in"), lp("3", "EN", "input"), lp("4", "NC", "no_connect"), lp("5", "VOUT", "power_out")},
		Model:      &ElectricalModel{Kind: "regulator", VoltageV: 3.3, DropoutVoltageV: 0.25, MaxCurrentA: 0.6, PinRoles: map[string]string{"in": "1", "out": "5", "ground": "2"}},
		Provenance: LibraryProvenance{Reference: "KiCad Package_TO_SOT_SMD:SOT-23-5 (JEDEC MO-178 AA)", Attribution: kicadAttribution},
		Notes:      []string{"Datasheet recommends 1 uF ceramic on VIN and VOUT.", "Tie EN to VIN for always-on."}}

	mcp73831 := &LibraryPart{ID: "mcp73831-sot23-5", Name: "MCP73831 single-cell LiPo charger (SOT-23-5)", Category: "power", Description: "Linear Li-Ion/Li-Po charge controller, 4.20 V (-2 variant). Pinout per Microchip DS20001984: 1 STAT, 2 VSS, 3 VBAT, 4 VDD, 5 PROG.", Keywords: []string{"lipo", "charger", "battery", "li-ion", "mcp73831"},
		Value: "MCP73831-2", Manufacturer: "Microchip Technology", MPN: "MCP73831T-2ACI/OT", DatasheetURL: "https://ww1.microchip.com/downloads/en/DeviceDoc/20001984g.pdf",
		Footprint:  sot23x5(sot23Model, clonePads(sot23_5Pads)...),
		Pins:       []LibraryPin{lp("1", "STAT", "tri_state"), lp("2", "VSS", "power_in"), lp("3", "VBAT", "power_out"), lp("4", "VDD", "power_in"), lp("5", "PROG", "input")},
		Model:      &ElectricalModel{Kind: "regulator", VoltageV: 4.2, PinRoles: map[string]string{"in": "4", "out": "3", "ground": "2"}},
		Provenance: LibraryProvenance{Reference: "KiCad Package_TO_SOT_SMD:SOT-23-5 (JEDEC MO-178 AA)", Attribution: kicadAttribution},
		Notes:      []string{"Charge current I_REG = 1000 V / R_PROG (mA with R_PROG in kOhm); e.g. 2 kOhm gives 500 mA.", "Datasheet recommends 4.7 uF on VDD and VBAT."}}

	ams1117 := &LibraryPart{ID: "ams1117-3.3", Name: "AMS1117-3.3 1 A LDO (SOT-223)", Category: "regulator", Description: "3.3 V, 1 A low-dropout regulator. Pinout: 1 GND/ADJ, 2 VOUT (and tab), 3 VIN.", Keywords: []string{"ldo", "3v3", "regulator", "sot-223", "ams1117"},
		Value: "AMS1117-3.3", Manufacturer: "Advanced Monolithic Systems", MPN: "AMS1117-3.3", LCSC: "C6186", DatasheetURL: "http://www.advanced-monolithic.com/pdf/ds1117.pdf",
		Footprint: fpFootprint("SOT-223-3_TabPin2", "smd", -4.4, -3.6, 4.4, 3.6, 3.7, 6.7, "${KICAD10_3DMODEL_DIR}/Package_TO_SOT_SMD.3dshapes/SOT-223.step",
			fpSMD("1", "1", "roundrect", -3.15, -2.3, 2, 1.5, 0.25),
			fpSMD("2", "2", "roundrect", -3.15, 0, 2, 1.5, 0.25),
			fpSMD("2-2", "2", "roundrect", 3.15, 0, 2, 3.8, 0.25),
			fpSMD("3", "3", "roundrect", -3.15, 2.3, 2, 1.5, 0.25)),
		Pins:       []LibraryPin{lp("1", "GND", "power_in", "ADJ"), lp("2", "VOUT", "power_out"), lp("3", "VIN", "power_in")},
		Model:      &ElectricalModel{Kind: "regulator", VoltageV: 3.3, DropoutVoltageV: 1.1, MaxCurrentA: 1.0, PinRoles: map[string]string{"in": "3", "out": "2", "ground": "1"}},
		Provenance: LibraryProvenance{Reference: "KiCad Package_TO_SOT_SMD:SOT-223-3_TabPin2", Attribution: kicadAttribution},
		Notes:      []string{"Datasheet requires >= 22 uF tantalum (or low-ESR equivalent) on the output for stability."}}

	usblc6 := &LibraryPart{ID: "usblc6-2sc6", Name: "USBLC6-2SC6 USB ESD protection (SOT-23-6)", Category: "protection", Description: "Two-line ESD protection for USB 2.0 data lines. Pinout per ST datasheet: 1 I/O1, 2 GND, 3 I/O2, 4 I/O2, 5 VBUS, 6 I/O1.", Keywords: []string{"esd", "tvs", "usb", "protection"},
		Value: "USBLC6-2SC6", Manufacturer: "STMicroelectronics", MPN: "USBLC6-2SC6", LCSC: "C7519", DatasheetURL: "https://www.st.com/resource/en/datasheet/usblc6-2.pdf",
		Footprint: fpFootprint("SOT-23-6", "smd", -2.05, -1.7, 2.05, 1.7, 1.6, 2.9, "${KICAD10_3DMODEL_DIR}/Package_TO_SOT_SMD.3dshapes/SOT-23-6.step",
			fpSMD("1", "1", "roundrect", -1.1375, -0.95, 1.325, 0.6, 0.25),
			fpSMD("2", "2", "roundrect", -1.1375, 0, 1.325, 0.6, 0.25),
			fpSMD("3", "3", "roundrect", -1.1375, 0.95, 1.325, 0.6, 0.25),
			fpSMD("4", "4", "roundrect", 1.1375, 0.95, 1.325, 0.6, 0.25),
			fpSMD("5", "5", "roundrect", 1.1375, 0, 1.325, 0.6, 0.25),
			fpSMD("6", "6", "roundrect", 1.1375, -0.95, 1.325, 0.6, 0.25)),
		Pins:       []LibraryPin{lp("1", "IO1", "passive", "I/O1 in"), lp("2", "GND", "passive"), lp("3", "IO2", "passive", "I/O2 in"), lp("4", "IO2", "passive", "I/O2 out"), lp("5", "VBUS", "passive"), lp("6", "IO1", "passive", "I/O1 out")},
		Provenance: LibraryProvenance{Reference: "KiCad Package_TO_SOT_SMD:SOT-23-6 (JEDEC MO-178 AB)", Attribution: kicadAttribution},
		Notes:      []string{"Pins 1/6 and 3/4 are the pass-through pairs of each protected line; route the data line across both pads."}}

	shtc3 := &LibraryPart{ID: "shtc3", Name: "Sensirion SHTC3 humidity/temperature sensor (DFN-4)", Category: "sensor", Description: "I2C humidity and temperature sensor, fixed address 0x70. Pinout per Sensirion datasheet: 1 VDD, 2 SCL, 3 SDA, 4 VSS, 5 exposed die pad.", Keywords: []string{"humidity", "temperature", "i2c", "sensirion", "shtc3"},
		Value: "SHTC3", Manufacturer: "Sensirion", MPN: "SHTC3-DIS-B2.5KS", DatasheetURL: "https://www.sensirion.com/fileadmin/user_upload/customers/sensirion/Dokumente/0_Datasheets/Humidity/Sensirion_Humidity_Sensors_SHTC3_Datasheet.pdf",
		Footprint: fpFootprint("Sensirion_DFN-4-1EP_2x2mm_P1mm_EP0.7x1.6mm", "smd", -1.45, -1.25, 1.45, 1.25, 2, 2, "${KICAD10_3DMODEL_DIR}/Sensor_Humidity.3dshapes/Sensirion_DFN-4-1EP_2x2mm_P1mm_EP0.7x1.6mm.step",
			fpSMD("1", "1", "roundrect", -0.925, -0.5, 0.55, 0.35, 0.25),
			fpSMD("2", "2", "roundrect", -0.925, 0.5, 0.55, 0.35, 0.25),
			fpSMD("3", "3", "roundrect", 0.925, 0.5, 0.55, 0.35, 0.25),
			fpSMD("4", "4", "roundrect", 0.925, -0.5, 0.55, 0.35, 0.25),
			fpSMD("5", "5", "roundrect", 0, 0, 0.7, 1.6, 0.25)),
		Pins:       []LibraryPin{lp("1", "VDD", "power_in"), lp("2", "SCL", "input"), lp("3", "SDA", "bidirectional"), lp("4", "VSS", "power_in"), lp("5", "EP", "passive", "die pad")},
		Model:      &ElectricalModel{Kind: "sensor", I2CAddress: 0x70, PinRoles: map[string]string{"vdd": "1", "scl": "2", "sda": "3", "ground": "4"}},
		Provenance: LibraryProvenance{Reference: "KiCad Sensor_Humidity:Sensirion_DFN-4-1EP_2x2mm_P1mm_EP0.7x1.6mm", Attribution: kicadAttribution},
		Notes:      []string{"The exposed die pad may be soldered and tied to VSS or left floating; it improves mechanical stability."}}

	dht22 := &LibraryPart{ID: "dht22", Name: "Aosong DHT22 / AM2302 temperature-humidity sensor (THT)", Category: "sensor", Description: "Single-wire digital humidity/temperature sensor, 4 leads on a 2.54 mm row: 1 VDD, 2 DATA, 3 NC, 4 GND.", Keywords: []string{"dht22", "am2302", "humidity", "temperature", "one-wire"},
		Value: "DHT22", Manufacturer: "Aosong", MPN: "AM2302", AltMPNs: []string{"DHT22"}, DatasheetURL: "http://akizukidenshi.com/download/ds/aosong/AM2302.pdf",
		Footprint: fpFootprint("Aosong_DHT22_15.1x7.7mm_P2.54mm", "tht", -4.1, -3.99, 4.1, 11.61, 7.7, 15.1, "",
			fpTHT("1", "1", "rect", 0, 0, 1.5, 1.5, 0.8),
			fpTHT("2", "2", "circle", 0, 2.54, 1.5, 1.5, 0.8),
			fpTHT("3", "3", "circle", 0, 5.08, 1.5, 1.5, 0.8),
			fpTHT("4", "4", "circle", 0, 7.62, 1.5, 1.5, 0.8)),
		Pins:       []LibraryPin{lp("1", "VDD", "power_in"), lp("2", "DATA", "bidirectional"), lp("3", "NC", "no_connect"), lp("4", "GND", "power_in")},
		Model:      &ElectricalModel{Kind: "sensor", PinRoles: map[string]string{"vdd": "1", "data": "2", "ground": "4"}},
		Provenance: LibraryProvenance{Reference: "Pads: KiCad Sensor:Aosong_DHT11_5.5x12.0_P2.54mm (same 4-lead 2.54 mm row, 1.5 mm pad / 0.8 mm drill); body 15.1 x 25 x 7.7 mm from the Aosong AM2302 datasheet", Attribution: kicadAttribution},
		Notes:      []string{"Body outline (7.7 x 15.1 mm footprint, 25 mm tall) is centred on the lead row; courtyard adds 0.25 mm.", "Datasheet recommends a 5.1 kOhm (short cable) pull-up on DATA."}}
	dht22.Footprint.HeightNM = mm(25)

	usbc := &LibraryPart{ID: "usb-c-gct-usb4105", Name: "USB-C receptacle, USB 2.0, GCT USB4105 (16P, top mount)", Category: "connector", Description: "USB Type-C receptacle, USB 2.0 16-pin, top-mount horizontal, 5 A. Four plated oval shell slots and two non-plated locating pegs.", Keywords: []string{"usb", "usb-c", "type-c", "receptacle", "connector"},
		Value: "USB-C", Manufacturer: "GCT", MPN: "USB4105-GF-A", AltMPNs: []string{"USB4105-15-A", "USB4105-GF-A-060", "USB4105-GF-A-120"}, DatasheetURL: "https://gct.co/files/drawings/usb4105.pdf",
		Footprint: fpFootprint("USB_C_Receptacle_GCT_USB4105-xx-A_16P_TopMnt_Horizontal", "mixed", -5.32, -4.76, 5.32, 4.18, 8.94, 7.35, "${KICAD10_3DMODEL_DIR}/Connector_USB.3dshapes/USB_C_Receptacle_GCT_USB4105-xx-A_16P_TopMnt_Horizontal.step",
			fpNPTH("NPTH", -2.89, -2.605, 0.65),
			fpNPTH("NPTH-2", 2.89, -2.605, 0.65),
			fpSMD("A1", "A1", "roundrect", -3.2, -3.68, 0.6, 1.15, 0.25),
			fpSMD("A4", "A4", "roundrect", -2.4, -3.68, 0.6, 1.15, 0.25),
			fpSMD("A5", "A5", "roundrect", -1.25, -3.68, 0.3, 1.15, 0.25),
			fpSMD("A6", "A6", "roundrect", -0.25, -3.68, 0.3, 1.15, 0.25),
			fpSMD("A7", "A7", "roundrect", 0.25, -3.68, 0.3, 1.15, 0.25),
			fpSMD("A8", "A8", "roundrect", 1.25, -3.68, 0.3, 1.15, 0.25),
			fpSMD("A9", "A9", "roundrect", 2.4, -3.68, 0.6, 1.15, 0.25),
			fpSMD("A12", "A12", "roundrect", 3.2, -3.68, 0.6, 1.15, 0.25),
			fpSMD("B1", "B1", "roundrect", 3.2, -3.68, 0.6, 1.15, 0.25),
			fpSMD("B4", "B4", "roundrect", 2.4, -3.68, 0.6, 1.15, 0.25),
			fpSMD("B5", "B5", "roundrect", 1.75, -3.68, 0.3, 1.15, 0.25),
			fpSMD("B6", "B6", "roundrect", 0.75, -3.68, 0.3, 1.15, 0.25),
			fpSMD("B7", "B7", "roundrect", -0.75, -3.68, 0.3, 1.15, 0.25),
			fpSMD("B8", "B8", "roundrect", -1.75, -3.68, 0.3, 1.15, 0.25),
			fpSMD("B9", "B9", "roundrect", -2.4, -3.68, 0.6, 1.15, 0.25),
			fpSMD("B12", "B12", "roundrect", -3.2, -3.68, 0.6, 1.15, 0.25),
			fpSlot("SH", "SH", "oval", -4.32, -3.105, 1, 2.1, 0.6, 1.7),
			fpSlot("SH-2", "SH", "oval", -4.32, 1.075, 1, 1.8, 0.6, 1.4),
			fpSlot("SH-3", "SH", "oval", 4.32, -3.105, 1, 2.1, 0.6, 1.7),
			fpSlot("SH-4", "SH", "oval", 4.32, 1.075, 1, 1.8, 0.6, 1.4)),
		Pins: []LibraryPin{
			lp("A1", "GND", "passive"), lp("A4", "VBUS", "passive"), lp("A5", "CC1", "bidirectional"), lp("A6", "D+", "bidirectional"), lp("A7", "D-", "bidirectional"), lp("A8", "SBU1", "bidirectional"), lp("A9", "VBUS", "passive"), lp("A12", "GND", "passive"),
			lp("B1", "GND", "passive"), lp("B4", "VBUS", "passive"), lp("B5", "CC2", "bidirectional"), lp("B6", "D+", "bidirectional"), lp("B7", "D-", "bidirectional"), lp("B8", "SBU2", "bidirectional"), lp("B9", "VBUS", "passive"), lp("B12", "GND", "passive"),
			lp("SH", "SHIELD", "passive"), lp("NPTH", "NPTH", "no_connect", "locating peg"),
		},
		Provenance: LibraryProvenance{Reference: "KiCad Connector_USB:USB_C_Receptacle_GCT_USB4105-xx-A_16P_TopMnt_Horizontal", Attribution: kicadAttribution},
		Notes: []string{
			"Pads A1/B12, A12/B1, A4/B9 and A9/B4 are shared lands: connect both pins of each pair to the same net.",
			"Connect A6+B6 (D+) and A7+B7 (D-) so either plug orientation works; CC1 and CC2 each need their own 5.1 kOhm pull-down for a UFP.",
			"Shell slots: 0.6 x 1.7 mm (front) and 0.6 x 1.4 mm (rear) plated slots in 1.0 x 2.1 / 1.0 x 1.8 mm oval pads; pegs: 0.65 mm NPTH.",
		}}

	qwiic := &LibraryPart{ID: "jst-sh-4-qwiic", Name: "Qwiic / STEMMA QT port, JST SH 4-pin 1.0 mm (SM04B-SRSS-TB)", Category: "connector", Description: "JST SH 1.00 mm pitch, 4 circuits, SMT side-entry, with two mounting pads. Pin order follows the SparkFun Qwiic convention: 1 GND, 2 3V3, 3 SDA, 4 SCL.", Keywords: []string{"qwiic", "stemma", "jst", "sh", "i2c", "connector"},
		Value: "Qwiic", Manufacturer: "JST", MPN: "SM04B-SRSS-TB(LF)(SN)", DatasheetURL: "http://www.jst-mfg.com/product/pdf/eng/eSH.pdf",
		Footprint: fpFootprint("JST_SH_SM04B-SRSS-TB_1x04-1MP_P1.00mm_Horizontal", "smd", -3.9, -3.28, 3.9, 3.28, 6, 4.25, "${KICAD10_3DMODEL_DIR}/Connector_JST.3dshapes/JST_SH_SM04B-SRSS-TB_1x04-1MP_P1.00mm_Horizontal.step",
			fpSMD("1", "1", "roundrect", -1.5, -2, 0.6, 1.55, 0.25),
			fpSMD("2", "2", "roundrect", -0.5, -2, 0.6, 1.55, 0.25),
			fpSMD("3", "3", "roundrect", 0.5, -2, 0.6, 1.55, 0.25),
			fpSMD("4", "4", "roundrect", 1.5, -2, 0.6, 1.55, 0.25),
			fpSMD("MP", "MP", "roundrect", -2.8, 1.875, 1.2, 1.8, 0.208333),
			fpSMD("MP-2", "MP", "roundrect", 2.8, 1.875, 1.2, 1.8, 0.208333)),
		Pins:       []LibraryPin{lp("1", "GND", "power_in"), lp("2", "3V3", "power_in"), lp("3", "SDA", "bidirectional"), lp("4", "SCL", "bidirectional"), lp("MP", "MP", "passive", "mounting pad")},
		Model:      &ElectricalModel{Kind: "digital", PinRoles: map[string]string{"ground": "1", "vdd": "2", "sda": "3", "scl": "4"}},
		Provenance: LibraryProvenance{Reference: "KiCad Connector_JST:JST_SH_SM04B-SRSS-TB_1x04-1MP_P1.00mm_Horizontal", Attribution: kicadAttribution},
		Notes:      []string{"Qwiic is 3.3 V only. Mounting pads (MP) are mechanical; tie them to GND or leave unconnected."}}

	pogoTarget := &LibraryPart{ID: "pogo-target-1x04-p2.54", Name: "Pogo-pin edge connector target, 1x04, 2.54 mm", Category: "connector", Description: "Four 2.0 mm round SMD target pads on a 2.54 mm row for a spring-loaded (pogo) programming/charging fixture. Mates with pogo-header-1x04-p2.54.", Keywords: []string{"pogo", "spring", "edge", "target", "programming", "test"},
		Value: "POGO 1x04",
		Footprint: fpFootprint("Apteva_PogoTarget_1x04_P2.54mm_D2.0mm", "smd", -1.5, -1.5, 1.5, 9.12, 2, 9.62, "",
			fpSMD("1", "1", "circle", 0, 0, 2, 2, 0),
			fpSMD("2", "2", "circle", 0, 2.54, 2, 2, 0),
			fpSMD("3", "3", "circle", 0, 5.08, 2, 2, 0),
			fpSMD("4", "4", "circle", 0, 7.62, 2, 2, 0)),
		Pins:       numberedPins(4),
		Provenance: LibraryProvenance{Reference: "Pad geometry: KiCad TestPoint:TestPoint_Pad_D2.0mm (2.0 mm circle, 3.0 mm courtyard) arrayed on the 2.54 mm pitch of the mating header", Attribution: kicadAttribution},
		Notes:      []string{"Copper-only part (no BOM line needed). Leave solder mask open and keep the pads free of silkscreen; ENIG finish is recommended for repeated spring-pin contact.", "Pitch and pin 1 position match pogo-header-1x04-p2.54 so the fixture lands 1:1."}}

	headerPads := func(n int, drill float64) []Pad {
		pads := []Pad{fpTHT("1", "1", "rect", 0, 0, 1.7, 1.7, drill)}
		for i := 2; i <= n; i++ {
			number := string(rune('0' + i))
			pads = append(pads, fpTHT(number, number, "circle", 0, float64(i-1)*2.54, 1.7, 1.7, drill))
		}
		return pads
	}
	pogoHeader := &LibraryPart{ID: "pogo-header-1x04-p2.54", Name: "Spring-loaded pogo pin header, 1x04, 2.54 mm (THT)", Category: "connector", Description: "Through-hole land pattern for a 4-position 2.54 mm spring-loaded (pogo) connector on the fixture/mating board.", Keywords: []string{"pogo", "spring", "header", "fixture", "connector"},
		Value:      "POGO HDR 1x04",
		Footprint:  fpFootprint("PinHeader_1x04_P2.54mm_Vertical", "tht", -1.77, -1.77, 1.77, 9.39, 2.54, 10.16, "", headerPads(4, 1.0)...),
		Pins:       numberedPins(4),
		Provenance: LibraryProvenance{Reference: "KiCad Connector_PinHeader_2.54mm:PinHeader_1x04_P2.54mm_Vertical (1.7 mm pad, 1.0 mm drill)", Attribution: kicadAttribution},
		Notes:      []string{"No MPN is pinned: confirm the selected spring-pin connector's tail diameter fits a 1.0 mm finished hole (most 2.54 mm single-row spring connectors do) before ordering."}}

	pir := &LibraryPart{ID: "pir-hc-sr501", Name: "HC-SR501 PIR motion sensor module (3-pin 2.54 mm socket)", Category: "sensor", Description: "Pyroelectric motion sensor module with on-board BISS0001 and regulator. Mounted through a 1x03 2.54 mm socket: 1 VCC, 2 OUT, 3 GND.", Keywords: []string{"pir", "motion", "presence", "hc-sr501", "sensor"},
		Value:      "HC-SR501",
		Footprint:  fpFootprint("PinSocket_1x03_P2.54mm_Vertical", "tht", -1.77, -1.77, 1.77, 6.85, 2.54, 7.62, "", headerPads(3, 1.0)...),
		Pins:       []LibraryPin{lp("1", "VCC", "power_in"), lp("2", "OUT", "output"), lp("3", "GND", "power_in")},
		Model:      &ElectricalModel{Kind: "digital", LogicHighV: 3.3, PinRoles: map[string]string{"vdd": "1", "out": "2", "ground": "3"}},
		Provenance: LibraryProvenance{Reference: "KiCad Connector_PinSocket_2.54mm:PinSocket_1x03_P2.54mm_Vertical; pin order from the HC-SR501 module silkscreen (VCC, OUT, GND)", Attribution: kicadAttribution},
		Notes:      []string{"Chosen over AM312 because the module's 3-pin 2.54 mm header is solderable/socketable with a standard land pattern.", "Module supply is 4.5-20 V (on-board regulator); OUT is a 3.3 V logic high, safe for 3.3 V MCUs.", "The 32 x 24 mm module body overhangs the socket; reserve that area with a mechanical envelope."}}

	oled := &LibraryPart{ID: "oled-ssd1306-0.96-i2c", Name: "0.96\" 128x64 OLED module, SSD1306, I2C (4-pin 2.54 mm)", Category: "display", Description: "Common SSD1306 OLED breakout on a 1x04 2.54 mm header: 1 GND, 2 VCC, 3 SCL, 4 SDA. I2C address 0x3C (0x3D with the address jumper moved).", Keywords: []string{"oled", "ssd1306", "display", "i2c", "128x64"},
		Value: "SSD1306 OLED", DatasheetURL: "https://cdn-shop.adafruit.com/datasheets/SSD1306.pdf",
		Footprint:  fpFootprint("PinSocket_1x04_P2.54mm_Vertical", "tht", -1.77, -1.77, 1.77, 9.39, 2.54, 10.16, "", headerPads(4, 1.0)...),
		Pins:       []LibraryPin{lp("1", "GND", "power_in"), lp("2", "VCC", "power_in"), lp("3", "SCL", "input"), lp("4", "SDA", "bidirectional")},
		Model:      &ElectricalModel{Kind: "digital", I2CAddress: 0x3C, PinRoles: map[string]string{"ground": "1", "vdd": "2", "scl": "3", "sda": "4"}},
		Provenance: LibraryProvenance{Reference: "KiCad Connector_PinSocket_2.54mm:PinSocket_1x04_P2.54mm_Vertical; SSD1306 datasheet for the I2C address", Attribution: kicadAttribution},
		Notes:      []string{"Some vendors swap GND and VCC on this header; check the module silkscreen before ordering.", "Most modules include I2C pull-ups and a 3.3 V regulator; VCC accepts 3.3-5 V on those boards."}}

	ec11 := &LibraryPart{ID: "encoder-alps-ec11e-switch", Name: "Alps EC11E rotary encoder with push switch (vertical)", Category: "encoder", Description: "11 mm incremental rotary encoder with momentary push switch. A/B quadrature with C common; S1/S2 switch; two mounting tabs in plated slots.", Keywords: []string{"encoder", "rotary", "ec11", "knob", "switch"},
		Value: "EC11E", Manufacturer: "Alps Alpine", DatasheetURL: "http://www.alps.com/prod/info/E/HTML/Encoder/Incremental/EC11/EC11E15204A3.html",
		Footprint: fpFootprint("RotaryEncoder_Alps_EC11E-Switch_Vertical_H20mm", "tht", -1.5, -4.6, 16, 9.6, 12, 11.6, "${KICAD10_3DMODEL_DIR}/Rotary_Encoder.3dshapes/RotaryEncoder_Alps_EC11E-Switch_Vertical_H20mm.step",
			fpTHT("A", "A", "rect", 0, 0, 2, 2, 1),
			fpTHT("B", "B", "circle", 0, 5, 2, 2, 1),
			fpTHT("C", "C", "circle", 0, 2.5, 2, 2, 1),
			fpSlot("MP", "MP", "rect", 7.5, -3.1, 3.2, 2, 2.8, 1.5),
			fpSlot("MP-2", "MP", "rect", 7.5, 8.1, 3.2, 2, 2.8, 1.5),
			fpTHT("S1", "S1", "circle", 14.5, 5, 2, 2, 1),
			fpTHT("S2", "S2", "circle", 14.5, 0, 2, 2, 1)),
		Pins:       []LibraryPin{lp("A", "A", "output", "quadrature A"), lp("B", "B", "output", "quadrature B"), lp("C", "C", "passive", "common"), lp("S1", "S1", "passive", "switch"), lp("S2", "S2", "passive", "switch"), lp("MP", "MP", "passive", "mounting tab")},
		Provenance: LibraryProvenance{Reference: "KiCad Rotary_Encoder:RotaryEncoder_Alps_EC11E-Switch_Vertical_H20mm", Attribution: kicadAttribution},
		Notes:      []string{"Mounting tabs: 2.8 x 1.5 mm plated slots in 3.2 x 2.0 mm pads; tie MP to GND for ESD.", "Exact EC11E variant (detents, shaft length) is chosen at sourcing; all share this land pattern."}}

	relay := &LibraryPart{ID: "relay-srd-05vdc-sl-c", Name: "SRD-05VDC-SL-C 5 V SPDT power relay (THT)", Category: "relay", Description: "Sugar-cube 10 A SPDT relay, 5 V coil. Pins per Sanyou SRD Form C: 1 COM, 2 and 5 coil, 3 NO, 4 NC.", Keywords: []string{"relay", "spdt", "srd", "songle", "sanyou", "5v"},
		Value: "SRD-05VDC-SL-C", Manufacturer: "Songle Relay", MPN: "SRD-05VDC-SL-C", DatasheetURL: "http://www.sanyourelay.ca/public/products/pdf/SRD.pdf",
		Footprint: fpFootprint("Relay_SPDT_SANYOU_SRD_Series_Form_C", "tht", -1.55, -7.95, 18.55, 7.95, 19.6, 15.4, "${KICAD10_3DMODEL_DIR}/Relay_THT.3dshapes/Relay_SPDT_SANYOU_SRD_Series_Form_C.step",
			fpTHT("1", "1", "circle", 0, 0, 3, 3, 1.3),
			fpTHT("2", "2", "circle", 1.95, 6.05, 2.5, 2.5, 1),
			fpTHT("3", "3", "circle", 14.15, 6.05, 3, 3, 1.3),
			fpTHT("4", "4", "circle", 14.2, -6, 3, 3, 1.3),
			fpTHT("5", "5", "circle", 1.95, -5.95, 2.5, 2.5, 1)),
		Pins:       []LibraryPin{lp("1", "COM", "passive"), lp("2", "COIL1", "passive"), lp("3", "NO", "passive"), lp("4", "NC", "passive"), lp("5", "COIL2", "passive")},
		Model:      &ElectricalModel{Kind: "switch", PinRoles: map[string]string{"common": "1", "no": "3", "nc": "4", "coil_a": "2", "coil_b": "5"}},
		Provenance: LibraryProvenance{Reference: "KiCad Relay_THT:Relay_SPDT_SANYOU_SRD_Series_Form_C (Songle SRD is pin- and footprint-compatible)", Attribution: kicadAttribution},
		Notes:      []string{"Drive the coil through a transistor with a flyback diode across pins 2/5.", "Mains-side traces need creepage/clearance well beyond the default rules; widen contact traces for the load current."}}
	relay.Footprint.HeightNM = mm(15.3)

	esp32c3 := &LibraryPart{ID: "esp32-c3-mini-1", Name: "ESP32-C3-MINI-1 Wi-Fi/BLE module", Category: "mcu", Description: "RISC-V ESP32-C3 module with PCB antenna, 13.2 x 16.6 x 2.4 mm, 53 lands (pad 49 = 3x3 ground array).", Keywords: []string{"esp32", "esp32-c3", "wifi", "ble", "module", "mcu", "risc-v"},
		Value: "ESP32-C3-MINI-1-N4", Manufacturer: "Espressif Systems", MPN: "ESP32-C3-MINI-1-N4", AltMPNs: []string{"ESP32-C3-MINI-1-H4"}, LCSC: "C2838502", DatasheetURL: "https://www.espressif.com/sites/default/files/documentation/esp32-c3-mini-1_datasheet_en.pdf",
		Footprint:  esp32C3Mini1Footprint(),
		Pins:       esp32C3Mini1Pins(),
		Model:      &ElectricalModel{Kind: "mcu", LogicHighV: 3.3, PinRoles: map[string]string{"ground": "1", "vdd": "3"}},
		Provenance: LibraryProvenance{Reference: "Espressif KiCad library Espressif:ESP32-C3-MINI-1 (land pattern from the ESP32-C3-MINI-1 datasheet)", Attribution: espressifAttribution},
		Notes:      []string{"Antenna is at the -Y end (pads 36-48 side); keep copper and components out of the area beyond that edge, ideally overhanging the board edge.", "GPIO18/GPIO19 are the native USB D-/D+.", "Pad 49 is nine 1.45 mm squares; the first is chamfered upstream and approximated by its bounding square."}}
	esp32c3.Footprint.HeightNM = mm(2.4)

	esp32s3 := &LibraryPart{ID: "esp32-s3-wroom-1", Name: "ESP32-S3-WROOM-1 Wi-Fi/BLE module", Category: "mcu", Description: "Dual-core Xtensa ESP32-S3 module with PCB antenna, 18 x 25.5 x 3.1 mm, 40 castellated lands plus exposed GND pad 41.", Keywords: []string{"esp32", "esp32-s3", "wifi", "ble", "module", "mcu", "usb-otg"},
		Value: "ESP32-S3-WROOM-1", Manufacturer: "Espressif Systems", MPN: "ESP32-S3-WROOM-1-N8R8", AltMPNs: []string{"ESP32-S3-WROOM-1-N4", "ESP32-S3-WROOM-1-N8", "ESP32-S3-WROOM-1-N16", "ESP32-S3-WROOM-1-N8R2", "ESP32-S3-WROOM-1-N16R8"}, DatasheetURL: "https://www.espressif.com/sites/default/files/documentation/esp32-s3-wroom-1_wroom-1u_datasheet_en.pdf",
		Footprint:  esp32S3Wroom1Footprint(),
		Pins:       esp32S3Wroom1Pins(),
		Model:      &ElectricalModel{Kind: "mcu", LogicHighV: 3.3, PinRoles: map[string]string{"ground": "1", "vdd": "2"}},
		Provenance: LibraryProvenance{Reference: "KiCad RF_Module:ESP32-S3-WROOM-1 (land pattern from the ESP32-S3-WROOM-1 datasheet)", Attribution: kicadAttribution},
		Notes: []string{
			"Antenna occupies the top 6 mm of the module (-Y end). KiCad's courtyard also reserves 15 mm either side of the antenna; that clearance is not part of this courtyard — add an antenna keepout (keepout.add) or place the antenna over the board edge.",
			"The KiCad footprint's 13 thermal vias in pad 41 are omitted; add GND vias under the exposed pad with via.add.",
			"GPIO19/GPIO20 are the native USB D-/D+.",
		}}
	esp32s3.Footprint.HeightNM = mm(3.1)

	testpoint := &LibraryPart{ID: "testpoint-pad-d1.5", Name: "Test point pad, 1.5 mm", Category: "testpoint", Description: "Round SMD test pad for bed-of-nails or probe access.", Keywords: []string{"testpoint", "test", "probe", "pad"},
		Footprint:  fpFootprint("TestPoint_Pad_D1.5mm", "smd", -1.25, -1.25, 1.25, 1.25, 1.5, 1.5, "", fpSMD("1", "1", "circle", 0, 0, 1.5, 1.5, 0)),
		Pins:       numberedPins(1),
		Provenance: LibraryProvenance{Reference: "KiCad TestPoint:TestPoint_Pad_D1.5mm", Attribution: kicadAttribution}}

	return append(parts, led, sot23, ap2112, ams1117, mcp73831, usblc6, shtc3, dht22, usbc, qwiic, pogoTarget, pogoHeader, pir, oled, ec11, relay, esp32c3, esp32s3, testpoint)
}

func esp32C3Mini1Footprint() LibraryFootprint {
	pads := []Pad{}
	// Pads 1-11: left column, x=-5.9, y=-1.3..6.7 step 0.8, rotated 90 deg
	// upstream so the 0.4 x 0.8 land is 0.8 wide in X.
	for i := 0; i < 11; i++ {
		n := itoa(1 + i)
		pads = append(pads, fpSMD(n, n, "rect", -5.9, -1.3+0.8*float64(i), 0.8, 0.4, 0))
	}
	// Pads 12-24: bottom row, y=7.6, x=-4.8..4.8 step 0.8.
	for i := 0; i < 13; i++ {
		n := itoa(12 + i)
		pads = append(pads, fpSMD(n, n, "rect", -4.8+0.8*float64(i), 7.6, 0.4, 0.8, 0))
	}
	// Pads 25-35: right column, x=5.9, y=6.7..-1.3.
	for i := 0; i < 11; i++ {
		n := itoa(25 + i)
		pads = append(pads, fpSMD(n, n, "rect", 5.9, 6.7-0.8*float64(i), 0.8, 0.4, 0))
	}
	// Pads 36-48: top row (antenna side), y=-2.2, x=4.8..-4.8.
	for i := 0; i < 13; i++ {
		n := itoa(36 + i)
		pads = append(pads, fpSMD(n, n, "rect", 4.8-0.8*float64(i), -2.2, 0.4, 0.8, 0))
	}
	// Pad 49: 3 x 3 array of 1.45 mm squares on a 1.975 mm grid.
	k := 0
	for _, x := range []float64{-1.975, 0, 1.975} {
		for _, y := range []float64{0.725, 2.7, 4.675} {
			k++
			id := "49"
			if k > 1 {
				id = "49-" + itoa(k)
			}
			pads = append(pads, fpSMD(id, "49", "rect", x, y, 1.45, 1.45, 0))
		}
	}
	// Pads 50-53: 0.7 mm corner GND lands.
	pads = append(pads, fpSMD("50", "50", "rect", 5.95, -2.25, 0.7, 0.7, 0), fpSMD("51", "51", "rect", 5.95, 7.65, 0.7, 0.7, 0), fpSMD("52", "52", "rect", -5.95, 7.65, 0.7, 0.7, 0), fpSMD("53", "53", "rect", -5.95, -2.25, 0.7, 0.7, 0))
	return fpFootprint("ESP32-C3-MINI-1", "smd", -6.8, -8.5, 6.8, 8.5, 13.2, 16.6, "${KICAD8_3RD_PARTY}/3dmodels/com_github_espressif_kicad-libraries/espressif.3dshapes/ESP32-C3-MINI-1.STEP", pads...)
}

func esp32C3Mini1Pins() []LibraryPin {
	named := map[int]LibraryPin{
		3: lp("3", "3V3", "power_in"), 5: lp("5", "IO2", "bidirectional", "GPIO2", "ADC1_CH2", "strapping"), 6: lp("6", "IO3", "bidirectional", "GPIO3", "ADC1_CH3"),
		8: lp("8", "EN", "input", "CHIP_PU"), 12: lp("12", "IO0", "bidirectional", "GPIO0", "ADC1_CH0", "XTAL_32K_P"), 13: lp("13", "IO1", "bidirectional", "GPIO1", "ADC1_CH1", "XTAL_32K_N"),
		16: lp("16", "IO10", "bidirectional", "GPIO10"), 18: lp("18", "IO4", "bidirectional", "GPIO4", "ADC1_CH4"), 19: lp("19", "IO5", "bidirectional", "GPIO5", "ADC2_CH0"),
		20: lp("20", "IO6", "bidirectional", "GPIO6"), 21: lp("21", "IO7", "bidirectional", "GPIO7"), 22: lp("22", "IO8", "bidirectional", "GPIO8", "strapping"), 23: lp("23", "IO9", "bidirectional", "GPIO9", "BOOT strapping"),
		26: lp("26", "IO18", "bidirectional", "GPIO18", "USB_D-"), 27: lp("27", "IO19", "bidirectional", "GPIO19", "USB_D+"), 30: lp("30", "IO20", "bidirectional", "GPIO20", "U0RXD"), 31: lp("31", "IO21", "bidirectional", "GPIO21", "U0TXD"),
	}
	ground := map[int]bool{1: true, 2: true, 11: true, 14: true}
	for n := 36; n <= 53; n++ {
		ground[n] = true
	}
	out := []LibraryPin{}
	for n := 1; n <= 53; n++ {
		switch {
		case ground[n]:
			out = append(out, lp(itoa(n), "GND", "power_in"))
		case named[n].Number != "":
			out = append(out, named[n])
		default:
			out = append(out, lp(itoa(n), "NC", "no_connect"))
		}
	}
	return out
}

func esp32S3Wroom1Footprint() LibraryFootprint {
	pads := []Pad{}
	// Pads 1-14: left column x=-8.75, y=-5.26 step 1.27 (1.5 x 0.9).
	for i := 0; i < 14; i++ {
		n := itoa(1 + i)
		pads = append(pads, fpSMD(n, n, "rect", -8.75, -5.26+1.27*float64(i), 1.5, 0.9, 0))
	}
	// Pads 15-26: bottom row y=12.5, x=-6.985 step 1.27, rotated (0.9 x 1.5).
	for i := 0; i < 12; i++ {
		n := itoa(15 + i)
		pads = append(pads, fpSMD(n, n, "rect", -6.985+1.27*float64(i), 12.5, 0.9, 1.5, 0))
	}
	// Pads 27-40: right column x=8.75, y=11.25..-5.26.
	for i := 0; i < 14; i++ {
		n := itoa(27 + i)
		pads = append(pads, fpSMD(n, n, "rect", 8.75, 11.25-1.27*float64(i), 1.5, 0.9, 0))
	}
	// Pad 41: 3.9 mm exposed GND pad.
	pads = append(pads, fpSMD("41", "41", "rect", -1.5, 2.46, 3.9, 3.9, 0))
	// Courtyard: KiCad's module-body courtyard (+-9.75 mm, 13.45 mm below the
	// origin) mirrored to the antenna end. KiCad's courtyard additionally
	// reserves the 48 x 21 mm antenna clearance; see the part notes.
	return fpFootprint("ESP32-S3-WROOM-1", "smd", -9.75, -13.45, 9.75, 13.45, 18, 25.5, "${KICAD10_3DMODEL_DIR}/RF_Module.3dshapes/ESP32-S3-WROOM-1.step", pads...)
}

func esp32S3Wroom1Pins() []LibraryPin {
	return []LibraryPin{
		lp("1", "GND", "power_in"), lp("2", "3V3", "power_in"), lp("3", "EN", "input", "CHIP_PU"),
		lp("4", "IO4", "bidirectional", "GPIO4", "TOUCH4", "ADC1_CH3"), lp("5", "IO5", "bidirectional", "GPIO5", "TOUCH5", "ADC1_CH4"), lp("6", "IO6", "bidirectional", "GPIO6", "TOUCH6", "ADC1_CH5"), lp("7", "IO7", "bidirectional", "GPIO7", "TOUCH7", "ADC1_CH6"),
		lp("8", "IO15", "bidirectional", "GPIO15", "U0RTS", "ADC2_CH4", "XTAL_32K_P"), lp("9", "IO16", "bidirectional", "GPIO16", "U0CTS", "ADC2_CH5", "XTAL_32K_N"), lp("10", "IO17", "bidirectional", "GPIO17", "U1TXD", "ADC2_CH6"), lp("11", "IO18", "bidirectional", "GPIO18", "U1RXD", "ADC2_CH7"),
		lp("12", "IO8", "bidirectional", "GPIO8", "TOUCH8", "ADC1_CH7"), lp("13", "IO19", "bidirectional", "GPIO19", "USB_D-", "U1RTS", "ADC2_CH8"), lp("14", "IO20", "bidirectional", "GPIO20", "USB_D+", "U1CTS", "ADC2_CH9"),
		lp("15", "IO3", "bidirectional", "GPIO3", "TOUCH3", "ADC1_CH2", "strapping"), lp("16", "IO46", "bidirectional", "GPIO46", "strapping"), lp("17", "IO9", "bidirectional", "GPIO9", "TOUCH9", "ADC1_CH8"), lp("18", "IO10", "bidirectional", "GPIO10", "TOUCH10", "ADC1_CH9"),
		lp("19", "IO11", "bidirectional", "GPIO11", "TOUCH11", "ADC2_CH0"), lp("20", "IO12", "bidirectional", "GPIO12", "TOUCH12", "ADC2_CH1"), lp("21", "IO13", "bidirectional", "GPIO13", "TOUCH13", "ADC2_CH2"), lp("22", "IO14", "bidirectional", "GPIO14", "TOUCH14", "ADC2_CH3"),
		lp("23", "IO21", "bidirectional", "GPIO21"), lp("24", "IO47", "bidirectional", "GPIO47", "SPICLK_P"), lp("25", "IO48", "bidirectional", "GPIO48", "SPICLK_N"), lp("26", "IO45", "bidirectional", "GPIO45", "strapping"),
		lp("27", "IO0", "bidirectional", "GPIO0", "BOOT strapping"), lp("28", "IO35", "bidirectional", "GPIO35", "SPIIO6"), lp("29", "IO36", "bidirectional", "GPIO36", "SPIIO7"), lp("30", "IO37", "bidirectional", "GPIO37", "SPIDQS"),
		lp("31", "IO38", "bidirectional", "GPIO38"), lp("32", "IO39", "bidirectional", "GPIO39", "MTCK"), lp("33", "IO40", "bidirectional", "GPIO40", "MTDO"), lp("34", "IO41", "bidirectional", "GPIO41", "MTDI"), lp("35", "IO42", "bidirectional", "GPIO42", "MTMS"),
		lp("36", "IO44", "bidirectional", "GPIO44", "U0RXD"), lp("37", "IO43", "bidirectional", "GPIO43", "U0TXD"), lp("38", "IO2", "bidirectional", "GPIO2", "TOUCH2", "ADC1_CH1"), lp("39", "IO1", "bidirectional", "GPIO1", "TOUCH1", "ADC1_CH0"),
		lp("40", "GND", "power_in"), lp("41", "GND", "power_in", "exposed pad"),
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
