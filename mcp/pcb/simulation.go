package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const simulationSchema = "apteva-pcb-simulation/v1"

type SimulationFault struct {
	Kind        string `json:"kind"` // open_component | short_nets
	ComponentID string `json:"component_id,omitempty"`
	NetA        string `json:"net_a,omitempty"`
	NetB        string `json:"net_b,omitempty"`
}

type SimulationOptions struct {
	DurationUS int64              `json:"duration_us,omitempty"`
	StepUS     int64              `json:"step_us,omitempty"`
	Sources    []SimulationSource `json:"sources,omitempty"`
	Probes     []SimulationProbe  `json:"probes,omitempty"`
	Faults     []SimulationFault  `json:"faults,omitempty"`
}

type WavePoint struct {
	TimeUS  int64   `json:"time_us"`
	Value   float64 `json:"value"`
	Digital string  `json:"digital,omitempty"`
}

type SimulationWaveform struct {
	ProbeID string      `json:"probe_id"`
	NetID   string      `json:"net_id"`
	Kind    string      `json:"kind"`
	Unit    string      `json:"unit"`
	Points  []WavePoint `json:"points"`
}

type SimulationResult struct {
	Schema       string               `json:"schema"`
	Engine       string               `json:"engine"`
	Status       string               `json:"status"`
	DurationUS   int64                `json:"duration_us"`
	StepUS       int64                `json:"step_us"`
	Samples      int                  `json:"samples"`
	Sources      []SimulationSource   `json:"sources"`
	Faults       []SimulationFault    `json:"faults,omitempty"`
	FinalVoltage map[string]float64   `json:"final_voltage_v"`
	FinalDigital map[string]string    `json:"final_digital"`
	Waveforms    []SimulationWaveform `json:"waveforms"`
	DeviceStates []map[string]any     `json:"device_states,omitempty"`
	Warnings     []string             `json:"warnings,omitempty"`
}

type resistorBranch struct {
	ComponentID string
	A, B        string
	Ohms        float64
}

type capacitorBranch struct {
	ComponentID string
	A, B        string
	Farads      float64
	PreviousV   float64
}

type regulatorBranch struct {
	ComponentID string
	Input       string
	Output      string
	Ground      string
	Voltage     float64
	Dropout     float64
}

