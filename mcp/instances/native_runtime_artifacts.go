package main

import (
	"crypto/sha256"
	"database/sql"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (b *nativeBackend) artifactDir(id string) string { return filepath.Join(b.root, "artifacts", id) }
func (b *nativeBackend) artifact(id string, sealed bool) (nativeArtifact, error) {
	var a nativeArtifact
	var raw string
	var s int
	if !nativeID(id) {
		return a, errors.New("invalid artifact_id")
	}
	if e := b.db.QueryRow(`SELECT metadata,sealed FROM native_artifacts WHERE id=?`, id).Scan(&raw, &s); e != nil {
		return a, errors.New("artifact not found")
	}
	if sealed && s != 1 {
		return a, errors.New("artifact must be sealed before deployment")
	}
	return a, json.Unmarshal([]byte(raw), &a)
}
func validateNativeArtifact(a nativeArtifact) (int64, error) {
	if !nativeID(a.ID) || !nativeID(a.AppName) || !nativeID(a.AppVersion) || !nativeFilePath(a.Executable) || (a.Platform != "linux-amd64" && a.Platform != "linux-arm64") || len(a.Files) == 0 || len(a.Files) > 1024 {
		return 0, errors.New("invalid artifact metadata or unsupported platform")
	}
	if a.AppCapabilities.Routing != "" && a.AppCapabilities.Routing != "key" && a.AppCapabilities.Routing != "stateless" {
		return 0, errors.New("invalid app routing capability")
	}
	var size int64
	found := false
	seen := map[string]bool{}
	for _, f := range a.Files {
		if !nativeFilePath(f.Path) || seen[f.Path] || !nativeDigest(f.SHA256) || f.Size < 0 || f.Size > nativeMaxFile {
			return 0, errors.New("invalid artifact file metadata")
		}
		seen[f.Path] = true
		size += f.Size
		if f.Path == a.Executable {
			found = f.Size >= 64
		}
	}
	if !found || size > nativeArtifactBudget {
		return 0, errors.New("artifact missing executable or exceeds size limit")
	}
	return size, nil
}
func (b *nativeBackend) registerArtifact(a nativeArtifact) error {
	size, e := validateNativeArtifact(a)
	if e != nil {
		return e
	}
	tx, e := b.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var old string
	e = tx.QueryRow(`SELECT fingerprint FROM native_artifacts WHERE id=?`, a.ID).Scan(&old)
	fingerprint := nativeHash(nativeJSON(a))
	if e == nil {
		if old != fingerprint {
			return errors.New("artifact ID already has different metadata")
		}
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	var used, count int64
	if e = tx.QueryRow(`SELECT COALESCE(sum(bytes),0),count(*) FROM native_artifacts`).Scan(&used, &count); e != nil {
		return e
	}
	if used+size > nativeArtifactBudget || count >= 128 {
		return errors.New("artifact staging budget exhausted")
	}
	if e = os.MkdirAll(b.artifactDir(a.ID), 0700); e != nil {
		return e
	}
	_, e = tx.Exec(`INSERT INTO native_artifacts(id,metadata,fingerprint,bytes) VALUES(?,?,?,?)`, a.ID, nativeJSON(a), fingerprint, size)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func checkNativeFile(root *os.Root, f nativeArtifactFile) error {
	file, e := root.Open(f.Path)
	if e != nil {
		return errors.New("artifact file missing")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != f.Size {
		return errors.New("artifact must contain regular files of the declared size")
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(file, f.Size+1))
	if e != nil || n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return errors.New("artifact file checksum or size mismatch")
	}
	return nil
}
func (b *nativeBackend) sealArtifact(a nativeArtifact) error {
	root, e := os.OpenRoot(b.artifactDir(a.ID))
	if e != nil {
		return e
	}
	defer root.Close()
	for _, f := range a.Files {
		if e = checkNativeFile(root, f); e != nil {
			return e
		}
	}
	file, e := root.Open(a.Executable)
	if e != nil {
		return e
	}
	defer file.Close()
	bin, e := elf.NewFile(file)
	if e != nil {
		return errors.New("executable is not a Linux ELF artifact")
	}
	defer bin.Close()
	want := elf.EM_X86_64
	if a.Platform == "linux-arm64" {
		want = elf.EM_AARCH64
	}
	if bin.Class != elf.ELFCLASS64 || bin.Data != elf.ELFDATA2LSB || (bin.OSABI != elf.ELFOSABI_NONE && bin.OSABI != elf.ELFOSABI_LINUX) || bin.Machine != want || (bin.Type != elf.ET_EXEC && bin.Type != elf.ET_DYN) {
		return errors.New("executable architecture/type does not match artifact platform")
	}
	_, e = b.db.Exec(`UPDATE native_artifacts SET sealed=1 WHERE id=?`, a.ID)
	return e
}
func (b *nativeBackend) artifactHTTP(w http.ResponseWriter, r *http.Request, tail string) {
	// Serialized against seal/PUT so no caller can alter a sealed artifact.
	b.artifactMu.Lock()
	defer b.artifactMu.Unlock()
	fail := func(e error) { http.Error(w, e.Error(), http.StatusBadRequest) }
	if tail == "" {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", 405)
			return
		}
		var a nativeArtifact
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&a); e != nil {
			fail(errors.New("invalid artifact metadata"))
			return
		}
		if e := b.registerArtifact(a); e != nil {
			fail(e)
			return
		}
		writeNative(w, a)
		return
	}
	parts := strings.SplitN(tail, "/", 2)
	if len(parts) == 1 && r.Method == http.MethodDelete {
		id := parts[0]
		if !nativeID(id) {
			fail(errors.New("invalid artifact_id"))
			return
		}
		var retained int
		if e := b.db.QueryRow(`SELECT count(*) FROM native_deployments WHERE json_extract(record,'$.artifact.id')=? AND json_extract(record,'$.execution.state')!='deleted'`, id).Scan(&retained); e != nil || retained > 0 {
			http.Error(w, "artifact has retained deployments", 409)
			return
		}
		root, e := os.OpenRoot(b.root)
		if e != nil {
			fail(e)
			return
		}
		defer root.Close()
		if e = root.RemoveAll("artifacts/" + id); e != nil {
			fail(e)
			return
		}
		if _, e = b.db.Exec(`DELETE FROM native_artifacts WHERE id=?`, id); e != nil {
			fail(e)
			return
		}
		writeNative(w, map[string]any{"artifact_id": id, "deleted": true})
		return
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	a, e := b.artifact(parts[0], false)
	if e != nil {
		fail(e)
		return
	}
	if parts[1] == "seal" && r.Method == http.MethodPost {
		if e = b.sealArtifact(a); e != nil {
			fail(e)
			return
		}
		writeNative(w, map[string]any{"artifact_id": a.ID, "sealed": true})
		return
	}
	if !strings.HasPrefix(parts[1], "files/") || r.Method != http.MethodPut {
		http.Error(w, "PUT file or POST seal required", 405)
		return
	}
	var sealed int
	b.db.QueryRow(`SELECT sealed FROM native_artifacts WHERE id=?`, a.ID).Scan(&sealed)
	if sealed != 0 {
		http.Error(w, "sealed artifact is immutable", 409)
		return
	}
	name := strings.TrimPrefix(parts[1], "files/")
	var f *nativeArtifactFile
	for i := range a.Files {
		if a.Files[i].Path == name {
			f = &a.Files[i]
			break
		}
	}
	if f == nil {
		fail(errors.New("undeclared artifact file"))
		return
	}
	root, e := os.OpenRoot(b.artifactDir(a.ID))
	if e != nil {
		fail(e)
		return
	}
	defer root.Close()
	if e = root.MkdirAll(filepath.Dir(name), 0700); e != nil {
		fail(e)
		return
	}
	temp := ".upload-" + newRequestID()
	file, e := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		fail(e)
		return
	}
	defer root.Remove(temp)
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(file, h), http.MaxBytesReader(w, r.Body, f.Size+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if e != nil || syncErr != nil || closeErr != nil || n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		fail(errors.New("artifact file checksum or size mismatch"))
		return
	}
	if e = root.Rename(temp, name); e != nil {
		fail(e)
		return
	}
	writeNative(w, map[string]any{"bytes": n, "sha256": f.SHA256})
}
func writeNative(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func nativeArtifactDigest(a nativeArtifact) string {
	for _, f := range a.Files {
		if f.Path == a.Executable {
			return f.SHA256
		}
	}
	return ""
}
func (b *nativeBackend) verifyArtifact(a nativeArtifact) error {
	root, e := os.OpenRoot(b.artifactDir(a.ID))
	if e != nil {
		return e
	}
	defer root.Close()
	for _, f := range a.Files {
		if e = checkNativeFile(root, f); e != nil {
			return fmt.Errorf("artifact invalid: %w", e)
		}
	}
	return nil
}
