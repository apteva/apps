package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	maxSTLBytes     = 32 << 20
	maxSTLTriangles = 50000
)

// stlTriangleCount validates an STL (binary or ASCII) and counts facets.
func stlTriangleCount(body []byte) (int, error) {
	if len(body) >= 84 {
		count := int(binary.LittleEndian.Uint32(body[80:84]))
		if 84+count*50 == len(body) {
			return count, nil
		}
	}
	text := bytes.TrimSpace(body)
	if !bytes.HasPrefix(bytes.ToLower(text[:min(len(text), 5)]), []byte("solid")) {
		return 0, errors.New("not a binary or ASCII STL file")
	}
	count := bytes.Count(bytes.ToLower(body), []byte("facet normal"))
	if count == 0 {
		return 0, errors.New("ASCII STL has no facets")
	}
	return count, nil
}

// importSTLAsset stores a mesh as reference geometry. Meshes are kept as
// triangles turned into B-rep faces by the runner, so they can be placed,
// measured, and checked for clearance, but they are never printed or edited.
func (s *Service) importSTLAsset(filename string, body []byte) (*SourceAsset, error) {
	if len(body) == 0 || len(body) > maxSTLBytes {
		return nil, fmt.Errorf("STL must be 1 byte to %d MB", maxSTLBytes>>20)
	}
	triangles, err := stlTriangleCount(body)
	if err != nil {
		return nil, err
	}
	if triangles > maxSTLTriangles {
		return nil, fmt.Errorf("STL has %d triangles; reference meshes are limited to %d (decimate it first)", triangles, maxSTLTriangles)
	}
	hash := digest(body)
	id := digest([]byte("stl\x00" + hash))
	asset := &SourceAsset{
		ID: id, Filename: filepath.Base(filename), SHA256: hash, SizeBytes: len(body),
		Units: "mm", SourceUnits: []string{"unspecified"}, Importer: "stl", EngineVersion: engineVersion,
		Components:  []SourceComponent{{ID: "mesh", Name: strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)), File: "original.stl", SHA256: hash}},
		Hierarchy:   json.RawMessage(`[]`),
		Diagnostics: []string{fmt.Sprintf("%d triangles; reference mesh, not editable or printable", triangles)},
	}
	dir := s.assetDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "original.stl"), body, 0o600); err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(asset)
	if err := os.WriteFile(filepath.Join(dir, "asset.json"), encoded, 0o600); err != nil {
		return nil, err
	}
	if _, err := s.store.db.Exec(`INSERT OR IGNORE INTO source_assets(project_id,id,metadata_json) VALUES(?,?,?)`, s.project, id, string(encoded)); err != nil {
		return nil, err
	}
	return asset, nil
}

// stlAssetFiles resolves import_stl operations to verified local files.
func (s *Service) stlAssetFiles(operations []map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, op := range operations {
		if op["type"] != "import_stl" {
			continue
		}
		id, _ := op["asset_id"].(string)
		asset, err := s.sourceAsset(id)
		if err != nil {
			return nil, err
		}
		if asset.Importer != "stl" {
			return nil, fmt.Errorf("asset %s is not an STL", id)
		}
		path := filepath.Join(s.assetDir(id), "original.stl")
		raw, err := os.ReadFile(path)
		if err != nil || digest(raw) != asset.SHA256 {
			return nil, errors.New("source STL missing or hash mismatch")
		}
		out[id] = map[string]any{"file": path, "sha256": asset.SHA256}
	}
	return out, nil
}

func (a *App) toolSTLImport(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	body, err := base64.StdEncoding.DecodeString(stringArg(args, "content_base64"))
	if err != nil {
		return nil, errors.New("invalid base64 STL")
	}
	asset, err := s.importSTLAsset(stringArg(args, "filename"), body)
	if err != nil {
		return nil, err
	}
	units := stringArg(args, "units")
	if units == "" {
		units = "mm"
	}
	name := stringArg(args, "name")
	if name == "" {
		name = asset.Components[0].Name
	}
	definition := DesignDefinition{
		Schema: designSchema, Units: "mm",
		Operations: []map[string]any{{"id": "mesh", "type": "import_stl", "asset_id": asset.ID, "units": units}},
		Output:     "mesh",
		Parts:      []DesignPart{{ID: "reference", Name: name, Output: "mesh", Manufacturing: &PartManufacturing{Classification: "reference"}}},
	}
	raw, _ := json.Marshal(definition)
	canonical, _, err := normalizeDefinition(raw, s.maxOperations)
	if err != nil {
		return nil, err
	}
	if _, _, err := s.materializeComponentSources(0, canonical); err != nil {
		return nil, err
	}
	design, err := s.store.CreateDesign(s.project, CreateDesignInput{
		Name: name, Description: "Reference mesh imported from " + asset.Filename, Kind: "mesh",
		Tags: []string{"reference", "stl"}, Definition: canonical, Parameters: []byte(`{}`),
		Note: "Imported STL reference", Author: callerName(ctx),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"design": design, "asset": asset}, nil
}
