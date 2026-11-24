package main

import (
	"math"
	"sort"
	"strings"
)

// Load-step model. A regulator output is treated as an ideal source behind
// a static output resistance (load regulation) and a control loop with time
// constant tau. When the load current steps by dI at t0, the output
// capacitance C supplies the difference until the loop catches up. With a
// critically damped loop the deviation is
//
//	dV(t) = -dI * (tau / C) * x * exp(-x),  x = (t - t0) / tau
//
// which has the physically required initial slope dI/C, a peak dip of
// dI*tau/(C*e) at t = t0 + tau, and full recovery. Load sources (kind
// "load") draw Value amps from NetID between StartUS and EndUS.

const (
	defaultRegulatorResponseUS = 20.0
	defaultOutputCapacitanceF  = 1e-6
)

type LoadStepReport struct {
	RegulatorID        string  `json:"regulator_id"`
	OutputNetID        string  `json:"output_net_id"`
	NominalV           float64 `json:"nominal_v"`
	StepA              float64 `json:"step_a"`
	StepAtUS           int64   `json:"step_at_us"`
	OutputCapacitanceF float64 `json:"output_capacitance_f"`
	ResponseTimeUS     float64 `json:"response_time_us"`
	StaticDropV        float64 `json:"static_drop_v"`
	PeakDipV           float64 `json:"peak_dip_v"`
	MinV               float64 `json:"min_v"`
	RecoveryUS         float64 `json:"recovery_us"` // to within 1% of the new steady state
	CurrentLimited     bool    `json:"current_limited,omitempty"`
	AssumedCapacitance bool    `json:"assumed_capacitance,omitempty"`
}

type loadEdge struct {
	TimeUS int64
	DeltaA float64
}

// regulatorLoad returns the load current on a net at time t.
func regulatorLoad(sources []SimulationSource, netID string, timeUS int64) float64 {
	total := 0.0
	for _, source := range sources {
		if !strings.EqualFold(source.Kind, "load") || source.NetID != netID {
			continue
		}
		if timeUS >= source.StartUS && (source.EndUS <= 0 || timeUS < source.EndUS) {
			total += source.Value
		}
	}
	return total
}

func loadEdges(sources []SimulationSource, netID string) []loadEdge {
	edges := []loadEdge{}
	for _, source := range sources {
		if !strings.EqualFold(source.Kind, "load") || source.NetID != netID || source.Value == 0 {
			continue
		}
		if source.StartUS > 0 {
			edges = append(edges, loadEdge{TimeUS: source.StartUS, DeltaA: source.Value})
		}
		if source.EndUS > source.StartUS {
			edges = append(edges, loadEdge{TimeUS: source.EndUS, DeltaA: -source.Value})
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].TimeUS < edges[j].TimeUS })
	return edges
}

// outputCapacitance sums capacitors between a regulator output and ground.
func outputCapacitance(capacitors []capacitorBranch, output, ground string) float64 {
	total := 0.0
	for _, c := range capacitors {
		if (c.A == output && c.B == ground) || (c.B == output && c.A == ground) {
			total += c.Farads
		}
	}
	return total
}

// regulatorOutputV is the loaded output voltage at time t.
func regulatorOutputV(r regulatorBranch, sources []SimulationSource, capacitance float64, timeUS int64) float64 {
	load := regulatorLoad(sources, r.Output, timeUS)
	if r.MaxCurrentA > 0 && load > r.MaxCurrentA {
		// Current limit: the output folds back in proportion to the overload.
		return math.Max(0, r.Voltage*r.MaxCurrentA/load)
	}
	v := r.Voltage - load*r.OutputResistance
	tau := r.ResponseTimeUS * 1e-6
	for _, edge := range loadEdges(sources, r.Output) {
		if timeUS < edge.TimeUS {
			continue
		}
		x := float64(timeUS-edge.TimeUS) * 1e-6 / tau
		v -= edge.DeltaA * (tau / capacitance) * x * math.Exp(-x)
	}
	return v
}

// loadStepReports computes the analytic dip and recovery for each step on
// each regulator output, independent of the waveform sample step.
func loadStepReports(regulators []regulatorBranch, capacitors []capacitorBranch, sources []SimulationSource, ground string) ([]LoadStepReport, []string) {
	reports := []LoadStepReport{}
	warnings := []string{}
	for _, r := range regulators {
		edges := loadEdges(sources, r.Output)
		if len(edges) == 0 {
			continue
		}
		capacitance := outputCapacitance(capacitors, r.Output, firstNonEmpty(r.Ground, ground))
		assumed := false
		if capacitance <= 0 {
			capacitance, assumed = defaultOutputCapacitanceF, true
			warnings = appendUnique(warnings, "regulator "+r.ComponentID+" has no output capacitor to ground; assumed 1 uF for the load step")
		}
		tau := r.ResponseTimeUS * 1e-6
		for _, edge := range edges {
			if edge.DeltaA <= 0 {
				continue // report the load-on steps, which cause the dip
			}
			after := regulatorLoad(sources, r.Output, edge.TimeUS)
			peak := edge.DeltaA * tau / (capacitance * math.E)
			static := after * r.OutputResistance
			// Recovery: deviation x*exp(-x) falls below 1% of nominal.
			threshold := 0.01 * r.Voltage
			recovery := 0.0
			if peak > threshold {
				scale := edge.DeltaA * tau / capacitance
				x := 1.0
				for ; x < 200 && scale*x*math.Exp(-x) > threshold; x += 0.01 {
				}
				recovery = x * r.ResponseTimeUS
			}
			report := LoadStepReport{RegulatorID: r.ComponentID, OutputNetID: r.Output, NominalV: r.Voltage, StepA: roundFloat(edge.DeltaA, 6), StepAtUS: edge.TimeUS, OutputCapacitanceF: capacitance, ResponseTimeUS: r.ResponseTimeUS, StaticDropV: roundFloat(static, 6), PeakDipV: roundFloat(peak+static, 6), MinV: roundFloat(r.Voltage-static-peak, 6), RecoveryUS: roundFloat(recovery, 3), AssumedCapacitance: assumed}
			if r.MaxCurrentA > 0 && after > r.MaxCurrentA {
				report.CurrentLimited = true
				report.MinV = roundFloat(math.Max(0, r.Voltage*r.MaxCurrentA/after), 6)
				report.PeakDipV = roundFloat(r.Voltage-report.MinV, 6)
				warnings = appendUnique(warnings, "regulator "+r.ComponentID+" load exceeds its current limit")
			}
			reports = append(reports, report)
		}
	}
	return reports, warnings
}
