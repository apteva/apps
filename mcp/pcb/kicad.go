package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type KiCadVerification struct {
	Schema string         `json:"schema"`
	Engine string         `json:"engine"`
	Status string         `json:"status"`
	Errors int            `json:"errors"`
	Checks []Check        `json:"checks"`
	Counts map[string]int `json:"counts"`
	SHA256 string         `json:"sha256"`
}

func renderKiCadPCB(def *Definition) []byte {
	var out bytes.Buffer
	f := func(format string, args ...any) { fmt.Fprintf(&out, format, args...) }
	f("(kicad_pcb (version 20240108) (generator pcb-studio-0.9)\n")
	f("  (general (thickness %.6f))\n", float64(def.Board.ThicknessNM)/1e6)
	f("  (paper \"A4\")\n  (layers\n")
	for _, layer := range kicadLayers(def) {
		f("    (%d \"%s\" %s)\n", layer.Number, layer.ID, layer.Kind)
	}
	f("  )\n  (setup (pad_to_mask_clearance 0))\n")
	netNumber := map[string]int{}
	for index, net := range def.Nets {
		netNumber[net.ID] = index + 1
		f("  (net %d \"%s\")\n", index+1, kicadEscape(net.Name))
	}
	netByNode := map[string]Net{}
	for _, net := range def.Nets {
		for _, node := range net.Nodes {
			netByNode[node.ComponentID+":"+node.PinID] = net
		}
	}
	for _, component := range def.Components {
		layer := "F.Cu"
		if component.Position.Side == "back" {
			layer = "B.Cu"
		}
		footprint := component.Footprint
		if footprint == "" {
			footprint = "Apteva_Unmapped"
		}
		f("  (footprint \"Apteva:%s\" (layer \"%s\") (at %.6f %.6f %.6f)\n", kicadEscape(footprint), layer, nmMM(component.Position.XNM), nmMM(component.Position.YNM), float64(component.Position.RotationUdeg)/1e6)
		f("    (property \"Reference\" \"%s\" (at 0 -2 0) (layer \"F.SilkS\"))\n", kicadEscape(component.Designator))
		f("    (property \"Value\" \"%s\" (at 0 2 0) (layer \"F.Fab\"))\n", kicadEscape(firstNonEmpty(component.Value, component.Name)))
		body := inferredBody(component)
		f("    (fp_rect (start %.6f %.6f) (end %.6f %.6f) (stroke (width 0.1) (type default)) (fill none) (layer \"F.Fab\"))\n", nmMM(-body.WidthNM/2), nmMM(-body.HeightNM/2), nmMM(body.WidthNM/2), nmMM(body.HeightNM/2))
		for _, pad := range component.Pads {
			typeName := "smd"
			layers := "\"F.Cu\" \"F.Paste\" \"F.Mask\""
			if pad.DrillNM > 0 || pad.DrillWidthNM > 0 {
				typeName, layers = "thru_hole", "\"*.Cu\" \"*.Mask\""
			}
			shape := kicadPadShape(pad.Shape)
			net := netByNode[component.ID+":"+pad.PinID]
			f("    (pad \"%s\" %s %s (at %.6f %.6f) (size %.6f %.6f)", kicadEscape(pad.ID), typeName, shape, nmMM(pad.XNM), nmMM(pad.YNM), nmMM(pad.WidthNM), nmMM(pad.HeightNM))
			if pad.DrillWidthNM > 0 && pad.DrillHeightNM > 0 {
				f(" (drill oval %.6f %.6f)", nmMM(pad.DrillWidthNM), nmMM(pad.DrillHeightNM))
			} else if pad.DrillNM > 0 {
				f(" (drill %.6f)", nmMM(pad.DrillNM))
			}
			f(" (layers %s)", layers)
			if net.ID != "" {
				f(" (net %d \"%s\")", netNumber[net.ID], kicadEscape(net.Name))
			}
			f(")\n")
		}
		f("  )\n")
	}
	for _, trace := range def.Traces {
		for index := 1; index < len(trace.Points); index++ {
			a, b := trace.Points[index-1], trace.Points[index]
			f("  (segment (start %.6f %.6f) (end %.6f %.6f) (width %.6f) (layer \"%s\") (net %d))\n", nmMM(a.XNM), nmMM(a.YNM), nmMM(b.XNM), nmMM(b.YNM), nmMM(trace.WidthNM), kicadEscape(trace.Layer), netNumber[trace.NetID])
		}
	}
	for _, via := range def.Vias {
		f("  (via (at %.6f %.6f) (size %.6f) (drill %.6f) (layers \"%s\" \"%s\") (net %d))\n", nmMM(via.XNM), nmMM(via.YNM), nmMM(via.DiameterNM), nmMM(via.DrillNM), kicadEscape(via.FromLayer), kicadEscape(via.ToLayer), netNumber[via.NetID])
	}
	for _, zone := range def.Zones {
		f("  (zone (net %d) (net_name \"%s\") (layer \"%s\") (hatch edge 0.5) (connect_pads (clearance %.6f)) (min_thickness 0.25) (polygon (pts", netNumber[zone.NetID], kicadEscape(netName(def, zone.NetID)), kicadEscape(zone.Layer), nmMM(max64(zone.ClearanceNM, def.Rules.MinClearanceNM)))
		for _, point := range zone.Polygon {
			f(" (xy %.6f %.6f)", nmMM(point.XNM), nmMM(point.YNM))
		}
		f(")))\n")
	}
	outline := boardPolygon(def.Board)
	for index := range outline {
		a, b := outline[index], outline[(index+1)%len(outline)]
		f("  (gr_line (start %.6f %.6f) (end %.6f %.6f) (stroke (width 0.1) (type default)) (layer \"Edge.Cuts\"))\n", nmMM(a.XNM), nmMM(a.YNM), nmMM(b.XNM), nmMM(b.YNM))
	}
	f(")\n")
	return out.Bytes()
}

