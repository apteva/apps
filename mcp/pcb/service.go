package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type Service struct {
	store        *Store
	ctx          *sdk.AppCtx
	project      string
	artifactRoot string
}

type SimulationRun struct {
	Result   *SimulationResult `json:"result"`
	Artifact *Artifact         `json:"artifact"`
}

type FirmwareRun struct {
	Result   *FirmwareRunResult `json:"result"`
	Artifact *Artifact          `json:"artifact"`
}

type WiringSimulationRun struct {
	Result   *WiringSimulation `json:"result"`
	Artifact *Artifact         `json:"artifact"`
}

type FabricationVerificationRun struct {
	Result   *FabricationVerification `json:"result"`
	Artifact *Artifact                `json:"artifact"`
}

func (s *Service) revision(designID, revisionID int64) (*Design, *Revision, *Definition, error) {
	design, err := s.store.GetDesign(s.project, designID)
	if err != nil {
		return nil, nil, nil, err
	}
	if revisionID == 0 {
		revisionID = design.CurrentRevisionID
	}
	revision, err := s.store.GetRevision(s.project, revisionID)
	if err != nil {
		return nil, nil, nil, err
	}
	if revision.DesignID != designID {
		return nil, nil, nil, errors.New("revision does not belong to design")
	}
	def, err := decodeDefinition(revision.Definition)
	return design, revision, def, err
}

func (s *Service) Validate(designID, revisionID int64) (*ValidationRun, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	run, err := s.store.SaveValidation(s.project, design.ID, revision.ID, validateDefinition(def))
	if err == nil && s.ctx != nil {
		s.ctx.Emit("pcb.validation.completed", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "validation_id": run.ID, "status": run.Status, "errors": run.Errors, "warnings": run.Warnings})
	}
	return run, err
}

func (s *Service) Render(designID, revisionID int64) (*Artifact, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	return s.persistArtifact(design, revision, "preview", "svg", "image/svg+xml", renderSVG(def), map[string]any{"engine": engineVersion, "units": "nm"})
}

func (s *Service) BOM(designID, revisionID int64) (*Artifact, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	return s.persistArtifact(design, revision, "bom", "csv", "text/csv", renderBOM(def), map[string]any{"engine": engineVersion, "components": len(def.Components)})
}

func (s *Service) WiringValidate(designID, revisionID int64) (*WiringValidation, error) {
	_, _, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	if def.Wiring == nil {
		return nil, errors.New("revision has no wiring workspace")
	}
	report := validateWiring(def.Wiring)
	return &report, nil
}

func (s *Service) WiringExport(designID, revisionID int64, format string) (*Artifact, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	if def.Wiring == nil {
		return nil, errors.New("revision has no wiring workspace")
	}
	report := validateWiring(def.Wiring)
	if report.Status == "failed" {
		return nil, fmt.Errorf("wiring failed validation with %d errors; export refused", report.Errors)
	}
	format = strings.ToLower(strings.TrimSpace(format))
	kind := "wiring"
	contentType := ""
	var body []byte
	switch format {
	case "svg":
		body, contentType = renderWiringSVG(def, nil), "image/svg+xml"
	case "png":
		body, contentType = renderWiringPNG(def), "image/png"
	case "tutorial-json", "json":
		format, body, contentType = "json", wiringTutorialJSON(def), "application/json"
	case "tutorial-zip", "zip":
		format, contentType = "zip", "application/zip"
		body, err = deterministicZip(wiringTutorialFiles(def))
	default:
		return nil, fmt.Errorf("unsupported wiring export format %q", format)
	}
	if err != nil {
		return nil, err
	}
	return s.persistArtifact(design, revision, kind, format, contentType, body, map[string]any{"engine": engineVersion, "schema": wiringSchema, "validation_status": report.Status})
}

