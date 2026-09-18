package main

// Native version control is deliberately independent from Git.  The working
// tree remains the editing surface; this package stores immutable Code
// revisions as content-addressed blobs and canonical tree manifests.  Git is
// an optional interoperability layer implemented by git_service.go.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
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
)

type nativeVCS struct {
	root  string
	store FileStore
	locks *repoLockSet
}

type nativeRevision struct {
	ID        string   `json:"id"`
	Tree      string   `json:"tree"`
	Parents   []string `json:"parents,omitempty"`
	Message   string   `json:"message"`
	Actor     string   `json:"actor,omitempty"`
	CreatedAt string   `json:"created_at"`
}

type nativeEntry struct {
	Blob string `json:"blob"`
	Mode uint32 `json:"mode"`
}

type nativeStatus struct {
	Native   bool   `json:"native"`
	Branch   string `json:"branch"`
	Head     string `json:"head,omitempty"`
	Dirty    bool   `json:"dirty"`
	Revision int    `json:"revision_count"`
	Remote   bool   `json:"remote_configured"`
}

func newNativeVCS(root string, store FileStore, locks *repoLockSet) *nativeVCS {
	return &nativeVCS{root: filepath.Join(root, "native-vcs"), store: store, locks: locks}
}

func (n *nativeVCS) repoDir(repo *Repo) string {
	return filepath.Join(n.root, strconv.FormatInt(repo.ID, 10))
}

func nativeStoreKey(repo *Repo) string          { return repoStoreKey(repo) }
func (n *nativeVCS) blobsDir(repo *Repo) string { return filepath.Join(n.repoDir(repo), "blobs") }
func (n *nativeVCS) revisionsDir(repo *Repo) string {
	return filepath.Join(n.repoDir(repo), "revisions")
}
func (n *nativeVCS) refsDir(repo *Repo) string  { return filepath.Join(n.repoDir(repo), "refs") }
func (n *nativeVCS) headPath(repo *Repo) string { return filepath.Join(n.repoDir(repo), "HEAD") }