func simulateDefinition(def *Definition, options SimulationOptions) (*SimulationResult, error) {
	if def == nil {
		return nil, fmt.Errorf("definition required")
	}
	merged := mergeSimulationOptions(def.Simulation, options)
	if merged.DurationUS <= 0 {
		merged.DurationUS = 10_000
	}
	if merged.StepUS <= 0 {
		merged.StepUS = 100
	}
	if merged.StepUS > merged.DurationUS {
		merged.StepUS = merged.DurationUS
	}
	samples := int(merged.DurationUS/merged.StepUS) + 1
	if samples > 2_000 {
		return nil, fmt.Errorf("simulation exceeds 2000 samples; increase step_us")
	}
	netIDs := make([]string, 0, len(def.Nets))
	netNames := map[string]string{}
	for _, net := range def.Nets {
		netIDs = append(netIDs, net.ID)
		netNames[net.ID] = net.Name
	}
	if len(netIDs) == 0 {
		return nil, fmt.Errorf("simulation requires at least one net")
	}
	sort.Strings(netIDs)
	ground := findGroundNet(def)
	if ground == "" {
		ground = netIDs[0]
	}
	if len(merged.Sources) == 0 {
		for _, net := range def.Nets {
			upper := strings.ToUpper(net.ID + " " + net.Name)
			if strings.Contains(upper, "USB5V") || strings.Contains(upper, "VBUS") || strings.Contains(upper, "+5V") {
				merged.Sources = append(merged.Sources, SimulationSource{ID: "auto-usb-5v", NetID: net.ID, Kind: "dc", Value: 5})
				break
			}
		}
	}
	if len(merged.Probes) == 0 {
		for _, netID := range netIDs {
			merged.Probes = append(merged.Probes, SimulationProbe{ID: "probe-" + safeNativeID(netID), NetID: netID, Kind: "voltage"})
			if len(merged.Probes) >= 32 {
				break
			}
		}
	}
	knownNets := map[string]bool{}
	for _, id := range netIDs {
		knownNets[id] = true
	}
	for _, source := range merged.Sources {
		if !knownNets[source.NetID] {
			return nil, fmt.Errorf("source %q references unknown net %q", source.ID, source.NetID)
		}
	}
	for _, probe := range merged.Probes {
		if !knownNets[probe.NetID] {
			return nil, fmt.Errorf("probe %q references unknown net %q", probe.ID, probe.NetID)
		}
	}

	resistors, capacitors, regulators, deviceStates := compileSimulationModel(def, merged.Faults)
	result := &SimulationResult{Schema: simulationSchema, Engine: engineVersion, Status: "passed", DurationUS: merged.DurationUS, StepUS: merged.StepUS, Samples: samples, Sources: merged.Sources, Faults: merged.Faults, FinalVoltage: map[string]float64{}, FinalDigital: map[string]string{}, DeviceStates: deviceStates, Warnings: []string{}}
	for _, probe := range merged.Probes {
		kind := probe.Kind
		if kind == "" {
			kind = "voltage"
		}
		unit := "V"
		if kind == "digital" {
			unit = "logic"
		}
		result.Waveforms = append(result.Waveforms, SimulationWaveform{ProbeID: probe.ID, NetID: probe.NetID, Kind: kind, Unit: unit, Points: []WavePoint{}})
	}
	previous := map[string]float64{ground: 0}
	for sample := 0; sample < samples; sample++ {
		timeUS := int64(sample) * merged.StepUS
		known := map[string]float64{ground: 0}
		for _, source := range merged.Sources {
			value := simulationSourceValue(source, timeUS)
			if existing, ok := known[source.NetID]; ok && math.Abs(existing-value) > 1e-9 {
				result.Warnings = appendUnique(result.Warnings, fmt.Sprintf("conflicting sources drive net %s", source.NetID))
			}
			known[source.NetID] = value
		}
		// Regulators are behavioral ideal sources. Iterate because a regulator
		// input may itself be produced by another regulator.
		for pass := 0; pass < len(regulators)+1; pass++ {
			changed := false
			for _, regulator := range regulators {
				input, ok := known[regulator.Input]
				if !ok {
					input = previous[regulator.Input]
					ok = input != 0
				}
				if ok && input >= regulator.Voltage+regulator.Dropout {
					if old, exists := known[regulator.Output]; !exists || math.Abs(old-regulator.Voltage) > 1e-12 {
						known[regulator.Output] = regulator.Voltage
						changed = true
					}
				}
			}
			if !changed {
				break
			}
		}
		voltages, err := solveSimulationStep(netIDs, known, resistors, capacitors, merged.Faults, previous, float64(merged.StepUS)*1e-6)
		if err != nil {
			result.Status = "warning"
			result.Warnings = appendUnique(result.Warnings, err.Error())
			voltages = map[string]float64{}
			for _, id := range netIDs {
				voltages[id] = known[id]
			}
		}
		for i := range capacitors {
			capacitors[i].PreviousV = voltages[capacitors[i].A] - voltages[capacitors[i].B]
		}
		previous = voltages
		for i := range result.Waveforms {
			wave := &result.Waveforms[i]
			voltage := voltages[wave.NetID]
			point := WavePoint{TimeUS: timeUS, Value: roundFloat(voltage, 9)}
			if wave.Kind == "digital" {
				point.Digital = digitalState(voltage)
			}
			wave.Points = append(wave.Points, point)
		}
	}
	for _, netID := range netIDs {
		voltage := previous[netID]
		result.FinalVoltage[netID] = roundFloat(voltage, 9)
		result.FinalDigital[netID] = digitalState(voltage)
	}
	for i := range result.DeviceStates {
		state := result.DeviceStates[i]
		if state["kind"] == "led" {
			anode, _ := state["anode_net"].(string)
			cathode, _ := state["cathode_net"].(string)
			forward, _ := state["forward_voltage_v"].(float64)
			state["on"] = result.FinalVoltage[anode]-result.FinalVoltage[cathode] >= forward
		}
		if state["kind"] == "i2c_sensor" {
			state["powered"] = sensorPowered(state, result.FinalVoltage)
		}
	}
	_ = netNames
	return result, nil
}

