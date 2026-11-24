package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// attachNativeContracts freezes the exact land pattern and native 3D bounding
// box into the revision. It is intentionally deterministic: library IDs are
// useful for interchange, but the embedded pads remain authoritative.
func attachNativeContracts(def *Definition) {
	models := map[string]ComponentEnvelope{}
	if def.Mechanical != nil {
		for _, envelope := range def.Mechanical.ComponentEnvelopes {
			if envelope.ComponentID != "" {
				models[envelope.ComponentID] = envelope
			}
		}
	}
	for index := range def.Components {
		component := &def.Components[index]
		pinIDs := map[string]bool{}
		for _, pin := range component.Pins {
			pinIDs[pin.ID] = true
		}
		throughHole := false
		for padIndex := range component.Pads {
			pad := &component.Pads[padIndex]
			if pad.DrillNM > 0 || pad.DrillWidthNM > 0 || pad.DrillHeightNM > 0 {
				throughHole = true
				pad.Plated = !pad.NonPlated
				if pad.SolderMaskExpansionNM == 0 {
					pad.SolderMaskExpansionNM = 75_000
				}
			} else {
				if pad.SolderMaskExpansionNM == 0 {
					pad.SolderMaskExpansionNM = 50_000
				}
				if pad.PasteExpansionNM == 0 {
					pad.PasteExpansionNM = -25_000
				}
			}
		}
		if libraryContractFrozen(component) {
			attachLibraryModel3D(component, models)
			continue
		}
		body := inferredBody(*component)
		gap := int64(250_000)
		courtyard := []Point{{XNM: -body.WidthNM/2 - gap, YNM: -body.HeightNM/2 - gap}, {XNM: body.WidthNM/2 + gap, YNM: -body.HeightNM/2 - gap}, {XNM: body.WidthNM/2 + gap, YNM: body.HeightNM/2 + gap}, {XNM: -body.WidthNM/2 - gap, YNM: body.HeightNM/2 + gap}}
		standard := "IPC-7351B"
		if throughHole {
			standard = "IPC-2221B"
		}
		libraryID := strings.TrimSpace(component.Footprint)
		contractBody, _ := json.Marshal(map[string]any{"library_id": libraryID, "revision": "apteva-2026.08", "pads": component.Pads, "courtyard": courtyard})
		sum := sha256.Sum256(contractBody)
		compatible := []string{}
		if strings.TrimSpace(component.MPN) != "" {
			compatible = append(compatible, component.MPN)
		}
		component.FootprintSpec = &FootprintSpec{LibraryID: libraryID, Revision: "apteva-2026.08", Standard: standard, Source: "apteva://footprints/" + libraryID, SourceSHA256: hex.EncodeToString(sum[:]), Verified: libraryID != "" && len(component.Pads) > 0, PinCount: len(pinIDs), Courtyard: courtyard, CompatibleMPNs: compatible}
		if envelope, ok := models[component.ID]; ok {
			modelBody, _ := json.Marshal(envelope)
			modelSum := sha256.Sum256(modelBody)
			component.Model3D = &Model3DSpec{LibraryID: envelope.ModelRef, Revision: "apteva-2026.08", SourceSHA256: hex.EncodeToString(modelSum[:]), WidthNM: envelope.WidthNM, DepthNM: envelope.DepthNM, HeightNM: envelope.ZMaxNM - envelope.ZMinNM, Verified: envelope.WidthNM > 0 && envelope.DepthNM > 0 && envelope.ZMaxNM > envelope.ZMinNM}
			component.FootprintSpec.AssemblyHeightNM = envelope.ZMaxNM - envelope.ZMinNM
		}
	}
}
