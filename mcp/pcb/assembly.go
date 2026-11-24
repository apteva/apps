package main

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	maxReasonBytes       = 4_000
	maxDatasheetRefBytes = 2_000
)

// applyAnnotation sets the design rationale on one component or one net.
func applyAnnotation(def *Definition, op Operation) error {
	if op.Annotation == nil {
		return errors.New("annotation.set requires annotation")
	}
	reason := strings.TrimSpace(op.Annotation.Reason)
	ref := strings.TrimSpace(op.Annotation.DatasheetRef)
	switch {
	case op.ComponentID != "" && op.NetID != "":
		return errors.New("annotation.set takes component_id or net_id, not both")
	case op.ComponentID != "":
		idx := findComponent(def.Components, op.ComponentID)
		if idx < 0 {
			return fmt.Errorf("component %q not found", op.ComponentID)
		}
		def.Components[idx].Reason, def.Components[idx].DatasheetRef = reason, ref
	case op.NetID != "":
		idx := findNet(def.Nets, op.NetID)
		if idx < 0 {
			return fmt.Errorf("net %q not found", op.NetID)
		}
		def.Nets[idx].Reason, def.Nets[idx].DatasheetRef = reason, ref
	default:
		return errors.New("annotation.set requires component_id or net_id")
	}
	return nil
}

func annotationLimits(def *Definition) error {
	for _, c := range def.Components {
		if len(c.Reason) > maxReasonBytes || len(c.DatasheetRef) > maxDatasheetRefBytes {
			return fmt.Errorf("component %q reason or datasheet_ref exceeds limits", c.ID)
		}
	}
	for _, n := range def.Nets {
		if len(n.Reason) > maxReasonBytes || len(n.DatasheetRef) > maxDatasheetRefBytes {
			return fmt.Errorf("net %q reason or datasheet_ref exceeds limits", n.ID)
		}
	}
	return nil
}

// PlacementRow is one line of the pick-and-place (centroid) file.
type PlacementRow struct {
	Designator  string  `json:"designator"`
	Value       string  `json:"value"`
	Package     string  `json:"package"`
	MPN         string  `json:"mpn,omitempty"`
	XMM         float64 `json:"x_mm"`
	YMM         float64 `json:"y_mm"`
	RotationDeg float64 `json:"rotation_deg"`
	Layer       string  `json:"layer"`
	Mount       string  `json:"mount"` // smd | tht
}

// placementRows lists every component that an assembler places: anything
// with copper pads. Test points and mounting holes without an MPN are left to
// the fab's bare-board process and excluded. Coordinates are the component
// origin in the same frame and units as the Gerber files, so a fab can
// overlay the two without an offset.
func placementRows(def *Definition) []PlacementRow {
	rows := []PlacementRow{}
	for _, c := range def.Components {
		if len(c.Pads) == 0 {
			continue
		}
		upper := strings.ToUpper(c.Designator)
		if (strings.HasPrefix(upper, "TP") || strings.HasPrefix(upper, "MH") || strings.HasPrefix(upper, "H")) && strings.TrimSpace(c.MPN) == "" {
			continue
		}
		mount := "smd"
		for _, pad := range c.Pads {
			if pad.DrillNM > 0 || pad.DrillWidthNM > 0 {
				mount = "tht"
			}
		}
		layer := "Top"
		if c.Position.Side == "back" || c.Position.Side == "bottom" {
			layer = "Bottom"
		}
		rotation := float64(c.Position.RotationUdeg) / 1e6
		for rotation < 0 {
			rotation += 360
		}
		for rotation >= 360 {
			rotation -= 360
		}
		pkg := c.Footprint
		if pkg == "" && c.FootprintSpec != nil {
			pkg = c.FootprintSpec.LibraryID
		}
		rows = append(rows, PlacementRow{Designator: c.Designator, Value: c.Value, Package: pkg, MPN: c.MPN, XMM: float64(c.Position.XNM) / 1e6, YMM: float64(c.Position.YNM) / 1e6, RotationDeg: rotation, Layer: layer, Mount: mount})
	}
	sort.Slice(rows, func(i, j int) bool { return naturalLess(rows[i].Designator, rows[j].Designator) })
	return rows
}

// renderPlacementCSV writes the centroid file. The header uses the column
// names JLCPCB, PCBWay and most assembly houses accept (Designator, Mid X,
// Mid Y, Layer, Rotation) plus value, package and mount type.
func renderPlacementCSV(def *Definition) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"Designator", "Val", "Package", "Mid X", "Mid Y", "Rotation", "Layer", "Type"})
	for _, row := range placementRows(def) {
		_ = w.Write([]string{row.Designator, row.Value, row.Package, mmField(row.XMM), mmField(row.YMM), strconv.FormatFloat(row.RotationDeg, 'f', 3, 64), row.Layer, strings.ToUpper(row.Mount)})
	}
	w.Flush()
	return buf.Bytes()
}