func atomicWrite(path string, body []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(body)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func hashBytes(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

func (n *nativeVCS) ensureRepo(repo *Repo) error {
	if repo == nil {
		return errors.New("repository required")
	}
	if err := os.MkdirAll(n.blobsDir(repo), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(n.revisionsDir(repo), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(n.refsDir(repo), "heads"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(n.refsDir(repo), "tags"), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(n.headPath(repo)); errors.Is(err, os.ErrNotExist) {
		if err := atomicWrite(n.headPath(repo), []byte("main\n"), 0o600); err != nil {
			return err
		}
	}
	if _, err := os.Stat(n.refPath(repo, "main", false)); errors.Is(err, os.ErrNotExist) {
		_, err := n.checkpointLocked(context.Background(), repo, "Initial repository revision", "system", true)
		return err
	}
	return nil
}

func (n *nativeVCS) backfill(db *sql.DB) error {
	rows, err := db.Query(`SELECT ` + repoColumns + ` FROM repositories`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		repo, scanErr := scanRepoRow(rows)
		if scanErr != nil {
			return scanErr
		}
		if err := n.ensureRepo(repo); err != nil {
			return fmt.Errorf("initialize native revisions for %s: %w", repo.Slug, err)
		}
	}
	return rows.Err()
}

func (n *nativeVCS) refPath(repo *Repo, name string, tag bool) string {
	dir := "heads"
	if tag {
		dir = "tags"
	}
	return filepath.Join(n.refsDir(repo), dir, name)
}

func (n *nativeVCS) currentBranch(repo *Repo) (string, error) {
	body, err := os.ReadFile(n.headPath(repo))
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(body))
	if name == "" {
		return "main", nil
	}
	return name, nil
}

func (n *nativeVCS) readRef(repo *Repo, name string, tag bool) string {
	body, err := os.ReadFile(n.refPath(repo, name, tag))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func (n *nativeVCS) loadRevision(repo *Repo, id string) (*nativeRevision, error) {
	if len(id) != 64 {
		return nil, errors.New("invalid native revision")
	}
	body, err := os.ReadFile(filepath.Join(n.revisionsDir(repo), id+".json"))
	if err != nil {
		return nil, err
	}
	var rev nativeRevision
	if err := json.Unmarshal(body, &rev); err != nil {
		return nil, err
	}
	if rev.ID != id {
		return nil, errors.New("native revision integrity check failed")
	}
	return &rev, nil
}

func (n *nativeVCS) loadTree(repo *Repo, tree string) (map[string]nativeEntry, error) {
	body, err := os.ReadFile(filepath.Join(n.revisionsDir(repo), "tree-"+tree+".json"))
	if err != nil {
		return nil, err
	}
	var entries map[string]nativeEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (n *nativeVCS) snapshot(repo *Repo) (map[string]nativeEntry, string, error) {
	files, err := n.store.List(nativeStoreKey(repo), "", true)
	if err != nil {
		return nil, "", err
	}
	entries := make(map[string]nativeEntry)
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if file.IsDir || shouldSkipGenerated(file.Path) {
			continue
		}
		path, err := normalisePath(file.Path)
		if err != nil {
			return nil, "", err
		}
		body, err := n.store.Read(nativeStoreKey(repo), path)
		if err != nil {
			return nil, "", err
		}
		blob := hashBytes(body)
		if err := os.MkdirAll(n.blobsDir(repo), 0o700); err != nil {
			return nil, "", err
		}
		blobPath := filepath.Join(n.blobsDir(repo), blob)
		if _, err := os.Stat(blobPath); errors.Is(err, os.ErrNotExist) {
			if err := atomicWrite(blobPath, body, 0o600); err != nil {
				return nil, "", err
			}
		}
		entries[path] = nativeEntry{Blob: blob, Mode: file.Mode}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var canonical bytes.Buffer
	for _, path := range paths {
		e := entries[path]
		fmt.Fprintf(&canonical, "%s\x00%s\x00%d\n", path, e.Blob, e.Mode)
	}
	tree := hashBytes(canonical.Bytes())
	body, err := json.Marshal(entries)
	if err != nil {
		return nil, "", err
	}
	if err := atomicWrite(filepath.Join(n.revisionsDir(repo), "tree-"+tree+".json"), body, 0o600); err != nil {
		return nil, "", err
	}
	return entries, tree, nil
}

func (n *nativeVCS) checkpointLocked(_ context.Context, repo *Repo, message, actor string, allowEmpty bool) (*nativeRevision, error) {
	if err := n.ensureDirs(repo); err != nil {
		return nil, err
	}
	_, tree, err := n.snapshot(repo)
	if err != nil {
		return nil, err
	}
	branch, err := n.currentBranch(repo)
	if err != nil {
		return nil, err
	}
	parent := n.readRef(repo, branch, false)
	if parent != "" {
		prev, e := n.loadRevision(repo, parent)
		if e != nil {
			return nil, e
		}
		if prev.Tree == tree && !allowEmpty {
			return prev, nil
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	canonical := struct {
		Tree      string   `json:"tree"`
		Parents   []string `json:"parents,omitempty"`
		Message   string   `json:"message"`
		Actor     string   `json:"actor,omitempty"`
		CreatedAt string   `json:"created_at"`
	}{tree, nonEmpty(parent), strings.TrimSpace(message), strings.TrimSpace(actor), now}
	seed, _ := json.Marshal(canonical)
	rev := &nativeRevision{ID: hashBytes(seed), Tree: tree, Message: canonical.Message, Actor: canonical.Actor, CreatedAt: now}
	if parent != "" {
		rev.Parents = []string{parent}
	}
	body, _ := json.Marshal(rev)
	if err := atomicWrite(filepath.Join(n.revisionsDir(repo), rev.ID+".json"), body, 0o600); err != nil {
		return nil, err
	}
	if err := atomicWrite(n.refPath(repo, branch, false), []byte(rev.ID+"\n"), 0o600); err != nil {
		return nil, err
	}
	return rev, nil
}

func (n *nativeVCS) ensureDirs(repo *Repo) error {
	for _, dir := range []string{n.blobsDir(repo), n.revisionsDir(repo), filepath.Join(n.refsDir(repo), "heads"), filepath.Join(n.refsDir(repo), "tags")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if _, err := os.Stat(n.headPath(repo)); errors.Is(err, os.ErrNotExist) {
		return atomicWrite(n.headPath(repo), []byte("main\n"), 0o600)
	}
	return nil
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func (n *nativeVCS) checkpoint(repo *Repo, message, actor string) (*nativeRevision, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.locks.lock(repoStoreKey(repo))()
	return n.checkpointLocked(context.Background(), repo, message, actor, false)
}

func (n *nativeVCS) status(repo *Repo) (*nativeStatus, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.locks.rlock(repoStoreKey(repo))()
	branch, err := n.currentBranch(repo)
	if err != nil {
		return nil, err
	}
	head := n.readRef(repo, branch, false)
	_, tree, err := n.snapshot(repo)
	if err != nil {
		return nil, err
	}
	dirty := true
	if head != "" {
		rev, e := n.loadRevision(repo, head)
		if e != nil {
			return nil, e
		}
		dirty = rev.Tree != tree
	}
	count := 0
	_ = filepath.WalkDir(n.revisionsDir(repo), func(path string, d os.DirEntry, e error) error {
		if e == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".json") && !strings.HasPrefix(d.Name(), "tree-") {
			count++
		}
		return nil
	})
	return &nativeStatus{Native: true, Branch: branch, Head: head, Dirty: dirty, Revision: count}, nil
}

func (n *nativeVCS) history(repo *Repo, limit int) ([]nativeRevision, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	branch, err := n.currentBranch(repo)
	if err != nil {
		return nil, err
	}
	id := n.readRef(repo, branch, false)
	out := []nativeRevision{}
	for id != "" && len(out) < limit {
		rev, e := n.loadRevision(repo, id)
		if e != nil {
			return nil, e
		}
		out = append(out, *rev)
		if len(rev.Parents) == 0 {
			break
		}
		id = rev.Parents[0]
	}
	return out, nil
}

func (n *nativeVCS) createBranch(repo *Repo, name, start string) error {
	if err := n.ensureRepo(repo); err != nil {
		return err
	}
	if _, err := normaliseRefName(name); err != nil {
		return err
	}
	if start == "" {
		branch, _ := n.currentBranch(repo)
		start = n.readRef(repo, branch, false)
	}
	if _, err := n.loadRevision(repo, start); err != nil {
		return err
	}
	if _, err := os.Stat(n.refPath(repo, name, false)); err == nil {
		return errors.New("branch already exists")
	}
	return atomicWrite(n.refPath(repo, name, false), []byte(start+"\n"), 0o600)
}

func (n *nativeVCS) branches(repo *Repo) (map[string]string, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(n.refsDir(repo), "heads"))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		out[entry.Name()] = n.readRef(repo, entry.Name(), false)
	}
	return out, nil
}

func (n *nativeVCS) switchBranch(repo *Repo, name string) error {
	if err := n.ensureRepo(repo); err != nil {
		return err
	}
	if _, err := normaliseRefName(name); err != nil {
		return err
	}
	if _, err := os.Stat(n.refPath(repo, name, false)); err != nil {
		return errors.New("branch not found")
	}
	status, err := n.status(repo)
	if err != nil {
		return err
	}
	if status.Dirty {
		return errors.New("working tree must be clean before switching branches")
	}
	if err := n.restoreRevision(repo, n.readRef(repo, name, false), nil); err != nil {
		return err
	}
	return atomicWrite(n.headPath(repo), []byte(name+"\n"), 0o600)
}

func normaliseRefName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || strings.Contains(name, "..") || strings.ContainsAny(name, "\\/ ~^:?*[\x00") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return "", errors.New("invalid ref name")
	}
	return filepath.ToSlash(name), nil
}

func (n *nativeVCS) createTag(repo *Repo, name, revision string) error {
	if err := n.ensureRepo(repo); err != nil {
		return err
	}
	if _, err := normaliseRefName(name); err != nil {
		return err
	}
	if revision == "" {
		branch, _ := n.currentBranch(repo)
		revision = n.readRef(repo, branch, false)
	}
	if _, err := n.loadRevision(repo, revision); err != nil {
		return err
	}
	if _, err := os.Stat(n.refPath(repo, name, true)); err == nil {
		return errors.New("tag already exists")
	}
	return atomicWrite(n.refPath(repo, name, true), []byte(revision+"\n"), 0o600)
}

func (n *nativeVCS) tags(repo *Repo) (map[string]string, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(n.refsDir(repo), "tags"))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out[e.Name()] = n.readRef(repo, e.Name(), true)
	}
	return out, nil
}

func (n *nativeVCS) restoreRevision(repo *Repo, revision string, only []string) error {
	rev, err := n.loadRevision(repo, revision)
	if err != nil {
		return err
	}
	target, err := n.loadTree(repo, rev.Tree)
	if err != nil {
		return err
	}
	current, _, err := n.snapshot(repo)
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	if len(only) == 0 {
		for path := range current {
			selected[path] = true
		}
		for path := range target {
			selected[path] = true
		}
	} else {
		for _, path := range only {
			clean, e := normalisePath(path)
			if e != nil {
				return e
			}
			selected[clean] = true
		}
	}
	changes := []fileMutation{}
	for path := range selected {
		want, hasWant := target[path]
		_, hasCurrent := current[path]
		if hasWant {
			body, e := os.ReadFile(filepath.Join(n.blobsDir(repo), want.Blob))
			if e != nil {
				return e
			}
			changes = append(changes, fileMutation{Path: path, Body: body, Mode: os.FileMode(want.Mode)})
		} else if hasCurrent {
			changes = append(changes, fileMutation{Path: path, Delete: true})
		}
	}
	return applyFileMutations(n.store, nativeStoreKey(repo), changes)
}

func (n *nativeVCS) exportRef(repo *Repo, revision string) ([]byte, string, error) {
	rev, err := n.loadRevision(repo, revision)
	if err != nil {
		return nil, "", err
	}
	tree, err := n.loadTree(repo, rev.Tree)
	if err != nil {
		return nil, "", err
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	paths := make([]string, 0, len(tree))
	for path := range tree {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		entry := tree[path]
		body, e := os.ReadFile(filepath.Join(n.blobsDir(repo), entry.Blob))
		if e != nil {
			_ = zw.Close()
			return nil, "", e
		}
		hdr := &zip.FileHeader{Name: path, Method: zip.Deflate}
		mode := os.FileMode(entry.Mode)
		if mode == 0 {
			mode = 0644
		}
		hdr.SetMode(mode)
		w, e := zw.CreateHeader(hdr)
		if e != nil {
			_ = zw.Close()
			return nil, "", e
		}
		if _, e = w.Write(body); e != nil {
			_ = zw.Close()
			return nil, "", e
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	return out.Bytes(), hashBytes(out.Bytes()), nil
}