func (s *Service) WiringSimulate(designID, revisionID int64, source string, iterations int) (*WiringSimulationRun, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	result, err := simulateWiring(def, source, iterations)
	if err != nil {
		return nil, err
	}
	body, _ := json.MarshalIndent(result, "", "  ")
	body = append(body, '\n')
	artifact, err := s.persistArtifact(design, revision, "wiring-simulation", "json", "application/json", body, map[string]any{"engine": engineVersion, "schema": wiringSimulationSchema, "status": result.Status})
	if err != nil {
		return &WiringSimulationRun{Result: result, Artifact: artifact}, err
	}
	if s.ctx != nil {
		s.ctx.Emit("pcb.wiring.simulation.completed", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "artifact_id": artifact.ID, "status": result.Status})
	}
	return &WiringSimulationRun{Result: result, Artifact: artifact}, nil
}

func (s *Service) RouteSuggest(designID, revisionID int64, options RouteOptions) (*RoutePlan, error) {
	_, _, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	return suggestRoutes(def, options)
}

func (s *Service) RouteApply(designID, revisionID int64, options RouteOptions, note, author string, allowPartial bool) (*Revision, *RoutePlan, error) {
	design, parent, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, nil, err
	}
	plan, err := suggestRoutes(def, options)
	if err != nil {
		return nil, nil, err
	}
	if plan.Status == "failed" || plan.Status == "partial" && !allowPartial {
		return nil, plan, fmt.Errorf("route plan is %s with %d failures; review it or set allow_partial", plan.Status, len(plan.Failures))
	}
	if len(plan.Operations) == 0 {
		return nil, plan, errors.New("route plan contains no changes")
	}
	canonical, _, hash, err := applyOperations(def, plan.Operations)
	if err != nil {
		return nil, plan, err
	}
	operations, _ := json.Marshal(plan.Operations)
	revision, err := s.store.CreateRevision(s.project, design.ID, parent.ID, canonical, operations, hash, strings.TrimSpace(note), author)
	if err == nil && s.ctx != nil {
		s.ctx.Emit("pcb.revision.created", revisionEvent(revision))
		s.ctx.Emit("pcb.routing.completed", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "status": plan.Status, "routed_nets": plan.RoutedNets, "trace_count": plan.Metrics.TraceCount, "via_count": plan.Metrics.ViaCount})
	}
	return revision, plan, err
}

func (s *Service) RouteRemove(designID, revisionID int64, netIDs []string, note, author string) (*Revision, error) {
	design, parent, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range netIDs {
		want[id] = true
	}
	operations := []Operation{}
	for _, trace := range def.Traces {
		if strings.HasPrefix(trace.ID, "auto-") && (len(want) == 0 || want[trace.NetID]) {
			operations = append(operations, Operation{Type: "trace.remove", TraceID: trace.ID})
		}
	}
	for _, via := range def.Vias {
		if strings.HasPrefix(via.ID, "auto-") && (len(want) == 0 || want[via.NetID]) {
			operations = append(operations, Operation{Type: "via.remove", ViaID: via.ID})
		}
	}
	if len(operations) == 0 {
		return nil, errors.New("no matching autorouted copper to remove")
	}
	canonical, _, hash, err := applyOperations(def, operations)
	if err != nil {
		return nil, err
	}
	operationsJSON, _ := json.Marshal(operations)
	revision, err := s.store.CreateRevision(s.project, design.ID, parent.ID, canonical, operationsJSON, hash, strings.TrimSpace(note), author)
	if err == nil && s.ctx != nil {
		s.ctx.Emit("pcb.revision.created", revisionEvent(revision))
	}
	return revision, err
}

func (s *Service) Simulate(designID, revisionID int64, options SimulationOptions) (*SimulationRun, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	result, err := simulateDefinition(def, options)
	if err != nil {
		return nil, err
	}
	body, _ := json.MarshalIndent(result, "", "  ")
	body = append(body, '\n')
	artifact, err := s.persistArtifact(design, revision, "simulation", "json", "application/json", body, map[string]any{"engine": engineVersion, "schema": simulationSchema, "samples": result.Samples, "status": result.Status})
	if err != nil {
		return &SimulationRun{Result: result, Artifact: artifact}, err
	}
	if s.ctx != nil {
		s.ctx.Emit("pcb.simulation.completed", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "artifact_id": artifact.ID, "status": result.Status, "samples": result.Samples})
	}
	return &SimulationRun{Result: result, Artifact: artifact}, nil
}

