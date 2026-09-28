package main

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
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

const zipImportTTL = 30 * time.Minute

type stagedZipImport struct {
	ID         string            `json:"id"`
	ProjectID  string            `json:"project_id"`
	Mode       string            `json:"mode"`
	Slug       string            `json:"slug"`
	RepoID     int64             `json:"repo_id,omitempty"`
	Name       string            `json:"name,omitempty"`
	Framework  string            `json:"framework,omitempty"`
	ArchiveSHA string            `json:"archive_sha256"`
	ExpiresAt  time.Time         `json:"expires_at"`
	Expected   map[string]string `json:"expected,omitempty"`
}

func (a *App) zipImportTools() []sdk.Tool {
	return []sdk.Tool{{
		Name:        "repos_import_zip",
		Description: "Preview and apply a ZIP to a Code repository without a Git remote. Preview: archive (base64 ZIP or a blobref:// handle rehydrated by Core), target_mode=create|overlay, dry_run=true, name for create, slug for overlay; optional slug/framework for create. Returns import_id, SHA-256, expiry and changed paths. Apply: import_id, confirm=true (and optionally slug); the exact staged ZIP is applied only if the destination still matches preview. Overlay never deletes absent files. Imported files are working-tree edits; checkpoint separately when ready. Example preview: {\"archive\":\"<base64 ZIP>\",\"target_mode\":\"create\",\"name\":\"Demo\",\"slug\":\"demo\",\"dry_run\":true}. Example apply: {\"import_id\":\"zipimp_<id>\",\"confirm\":true}.",
		InputSchema: schemaObject(map[string]any{
			"archive":     map[string]any{"type": "string", "description": "Base64 ZIP or blobref:// handle. Core rehydrates a handle into a binary envelope before delivery."},
			"target_mode": map[string]any{"type": "string", "enum": []string{"create", "overlay"}},
			"dry_run":     map[string]any{"type": "boolean"},
			"name":        map[string]any{"type": "string"},
			"slug":        map[string]any{"type": "string"},
			"framework":   map[string]any{"type": "string"},
			"import_id":   map[string]any{"type": "string"},
			"confirm":     map[string]any{"type": "boolean"},
		}, nil),
		Handler: a.toolImportZip,
	}}
}

func decodeZipArchive(arg any) ([]byte, error) {
	var encoded string
	switch v := arg.(type) {
	case string:
		if strings.HasPrefix(v, "blobref://") || strings.Contains(v, `"_file_ref"`) {
			return nil, errors.New("unresolved blobref:// handle; pass it through Core so the binary payload is rehydrated")
		}
		if strings.HasPrefix(strings.TrimSpace(v), "{") {
			var env json.RawMessage = []byte(v)
			return decodeBinaryEnvelope(env)
		}
		encoded = v
	case map[string]any:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return decodeBinaryEnvelope(raw)
	default:
		return nil, errors.New("archive must be a base64 ZIP or rehydrated binary handle")
	}
	limit := currentImportLimits().CompressedBytes
	if int64(base64.StdEncoding.DecodedLen(len(encoded))) > limit+2 {
		return nil, fmt.Errorf("archive exceeds compressed limit of %d bytes", limit)
	}
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode archive base64: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("archive exceeds compressed limit of %d bytes", limit)
	}
	return body, nil
}

func parseZipImport(body []byte) ([]fileMutation, error) {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("not a valid ZIP: %w", err)
	}
	changes, err := zipFileMutations(zr)
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for _, change := range changes {
		paths[change.Path] = true
	}
	for _, change := range changes {
		for parent := filepath.ToSlash(filepath.Dir(change.Path)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if paths[parent] {
				return nil, fmt.Errorf("archive file %q is also a parent of %q", parent, change.Path)
			}
		}
	}
	return changes, nil
}

func (a *App) zipImportDir() string { return filepath.Join(a.dataDir, "zip-imports") }

