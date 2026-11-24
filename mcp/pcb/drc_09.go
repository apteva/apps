package main

import (
	"encoding/hex"
	"math"
	"strings"
)

// validateProductionGeometry contains the 0.9 fabrication checks that operate
// on exact native copper polygons and immutable footprint contracts.
func validateProductionGeometry(def *Definition) []Check {
	checks := []Check{}
	add := func(code, severity, message string, ids ...string) {
		checks = append(checks, Check{Code: code, Severity: severity, Message: message, ObjectIDs: ids})
	}
	board := boardPolygon(def.Board)
	netByNode := map[string]string{}
	for _, net := range def.Nets {
		for _, node := range net.Nodes {
			netByNode[node.ComponentID+":"+node.PinID] = net.ID
		}
	}
	pads := []*placedPad{}
	for _, component := range def.Components {
		contract := component.FootprintSpec
		if len(component.Pads) > 0 && contract == nil {
			add("FOOTPRINT_CONTRACT_MISSING", "warning", "Native pads are present but footprint provenance is not frozen", component.ID)
		} else if contract != nil {
			pinIDs := map[string]bool{}
			for _, pin := range component.Pins {
				pinIDs[pin.ID] = true
			}
			if strings.TrimSpace(contract.LibraryID) == "" || strings.TrimSpace(contract.Revision) == "" {
				add("FOOTPRINT_CONTRACT_ID", "error", "Footprint contract needs a library ID and revision", component.ID)
			}
			if contract.LibraryID != component.Footprint {
				add("FOOTPRINT_CONTRACT_MISMATCH", "error", "Footprint display mapping differs from the frozen library ID", component.ID)
			}
			if !contract.Verified {
				add("FOOTPRINT_UNVERIFIED", "warning", "Footprint contract has not been verified", component.ID)
			}
			if contract.PinCount != len(pinIDs) {
				add("FOOTPRINT_PIN_COUNT", "error", "Footprint contract pin count does not match the component", component.ID)
			}
			if len(contract.SourceSHA256) != 64 {
				add("FOOTPRINT_SOURCE_HASH", "error", "Footprint contract needs a SHA-256 source identity", component.ID)
			} else if _, err := hex.DecodeString(contract.SourceSHA256); err != nil {
				add("FOOTPRINT_SOURCE_HASH", "error", "Footprint source identity is not valid hexadecimal SHA-256", component.ID)
			}
			if len(contract.Courtyard) < 4 {
				add("FOOTPRINT_COURTYARD_MISSING", "error", "Verified footprint needs an embedded courtyard", component.ID)
			}
			if component.MPN != "" && len(contract.CompatibleMPNs) > 0 && !containsString(contract.CompatibleMPNs, component.MPN) {
				add("FOOTPRINT_MPN_MISMATCH", "error", "Component MPN is not listed as compatible with the frozen footprint", component.ID)
			}
		}
		if component.Model3D != nil {
			model := component.Model3D
			if model.LibraryID == "" || model.Revision == "" || model.WidthNM <= 0 || model.DepthNM <= 0 || model.HeightNM <= 0 {
				add("MODEL_3D_CONTRACT", "error", "Native 3D model contract has incomplete identity or dimensions", component.ID)
			}
			if !model.Verified {
				add("MODEL_3D_UNVERIFIED", "warning", "Native 3D model contract is not verified", component.ID)
			}
		}
		for _, pad := range component.Pads {
			placed := placePad(component, pad)
			placed.NetID = netByNode[component.ID+":"+pad.PinID]
			pads = append(pads, placed)
			if pad.PasteExpansionNM != 0 && (pad.WidthNM+2*pad.PasteExpansionNM <= 0 || pad.HeightNM+2*pad.PasteExpansionNM <= 0) {
				add("PASTE_APERTURE_INVALID", "error", "Paste expansion removes the entire aperture", component.ID, pad.ID)
			}
			for _, point := range placed.Polygon {
				if !pointInPolygonOrBoundary(point, board) {
					add("PAD_OUTSIDE_OUTLINE", "error", "Pad lies outside the polygonal board outline", component.ID, pad.ID)
					break
				}
			}
			if polygonEdgeDistance(placed.Polygon, board) < float64(def.Rules.MinEdgeClearanceNM) {
				add("PAD_EDGE_CLEARANCE", "error", "Pad violates polygonal board-edge clearance", component.ID, pad.ID)
			}
			if pad.DrillNM > 0 && !pad.Plated && !pad.NonPlated {
				add("PAD_PLATING_UNDECLARED", "error", "Drilled footprint pad must explicitly declare plating", component.ID, pad.ID)
			}
			if (pad.DrillWidthNM > 0) != (pad.DrillHeightNM > 0) {
				add("PAD_SLOT_GEOMETRY", "error", "Slotted drill needs both drill_width_nm and drill_height_nm", component.ID, pad.ID)
			}
		}
	}

	for i := 0; i < len(pads); i++ {
		for j := i + 1; j < len(pads); j++ {
			a, b := pads[i], pads[j]
			if !shareLayer(a.Pad.Layers, b.Pad.Layers) {
				continue
			}
			maskGap := polygonDistance(a.Polygon, b.Polygon) - float64(a.Pad.SolderMaskExpansionNM+b.Pad.SolderMaskExpansionNM)
			if maskGap < float64(def.Rules.MinSolderMaskSliverNM) {
				add("SOLDER_MASK_SLIVER", "error", "Solder-mask web between pads is below the manufacturing rule", a.ComponentID+":"+a.Pad.ID, b.ComponentID+":"+b.Pad.ID)
			}
		}
	}

	for index, via := range def.Vias {
		if via.DrillNM > 0 && (via.DiameterNM-via.DrillNM)/2 < def.Rules.MinAnnularRingNM {
			add("VIA_ANNULAR_RING", "error", "Via annular ring is below the manufacturing rule", via.ID)
		}
		if !pointInPolygonOrBoundary(Point{XNM: via.XNM, YNM: via.YNM}, board) || pointToPolygonEdgeDistance(Point{XNM: via.XNM, YNM: via.YNM}, board) < float64(via.DiameterNM/2+def.Rules.MinEdgeClearanceNM) {
			add("VIA_POLYGON_EDGE_CLEARANCE", "error", "Via violates the polygonal board outline clearance", via.ID)
		}
		for otherIndex := index + 1; otherIndex < len(def.Vias); otherIndex++ {
			other := def.Vias[otherIndex]
			if via.NetID == other.NetID || !viaLayerOverlap(def, via, other) {
				continue
			}
			minimum := float64(via.DiameterNM+other.DiameterNM)/2 + float64(def.Rules.MinClearanceNM)
			if pointDistance(Point{XNM: via.XNM, YNM: via.YNM}, Point{XNM: other.XNM, YNM: other.YNM}) < minimum {
				add("VIA_CLEARANCE", "error", "Different-net vias violate copper clearance", via.ID, other.ID)
			}
		}
		for _, trace := range def.Traces {
			if via.NetID == trace.NetID || !layerBetween(def, trace.Layer, via.FromLayer, via.ToLayer) {
				continue
			}
			minimum := float64(via.DiameterNM+trace.WidthNM)/2 + float64(def.Rules.MinClearanceNM)
			if pointToTraceDistance(Point{XNM: via.XNM, YNM: via.YNM}, trace) < minimum {
				add("VIA_TRACE_CLEARANCE", "error", "Trace violates clearance to a different-net via", via.ID, trace.ID)
			}
		}
		for _, pad := range pads {
			if via.NetID == pad.NetID || !layerBetweenAny(def, pad.Pad.Layers, via.FromLayer, via.ToLayer) {
				continue
			}
			if pointToPolygonDistance(Point{XNM: via.XNM, YNM: via.YNM}, pad.Polygon)-float64(via.DiameterNM)/2 < float64(def.Rules.MinClearanceNM) {
				add("VIA_PAD_CLEARANCE", "error", "Via violates clearance to a different-net pad", via.ID, pad.ComponentID+":"+pad.Pad.ID)
			}
		}
	}
	return checks
}

