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

func (s *Service) Manufacturing(designID, revisionID int64) (*Artifact, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	report := validateDefinition(def)
	if report.Status == "failed" {
		return nil, fmt.Errorf("revision failed validation with %d errors; manufacturing output refused", report.Errors)
	}
	body, err := zipManufacturingSet(def)
	if err != nil {
		return nil, err
	}
	return s.persistArtifact(design, revision, "manufacturing", "zip", "application/zip", body, map[string]any{"engine": engineVersion, "format": "Gerber X2 + Excellon", "files": manufacturingFileSummary(def), "validation_status": report.Status})
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
	files := map[string][]byte{}
	source, _ := json.MarshalIndent(def, "", "  ")
	files["source/pcb.json"] = append(source, '\n')
	validation, _ := json.MarshalIndent(map[string]any{
		"design_id": design.ID, "revision_id": revision.ID, "report": report,
	}, "", "  ")
	files["validation/report.json"] = append(validation, '\n')
	files["outputs/board.svg"] = renderSVG(def)
	files["outputs/bom.csv"] = renderBOM(def)
	for name, body := range manufacturingFiles(def) {
		files["manufacturing/"+name] = body
	}
	hashes := map[string]string{}
	for name, body := range files {
		sum := sha256.Sum256(body)
		hashes[name] = hex.EncodeToString(sum[:])
	}
	manifest := map[string]any{"schema": releaseSchema, "engine": engineVersion, "design_id": design.ID, "design_name": design.Name, "revision_id": revision.ID, "revision_number": revision.Number, "source_sha256": revision.SourceSHA256, "validation_status": report.Status, "note": strings.TrimSpace(note), "created_at": revision.CreatedAt, "files": hashes, "compatibility": map[string]any{"native_format": pcbSchema, "original_sources_preserved": true}}
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
	name := fmt.Sprintf("%s-r%d.%s", safeFilename(design.Name), revision.Number, ext)
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