func zipImportID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "zipimp_" + hex.EncodeToString(random[:]), nil
}

func validZipImportID(id string) bool {
	if len(id) != len("zipimp_")+32 || !strings.HasPrefix(id, "zipimp_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "zipimp_"))
	return err == nil
}

func (a *App) zipStagePaths(id string) (string, string, error) {
	if !validZipImportID(id) {
		return "", "", errors.New("invalid import_id")
	}
	dir := a.zipImportDir()
	return filepath.Join(dir, id+".json"), filepath.Join(dir, id+".zip"), nil
}

func (a *App) pruneZipImports() {
	entries, err := os.ReadDir(a.zipImportDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		metaPath, zipPath, err := a.zipStagePaths(id)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err == nil && time.Since(info.ModTime()) > zipImportTTL {
			_ = os.Remove(metaPath)
			_ = os.Remove(zipPath)
		}
	}
}

// destinationState captures both content and file type. Directory ancestors
// are included so a changed parent cannot redirect or invalidate the import.
func destinationState(store FileStore, slug, path string) (string, error) {
	if local, ok := store.(FileStoreLocalPath); ok {
		full := filepath.Join(local.RepoPath(slug), filepath.FromSlash(path))
		info, err := os.Lstat(full)
		if errors.Is(err, os.ErrNotExist) {
			return "absent", nil
		}
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return fmt.Sprintf("dir:%o", info.Mode().Perm()), nil
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("destination %q is not a regular file or directory", path)
		}
	}
	meta, err := store.Stat(slug, path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	if meta.IsDir {
		return fmt.Sprintf("dir:%o", meta.Mode), nil
	}
	if meta.SHA256 == "" {
		return "", fmt.Errorf("cannot hash existing destination %q", path)
	}
	return fmt.Sprintf("file:%s:%o", meta.SHA256, meta.Mode), nil
}

func previewZipDestination(store FileStore, slug string, changes []fileMutation) (map[string]string, int, int, int, []string, error) {
	expected := map[string]string{}
	var added, modified, unchanged int
	var overwritten []string
	for _, change := range changes {
		for parent := filepath.ToSlash(filepath.Dir(change.Path)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if _, seen := expected[parent]; seen {
				continue
			}
			state, err := destinationState(store, slug, parent)
			if err != nil {
				return nil, 0, 0, 0, nil, err
			}
			if state != "absent" && !strings.HasPrefix(state, "dir:") {
				return nil, 0, 0, 0, nil, fmt.Errorf("non-directory destination parent %q", parent)
			}
			expected[parent] = state
		}
		state, err := destinationState(store, slug, change.Path)
		if err != nil {
			return nil, 0, 0, 0, nil, err
		}
		if strings.HasPrefix(state, "dir:") {
			return nil, 0, 0, 0, nil, fmt.Errorf("destination %q is a directory", change.Path)
		}
		expected[change.Path] = state
		switch {
		case state == "absent":
			added++
		case state == fmt.Sprintf("file:%s:%o", hashBytes(change.Body), change.Mode):
			unchanged++
		default:
			modified++
			overwritten = append(overwritten, change.Path)
		}
	}
	sort.Strings(overwritten)
	return expected, added, modified, unchanged, overwritten, nil
}

func verifyZipDestination(store FileStore, slug string, expected map[string]string) error {
	for path, want := range expected {
		got, err := destinationState(store, slug, path)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("destination changed since preview at %q; preview again", path)
		}
	}
	return nil
}

func (a *App) toolImportZip(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	a.zipImports.Lock()
	defer a.zipImports.Unlock()
	a.pruneZipImports()
	if id := strArg(args, "import_id"); id != "" {
		return a.applyStagedZip(ctx, pid, id, args)
	}
	if !boolArg(args, "dry_run") {
		return nil, errors.New("preview requires dry_run=true; apply requires import_id and confirm=true")
	}
	return a.previewStagedZip(ctx, pid, args)
}