func boardPolygon(board Board) []Point {
	if len(board.Outline) >= 3 {
		return board.Outline
	}
	return boardOutline(board)
}

func polygonDistance(a, b []Point) float64 {
	if len(a) == 0 || len(b) == 0 {
		return math.MaxFloat64
	}
	if polygonsIntersect(a, b) || pointInPolygonOrBoundary(a[0], b) || pointInPolygonOrBoundary(b[0], a) {
		return 0
	}
	minimum := math.MaxFloat64
	for i := range a {
		a0, a1 := a[i], a[(i+1)%len(a)]
		for j := range b {
			b0, b1 := b[j], b[(j+1)%len(b)]
			minimum = math.Min(minimum, segmentDistance(a0, a1, b0, b1))
		}
	}
	return minimum
}

func tracePolygonDistance(trace Trace, polygon []Point) float64 {
	minimum := math.MaxFloat64
	for index := 1; index < len(trace.Points); index++ {
		a, b := trace.Points[index-1], trace.Points[index]
		if pointInPolygonOrBoundary(a, polygon) || pointInPolygonOrBoundary(b, polygon) {
			return 0
		}
		for edge := range polygon {
			minimum = math.Min(minimum, segmentDistance(a, b, polygon[edge], polygon[(edge+1)%len(polygon)]))
		}
	}
	return minimum
}

func pointToPolygonDistance(point Point, polygon []Point) float64 {
	if pointInPolygonOrBoundary(point, polygon) {
		return 0
	}
	return pointToPolygonEdgeDistance(point, polygon)
}

func pointToPolygonEdgeDistance(point Point, polygon []Point) float64 {
	minimum := math.MaxFloat64
	for index := range polygon {
		minimum = math.Min(minimum, pointSegmentDistance(point, polygon[index], polygon[(index+1)%len(polygon)]))
	}
	return minimum
}

func polygonEdgeDistance(inner, outer []Point) float64 {
	minimum := math.MaxFloat64
	for _, point := range inner {
		minimum = math.Min(minimum, pointToPolygonEdgeDistance(point, outer))
	}
	return minimum
}

func pointInPolygonOrBoundary(point Point, polygon []Point) bool {
	if pointInPolygon(point, polygon) {
		return true
	}
	return pointToPolygonEdgeDistance(point, polygon) < 1
}

func viaLayerOverlap(def *Definition, a, b Via) bool {
	for _, layer := range def.Board.Layers {
		if layer.Kind == "copper" && layerBetween(def, layer.ID, a.FromLayer, a.ToLayer) && layerBetween(def, layer.ID, b.FromLayer, b.ToLayer) {
			return true
		}
	}
	return false
}

func layerBetweenAny(def *Definition, layers []string, from, to string) bool {
	for _, layer := range layers {
		if layerBetween(def, layer, from, to) {
			return true
		}
	}
	return false
}