func mergeSimulationOptions(spec *SimulationSpec, options SimulationOptions) SimulationOptions {
	if spec == nil {
		return options
	}
	if options.DurationUS == 0 {
		options.DurationUS = spec.DurationUS
	}
	if options.StepUS == 0 {
		options.StepUS = spec.StepUS
	}
	if len(options.Sources) == 0 {
		options.Sources = append([]SimulationSource(nil), spec.Sources...)
	}
	if len(options.Probes) == 0 {
		options.Probes = append([]SimulationProbe(nil), spec.Probes...)
	}
	return options
}

func compileSimulationModel(def *Definition, faults []SimulationFault) ([]resistorBranch, []capacitorBranch, []regulatorBranch, []map[string]any) {
	netByNode := map[string]string{}
	for _, net := range def.Nets {
		for _, node := range net.Nodes {
			netByNode[node.ComponentID+":"+node.PinID] = net.ID
		}
	}
	open := map[string]bool{}
	for _, fault := range faults {
		if fault.Kind == "open_component" {
			open[fault.ComponentID] = true
		}
	}
	resistors := []resistorBranch{}
	capacitors := []capacitorBranch{}
	regulators := []regulatorBranch{}
	devices := []map[string]any{}
	for _, component := range def.Components {
		if open[component.ID] {
			continue
		}
		kind := componentModelKind(component)
		nets := componentNets(component, netByNode)
		switch kind {
		case "resistor":
			if len(nets) >= 2 {
				ohms := modelResistance(component)
				if ohms > 0 {
					resistors = append(resistors, resistorBranch{ComponentID: component.ID, A: nets[0], B: nets[1], Ohms: ohms})
				}
			}
		case "capacitor":
			if len(nets) >= 2 {
				farads := modelCapacitance(component)
				if farads > 0 {
					capacitors = append(capacitors, capacitorBranch{ComponentID: component.ID, A: nets[0], B: nets[1], Farads: farads})
				}
			}
		case "regulator":
			input := netForRole(component, "in", netByNode, "vin", "input")
			output := netForRole(component, "out", netByNode, "vout", "output")
			ground := netForRole(component, "ground", netByNode, "gnd", "ground")
			if input != "" && output != "" {
				voltage, dropout := 3.3, 0.25
				if component.Model != nil {
					if component.Model.VoltageV > 0 {
						voltage = component.Model.VoltageV
					}
					if component.Model.DropoutVoltageV > 0 {
						dropout = component.Model.DropoutVoltageV
					}
				}
				regulators = append(regulators, regulatorBranch{ComponentID: component.ID, Input: input, Output: output, Ground: ground, Voltage: voltage, Dropout: dropout})
			}
		case "led":
			anode := netForRole(component, "anode", netByNode, "anode", "a", "1")
			cathode := netForRole(component, "cathode", netByNode, "cathode", "k", "2", "gnd")
			forward := 2.0
			if component.Model != nil && component.Model.ForwardVoltageV > 0 {
				forward = component.Model.ForwardVoltageV
			}
			devices = append(devices, map[string]any{"component_id": component.ID, "kind": "led", "anode_net": anode, "cathode_net": cathode, "forward_voltage_v": forward})
		case "sensor":
			state := map[string]any{"component_id": component.ID, "kind": "i2c_sensor", "address": 0x70, "values": map[string]float64{"temperature_c": 23.5, "humidity_percent": 45}}
			if component.Model != nil && component.Model.I2CAddress > 0 {
				state["address"] = component.Model.I2CAddress
			}
			state["vdd_net"] = netForRole(component, "vdd", netByNode, "vdd", "vcc", "3v3")
			state["ground_net"] = netForRole(component, "ground", netByNode, "gnd")
			state["sda_net"] = netForRole(component, "sda", netByNode, "sda")
			state["scl_net"] = netForRole(component, "scl", netByNode, "scl")
			devices = append(devices, state)
		}
	}
	return resistors, capacitors, regulators, devices
}