// renderAssemblyBOM is the assembly-house BOM: one line per part number with
// the designators to populate.
func renderAssemblyBOM(def *Definition) []byte {
	type group struct {
		Comment, Footprint, MPN string
		Refs                    []string
	}
	placed := map[string]bool{}
	for _, row := range placementRows(def) {
		placed[row.Designator] = true
	}
	groups := map[string]*group{}
	for _, c := range def.Components {
		if !placed[c.Designator] {
			continue
		}
		key := c.MPN + "\x00" + c.Value + "\x00" + c.Footprint
		g := groups[key]
		if g == nil {
			comment := c.Value
			if comment == "" {
				comment = c.Name
			}
			g = &group{Comment: comment, Footprint: c.Footprint, MPN: c.MPN}
			groups[key] = g
		}
		g.Refs = append(g.Refs, c.Designator)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"Comment", "Designator", "Footprint", "Manufacturer Part Number", "Quantity"})
	for _, key := range keys {
		g := groups[key]
		sort.Slice(g.Refs, func(i, j int) bool { return naturalLess(g.Refs[i], g.Refs[j]) })
		_ = w.Write([]string{g.Comment, strings.Join(g.Refs, ","), g.Footprint, g.MPN, strconv.Itoa(len(g.Refs))})
	}
	w.Flush()
	return buf.Bytes()
}

// verifyPlacementCSV re-reads a centroid file and checks it against the
// design: every placed component appears once, at its position, on the board.
func verifyPlacementCSV(def *Definition, body []byte) []Check {
	checks := []Check{}
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil || len(records) == 0 {
		return []Check{{Code: "CPL_PARSE", Severity: "error", Message: "Pick-and-place file could not be parsed"}}
	}
	header := map[string]int{}
	for i, name := range records[0] {
		header[name] = i
	}
	for _, required := range []string{"Designator", "Mid X", "Mid Y", "Layer", "Rotation"} {
		if _, ok := header[required]; !ok {
			checks = append(checks, Check{Code: "CPL_HEADER", Severity: "error", Message: "Pick-and-place header lacks " + required})
		}
	}
	if len(checks) > 0 {
		return checks
	}
	expected := map[string]PlacementRow{}
	for _, row := range placementRows(def) {
		expected[row.Designator] = row
	}
	seen := map[string]bool{}
	widthMM, heightMM := float64(def.Board.WidthNM)/1e6, float64(def.Board.HeightNM)/1e6
	for _, record := range records[1:] {
		ref := record[header["Designator"]]
		want, ok := expected[ref]
		if !ok {
			checks = append(checks, Check{Code: "CPL_UNKNOWN", Severity: "error", Message: "Pick-and-place lists a designator not in the design", ObjectIDs: []string{ref}})
			continue
		}
		if seen[ref] {
			checks = append(checks, Check{Code: "CPL_DUPLICATE", Severity: "error", Message: "Designator appears twice", ObjectIDs: []string{ref}})
		}
		seen[ref] = true
		x, errX := strconv.ParseFloat(record[header["Mid X"]], 64)
		y, errY := strconv.ParseFloat(record[header["Mid Y"]], 64)
		if errX != nil || errY != nil || absFloat(x-want.XMM) > 0.001 || absFloat(y-want.YMM) > 0.001 {
			checks = append(checks, Check{Code: "CPL_POSITION", Severity: "error", Message: "Pick-and-place position does not match the design", ObjectIDs: []string{ref}})
		}
		if x < 0 || y < 0 || x > widthMM || y > heightMM {
			checks = append(checks, Check{Code: "CPL_OFF_BOARD", Severity: "error", Message: "Placement centroid is outside the board", ObjectIDs: []string{ref}})
		}
	}
	for ref := range expected {
		if !seen[ref] {
			checks = append(checks, Check{Code: "CPL_MISSING", Severity: "error", Message: "Placed component is missing from the pick-and-place file", ObjectIDs: []string{ref}})
		}
	}
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Code != checks[j].Code {
			return checks[i].Code < checks[j].Code
		}
		return strings.Join(checks[i].ObjectIDs, ",") < strings.Join(checks[j].ObjectIDs, ",")
	})
	return checks
}

// assemblyFiles is the set an assembly house needs on top of the bare-board
// Gerbers: the centroid file and the assembly BOM.
func assemblyFiles(def *Definition) map[string][]byte {
	return map[string][]byte{
		"assembly/pick-and-place.csv": renderPlacementCSV(def),
		"assembly/bom.csv":            renderAssemblyBOM(def),
	}
}

func mmField(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }

// naturalLess orders designators the way people read them: R2 before R10.
func naturalLess(a, b string) bool {
	pa, na := splitDesignator(a)
	pb, nb := splitDesignator(b)
	if pa != pb {
		return pa < pb
	}
	if na != nb {
		return na < nb
	}
	return a < b
}

func splitDesignator(v string) (string, int) {
	i := len(v)
	for i > 0 && v[i-1] >= '0' && v[i-1] <= '9' {
		i--
	}
	n, _ := strconv.Atoi(v[i:])
	return v[:i], n
}
