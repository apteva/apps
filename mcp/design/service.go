package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	result, buildErr := s.engine.Build(ctx, design.Name, canonical, parameters, runnerFormats)
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
	artifacts, err := s.persistEngineArtifacts(design, revision, run, result, formats)
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
	return &BuildResult{Run: *run, Report: result.Report, Checks: checks, Artifacts: artifacts, Parameters: result.Parameters}, nil
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

func (s *Service) persistEngineArtifacts(design *Design, revision *Revision, run *BuildRun, result *EngineResult, requested []string) ([]Artifact, error) {
	byFormat := map[string]EngineArtifact{}
	for _, artifact := range result.Artifacts {
		byFormat[artifact.Format] = artifact
	}
	meshArtifact, ok := byFormat["mesh-json"]
	if !ok {
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
	generated := map[string]struct {
		Body        []byte
		ContentType string
	}{}
	for format, engineArtifact := range byFormat {
		if !wanted[format] {
			continue
		}
		body, err := os.ReadFile(engineArtifact.Path)
		if err != nil {
			return nil, err
		}
		generated[format] = struct {
			Body        []byte
			ContentType string
		}{body, engineArtifact.ContentType}
	}
	if wanted["3mf"] {
		body, err := meshTo3MF(meshBody)
		if err != nil {
			return nil, err
		}
		generated["3mf"] = struct {
			Body        []byte
			ContentType string
		}{body, "model/3mf"}
	}
	if wanted["glb"] {
		body, err := meshToGLB(meshBody)
		if err != nil {
			return nil, err
		}
		generated["glb"] = struct {
			Body        []byte
			ContentType string
		}{body, "model/gltf-binary"}
	}
	formats := make([]string, 0, len(generated))
	for format := range generated {
		formats = append(formats, format)
	}
	sort.Strings(formats)
	out := make([]Artifact, 0, len(formats))
	for _, format := range formats {
		file := generated[format]
		artifact, err := s.persistArtifact(design, revision, run, format, file.ContentType, file.Body)
		if err != nil {
			return nil, err
		}
		out = append(out, *artifact)
	}
	return out, nil
}

func (s *Service) persistArtifact(design *Design, revision *Revision, run *BuildRun, format, contentType string, body []byte) (*Artifact, error) {
	hash := sha256.Sum256(body)
	hashText := hex.EncodeToString(hash[:])
	ext := format
	if format == "mesh-json" {
		ext = "mesh.json"
	}
	name := fmt.Sprintf("%s-r%d.%s", safeFilename(design.Name), revision.RevisionNumber, ext)
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
	metadata, _ := json.Marshal(map[string]any{"engine": engineName, "engine_version": engineVersion})
	buildID := run.ID
	artifact, err := s.store.SaveArtifact(Artifact{
		DesignID: design.ID, RevisionID: revision.ID, BuildRunID: &buildID,
		Kind: ternary(format == "mesh-json" || format == "glb", "preview", "export"), Format: format,
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
	var output struct {
		ID int64 `json:"id"`
	}
	err := s.ctx.PlatformAPI().CallAppResult("storage", "files_upload", map[string]any{
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
		return nil, errors.New("revision failed validation; manufacturing package refused")
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