func (s *Service) Firmware(designID, revisionID int64, options FirmwareOptions) (*FirmwareRun, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	result, err := runFirmwareLab(def, options)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(options.ExecutorFunction) != "" {
		if s.ctx == nil || s.ctx.PlatformAPI() == nil {
			return nil, errors.New("firmware executor platform client unavailable")
		}
		bound := s.ctx.IntegrationFor("firmware_executor")
		if bound == nil || bound.Kind != "app" || bound.InstallID <= 0 {
			return nil, errors.New("executor_function requires a bound Functions app")
		}
		var external map[string]any
		err = s.ctx.PlatformAPI().CallAppResult("functions", "functions_invoke", map[string]any{"name": options.ExecutorFunction, "event": map[string]any{"action": "compile_and_run_arduino", "language": options.Language, "board": result.Board, "source": options.Source, "iterations": options.Iterations, "virtual_devices": result.VirtualDevices, "_project_id": s.project}}, &external)
		if err != nil {
			return nil, fmt.Errorf("firmware executor: %w", err)
		}
		result.Executor = map[string]any{"app": "functions", "install_id": bound.InstallID, "function": options.ExecutorFunction, "response": external}
		result.Runtime = "apteva-functions + " + result.Runtime
	}
	body := firmwareResultJSON(result)
	artifact, err := s.persistArtifact(design, revision, "firmware", "json", "application/json", body, map[string]any{"engine": engineVersion, "schema": firmwareSchema, "runtime": result.Runtime, "board": result.Board})
	if err != nil {
		return &FirmwareRun{Result: result, Artifact: artifact}, err
	}
	if s.ctx != nil {
		s.ctx.Emit("pcb.firmware.completed", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "artifact_id": artifact.ID, "status": result.Status, "board": result.Board})
	}
	return &FirmwareRun{Result: result, Artifact: artifact}, nil
}

func (s *Service) Manufacturing(designID, revisionID int64) (*Artifact, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	report := validateDefinition(def)
	if report.Status == "failed" {
		return nil, fmt.Errorf("revision failed validation with %d errors; manufacturing output refused", report.Errors)
	}
	files := manufacturingFiles(def)
	verification := verifyManufacturingFiles(def, files)
	if verification.Status == "failed" {
		return nil, fmt.Errorf("independent fabrication verification failed with %d errors; manufacturing output refused", verification.Errors)
	}
	body, err := deterministicZip(files)
	if err != nil {
		return nil, err
	}
	return s.persistArtifact(design, revision, "manufacturing", "zip", "application/zip", body, map[string]any{"engine": engineVersion, "format": "Gerber X2 + Excellon", "files": manufacturingFileSummary(def), "validation_status": report.Status, "fabrication_verification_status": verification.Status, "parsed_draws": verification.Summary["draws"], "parsed_holes": verification.Summary["holes"]})
}

func (s *Service) VerifyManufacturing(designID, revisionID int64) (*FabricationVerificationRun, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	result := verifyManufacturingFiles(def, manufacturingFiles(def))
	body, _ := json.MarshalIndent(result, "", "  ")
	body = append(body, '\n')
	artifact, err := s.persistArtifact(design, revision, "verification", "json", "application/json", body, map[string]any{"engine": engineVersion, "schema": fabricationVerificationSchema, "status": result.Status, "errors": result.Errors})
	if err != nil {
		return &FabricationVerificationRun{Result: result, Artifact: artifact}, err
	}
	if s.ctx != nil {
		s.ctx.Emit("pcb.fabrication.verified", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "artifact_id": artifact.ID, "status": result.Status, "errors": result.Errors})
	}
	return &FabricationVerificationRun{Result: result, Artifact: artifact}, nil
}