func solveSimulationStep(netIDs []string, known map[string]float64, resistors []resistorBranch, capacitors []capacitorBranch, faults []SimulationFault, previous map[string]float64, dt float64) (map[string]float64, error) {
	unknown := []string{}
	index := map[string]int{}
	for _, id := range netIDs {
		if _, ok := known[id]; !ok {
			index[id] = len(unknown)
			unknown = append(unknown, id)
		}
	}
	result := map[string]float64{}
	for id, value := range known {
		result[id] = value
	}
	if len(unknown) == 0 {
		return result, nil
	}
	matrix := make([][]float64, len(unknown))
	rhs := make([]float64, len(unknown))
	for i := range matrix {
		matrix[i] = make([]float64, len(unknown))
		matrix[i][i] = 1e-12
	}
	stampConductance := func(a, b string, conductance, history float64) {
		ia, aUnknown := index[a]
		ib, bUnknown := index[b]
		if aUnknown {
			matrix[ia][ia] += conductance
			if bUnknown {
				matrix[ia][ib] -= conductance
			} else {
				rhs[ia] += conductance * known[b]
			}
			rhs[ia] += history
		}
		if bUnknown {
			matrix[ib][ib] += conductance
			if aUnknown {
				matrix[ib][ia] -= conductance
			} else {
				rhs[ib] += conductance * known[a]
			}
			rhs[ib] -= history
		}
	}
	for _, branch := range resistors {
		stampConductance(branch.A, branch.B, 1/branch.Ohms, 0)
	}
	if dt > 0 {
		for _, branch := range capacitors {
			g := branch.Farads / dt
			stampConductance(branch.A, branch.B, g, g*branch.PreviousV)
		}
	}
	for _, fault := range faults {
		if fault.Kind == "short_nets" && fault.NetA != "" && fault.NetB != "" {
			stampConductance(fault.NetA, fault.NetB, 1e6, 0)
		}
	}
	solution, err := gaussianSolve(matrix, rhs)
	if err != nil {
		return nil, fmt.Errorf("electrical solve: %w", err)
	}
	for id, i := range index {
		result[id] = solution[i]
	}
	for _, id := range netIDs {
		if _, ok := result[id]; !ok {
			result[id] = previous[id]
		}
	}
	return result, nil
}

func gaussianSolve(matrix [][]float64, rhs []float64) ([]float64, error) {
	n := len(rhs)
	for column := 0; column < n; column++ {
		pivot := column
		for row := column + 1; row < n; row++ {
			if math.Abs(matrix[row][column]) > math.Abs(matrix[pivot][column]) {
				pivot = row
			}
		}
		if math.Abs(matrix[pivot][column]) < 1e-18 {
			return nil, fmt.Errorf("singular matrix at column %d", column)
		}
		matrix[column], matrix[pivot] = matrix[pivot], matrix[column]
		rhs[column], rhs[pivot] = rhs[pivot], rhs[column]
		scale := matrix[column][column]
		for j := column; j < n; j++ {
			matrix[column][j] /= scale
		}
		rhs[column] /= scale
		for row := 0; row < n; row++ {
			if row == column {
				continue
			}
			factor := matrix[row][column]
			if factor == 0 {
				continue
			}
			for j := column; j < n; j++ {
				matrix[row][j] -= factor * matrix[column][j]
			}
			rhs[row] -= factor * rhs[column]
		}
	}
	return rhs, nil
}

func componentModelKind(component Component) string {
	if component.Model != nil && component.Model.Kind != "" {
		return strings.ToLower(component.Model.Kind)
	}
	designator := strings.ToUpper(component.Designator)
	name := strings.ToLower(component.Name + " " + component.Value)
	switch {
	case strings.HasPrefix(designator, "R"):
		return "resistor"
	case strings.HasPrefix(designator, "C"):
		return "capacitor"
	case strings.Contains(name, "regulator") || strings.Contains(name, "ldo"):
		return "regulator"
	case strings.Contains(name, "led"):
		return "led"
	case strings.Contains(name, "temperature") || strings.Contains(name, "humidity") || strings.Contains(name, "sensor"):
		return "sensor"
	case strings.Contains(name, "microcontroller") || strings.Contains(name, "esp32") || strings.Contains(name, "arduino"):
		return "mcu"
	default:
		return "passive"
	}
}