type kicadLayer struct {
	Number   int
	ID, Kind string
}

func kicadLayers(def *Definition) []kicadLayer {
	known := map[string]kicadLayer{"F.Cu": {0, "F.Cu", "signal"}, "B.Cu": {31, "B.Cu", "signal"}, "F.SilkS": {37, "F.SilkS", "user \"F.Silkscreen\""}, "B.SilkS": {36, "B.SilkS", "user \"B.Silkscreen\""}, "F.Mask": {39, "F.Mask", "user"}, "B.Mask": {38, "B.Mask", "user"}, "Edge.Cuts": {44, "Edge.Cuts", "user"}}
	seen := map[string]bool{}
	out := []kicadLayer{}
	for _, layer := range def.Board.Layers {
		if item, ok := known[layer.ID]; ok && !seen[layer.ID] {
			seen[layer.ID] = true
			out = append(out, item)
		}
	}
	for _, id := range []string{"F.Cu", "B.Cu", "F.SilkS", "B.SilkS", "F.Mask", "B.Mask", "Edge.Cuts"} {
		if !seen[id] {
			out = append(out, known[id])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

func verifyKiCadPCB(def *Definition, body []byte) *KiCadVerification {
	sum := sha256.Sum256(body)
	report := &KiCadVerification{Schema: kicadExchangeSchema, Engine: engineVersion, Status: "passed", Checks: []Check{}, Counts: map[string]int{}, SHA256: hex.EncodeToString(sum[:])}
	add := func(code, message string) {
		report.Checks = append(report.Checks, Check{Code: code, Severity: "error", Message: message})
		report.Errors++
		report.Status = "failed"
	}
	root, err := parseSExpressions(string(body))
	if err != nil {
		add("KICAD_SYNTAX", err.Error())
		return report
	}
	if root.atom() != "kicad_pcb" {
		add("KICAD_ROOT", "Exchange file does not contain a kicad_pcb root")
		return report
	}
	report.Counts["nets"] = len(root.children("net"))
	report.Counts["footprints"] = len(root.children("footprint"))
	report.Counts["segments"] = len(root.children("segment"))
	report.Counts["vias"] = len(root.children("via"))
	report.Counts["edge_lines"] = countLayerNodes(root, "gr_line", "Edge.Cuts")
	pads := 0
	for _, footprint := range root.children("footprint") {
		pads += len(footprint.children("pad"))
	}
	report.Counts["pads"] = pads
	expectedSegments := 0
	for _, trace := range def.Traces {
		if len(trace.Points) > 1 {
			expectedSegments += len(trace.Points) - 1
		}
	}
	expected := map[string]int{"nets": len(def.Nets), "footprints": len(def.Components), "segments": expectedSegments, "vias": len(def.Vias), "edge_lines": len(boardPolygon(def.Board))}
	expectedPads := 0
	for _, component := range def.Components {
		expectedPads += len(component.Pads)
	}
	expected["pads"] = expectedPads
	for kind, count := range expected {
		if report.Counts[kind] != count {
			add("KICAD_COUNT_"+strings.ToUpper(kind), fmt.Sprintf("KiCad %s count %d does not match native count %d", kind, report.Counts[kind], count))
		}
	}
	return report
}

func importKiCadPCB(name string, body []byte) (*Definition, error) {
	root, err := parseSExpressions(string(body))
	if err != nil {
		return nil, err
	}
	if root.atom() != "kicad_pcb" {
		return nil, errors.New("KiCad file needs a kicad_pcb root")
	}
	def := emptyDefinition(name)
	def.Board.Layers = []Layer{{ID: "F.Cu", Kind: "copper", Order: 0}, {ID: "B.Cu", Kind: "copper", Order: 1}, {ID: "F.SilkS", Kind: "silkscreen", Order: 2}, {ID: "Edge.Cuts", Kind: "mechanical", Order: 3}}
	netIDs := map[int]string{}
	netNames := map[int]string{}
	for _, node := range root.children("net") {
		if len(node.List) < 3 {
			continue
		}
		number, _ := strconv.Atoi(node.List[1].atom())
		id := kicadSafeNativeID(node.List[2].atom(), fmt.Sprintf("net-%d", number))
		netIDs[number], netNames[number] = id, node.List[2].atom()
		def.Nets = append(def.Nets, Net{ID: id, Name: node.List[2].atom()})
	}
	for componentIndex, node := range root.children("footprint") {
		footprint := strings.TrimPrefix(node.value(1), "Apteva:")
		at := node.child("at")
		x, y, rotation := at.number(1), at.number(2), at.number(3)
		designator, value := fmt.Sprintf("U%d", componentIndex+1), footprint
		for _, property := range node.children("property") {
			if property.value(1) == "Reference" {
				designator = property.value(2)
			}
			if property.value(1) == "Value" {
				value = property.value(2)
			}
		}
		id := kicadSafeNativeID(strings.ToLower(designator), fmt.Sprintf("component-%d", componentIndex+1))
		component := Component{ID: id, Designator: designator, Name: value, Value: value, Footprint: footprint, Position: Position{XNM: mmNM(x), YNM: mmNM(y), RotationUdeg: int64(mathRound(rotation * 1e6)), Side: "front"}}
		for padIndex, padNode := range node.children("pad") {
			padID := kicadSafeNativeID(padNode.value(1), fmt.Sprintf("pad-%d", padIndex+1))
			pinID := padID
			shape := padNode.value(3)
			if shape == "rect" || shape == "circle" || shape == "oval" {
			} else {
				shape = "roundrect"
			}
			padAt, size, drill := padNode.child("at"), padNode.child("size"), padNode.child("drill")
			layers := []string{}
			for _, value := range padNode.child("layers").List[1:] {
				if value.atom() == "F.Cu" || value.atom() == "B.Cu" || value.atom() == "*.Cu" {
					layers = append(layers, value.atom())
				}
			}
			if containsString(layers, "*.Cu") {
				layers = []string{"F.Cu", "B.Cu"}
			}
			p := Pad{ID: padID, PinID: pinID, Shape: shape, XNM: mmNM(padAt.number(1)), YNM: mmNM(padAt.number(2)), WidthNM: mmNM(size.number(1)), HeightNM: mmNM(size.number(2)), Layers: layers}
			if drill != nil {
				p.DrillNM = mmNM(drill.lastNumber())
				p.Plated = padNode.value(2) == "thru_hole"
			}
			component.Pads = append(component.Pads, p)
			component.Pins = append(component.Pins, Pin{ID: pinID, Number: padID, Name: padID, ElectricalType: "passive", Pad: padID})
			if netNode := padNode.child("net"); netNode != nil {
				number := int(netNode.number(1))
				netID := netIDs[number]
				for i := range def.Nets {
					if def.Nets[i].ID == netID {
						def.Nets[i].Nodes = append(def.Nets[i].Nodes, Node{ComponentID: id, PinID: pinID})
					}
				}
			}
		}
		component.Body = &Body{WidthNM: 2_000_000, HeightNM: 2_000_000}
		def.Components = append(def.Components, component)
	}
	for index, node := range root.children("segment") {
		start, end := node.child("start"), node.child("end")
		netNumber := int(node.child("net").number(1))
		def.Traces = append(def.Traces, Trace{ID: fmt.Sprintf("kicad-segment-%d", index+1), NetID: netIDs[netNumber], Layer: node.child("layer").value(1), WidthNM: mmNM(node.child("width").number(1)), Points: []Point{{XNM: mmNM(start.number(1)), YNM: mmNM(start.number(2))}, {XNM: mmNM(end.number(1)), YNM: mmNM(end.number(2))}}})
	}
	for index, node := range root.children("via") {
		at, layers := node.child("at"), node.child("layers")
		netNumber := int(node.child("net").number(1))
		def.Vias = append(def.Vias, Via{ID: fmt.Sprintf("kicad-via-%d", index+1), NetID: netIDs[netNumber], XNM: mmNM(at.number(1)), YNM: mmNM(at.number(2)), DiameterNM: mmNM(node.child("size").number(1)), DrillNM: mmNM(node.child("drill").lastNumber()), FromLayer: layers.value(1), ToLayer: layers.value(2)})
	}
	for _, node := range root.children("gr_line") {
		if node.child("layer").value(1) != "Edge.Cuts" {
			continue
		}
		start := node.child("start")
		def.Board.Outline = append(def.Board.Outline, Point{XNM: mmNM(start.number(1)), YNM: mmNM(start.number(2))})
	}
	if len(def.Board.Outline) >= 3 {
		var maxX, maxY int64
		for _, point := range def.Board.Outline {
			if point.XNM > maxX {
				maxX = point.XNM
			}
			if point.YNM > maxY {
				maxY = point.YNM
			}
		}
		def.Board.WidthNM, def.Board.HeightNM = maxX, maxY
	}
	attachNativeContracts(&def)
	return &def, nil
}

type sexpr struct {
	Atom string
	List []*sexpr
}

func (s *sexpr) atom() string {
	if s == nil {
		return ""
	}
	return s.Atom
}
func (s *sexpr) value(index int) string {
	if s == nil || index >= len(s.List) {
		return ""
	}
	return s.List[index].atom()
}
func (s *sexpr) number(index int) float64 {
	value, _ := strconv.ParseFloat(s.value(index), 64)
	return value
}
func (s *sexpr) lastNumber() float64 {
	if s == nil {
		return 0
	}
	for i := len(s.List) - 1; i >= 1; i-- {
		if value, err := strconv.ParseFloat(s.List[i].atom(), 64); err == nil {
			return value
		}
	}
	return 0
}
func (s *sexpr) child(name string) *sexpr {
	for _, item := range s.List {
		if item.atom() == name {
			return item
		}
	}
	return nil
}
func (s *sexpr) children(name string) []*sexpr {
	out := []*sexpr{}
	for _, item := range s.List {
		if item.atom() == name {
			out = append(out, item)
		}
	}
	return out
}

func parseSExpressions(source string) (*sexpr, error) {
	tokens, err := sexprTokens(source)
	if err != nil {
		return nil, err
	}
	position := 0
	var parse func() (*sexpr, error)
	parse = func() (*sexpr, error) {
		if position >= len(tokens) || tokens[position] != "(" {
			return nil, errors.New("expected opening parenthesis")
		}
		position++
		node := &sexpr{}
		for position < len(tokens) && tokens[position] != ")" {
			if tokens[position] == "(" {
				child, err := parse()
				if err != nil {
					return nil, err
				}
				node.List = append(node.List, child)
			} else {
				node.List = append(node.List, &sexpr{Atom: tokens[position]})
				position++
			}
		}
		if position >= len(tokens) {
			return nil, errors.New("unterminated KiCad expression")
		}
		position++
		if len(node.List) > 0 {
			node.Atom = node.List[0].Atom
		}
		return node, nil
	}
	root, err := parse()
	if err != nil {
		return nil, err
	}
	if position != len(tokens) {
		return nil, errors.New("multiple KiCad root expressions")
	}
	return root, nil
}

func sexprTokens(source string) ([]string, error) {
	tokens := []string{}
	for index := 0; index < len(source); {
		r := rune(source[index])
		if unicode.IsSpace(r) {
			index++
			continue
		}
		if source[index] == ';' {
			for index < len(source) && source[index] != '\n' {
				index++
			}
			continue
		}
		if source[index] == '(' || source[index] == ')' {
			tokens = append(tokens, source[index:index+1])
			index++
			continue
		}
		if source[index] == '"' {
			index++
			var value strings.Builder
			closed := false
			for index < len(source) {
				if source[index] == '\\' && index+1 < len(source) {
					value.WriteByte(source[index+1])
					index += 2
					continue
				}
				if source[index] == '"' {
					index++
					closed = true
					break
				}
				value.WriteByte(source[index])
				index++
			}
			if !closed {
				return nil, errors.New("unterminated KiCad string")
			}
			tokens = append(tokens, value.String())
			continue
		}
		start := index
		for index < len(source) && !unicode.IsSpace(rune(source[index])) && source[index] != '(' && source[index] != ')' {
			index++
		}
		tokens = append(tokens, source[start:index])
	}
	return tokens, nil
}

func countLayerNodes(root *sexpr, kind, layer string) int {
	count := 0
	for _, node := range root.children(kind) {
		if node.child("layer").value(1) == layer {
			count++
		}
	}
	return count
}
func kicadPadShape(shape string) string {
	if shape == "rect" || shape == "circle" || shape == "oval" {
		return shape
	}
	return "roundrect"
}
func kicadEscape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "\"", "\\\"")
}
func nmMM(value int64) float64 { return float64(value) / 1e6 }
func mmNM(value float64) int64 { return int64(mathRound(value * 1e6)) }
func mathRound(value float64) float64 {
	if value < 0 {
		return float64(int64(value - .5))
	}
	return float64(int64(value + .5))
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
func netName(def *Definition, id string) string {
	for _, net := range def.Nets {
		if net.ID == id {
			return net.Name
		}
	}
	return id
}

var invalidNativeID = regexp.MustCompile(`[^A-Za-z0-9_.:-]+`)

func kicadSafeNativeID(value, fallback string) string {
	value = strings.Trim(invalidNativeID.ReplaceAllString(value, "-"), "-")
	if idValid(value) {
		return value
	}
	return fallback
}

func kicadVerificationJSON(report *KiCadVerification) []byte {
	body, _ := json.MarshalIndent(report, "", "  ")
	return append(body, '\n')
}
