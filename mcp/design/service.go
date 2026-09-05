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
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type Service struct {
	store         *Store
	engine        *Engine
	ctx           *sdk.AppCtx
	project       string
	artifactRoot  string
	maxOperations int
}

func (s *Service) Build(ctx context.Context, designID, revisionID int64, requested []string) (*BuildResult, error) {
	design, err := s.store.GetDesign(s.project, designID)
	if err != nil {
		return nil, err
	}
	if revisionID == 0 {
		revisionID = design.CurrentRevisionID
	}
	revision, err := s.store.GetRevision(s.project, revisionID)
	if err != nil {
		return nil, err
	}
	if revision.DesignID != design.ID {
		return nil, errors.New("revision does not belong to design")
	}
	canonical, definition, err := normalizeDefinition(revision.Definition, s.maxOperations)
	if err != nil {
		return nil, err
	}
	parameters, err := normalizeParameters(revision.Parameters, definition)
	if err != nil {
		return nil, err
	}
	formats, err := normalizeFormats(requested)
	if err != nil {
		return nil, err
	}
	runnerFormats := []string{"mesh-json"}
	for _, format := range formats {
		if format == "step" || format == "stl" {
			runnerFormats = append(runnerFormats, format)
		}
	}
	runnerFormats = uniqueStrings(runnerFormats)
	runID, err := s.store.StartBuild(design.ID, revision.ID)
	if err != nil {
		return nil, err
	}
	runtimeDefinition, dependencies, resolveErr := s.materializeComponentSources(revision.ID, canonical)
	if resolveErr != nil {
		run, _ := s.store.FinishBuild(runID, "failed", map[string]any{}, []any{}, resolveErr.Error(), 0)
		if run != nil {
			return &BuildResult{Run: *run}, resolveErr
		}
		return nil, resolveErr
	}
	result, buildErr := s.engine.Build(ctx, design.Name, runtimeDefinition, parameters, runnerFormats)
	if buildErr != nil {
		duration := time.Duration(0)
		if result != nil {
			duration = result.Duration
		}
		run, _ := s.store.FinishBuild(runID, "failed", map[string]any{}, []any{}, buildErr.Error(), duration)
		if s.ctx != nil {
			s.ctx.Emit("design.build.failed", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "build_id": runID, "error": buildErr.Error()})
		}
		if run != nil {
			return &BuildResult{Run: *run}, buildErr
		}
		return nil, buildErr
	}
	checks := evaluateChecks(definition, result.Report)
	buildStatus := "passed"
	for _, check := range checks {
		if check.Status == "fail" {
			buildStatus = "failed"
			break
		}
		if check.Status == "warning" && buildStatus == "passed" {
			buildStatus = "warning"
		}
	}
	run, err := s.store.FinishBuild(runID, buildStatus, result.Report, checks, "", result.Duration)
	if err != nil {
		return nil, err
	}
	artifacts, err := s.persistEngineArtifacts(design, revision, run, result, formats, definition)
	if err != nil {
		return nil, err
	}
	if s.ctx != nil {
		event := "design.build.completed"
		if buildStatus == "failed" {
			event = "design.build.failed"
		}
		s.ctx.Emit(event, map[string]any{
			"design_id": design.ID, "revision_id": revision.ID, "build_id": run.ID, "status": buildStatus,
		})
	}
	return &BuildResult{Run: *run, Report: result.Report, Checks: checks, Artifacts: artifacts, Parameters: result.Parameters, Dependencies: dependencies}, nil
}

func normalizeFormats(input []string) ([]string, error) {
	allowed := map[string]bool{"mesh-json": true, "step": true, "stl": true, "3mf": true, "glb": true}
	if len(input) == 0 {
		return []string{"mesh-json"}, nil
	}
	out := []string{}
	for _, format := range input {
		format = strings.ToLower(strings.TrimSpace(format))
		if !allowed[format] {
			return nil, fmt.Errorf("unsupported format %q", format)
		}
		out = append(out, format)
	}
	return uniqueStrings(out), nil
}