func componentNets(component Component, netByNode map[string]string) []string {
	out, seen := []string{}, map[string]bool{}
	for _, pin := range component.Pins {
		if net := netByNode[component.ID+":"+pin.ID]; net != "" && !seen[net] {
			out = append(out, net)
			seen[net] = true
		}
	}
	return out
}

func netForRole(component Component, role string, netByNode map[string]string, aliases ...string) string {
	if component.Model != nil && component.Model.PinRoles != nil {
		if pinID := component.Model.PinRoles[role]; pinID != "" {
			return netByNode[component.ID+":"+pinID]
		}
	}
	want := append([]string{role}, aliases...)
	for _, pin := range component.Pins {
		candidate := strings.ToLower(pin.ID + " " + pin.Name + " " + pin.Number)
		for _, alias := range want {
			if strings.Contains(candidate, strings.ToLower(alias)) {
				if net := netByNode[component.ID+":"+pin.ID]; net != "" {
					return net
				}
			}
		}
	}
	return ""
}

func modelResistance(component Component) float64 {
	if component.Model != nil && component.Model.ResistanceOhms > 0 {
		return component.Model.ResistanceOhms
	}
	return parseEngineeringValue(component.Value, 'r')
}
func modelCapacitance(component Component) float64 {
	if component.Model != nil && component.Model.CapacitanceF > 0 {
		return component.Model.CapacitanceF
	}
	return parseEngineeringValue(component.Value, 'c')
}

func parseEngineeringValue(value string, kind byte) float64 {
	v := strings.TrimSpace(strings.ToLower(value))
	v = strings.ReplaceAll(v, "ohms", "")
	v = strings.ReplaceAll(v, "ohm", "")
	v = strings.ReplaceAll(v, "Ω", "")
	if kind == 'c' {
		v = strings.TrimSuffix(v, "f")
	}
	multiplier := 1.0
	suffixes := []struct {
		suffix     string
		multiplier float64
	}{{"meg", 1e6}, {"k", 1e3}, {"m", 1e-3}, {"u", 1e-6}, {"µ", 1e-6}, {"n", 1e-9}, {"p", 1e-12}}
	for _, suffix := range suffixes {
		if strings.HasSuffix(v, suffix.suffix) {
			multiplier = suffix.multiplier
			v = strings.TrimSuffix(v, suffix.suffix)
			break
		}
	}
	number, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return number * multiplier
}

func simulationSourceValue(source SimulationSource, timeUS int64) float64 {
	if timeUS < source.StartUS {
		return 0
	}
	switch strings.ToLower(source.Kind) {
	case "clock":
		period := source.PeriodUS
		if period <= 0 {
			period = 1_000
		}
		duty := source.DutyCycle
		if duty <= 0 || duty >= 1 {
			duty = 0.5
		}
		if float64((timeUS-source.StartUS)%period) < float64(period)*duty {
			if source.Value == 0 {
				return 3.3
			}
			return source.Value
		}
		return 0
	case "digital":
		if source.Value == 0 {
			return 3.3
		}
		return source.Value
	default:
		return source.Value
	}
}

func findGroundNet(def *Definition) string {
	for _, net := range def.Nets {
		upper := strings.ToUpper(net.ID + " " + net.Name)
		if upper == "GND GND" || strings.Contains(upper, "GROUND") || net.ID == "gnd" {
			return net.ID
		}
	}
	return ""
}

func digitalState(voltage float64) string {
	switch {
	case voltage <= 0.8:
		return "low"
	case voltage >= 2.0:
		return "high"
	default:
		return "floating"
	}
}
func roundFloat(value float64, digits int) float64 {
	scale := math.Pow10(digits)
	return math.Round(value*scale) / scale
}
func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
func sensorPowered(state map[string]any, voltages map[string]float64) bool {
	vdd, _ := state["vdd_net"].(string)
	ground, _ := state["ground_net"].(string)
	return vdd != "" && voltages[vdd]-voltages[ground] >= 1.8
}
