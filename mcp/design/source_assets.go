package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const maxSTEPBytes = 96 << 20

type SourceComponent struct {
	MeshSHA256 string      `json:"mesh_sha256,omitempty"`
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	ParentID   string      `json:"parent_id,omitempty"`
	File       string      `json:"file"`
	SHA256     string      `json:"sha256"`
	Transform  [][]float64 `json:"transform"`
}
type SourceAsset struct {
	EngineVersion string            `json:"engine_version"`
	Geometry      *GeometryReport   `json:"geometry,omitempty"`
	ID            string            `json:"id"`
	Filename      string            `json:"filename"`
	SHA256        string            `json:"sha256"`
	SizeBytes     int               `json:"size_bytes"`
	Evidence      ComponentEvidence `json:"evidence"`
	SourceUnits   []string          `json:"source_units"`
	Units         string            `json:"units"`
	Importer      string            `json:"importer"`
	Components    []SourceComponent `json:"components"`
	Hierarchy     json.RawMessage   `json:"hierarchy"`
	Diagnostics   []string          `json:"diagnostics"`
}

func digest(body []byte) string { h := sha256.Sum256(body); return hex.EncodeToString(h[:]) }
func (s *Service) assetDir(id string) string {
	return filepath.Join(s.artifactRoot, "sources", digest([]byte(s.project)), id)
}
func (s *Service) sourceAsset(id string) (*SourceAsset, error) {
	if !sha256RE.MatchString(id) {
		return nil, errors.New("invalid asset id")
	}
	var body []byte
	if err := s.store.db.QueryRow(`SELECT metadata_json FROM source_assets WHERE project_id=? AND id=?`, s.project, id).Scan(&body); err != nil {
		return nil, fmt.Errorf("source asset not found in this project: %w", err)
	}
	var a SourceAsset
	err := json.Unmarshal(body, &a)
	return &a, err
}
func (s *Service) importSTEP(ctx context.Context, filename string, body []byte, e ComponentEvidence) (*SourceAsset, error) {
	if len(body) == 0 || len(body) > maxSTEPBytes {
		return nil, errors.New("STEP file must be between 1 byte and 96 MiB")
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".step" && ext != ".stp" {
		return nil, errors.New("upload a .step or .stp file")
	}
	if e.Model == "" {
		return nil, errors.New("exact component model is required")
	}
	if e.GeometryStatus == "" {
		e.GeometryStatus = "manufacturer_cad"
	}
	probe := &DesignDefinition{Parts: []DesignPart{{ID: "import", Evidence: &e}}}
	if err := validateInspectionDefinition(probe, nil); err != nil {
		return nil, err
	}
	hash := digest(body)
	meta, _ := json.Marshal(map[string]any{"sha256": hash, "filename": filepath.Base(filename), "evidence": e, "importer": "occt-xde-7.4.0p1-v1"})
	id := digest(meta)
	if existing, err := s.sourceAsset(id); err == nil {
		return existing, nil
	}
	reserved := false
	select {
	case s.engine.buildSlots <- struct{}{}:
		reserved = true
		defer func() {
			if reserved {
				<-s.engine.buildSlots
			}
		}()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, s.engine.timeout)
	defer cancel()
	bun, err := s.engine.bun(ctx)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(s.artifactRoot, "import-tmp")
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(root, "step-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "original.step")
	if err = os.WriteFile(source, body, 0600); err != nil {
		return nil, err
	}
	req, _ := json.Marshal(map[string]any{"input": source, "output": dir})
	cmd := exec.CommandContext(ctx, bun, filepath.Join(s.engine.root, "runner", engineVersion, "step-import.mjs"))
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(req)
	cmd.Env = []string{"HOME=" + dir, "TMPDIR=" + dir, "PATH=" + filepath.Dir(bun), "NO_COLOR=1"}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("STEP import exceeded its time limit: %w", ctx.Err())
	}
	parsed, err := parseRunnerOutput(out.Bytes())
	if err != nil {
		return nil, fmt.Errorf("STEP import failed (%v); simplify the model or check its format: %s", runErr, stderr.String())
	}
	if !parsed.OK {
		return nil, errors.New(parsed.Error)
	}
	var a SourceAsset
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		if bytes.HasPrefix(lines[i], []byte(`{"ok":`)) {
			err = json.Unmarshal(lines[i], &a)
			break
		}
	}
	if err != nil || len(a.Components) == 0 {
		return nil, errors.New("STEP import produced no components")
	}
	a.ID = id
	a.Filename = filepath.Base(filename)
	a.SHA256 = hash
	a.SizeBytes = len(body)
	a.Evidence = e
	for i := range a.Components {
		c := &a.Components[i]
		if filepath.Base(c.File) != c.File || !parameterNameRE.MatchString(c.ID) {
			return nil, errors.New("invalid imported component path")
		}
		b, err := os.ReadFile(filepath.Join(dir, c.File))
		if err != nil {
			return nil, err
		}
		c.SHA256 = digest(b)
	}
	// Validate and measure imported B-reps before accepting the source.
	<-s.engine.buildSlots
	reserved = false
	files := map[string]string{}
	for _, c := range a.Components {
		files[c.ID] = filepath.Join(dir, c.File)
	}
	runtime := map[string]any{"schema": designSchema, "units": "mm", "operations": []any{map[string]any{"id": "imported", "type": "import_step", "asset_id": id}}, "output": "imported", "_assets": map[string]any{id: map[string]any{"metadata": a, "files": files}}}
	runtimeJSON, _ := json.Marshal(runtime)
	geometry, geometryErr := s.engine.Build(ctx, e.Model, runtimeJSON, []byte(`{}`), []string{"mesh-json"})
	if geometryErr != nil {
		return nil, fmt.Errorf("imported geometry cannot be evaluated: %w", geometryErr)
	}
	a.Geometry = &geometry.Report
	a.EngineVersion = engineVersion
	for _, artifact := range geometry.Artifacts {
		if artifact.Format != "mesh-json" || artifact.PartID != "" {
			continue
		}
		body, err := os.ReadFile(artifact.Path)
		if err != nil {
			return nil, err
		}
		var document struct {
			Parts []json.RawMessage `json:"parts"`
		}
		if err = json.Unmarshal(body, &document); err != nil {
			return nil, err
		}
		for _, raw := range document.Parts {
			var item struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(raw, &item)
			for index := range a.Components {
				if a.Components[index].ID != item.ID {
					continue
				}
				name := a.Components[index].ID + ".mesh.json"
				if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
					return nil, err
				}
				a.Components[index].MeshSHA256 = digest(raw)
			}
		}
		break
	}

	for _, t := range geometry.Report.Topology {
		if t.Status != "pass" {
			a.Diagnostics = append(a.Diagnostics, "Imported geometry includes invalid/open topology; reliable solid clearance is indeterminate")
		}
	}
	encoded, _ := json.Marshal(a)
	if err = os.WriteFile(filepath.Join(dir, "asset.json"), encoded, 0600); err != nil {
		return nil, err
	}
	target := s.assetDir(id)
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return nil, err
	}
	// A concurrent identical import can safely reuse the content-addressed directory.
	if err = os.Rename(dir, target); err != nil {
		if _, statErr := os.Stat(filepath.Join(target, "asset.json")); statErr != nil {
			return nil, err
		}
	}
	_, err = s.store.db.Exec(`INSERT OR IGNORE INTO source_assets(project_id,id,metadata_json) VALUES(?,?,?)`, s.project, id, string(encoded))
	if err != nil {
		return nil, err
	}
	return &a, nil
}
func (s *Service) materializeAssets(canonical []byte) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &obj); err != nil {
		return nil, err
	}
	var d DesignDefinition
	if err := json.Unmarshal(canonical, &d); err != nil {
		return nil, err
	}
	assets := map[string]any{}
	for _, op := range d.Operations {
		if op["type"] != "import_step" {
			continue
		}
		id, _ := op["asset_id"].(string)
		a, err := s.sourceAsset(id)
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(filepath.Join(s.assetDir(id), "original.step"))
		if err != nil || digest(raw) != a.SHA256 {
			return nil, errors.New("source STEP missing or hash mismatch")
		}
		files := map[string]string{}
		meshes := map[string]string{}
		for _, c := range a.Components {
			p := filepath.Join(s.assetDir(id), c.File)
			b, err := os.ReadFile(p)
			if err != nil || digest(b) != c.SHA256 {
				return nil, errors.New("normalized component missing or hash mismatch")
			}
			files[c.ID] = p
			if c.MeshSHA256 != "" && a.EngineVersion == engineVersion {
				meshPath := filepath.Join(s.assetDir(id), c.ID+".mesh.json")
				b, err := os.ReadFile(meshPath)
				if err != nil || digest(b) != c.MeshSHA256 {
					return nil, errors.New("cached component mesh missing or hash mismatch")
				}
				meshes[c.ID] = meshPath
			}
		}
		assets[id] = map[string]any{"metadata": a, "files": files, "meshes": meshes}
	}
	if len(assets) > 0 {
		obj["_assets"], _ = json.Marshal(assets)
	}
	return json.Marshal(obj)
}
func (a *App) toolSTEPImport(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	body, err := base64.StdEncoding.DecodeString(stringArg(args, "content_base64"))
	if err != nil {
		return nil, errors.New("invalid base64 STEP")
	}
	var evidence ComponentEvidence
	b, _ := json.Marshal(args["evidence"])
	if err = json.Unmarshal(b, &evidence); err != nil {
		return nil, err
	}
	asset, err := s.importSTEP(ctx, stringArg(args, "filename"), body, evidence)
	if err != nil {
		return nil, err
	}
	return s.designFromAsset(asset, stringArg(args, "name"))
}
func (s *Service) designFromAsset(a *SourceAsset, name string) (any, error) {
	if name == "" {
		name = a.Evidence.Model
	}
	d := DesignDefinition{Schema: designSchema, Units: "mm", Operations: []map[string]any{{"id": "imported", "type": "import_step", "asset_id": a.ID}}, Output: "imported", Parts: []DesignPart{{ID: "component", Name: name, Output: "imported", Evidence: &a.Evidence, Manufacturing: &PartManufacturing{Classification: "purchased"}}}}
	b, _ := json.Marshal(d)
	canonical, _, err := normalizeDefinition(b, 256)
	if err != nil {
		return nil, err
	}
	design, err := s.store.CreateDesign(s.project, CreateDesignInput{Name: name, Kind: "parametric", Definition: canonical, Parameters: []byte(`{}`), Note: "Imported immutable manufacturer STEP"})
	if err != nil {
		return nil, err
	}
	return map[string]any{"asset": a, "design": design}, nil
}
func (a *App) handleSources(w http.ResponseWriter, r *http.Request) {
	app := a.ctx
	if p := r.Header.Get("X-Apteva-Project-ID"); p != "" && app != nil {
		app = app.WithProject(p)
	}
	s, err := a.service(app)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		id := r.URL.Query().Get("id")
		if id != "" {
			asset, e := s.sourceAsset(id)
			if e != nil {
				writeHTTPError(w, e)
				return
			}
			writeJSON(w, 200, map[string]any{"asset": asset})
			return
		}
		rows, e := s.store.db.Query(`SELECT metadata_json FROM source_assets WHERE project_id=? ORDER BY created_at DESC LIMIT 200`, s.project)
		if e != nil {
			writeHTTPError(w, e)
			return
		}
		defer rows.Close()
		items := []json.RawMessage{}
		for rows.Next() {
			var b []byte
			if e = rows.Scan(&b); e != nil {
				writeHTTPError(w, e)
				return
			}
			items = append(items, json.RawMessage(b))
		}
		writeJSON(w, 200, map[string]any{"assets": items})
		return
	}
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, "GET", "POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSTEPBytes+(1<<20))
	if err = r.ParseMultipartForm(4 << 20); err != nil {
		writeJSONError(w, 400, "Expected multipart file and evidence JSON (maximum 96 MiB)")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, 400, "STEP file required")
		return
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	var evidence ComponentEvidence
	if err = json.Unmarshal([]byte(r.FormValue("evidence")), &evidence); err != nil {
		writeJSONError(w, 400, "evidence JSON required")
		return
	}
	asset, err := s.importSTEP(r.Context(), header.Filename, body, evidence)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	result, err := s.designFromAsset(asset, r.FormValue("name"))
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 201, result)
}