func uniqueStrings(input []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, item := range input {
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func (s *Service) persistEngineArtifacts(design *Design, revision *Revision, run *BuildRun, result *EngineResult, requested []string, definition *DesignDefinition) ([]Artifact, error) {
	var meshArtifact EngineArtifact
	partMeshes := map[string]EngineArtifact{}
	for _, artifact := range result.Artifacts {
		if artifact.Format != "mesh-json" {
			continue
		}
		if artifact.PartID == "" {
			meshArtifact = artifact
		} else {
			partMeshes[artifact.PartID] = artifact
		}
	}
	if meshArtifact.Path == "" {
		return nil, errors.New("geometry runner did not return mesh-json")
	}
	meshBody, err := os.ReadFile(meshArtifact.Path)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{"mesh-json": true}
	for _, format := range requested {
		wanted[format] = true
	}
	type generatedArtifact struct {
		Format, ContentType, PartID, PartName string
		Body                                  []byte
	}
	generated := []generatedArtifact{{Format: "mesh-json", ContentType: "application/json", Body: meshBody}}
	for _, artifact := range result.Artifacts {
		if artifact.Format == "mesh-json" || !wanted[artifact.Format] {
			continue
		}
		body, err := os.ReadFile(artifact.Path)
		if err != nil {
			return nil, err
		}
		generated = append(generated, generatedArtifact{
			Format: artifact.Format, ContentType: artifact.ContentType, Body: body,
			PartID: artifact.PartID, PartName: artifact.PartName,
		})
	}
	if wanted["3mf"] {
		body, err := meshTo3MF(meshBody)
		if err != nil {
			return nil, err
		}
		generated = append(generated, generatedArtifact{Format: "3mf", ContentType: "model/3mf", Body: body})
		for _, part := range definition.Parts {
			mesh, ok := partMeshes[part.ID]
			if !ok {
				continue
			}
			partBody, err := os.ReadFile(mesh.Path)
			if err != nil {
				return nil, err
			}
			body, err := meshTo3MF(partBody)
			if err != nil {
				return nil, fmt.Errorf("create 3MF for part %s: %w", part.ID, err)
			}
			generated = append(generated, generatedArtifact{Format: "3mf", ContentType: "model/3mf", Body: body, PartID: part.ID, PartName: part.Name})
		}
	}
	if wanted["glb"] {
		body, err := meshToGLB(meshBody)
		if err != nil {
			return nil, err
		}
		generated = append(generated, generatedArtifact{Format: "glb", ContentType: "model/gltf-binary", Body: body})
	}
	out := make([]Artifact, 0, len(generated))
	for _, file := range generated {
		artifact, err := s.persistArtifactForPart(design, revision, run, file.Format, file.ContentType, file.Body, file.PartID, file.PartName, definition)
		if err != nil {
			return nil, err
		}
		out = append(out, *artifact)
	}
	return out, nil
}

func (s *Service) persistArtifact(design *Design, revision *Revision, run *BuildRun, format, contentType string, body []byte) (*Artifact, error) {
	return s.persistArtifactForPart(design, revision, run, format, contentType, body, "", "", nil)
}

func (s *Service) persistArtifactForPart(design *Design, revision *Revision, run *BuildRun, format, contentType string, body []byte, partID, partName string, definition *DesignDefinition) (*Artifact, error) {
	hash := sha256.Sum256(body)
	hashText := hex.EncodeToString(hash[:])
	ext := format
	if format == "mesh-json" {
		ext = "mesh.json"
	}
	base := safeFilename(design.Name)
	if partID != "" {
		base += "-" + safeFilename(partID)
	}
	name := fmt.Sprintf("%s-r%d.%s", base, revision.RevisionNumber, ext)
	dir := filepath.Join(s.artifactRoot, fmt.Sprintf("design-%d", design.ID), fmt.Sprintf("revision-%d", revision.ID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return nil, err
	}
	var storageID *int64
	if format != "mesh-json" {
		if id, err := s.uploadStorage(name, contentType, body, design.ID, revision.ID, hashText); err == nil && id > 0 {
			storageID = &id
		} else if err != nil && s.ctx != nil {
			s.ctx.Logger().Warn("design artifact storage upload failed; retaining local copy", "format", format, "err", err)
		}
	}
	artifactMetadata := map[string]any{"engine": engineName, "engine_version": engineVersion}
	if partID != "" {
		artifactMetadata["part_id"] = partID
		artifactMetadata["part_name"] = partName
		if definition != nil {
			for _, part := range definition.Parts {
				if part.ID == partID {
					artifactMetadata["material_id"] = part.MaterialID
					artifactMetadata["manufacturing"] = part.Manufacturing
					if part.Source != nil {
						artifactMetadata["component_source"] = part.Source
					}
					break
				}
			}
		}
	}
	metadata, _ := json.Marshal(artifactMetadata)
	buildID := run.ID
	artifact, err := s.store.SaveArtifact(Artifact{
		DesignID: design.ID, RevisionID: revision.ID, BuildRunID: &buildID,
		Kind: ternary(partID != "", "part-export", ternary(format == "mesh-json" || format == "glb", "preview", "export")), Format: format,
		Name: name, ContentType: contentType, SHA256: hashText, SizeBytes: int64(len(body)),
		StorageFileID: storageID, LocalPath: path, Metadata: metadata,
	})
	if err != nil {
		return nil, err
	}
	if s.ctx != nil {
		s.ctx.Emit("design.artifact.created", map[string]any{
			"design_id": design.ID, "revision_id": revision.ID, "artifact_id": artifact.ID,
			"format": format, "sha256": hashText, "storage_file_id": storageID,
		})
	}
	return artifact, nil
}

func (s *Service) uploadStorage(name, contentType string, body []byte, designID, revisionID int64, hash string) (int64, error) {
	if s.ctx == nil || s.ctx.PlatformAPI() == nil {
		return 0, errors.New("storage platform client unavailable")
	}
	bound := s.ctx.IntegrationFor("storage")
	if bound == nil || bound.Kind != "app" || bound.InstallID <= 0 {
		return 0, errors.New("bind Storage to the required storage role first")
	}
	tool := "files_upload"
	if bound.ToolFor != nil {
		tool = bound.ToolFor("files.write")
	}
	var output struct {
		ID int64 `json:"id"`
	}
	err := s.ctx.PlatformAPI().CallAppResult("storage", tool, map[string]any{
		"name": name, "folder": fmt.Sprintf("/.design/%d/", designID),
		"content_base64": base64.StdEncoding.EncodeToString(body), "content_type": contentType,
		"source": "design-studio", "tags": []string{"design", fmt.Sprintf("revision:%d", revisionID), "sha256:" + hash},
		"_project_id": s.project,
	}, &output)
	if err != nil {
		return 0, err
	}
	return output.ID, nil
}

func safeFilename(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var output strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			output.WriteRune(char)
		} else if output.Len() > 0 && !strings.HasSuffix(output.String(), "-") {
			output.WriteByte('-')
		}
	}
	result := strings.Trim(output.String(), "-")
	if result == "" {
		return "design"
	}
	return result
}

func (s *Service) ManufacturingPackage(ctx context.Context, designID, revisionID int64) (*Artifact, error) {
	design, err := s.store.GetDesign(s.project, designID)
	if err != nil {
		return nil, err
	}
	if revisionID == 0 {
		revisionID = design.CurrentRevisionID
	}
	revision, err := s.store.GetRevision(s.project, revisionID)
	if err != nil {
		return nil, err
	}
	result, err := s.Build(ctx, designID, revisionID, []string{"step", "stl", "3mf", "glb"})
	if err != nil && result == nil {
		return nil, err
	}
	if result.Run.Status == "failed" {
		return nil, fmt.Errorf("revision failed validation; manufacturing package refused: %v", result.Checks)
	}
	var definition DesignDefinition
	if err := json.Unmarshal(revision.Definition, &definition); err != nil {
		return nil, fmt.Errorf("decode manufacturing definition: %w", err)
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	add := func(name string, body []byte) error {
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = entry.Write(body)
		return err
	}
	if err := add("design.json", revision.Definition); err != nil {
		return nil, err
	}
	if err := add("parameters.json", revision.Parameters); err != nil {
		return nil, err
	}
	if definition.OpenHardware != nil {
		metadata, _ := json.MarshalIndent(definition.OpenHardware, "", "  ")
		if err := add("open-hardware.json", metadata); err != nil {
			return nil, err
		}
		if err := add("README.md", []byte(definition.OpenHardware.Readme+"\n")); err != nil {
			return nil, err
		}
		if err := add("ASSEMBLY.md", []byte(definition.OpenHardware.AssemblyGuide+"\n")); err != nil {
			return nil, err
		}
		if err := add("LICENSE.spdx", []byte(definition.OpenHardware.License+"\n")); err != nil {
			return nil, err
		}
	}
	if definition.Assembly != nil {
		assembly, _ := json.MarshalIndent(definition.Assembly, "", "  ")
		if err := add("assembly.json", assembly); err != nil {
			return nil, err
		}
	}
	dependencies, _ := json.MarshalIndent(result.Dependencies, "", "  ")
	if err := add("dependencies.json", dependencies); err != nil {
		return nil, err
	}
	profiles, _ := json.MarshalIndent(definition.PrintProfiles, "", "  ")
	if err := add("print-profiles.json", profiles); err != nil {
		return nil, err
	}
	bom, err := manufacturingBOMCSV(&definition)
	if err != nil {
		return nil, err
	}
	if err := add("bom.csv", bom); err != nil {
		return nil, err
	}
	report, _ := json.MarshalIndent(result.Report, "", "  ")
	checks, _ := json.MarshalIndent(result.Checks, "", "  ")
	if err := add("validation/report.json", report); err != nil {
		return nil, err
	}
	if err := add("validation/checks.json", checks); err != nil {
		return nil, err
	}
	manifest := map[string]any{
		"schema": "apteva-manufacturing-package/v1", "design_id": design.ID,
		"revision_id": revision.ID, "revision_number": revision.RevisionNumber,
		"source_sha256": revision.SourceSHA256, "units": "mm", "engine": engineVersion,
		"validation_status": result.Run.Status, "created_at": time.Now().UTC().Format(time.RFC3339),
		"part_count": len(definition.Parts), "assembly_instance_count": len(assemblyInstances(&definition)),
		"dependency_count": len(result.Dependencies),
	}
	if definition.OpenHardware != nil {
		manifest["open_hardware"] = map[string]any{"license": definition.OpenHardware.License, "project": definition.OpenHardware.ProjectName, "version": definition.OpenHardware.Version}
	}
	manifestBody, _ := json.MarshalIndent(manifest, "", "  ")
	if err := add("manifest.json", manifestBody); err != nil {
		return nil, err
	}
	for _, artifact := range result.Artifacts {
		if artifact.Format == "mesh-json" || artifact.LocalPath == "" {
			continue
		}
		body, err := os.ReadFile(artifact.LocalPath)
		if err != nil {
			archive.Close()
			return nil, err
		}
		if err := add("files/"+artifact.Name, body); err != nil {
			_ = archive.Close()
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	build := result.Run
	artifact, err := s.persistArtifact(design, revision, &build, "zip", "application/zip", output.Bytes())
	if err != nil {
		return nil, err
	}
	artifact.Kind = "manufacturing-package"
	_, _ = s.store.db.Exec(`UPDATE artifacts SET kind = 'manufacturing-package' WHERE id = ?`, artifact.ID)
	if s.ctx != nil {
		s.ctx.Emit("manufacturing.package.created", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "artifact_id": artifact.ID})
	}
	return artifact, nil
}

func assemblyInstances(definition *DesignDefinition) []AssemblyInstance {
	if definition.Assembly == nil {
		return nil
	}
	return definition.Assembly.Instances
}

func manufacturingBOMCSV(definition *DesignDefinition) ([]byte, error) {
	quantities := map[string]int{}
	if definition.Assembly != nil {
		for _, instance := range definition.Assembly.Instances {
			quantity := instance.Quantity
			if quantity <= 0 {
				quantity = 1
			}
			quantities[instance.PartID] += quantity
		}
	}
	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	if err := writer.Write([]string{"id", "name", "quantity", "classification", "part_id", "material_id", "process", "manufacturer", "part_number", "source", "notes"}); err != nil {
		return nil, err
	}
	for _, part := range definition.Parts {
		quantity := quantities[part.ID]
		if quantity == 0 {
			quantity = part.Quantity
		}
		if quantity == 0 {
			quantity = 1
		}
		classification, process := "fabricated", ""
		if part.Manufacturing != nil {
			classification, process = part.Manufacturing.Classification, part.Manufacturing.Process
		}
		source := ""
		if part.Source != nil {
			source = fmt.Sprintf("design:%d@revision:%d", part.Source.DesignID, part.Source.RevisionID)
			if part.Source.PartID != "" {
				source += "#part:" + part.Source.PartID
			}
		}
		if err := writer.Write([]string{part.ID, part.Name, fmt.Sprint(quantity), classification, part.ID, part.MaterialID, process, "", "", source, part.Description}); err != nil {
			return nil, err
		}
	}
	for _, item := range definition.BOM {
		if item.PartID != "" {
			continue
		}
		if err := writer.Write([]string{item.ID, item.Name, fmt.Sprint(item.Quantity), item.Classification, "", "", "", item.Manufacturer, item.PartNumber, item.Source, item.Notes}); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