func (s *Service) Release(_ context.Context, designID, revisionID int64, note string) (*Artifact, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	report := validateDefinition(def)
	run, err := s.store.SaveValidation(s.project, design.ID, revision.ID, report)
	if err != nil {
		return nil, err
	}
	if s.ctx != nil {
		s.ctx.Emit("pcb.validation.completed", map[string]any{
			"design_id": design.ID, "revision_id": revision.ID, "validation_id": run.ID,
			"status": run.Status, "errors": run.Errors, "warnings": run.Warnings,
		})
	}
	if report.Status == "failed" {
		return nil, fmt.Errorf("revision failed validation with %d errors; release refused", report.Errors)
	}
	manufacturing := manufacturingFiles(def)
	verification := verifyManufacturingFiles(def, manufacturing)
	if verification.Status == "failed" {
		return nil, fmt.Errorf("independent fabrication verification failed with %d errors; release refused", verification.Errors)
	}
	files := map[string][]byte{}
	source, _ := json.MarshalIndent(def, "", "  ")
	files["source/pcb.json"] = append(source, '\n')
	validation, _ := json.MarshalIndent(map[string]any{
		"design_id": design.ID, "revision_id": revision.ID, "report": report,
	}, "", "  ")
	files["validation/report.json"] = append(validation, '\n')
	verificationBody, _ := json.MarshalIndent(verification, "", "  ")
	files["verification/fabrication.json"] = append(verificationBody, '\n')
	files["outputs/board.svg"] = renderSVG(def)
	files["outputs/bom.csv"] = renderBOM(def)
	if def.Wiring != nil {
		files["wiring/illustration.svg"] = renderWiringSVG(def, nil)
		files["wiring/illustration.png"] = renderWiringPNG(def)
		files["wiring/tutorial.json"] = wiringTutorialJSON(def)
	}
	for name, body := range manufacturing {
		files["manufacturing/"+name] = body
	}
	hashes := map[string]string{}
	for name, body := range files {
		sum := sha256.Sum256(body)
		hashes[name] = hex.EncodeToString(sum[:])
	}
	manifest := map[string]any{"schema": releaseSchema, "engine": engineVersion, "design_id": design.ID, "design_name": design.Name, "revision_id": revision.ID, "revision_number": revision.Number, "source_sha256": revision.SourceSHA256, "validation_status": report.Status, "fabrication_verification_status": verification.Status, "note": strings.TrimSpace(note), "created_at": revision.CreatedAt, "files": hashes, "compatibility": map[string]any{"native_format": pcbSchema, "original_sources_preserved": true}}
	manifestBody, _ := json.MarshalIndent(manifest, "", "  ")
	files["manifest.json"] = append(manifestBody, '\n')
	body, err := deterministicZip(files)
	if err != nil {
		return nil, err
	}
	artifact, err := s.persistArtifact(design, revision, "release", "zip", "application/zip", body, map[string]any{"schema": releaseSchema, "validation_status": report.Status})
	if err == nil && s.ctx != nil {
		s.ctx.Emit("pcb.release.created", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "artifact_id": artifact.ID})
	}
	return artifact, err
}