func (a *App) previewStagedZip(ctx *sdk.AppCtx, pid string, args map[string]any) (any, error) {
	mode := strArg(args, "target_mode")
	if mode != "create" && mode != "overlay" {
		return nil, errors.New("target_mode must be create or overlay")
	}
	body, err := decodeZipArchive(args["archive"])
	if err != nil {
		return nil, err
	}
	changes, err := parseZipImport(body)
	if err != nil {
		return nil, err
	}
	slug := strArg(args, "slug")
	stage := stagedZipImport{ProjectID: pid, Mode: mode, ArchiveSHA: hashBytes(body), ExpiresAt: time.Now().Add(zipImportTTL)}
	var repo *Repo
	if mode == "create" {
		stage.Name = strings.TrimSpace(strArg(args, "name"))
		if stage.Name == "" {
			return nil, errors.New("name required for create")
		}
		if slug == "" {
			slug = stage.Name
		}
		slug = slugify(slug)
		stage.Framework = strArg(args, "framework")
		if stage.Framework == "" {
			stage.Framework = "blank"
		}
		if !validFramework(stage.Framework) {
			return nil, fmt.Errorf("framework %q not supported", stage.Framework)
		}
		existing, err := dbGetRepoBySlug(ctx.AppDB(), pid, slug)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return nil, fmt.Errorf("slug %q already taken in this project", slug)
		}
	} else {
		if slug == "" {
			return nil, errors.New("slug required for overlay")
		}
		repo, err = dbGetRepoBySlug(ctx.AppDB(), pid, slug)
		if err != nil {
			return nil, err
		}
		if repo == nil || repo.ArchivedAt != "" {
			return nil, fmt.Errorf("repository %q not found", slug)
		}
		stage.RepoID = repo.ID
	}
	stage.Slug = slug
	var added, modified, unchanged int
	var overwritten []string
	if repo == nil {
		added = len(changes)
	} else {
		_, err = withRepoWrite(a.storeFor(repo), slug, func(raw FileStore) (int, error) {
			stage.Expected, added, modified, unchanged, overwritten, err = previewZipDestination(raw, slug, changes)
			return 0, err
		})
		if err != nil {
			return nil, err
		}
	}
	stage.ID, err = zipImportID()
	if err != nil {
		return nil, err
	}
	metaPath, zipPath, _ := a.zipStagePaths(stage.ID)
	if err := atomicWrite(zipPath, body, 0o600); err != nil {
		return nil, err
	}
	meta, err := json.Marshal(stage)
	if err != nil {
		_ = os.Remove(zipPath)
		return nil, err
	}
	if err := atomicWrite(metaPath, meta, 0o600); err != nil {
		_ = os.Remove(zipPath)
		return nil, err
	}
	if len(overwritten) > 100 {
		overwritten = overwritten[:100]
	}
	return map[string]any{"import_id": stage.ID, "expires_at": stage.ExpiresAt, "archive_sha256": stage.ArchiveSHA, "archive_bytes": len(body), "target_mode": mode, "slug": slug, "files_total": len(changes), "added": added, "modified": modified, "unchanged": unchanged, "overwritten_paths": overwritten, "overwritten_paths_truncated": modified > len(overwritten)}, nil
}

