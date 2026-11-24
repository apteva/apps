package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// RotationOffset corrects a footprint's zero orientation to the one an
// assembly house's part library uses. Pattern is a Go regular expression
// matched against the component footprint (or footprint library ID).
type RotationOffset struct {
	Pattern string  `json:"pattern"`
	Degrees float64 `json:"degrees"`
}

// jlcRotationOffsets are reference corrections for JLCPCB's part library,
// following the defaults shipped by the kicad-jlcpcb-tools plugin. JLCPCB
// re-checks orientation in its DFM review; confirm on the placement preview
// before paying. Per-component assembly_rotation_offset_deg and the active
// fab profile's rotation_offsets take precedence, in that order.
var jlcRotationOffsets = []RotationOffset{
	{Pattern: `^SOT-223`, Degrees: 180},
	{Pattern: `^SOT-23`, Degrees: 180},
	{Pattern: `^SOT-353`, Degrees: 180},
	{Pattern: `^QFN-`, Degrees: 270},
	{Pattern: `^LQFP-`, Degrees: 270},
	{Pattern: `^TQFP-`, Degrees: 270},
	{Pattern: `^SOP-`, Degrees: 270},
	{Pattern: `^TSSOP-`, Degrees: 270},
	{Pattern: `^DFN-`, Degrees: 270},
	{Pattern: `^SOIC-`, Degrees: 270},
	{Pattern: `^MSOP-10_`, Degrees: 270},
}

const maxRotationOffsets = 64

var lcscPartPattern = regexp.MustCompile(`^C[0-9]{1,12}$`)

func rotationOffsetLimits(def *Definition) error {
	check := func(owner string, offsets []RotationOffset) error {
		if len(offsets) > maxRotationOffsets {
			return fmt.Errorf("%s has more than %d rotation offsets", owner, maxRotationOffsets)
		}
		for _, o := range offsets {
			if _, err := regexp.Compile(o.Pattern); err != nil || strings.TrimSpace(o.Pattern) == "" || len(o.Pattern) > 200 {
				return fmt.Errorf("%s rotation offset pattern %q is not a valid regular expression", owner, o.Pattern)
			}
			if math.IsNaN(o.Degrees) || math.Abs(o.Degrees) > 360 {
				return fmt.Errorf("%s rotation offset %q must be between -360 and 360 degrees", owner, o.Pattern)
			}
		}
		return nil
	}
	for _, p := range def.FabProfiles {
		if err := check("fab profile "+p.ID, p.RotationOffsets); err != nil {
			return err
		}
	}
	for _, c := range def.Components {
		if c.AssemblyRotationOffsetDeg != nil && (math.IsNaN(*c.AssemblyRotationOffsetDeg) || math.Abs(*c.AssemblyRotationOffsetDeg) > 360) {
			return fmt.Errorf("component %q assembly_rotation_offset_deg must be between -360 and 360", c.ID)
		}
		if c.LCSC != "" && !lcscPartPattern.MatchString(c.LCSC) {
			return fmt.Errorf("component %q lcsc %q must look like C2040", c.ID, c.LCSC)
		}
	}
	return nil
}

// assemblyRotationOffset resolves the correction for one component.
func assemblyRotationOffset(def *Definition, c Component, pkg string) (float64, string) {
	if c.AssemblyRotationOffsetDeg != nil {
		return *c.AssemblyRotationOffsetDeg, "component"
	}
	tables := []struct {
		source  string
		offsets []RotationOffset
	}{}
	if p, ok := activeFabProfile(def); ok && len(p.RotationOffsets) > 0 {
		tables = append(tables, struct {
			source  string
			offsets []RotationOffset
		}{"fab_profile:" + p.ID, p.RotationOffsets})
	}
	tables = append(tables, struct {
		source  string
		offsets []RotationOffset
	}{"jlcpcb-reference", jlcRotationOffsets})
	names := []string{pkg}
	if i := strings.LastIndex(pkg, ":"); i >= 0 {
		names = append(names, pkg[i+1:])
	}
	for _, table := range tables {
		for _, o := range table.offsets {
			re, err := regexp.Compile(o.Pattern)
			if err != nil {
				continue
			}
			for _, name := range names {
				if re.MatchString(name) {
					return o.Degrees, table.source
				}
			}
		}
	}
	return 0, ""
}

func normalizeDegrees(v float64) float64 {
	v = math.Mod(v, 360)
	if v < 0 {
		v += 360
	}
	return v
}

// renderJLCPlacementCSV writes JLCPCB's CPL format: Designator, Mid X, Mid Y,
// Layer, Rotation, with the rotation corrected to JLCPCB's library zero.
func renderJLCPlacementCSV(def *Definition) []byte {
	byDesignator := map[string]Component{}
	for _, c := range def.Components {
		byDesignator[c.Designator] = c
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"Designator", "Mid X", "Mid Y", "Layer", "Rotation"})
	for _, row := range placementRows(def) {
		offset, _ := assemblyRotationOffset(def, byDesignator[row.Designator], row.Package)
		_ = w.Write([]string{row.Designator, mmField(row.XMM) + "mm", mmField(row.YMM) + "mm", row.Layer, strconv.FormatFloat(normalizeDegrees(row.RotationDeg+offset), 'f', 0, 64)})
	}
	w.Flush()
	return buf.Bytes()
}

// renderJLCBOM writes JLCPCB's assembly BOM: Comment, Designator, Footprint,
// LCSC Part #. Parts without an LCSC number are still listed (JLCPCB asks
// for them in the upload step) and reported by jlcAssemblyChecks.
func renderJLCBOM(def *Definition) []byte {
	placed := map[string]bool{}
	for _, row := range placementRows(def) {
		placed[row.Designator] = true
	}
	type group struct {
		Comment, Footprint, LCSC string
		Refs                     []string
	}
	groups := map[string]*group{}
	for _, c := range def.Components {
		if !placed[c.Designator] {
			continue
		}
		comment := c.Value
		if comment == "" {
			comment = c.Name
		}
		key := c.LCSC + "\x00" + c.MPN + "\x00" + comment + "\x00" + c.Footprint
		if groups[key] == nil {
			groups[key] = &group{Comment: comment, Footprint: c.Footprint, LCSC: c.LCSC}
		}
		groups[key].Refs = append(groups[key].Refs, c.Designator)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"Comment", "Designator", "Footprint", "LCSC Part #"})
	for _, k := range keys {
		g := groups[k]
		sort.Slice(g.Refs, func(i, j int) bool { return naturalLess(g.Refs[i], g.Refs[j]) })
		_ = w.Write([]string{g.Comment, strings.Join(g.Refs, ","), g.Footprint, g.LCSC})
	}
	w.Flush()
	return buf.Bytes()
}

// jlcAssemblyChecks reports what JLCPCB assembly would reject or ask about.
func jlcAssemblyChecks(def *Definition) []Check {
	checks := []Check{}
	missing := []string{}
	for _, row := range placementRows(def) {
		for _, c := range def.Components {
			if c.Designator == row.Designator && c.LCSC == "" {
				missing = append(missing, c.ID)
			}
		}
	}
	if len(missing) > 0 {
		checks = append(checks, Check{Code: "ASSEMBLY_LCSC_MISSING", Severity: "warning", Message: fmt.Sprintf("%d placed parts have no LCSC part number; JLCPCB assembly needs one per line (use pcb_lcsc_search / pcb_lcsc_match)", len(missing)), ObjectIDs: missing})
	}
	return checks
}