func (s *Service) persistArtifact(design *Design, revision *Revision, kind, format, contentType string, body []byte, metadata map[string]any) (*Artifact, error) {
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	ext := format
	// Manufacturing and release artifacts are both ZIP files, but they contain
	// different payloads. Include their semantic kind so one local-cache write
	// can never overwrite the other for the same immutable revision.
	suffix := ""
	if kind != "preview" && kind != "bom" {
		suffix = "-" + safeFilename(kind)
	}
	// Simulations and firmware runs are repeatable experiments on the same
	// immutable revision. Content-address their names so a fault run, changed
	// probe set, or different sketch can never overwrite an earlier local
	// result. Manufacturing and release names remain stable for downstream
	// handoff conventions.
	if kind == "simulation" || kind == "firmware" || kind == "wiring-simulation" {
		digest := hash
		if len(digest) > 12 {
			digest = digest[:12]
		}
		suffix += "-" + digest
	}
	name := fmt.Sprintf("%s-r%d%s.%s", safeFilename(design.Name), revision.Number, suffix, ext)
	dir := filepath.Join(s.artifactRoot, fmt.Sprintf("design-%d", design.ID), fmt.Sprintf("revision-%d", revision.ID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return nil, err
	}
	storageID := ""
	var uploadErr error
	if s.ctx != nil {
		storageID, uploadErr = s.uploadStorage(name, contentType, body, design.ID, revision.ID, hash)
		if uploadErr != nil {
			s.ctx.Logger().Error("required PCB artifact Storage upload failed", "format", format, "err", uploadErr)
			metadata["storage_upload_status"] = "failed"
			metadata["storage_upload_error"] = uploadErr.Error()
		} else {
			metadata["storage_upload_status"] = "uploaded"
		}
	} else {
		metadata["storage_upload_status"] = "local-only"
	}
	meta, _ := json.Marshal(metadata)
	artifact, err := s.store.SaveArtifact(s.project, Artifact{DesignID: design.ID, RevisionID: revision.ID, Kind: kind, Format: format, Name: name, ContentType: contentType, LocalPath: path, StorageFileID: storageID, SHA256: hash, SizeBytes: int64(len(body)), Metadata: meta})
	if err == nil && s.ctx != nil {
		s.ctx.Emit("pcb.artifact.created", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "artifact_id": artifact.ID, "kind": kind, "format": format, "storage_file_id": storageID})
	}
	if err != nil {
		return nil, err
	}
	if uploadErr != nil {
		return artifact, fmt.Errorf("artifact retained locally but required Storage upload failed: %w", uploadErr)
	}
	return artifact, nil
}

func (s *Service) uploadStorage(name, contentType string, body []byte, designID, revisionID int64, hash string) (string, error) {
	if s.ctx == nil || s.ctx.PlatformAPI() == nil {
		return "", errors.New("storage platform client unavailable")
	}
	bound := s.ctx.IntegrationFor("storage")
	if bound == nil || bound.Kind != "app" || bound.InstallID <= 0 {
		return "", errors.New("no Storage app is bound to the storage role")
	}
	// App binding values are app-install IDs. BoundIntegration.AppName is
	// populated through the legacy agent directory and can therefore resolve
	// to an unrelated agent with the same numeric ID. This role permits only
	// the canonical Storage app; authorization still enforces InstallID.
	const storageApp = "storage"
	var out struct {
		ID int64 `json:"id"`
	}
	err := s.ctx.PlatformAPI().CallAppResult(storageApp, "files_upload", map[string]any{"name": name, "folder": fmt.Sprintf("/.pcb/%d/", designID), "content_base64": base64.StdEncoding.EncodeToString(body), "content_type": contentType, "source": "pcb-studio", "tags": []string{"pcb", fmt.Sprintf("revision:%d", revisionID), "sha256:" + hash}, "_project_id": s.project}, &out)
	if err != nil {
		return "", err
	}
	if out.ID <= 0 {
		return "", errors.New("Storage upload returned no file id")
	}
	return strconv.FormatInt(out.ID, 10), nil
}

func renderBOM(def *Definition) []byte {
	type group struct {
		Value, MPN, Footprint, Name string
		Refs                        []string
	}
	groups := map[string]*group{}
	for _, c := range def.Components {
		key := strings.Join([]string{c.MPN, c.Value, c.Footprint, c.Name}, "\x00")
		g := groups[key]
		if g == nil {
			g = &group{Value: c.Value, MPN: c.MPN, Footprint: c.Footprint, Name: c.Name}
			groups[key] = g
		}
		g.Refs = append(g.Refs, c.Designator)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"quantity", "designators", "value", "manufacturer_part_number", "footprint", "name"})
	for _, key := range keys {
		g := groups[key]
		sort.Strings(g.Refs)
		_ = w.Write([]string{strconv.Itoa(len(g.Refs)), strings.Join(g.Refs, " "), g.Value, g.MPN, g.Footprint, g.Name})
	}
	w.Flush()
	return buf.Bytes()
}

func deterministicZip(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	stamp := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range names {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetModTime(stamp)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err = w.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func trimFloat(v float64) string {
	s := strconv.FormatFloat(v, 'f', 6, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}
func safeFilename(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	var b strings.Builder
	dash := false
	for _, r := range v {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "pcb"
	}
	return out
}