func (a *App) applyStagedZip(ctx *sdk.AppCtx, pid, id string, args map[string]any) (any, error) {
	if !boolArg(args, "confirm") {
		return nil, errors.New("confirm=true required to apply a previewed ZIP")
	}
	metaPath, zipPath, err := a.zipStagePaths(id)
	if err != nil {
		return nil, err
	}
	metaBody, err := os.ReadFile(metaPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("import_id not found or expired; preview again")
	}
	if err != nil {
		return nil, err
	}
	var stage stagedZipImport
	if err := json.Unmarshal(metaBody, &stage); err != nil {
		return nil, err
	}
	if stage.ID != id || stage.ProjectID != pid {
		return nil, errors.New("import_id does not belong to this project")
	}
	if time.Now().After(stage.ExpiresAt) {
		return nil, errors.New("import_id expired; preview again")
	}
	if slug := strArg(args, "slug"); slug != "" && slug != stage.Slug {
		return nil, errors.New("slug does not match previewed import")
	}
	info, err := os.Stat(zipPath)
	if err != nil {
		return nil, err
	}
	if info.Size() > currentImportLimits().CompressedBytes {
		return nil, errors.New("staged archive exceeds compressed limit")
	}
	body, err := os.ReadFile(zipPath)
	if err != nil {
		return nil, err
	}
	if hashBytes(body) != stage.ArchiveSHA {
		return nil, errors.New("staged archive checksum changed; preview again")
	}
	changes, err := parseZipImport(body)
	if err != nil {
		return nil, err
	}
	var repo *Repo
	if stage.Mode == "overlay" {
		repo, err = dbGetRepoBySlug(ctx.AppDB(), pid, stage.Slug)
		if err != nil {
			return nil, err
		}
		if repo == nil || repo.ID != stage.RepoID || repo.ArchivedAt != "" {
			return nil, errors.New("repository changed since preview; preview again")
		}
		_, err = withRepoWrite(a.storeFor(repo), stage.Slug, func(raw FileStore) (int, error) {
			if err := verifyZipDestination(raw, stage.Slug, stage.Expected); err != nil {
				return 0, err
			}
			return len(changes), applyFileMutations(raw, stage.Slug, changes)
		})
		if err != nil {
			return nil, err
		}
	} else if stage.Mode == "create" {
		if stage.Name == "" || !validFramework(stage.Framework) {
			return nil, errors.New("invalid staged repository metadata")
		}
		repo, err = dbCreateRepo(ctx.AppDB(), pid, CreateRepoInput{Name: stage.Name, Slug: stage.Slug, Framework: stage.Framework})
		if err != nil {
			return nil, err
		}
		rollback := func(cause error) (any, error) {
			storeErr := a.storeFor(repo).DropRepo(repo.Slug)
			if a.native != nil {
				_ = os.RemoveAll(a.native.repoDir(repo))
			}
			dbErr := dbHardDeleteRepo(ctx.AppDB(), pid, repo.Slug)
			if storeErr != nil || dbErr != nil {
				return nil, fmt.Errorf("%w; rollback failed: storage=%v database=%v", cause, storeErr, dbErr)
			}
			return nil, cause
		}
		if err := a.storeFor(repo).CreateRepo(repo.Slug); err != nil {
			return rollback(err)
		}
		if err := a.ensureNativeRevision(repo); err != nil {
			return rollback(err)
		}
		_, err = withRepoWrite(a.storeFor(repo), repo.Slug, func(raw FileStore) (int, error) {
			for _, change := range changes {
				state, err := destinationState(raw, repo.Slug, change.Path)
				if err != nil {
					return 0, err
				}
				if state != "absent" {
					return 0, fmt.Errorf("new repository changed during import at %q", change.Path)
				}
			}
			return len(changes), applyFileMutations(raw, repo.Slug, changes)
		})
		if err != nil {
			return rollback(err)
		}
	} else {
		return nil, errors.New("invalid staged target mode")
	}
	_ = dbRecordImport(ctx.AppDB(), repo.ID, "zip")
	if stage.Mode == "create" {
		ctx.Emit("repo.added", map[string]any{"id": repo.ID, "slug": repo.Slug, "name": repo.Name, "framework": repo.Framework})
	}
	ctx.Emit("repo.imported", map[string]any{"slug": repo.Slug})
	_ = os.Remove(metaPath)
	_ = os.Remove(zipPath)
	return map[string]any{"repository": repo, "files_imported": len(changes), "archive_sha256": stage.ArchiveSHA, "target_mode": stage.Mode}, nil
}
