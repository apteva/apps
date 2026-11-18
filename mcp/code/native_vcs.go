package main

// Native version control is deliberately independent from Git.  The working
// tree remains the editing surface; this package stores immutable Code
// revisions as content-addressed blobs and canonical tree manifests.  Git is
// an optional interoperability layer implemented by git_service.go.
//
// Storage format 2 (Code 0.15) keeps every format-1 object readable and every
// revision ID unchanged, and adds:
//
//   - index.json: path -> (size, mtime, mode, blob) so a snapshot only rereads
//     files whose stat changed (racily-clean entries are always rehashed);
//   - trees/<hash>.json: one tree object per folder, referenced by a
//     revision's "root", so unchanged folders are shared between revisions
//     and diffs skip them;
//   - logs/{heads,tags}/<name>.jsonl: an append-only log of every ref move,
//     used by repos_undo and as garbage-collection roots;
//   - compare-and-swap ref updates and repository-lock discipline for every
//     mutation;
//   - garbage collection of unreachable objects after a grace period.
//
// Each revision still carries the format-1 flat tree hash in "tree" (and the
// flat tree file is still written), so a 0.14 build can read history written
// by 0.15 after a rollback.

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

const (
	nativeFormatVersion = 2
	nativeGCGrace       = 14 * 24 * time.Hour
	nativeGCInterval    = 24 * time.Hour
	// Files modified this close to the previous index write are "racily
	// clean": a later edit inside the same timestamp tick would keep size and
	// mtime unchanged, so they are always rehashed.
	nativeRacyWindow = 2 * time.Second
)

var (
	errNativeRefMoved        = errors.New("native ref moved")
	errNativeMergeInProgress = errors.New("a native merge is in progress; resolve and continue it, or abort it, first (repos_merge)")
	errNativeProtected       = errors.New("branch is protected")
)

type nativeVCS struct {
	root  string
	store FileStore
	locks *repoLockSet
	now   func() time.Time
}

type nativeRevision struct {
	ID        string   `json:"id"`
	Tree      string   `json:"tree"`
	Root      string   `json:"root,omitempty"`
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

// nativeTreeEntry is one child of a folder tree object.
type nativeTreeEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "blob" or "tree"
	Hash string `json:"hash"`
	Mode uint32 `json:"mode,omitempty"`
}

type nativeSnapshot struct {
	Entries map[string]nativeEntry
	Tree    string // format-1 flat tree hash (revision identity)
	Root    string // folder tree root hash; set only for persisted snapshots
}

type nativeIndexEntry struct {
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime_ns"`
	Mode  uint32 `json:"mode"`
	Blob  string `json:"blob"`
}

type nativeIndex struct {
	Version   int                         `json:"version"`
	WrittenAt int64                       `json:"written_at_ns"`
	Entries   map[string]nativeIndexEntry `json:"entries"`
}

type nativeRefLogEntry struct {
	Old     string `json:"old,omitempty"`
	New     string `json:"new"`
	Actor   string `json:"actor,omitempty"`
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
	At      string `json:"at"`
}

func newNativeVCS(root string, store FileStore, locks *repoLockSet) *nativeVCS {
	return &nativeVCS{root: filepath.Join(root, "native-vcs"), store: store, locks: locks, now: time.Now}
}

func (n *nativeVCS) repoDir(repo *Repo) string {
	return filepath.Join(n.root, strconv.FormatInt(repo.ID, 10))
}

func nativeStoreKey(repo *Repo) string          { return repoStoreKey(repo) }
func (n *nativeVCS) blobsDir(repo *Repo) string { return filepath.Join(n.repoDir(repo), "blobs") }
func (n *nativeVCS) revisionsDir(repo *Repo) string {
	return filepath.Join(n.repoDir(repo), "revisions")
}
func (n *nativeVCS) treesDir(repo *Repo) string   { return filepath.Join(n.repoDir(repo), "trees") }
func (n *nativeVCS) refsDir(repo *Repo) string    { return filepath.Join(n.repoDir(repo), "refs") }
func (n *nativeVCS) logsDir(repo *Repo) string    { return filepath.Join(n.repoDir(repo), "logs") }
func (n *nativeVCS) headPath(repo *Repo) string   { return filepath.Join(n.repoDir(repo), "HEAD") }
func (n *nativeVCS) indexPath(repo *Repo) string  { return filepath.Join(n.repoDir(repo), "index.json") }
func (n *nativeVCS) formatPath(repo *Repo) string { return filepath.Join(n.repoDir(repo), "format") }
func (n *nativeVCS) gcStampPath(repo *Repo) string {
	return filepath.Join(n.repoDir(repo), "gc-last")
}

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

func appendJSONLine(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	// A crash can leave a torn final line; start on a fresh line so this
	// entry is not glued onto it and lost with it.
	if info, statErr := f.Stat(); statErr == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, readErr := f.ReadAt(last, info.Size()-1); readErr == nil && last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	if _, err = f.Write(append(line, '\n')); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func hashBytes(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

// rawStore returns the unlocked store. Every native entry point owns the
// repository lock itself; going through lockedFileStore while holding it would
// deadlock (write lock -> store read lock).
func (n *nativeVCS) rawStore() FileStore {
	if locked, ok := n.store.(*lockedFileStore); ok {
		return locked.inner
	}
	return n.store
}

func (n *nativeVCS) lock(repo *Repo) func()  { return n.locks.lock(nativeStoreKey(repo)) }
func (n *nativeVCS) rlock(repo *Repo) func() { return n.locks.rlock(nativeStoreKey(repo)) }

// ─── Repository lifecycle and format migration ──────────────────────────

func (n *nativeVCS) ensureRepo(repo *Repo) error {
	if repo == nil {
		return errors.New("repository required")
	}
	if n.initialised(repo) {
		return nil
	}
	defer n.lock(repo)()
	return n.ensureRepoLocked(repo)
}

func (n *nativeVCS) initialised(repo *Repo) bool {
	body, err := os.ReadFile(n.formatPath(repo))
	if err != nil || strings.TrimSpace(string(body)) != strconv.Itoa(nativeFormatVersion) {
		return false
	}
	return fileExists(n.refPath(repo, "main", false)) && fileExists(n.headPath(repo))
}

func (n *nativeVCS) ensureRepoLocked(repo *Repo) error {
	if repo == nil {
		return errors.New("repository required")
	}
	if err := n.ensureDirs(repo); err != nil {
		return err
	}
	if err := n.migrateLocked(repo); err != nil {
		return err
	}
	if !fileExists(n.refPath(repo, "main", false)) {
		_, err := n.commitLocked(repo, nativeCommit{Message: "Initial repository revision", Actor: "system", AllowEmpty: true, Action: "init"})
		return err
	}
	return nil
}

func (n *nativeVCS) ensureDirs(repo *Repo) error {
	for _, dir := range []string{
		n.blobsDir(repo), n.revisionsDir(repo), n.treesDir(repo),
		filepath.Join(n.refsDir(repo), "heads"), filepath.Join(n.refsDir(repo), "tags"),
		filepath.Join(n.logsDir(repo), "heads"), filepath.Join(n.logsDir(repo), "tags"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if !fileExists(n.headPath(repo)) {
		return atomicWrite(n.headPath(repo), []byte("main\n"), 0o600)
	}
	return nil
}

// migrateLocked upgrades a format-1 repository in place. Format 1 objects are
// read as-is (no rewrite, so revision IDs are untouched); the migration seeds
// each existing ref's log so its current value is an undo and GC root.
func (n *nativeVCS) migrateLocked(repo *Repo) error {
	body, _ := os.ReadFile(n.formatPath(repo))
	current, _ := strconv.Atoi(strings.TrimSpace(string(body)))
	if current >= nativeFormatVersion {
		return nil
	}
	for _, tag := range []bool{false, true} {
		refs, err := n.listRefs(repo, tag)
		if err != nil {
			return err
		}
		for name, id := range refs {
			if id == "" || fileExists(n.logPath(repo, name, tag)) {
				continue
			}
			entry := nativeRefLogEntry{New: id, Actor: "system", Action: "migrate", Message: "Storage format 2", At: n.timestamp()}
			if err := appendJSONLine(n.logPath(repo, name, tag), entry); err != nil {
				return err
			}
		}
	}
	return atomicWrite(n.formatPath(repo), []byte(strconv.Itoa(nativeFormatVersion)+"\n"), 0o600)
}

func (n *nativeVCS) backfill(db *sql.DB) error {
	repos, err := nativeAllRepos(db)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		if err := n.ensureRepo(repo); err != nil {
			return fmt.Errorf("initialize native revisions for %s: %w", repo.Slug, err)
		}
	}
	return nil
}

func nativeAllRepos(db *sql.DB) ([]*Repo, error) {
	rows, err := db.Query(`SELECT ` + repoColumns + ` FROM repositories`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Repo{}
	for rows.Next() {
		repo, scanErr := scanRepoRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, repo)
	}
	return out, rows.Err()
}

func (n *nativeVCS) timestamp() string { return n.now().UTC().Format(time.RFC3339Nano) }

// ─── Refs and ref logs ──────────────────────────────────────────────────

func (n *nativeVCS) refPath(repo *Repo, name string, tag bool) string {
	dir := "heads"
	if tag {
		dir = "tags"
	}
	return filepath.Join(n.refsDir(repo), dir, name)
}

func (n *nativeVCS) logPath(repo *Repo, name string, tag bool) string {
	dir := "heads"
	if tag {
		dir = "tags"
	}
	return filepath.Join(n.logsDir(repo), dir, name+".jsonl")
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

func (n *nativeVCS) listRefs(repo *Repo, tag bool) (map[string]string, error) {
	dir := "heads"
	if tag {
		dir = "tags"
	}
	entries, err := os.ReadDir(filepath.Join(n.refsDir(repo), dir))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".tmp-") {
			continue
		}
		out[entry.Name()] = n.readRef(repo, entry.Name(), tag)
	}
	return out, nil
}

// moveRefLocked is the only way a ref changes. With checkOld it refuses to
// overwrite a ref whose current value differs from expectOld ("" = must not
// exist yet). Every move is appended to the ref's log.
func (n *nativeVCS) moveRefLocked(repo *Repo, name string, tag bool, checkOld bool, expectOld, newID string, entry nativeRefLogEntry) error {
	current := n.readRef(repo, name, tag)
	if checkOld && current != expectOld {
		if expectOld == "" {
			return fmt.Errorf("%w: %s already exists at %s", errNativeRefMoved, name, current)
		}
		return fmt.Errorf("%w: %s expected %s, now %s", errNativeRefMoved, name, expectOld, current)
	}
	if err := atomicWrite(n.refPath(repo, name, tag), []byte(newID+"\n"), 0o600); err != nil {
		return err
	}
	entry.Old, entry.New, entry.At = current, newID, n.timestamp()
	return appendJSONLine(n.logPath(repo, name, tag), entry)
}

func (n *nativeVCS) readRefLog(repo *Repo, name string, tag bool) ([]nativeRefLogEntry, error) {
	body, err := os.ReadFile(n.logPath(repo, name, tag))
	if errors.Is(err, os.ErrNotExist) {
		return []nativeRefLogEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []nativeRefLogEntry{}
	for _, line := range bytes.Split(body, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry nativeRefLogEntry
		if json.Unmarshal(line, &entry) != nil {
			continue // a torn final line from a crash is skipped, not fatal
		}
		out = append(out, entry)
	}
	return out, nil
}

// ─── Objects ────────────────────────────────────────────────────────────

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

// loadTree reads a format-1 flat tree manifest.
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

// revisionEntries returns every path of a revision, from the flat manifest
// when present and otherwise from its folder trees.
func (n *nativeVCS) revisionEntries(repo *Repo, rev *nativeRevision) (map[string]nativeEntry, error) {
	entries, err := n.loadTree(repo, rev.Tree)
	if err == nil {
		return entries, nil
	}
	if rev.Root == "" || !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	out := map[string]nativeEntry{}
	if err := n.readFolderTree(repo, rev.Root, "", out); err != nil {
		return nil, err
	}
	if flatTreeHash(out) != rev.Tree {
		return nil, errors.New("native tree integrity check failed")
	}
	return out, nil
}

func flatTreeHash(entries map[string]nativeEntry) string {
	paths := make([]string, 0, len(entries))
	for path := range entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var canonical bytes.Buffer
	for _, path := range paths {
		e := entries[path]
		fmt.Fprintf(&canonical, "%s\x00%s\x00%d\n", path, e.Blob, e.Mode)
	}
	return hashBytes(canonical.Bytes())
}

func folderTreeHash(children []nativeTreeEntry) string {
	var canonical bytes.Buffer
	for _, c := range children {
		fmt.Fprintf(&canonical, "%s\x00%s\x00%s\x00%d\n", c.Kind, c.Name, c.Hash, c.Mode)
	}
	return hashBytes(canonical.Bytes())
}

func (n *nativeVCS) loadFolderTree(repo *Repo, hash string) ([]nativeTreeEntry, error) {
	if len(hash) != 64 {
		return nil, errors.New("invalid native tree")
	}
	body, err := os.ReadFile(filepath.Join(n.treesDir(repo), hash+".json"))
	if err != nil {
		return nil, err
	}
	var children []nativeTreeEntry
	if err := json.Unmarshal(body, &children); err != nil {
		return nil, err
	}
	if folderTreeHash(children) != hash {
		return nil, errors.New("native tree integrity check failed")
	}
	return children, nil
}

func (n *nativeVCS) readFolderTree(repo *Repo, hash, prefix string, out map[string]nativeEntry) error {
	children, err := n.loadFolderTree(repo, hash)
	if err != nil {
		return err
	}
	for _, c := range children {
		path := c.Name
		if prefix != "" {
			path = prefix + "/" + c.Name
		}
		if c.Kind == "tree" {
			if err := n.readFolderTree(repo, c.Hash, path, out); err != nil {
				return err
			}
			continue
		}
		out[path] = nativeEntry{Blob: c.Hash, Mode: c.Mode}
	}
	return nil
}

// writeFolderTrees stores one tree object per folder (bottom-up) and returns
// the root hash. Existing objects are not rewritten.
func (n *nativeVCS) writeFolderTrees(repo *Repo, entries map[string]nativeEntry) (string, error) {
	type node struct {
		files map[string]nativeEntry
		dirs  map[string]*node
	}
	newNode := func() *node { return &node{files: map[string]nativeEntry{}, dirs: map[string]*node{}} }
	root := newNode()
	for path, entry := range entries {
		parts := strings.Split(path, "/")
		cur := root
		for _, part := range parts[:len(parts)-1] {
			next := cur.dirs[part]
			if next == nil {
				next = newNode()
				cur.dirs[part] = next
			}
			cur = next
		}
		cur.files[parts[len(parts)-1]] = entry
	}
	var write func(*node) (string, error)
	write = func(nd *node) (string, error) {
		children := make([]nativeTreeEntry, 0, len(nd.files)+len(nd.dirs))
		for name, e := range nd.files {
			children = append(children, nativeTreeEntry{Name: name, Kind: "blob", Hash: e.Blob, Mode: e.Mode})
		}
		for name, child := range nd.dirs {
			hash, err := write(child)
			if err != nil {
				return "", err
			}
			children = append(children, nativeTreeEntry{Name: name, Kind: "tree", Hash: hash})
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })
		hash := folderTreeHash(children)
		path := filepath.Join(n.treesDir(repo), hash+".json")
		if !fileExists(path) {
			body, err := json.Marshal(children)
			if err != nil {
				return "", err
			}
			if err := atomicWrite(path, body, 0o600); err != nil {
				return "", err
			}
		}
		return hash, nil
	}
	return write(root)
}

func (n *nativeVCS) writeBlob(repo *Repo, blob string, body []byte) error {
	path := filepath.Join(n.blobsDir(repo), blob)
	if fileExists(path) {
		return nil
	}
	return atomicWrite(path, body, 0o600)
}

// ─── Snapshot with change index ─────────────────────────────────────────

func (n *nativeVCS) loadIndex(repo *Repo) *nativeIndex {
	idx := &nativeIndex{Version: 1, Entries: map[string]nativeIndexEntry{}}
	body, err := os.ReadFile(n.indexPath(repo))
	if err != nil {
		return idx
	}
	var loaded nativeIndex
	if json.Unmarshal(body, &loaded) != nil || loaded.Version != 1 || loaded.Entries == nil {
		return idx // a damaged index only costs a full rehash
	}
	return &loaded
}

// snapshot is kept for callers that already hold the repository lock.
func (n *nativeVCS) snapshot(repo *Repo) (map[string]nativeEntry, string, error) {
	snap, err := n.snapshotLocked(repo, true)
	if err != nil {
		return nil, "", err
	}
	return snap.Entries, snap.Tree, nil
}

// snapshotLocked hashes the working tree. Files whose size, mtime and mode
// match the index (and are not racily clean) reuse the indexed blob hash
// without being read. persist stores blobs and tree objects for a checkpoint.
func (n *nativeVCS) snapshotLocked(repo *Repo, persist bool) (*nativeSnapshot, error) {
	store := n.rawStore()
	key := nativeStoreKey(repo)
	started := n.now()
	files, err := store.List(key, "", true)
	if err != nil {
		return nil, err
	}
	localRoot := ""
	if local, ok := store.(FileStoreLocalPath); ok {
		localRoot = local.RepoPath(key)
	}
	prev := n.loadIndex(repo)
	trustedBefore := prev.WrittenAt - int64(nativeRacyWindow)
	next := &nativeIndex{Version: 1, WrittenAt: started.UnixNano(), Entries: map[string]nativeIndexEntry{}}
	indexChanged := len(prev.Entries) == 0
	racy := false
	entries := make(map[string]nativeEntry)
	for _, file := range files {
		if file.IsDir || shouldSkipGenerated(file.Path) {
			continue
		}
		path, err := normalisePath(file.Path)
		if err != nil {
			return nil, err
		}
		var stat nativeIndexEntry
		stated := false
		if localRoot != "" {
			if info, e := os.Lstat(filepath.Join(localRoot, filepath.FromSlash(path))); e == nil && info.Mode().IsRegular() {
				stat = nativeIndexEntry{Size: info.Size(), MTime: info.ModTime().UnixNano(), Mode: file.Mode}
				stated = true
			}
		}
		blob := ""
		if stated {
			old, ok := prev.Entries[path]
			if ok && old.Blob != "" && old.Size == stat.Size && old.MTime == stat.MTime && old.Mode == stat.Mode && stat.MTime < trustedBefore {
				blob = old.Blob
				if persist && !fileExists(filepath.Join(n.blobsDir(repo), blob)) {
					blob = "" // hashed by a status call but never stored
				}
			}
		}
		if blob == "" {
			body, err := store.Read(key, path)
			if err != nil {
				return nil, err
			}
			blob = hashBytes(body)
			if persist {
				if err := n.writeBlob(repo, blob, body); err != nil {
					return nil, err
				}
			}
		}
		entries[path] = nativeEntry{Blob: blob, Mode: file.Mode}
		if stated {
			stat.Blob = blob
			next.Entries[path] = stat
			if old, ok := prev.Entries[path]; !ok || old != stat {
				indexChanged = true
			}
			if stat.MTime >= next.WrittenAt-int64(nativeRacyWindow) {
				racy = true
			}
		}
	}
	if len(next.Entries) != len(prev.Entries) {
		indexChanged = true
	}
	snap := &nativeSnapshot{Entries: entries, Tree: flatTreeHash(entries)}
	if persist {
		flat := filepath.Join(n.revisionsDir(repo), "tree-"+snap.Tree+".json")
		if !fileExists(flat) {
			body, err := json.Marshal(entries)
			if err != nil {
				return nil, err
			}
			if err := atomicWrite(flat, body, 0o600); err != nil {
				return nil, err
			}
		}
		root, err := n.writeFolderTrees(repo, entries)
		if err != nil {
			return nil, err
		}
		snap.Root = root
	}
	if localRoot != "" && (indexChanged || racy) {
		if body, err := json.Marshal(next); err == nil {
			_ = atomicWrite(n.indexPath(repo), body, 0o600) // a cache: failure only costs rehashing
		}
	}
	return snap, nil
}

// ─── Revisions ──────────────────────────────────────────────────────────

type nativeCommit struct {
	Message      string
	Actor        string
	AllowEmpty   bool
	ExpectedHead string
	Action       string
	ExtraParents []string
	InMerge      bool // only repos_merge may commit while a merge is in progress
	// AllowProtected lets internal writers (Git auto-sync, change-request
	// merges, repository creation) record revisions on a protected branch.
	AllowProtected bool
}

func (n *nativeVCS) checkpointLocked(_ context.Context, repo *Repo, message, actor string, allowEmpty bool) (*nativeRevision, error) {
	return n.commitLocked(repo, nativeCommit{Message: message, Actor: actor, AllowEmpty: allowEmpty, Action: "checkpoint", AllowProtected: true})
}

func (n *nativeVCS) commitLocked(repo *Repo, opts nativeCommit) (*nativeRevision, error) {
	if err := n.ensureDirs(repo); err != nil {
		return nil, err
	}
	if !opts.InMerge && n.mergeInProgress(repo) {
		return nil, errNativeMergeInProgress
	}
	branch, err := n.currentBranch(repo)
	if err != nil {
		return nil, err
	}
	if !opts.AllowProtected {
		if err := n.guardProtectedLocked(repo, branch); err != nil {
			return nil, err
		}
	}
	parent := n.readRef(repo, branch, false)
	if opts.ExpectedHead != "" && opts.ExpectedHead != parent {
		return nil, fmt.Errorf("%w: branch %s expected head %s, current head %s", errNativeRefMoved, branch, opts.ExpectedHead, parent)
	}
	snap, err := n.snapshotLocked(repo, true)
	if err != nil {
		return nil, err
	}
	if parent != "" {
		prev, e := n.loadRevision(repo, parent)
		if e != nil {
			return nil, e
		}
		if prev.Tree == snap.Tree && !opts.AllowEmpty {
			return prev, nil
		}
	}
	parents := append(nonEmpty(parent), opts.ExtraParents...)
	rev, err := n.writeRevisionLocked(repo, snap, parents, opts.Message, opts.Actor)
	if err != nil {
		return nil, err
	}
	action := opts.Action
	if action == "" {
		action = "checkpoint"
	}
	entry := nativeRefLogEntry{Actor: rev.Actor, Action: action, Message: rev.Message}
	if err := n.moveRefLocked(repo, branch, false, true, parent, rev.ID, entry); err != nil {
		return nil, err
	}
	return rev, nil
}

// writeRevisionLocked stores a revision object for a persisted snapshot
// without moving any ref.
func (n *nativeVCS) writeRevisionLocked(repo *Repo, snap *nativeSnapshot, parents []string, message, actor string) (*nativeRevision, error) {
	now := n.timestamp()
	canonical := struct {
		Tree      string   `json:"tree"`
		Parents   []string `json:"parents,omitempty"`
		Message   string   `json:"message"`
		Actor     string   `json:"actor,omitempty"`
		CreatedAt string   `json:"created_at"`
	}{snap.Tree, parents, strings.TrimSpace(message), strings.TrimSpace(actor), now}
	seed, _ := json.Marshal(canonical)
	rev := &nativeRevision{ID: hashBytes(seed), Tree: snap.Tree, Root: snap.Root, Parents: parents, Message: canonical.Message, Actor: canonical.Actor, CreatedAt: now}
	body, _ := json.Marshal(rev)
	if err := atomicWrite(filepath.Join(n.revisionsDir(repo), rev.ID+".json"), body, 0o600); err != nil {
		return nil, err
	}
	return rev, nil
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func (n *nativeVCS) checkpoint(repo *Repo, message, actor string) (*nativeRevision, error) {
	return n.checkpointExpect(repo, message, actor, "")
}

// checkpointExpect refuses to move the branch when expectedHead is set and
// no longer matches it.
func (n *nativeVCS) checkpointExpect(repo *Repo, message, actor, expectedHead string) (*nativeRevision, error) {
	if repo == nil {
		return nil, errors.New("repository required")
	}
	defer n.lock(repo)()
	if err := n.ensureRepoLocked(repo); err != nil {
		return nil, err
	}
	return n.commitLocked(repo, nativeCommit{Message: message, Actor: actor, ExpectedHead: strings.TrimSpace(expectedHead), Action: "checkpoint"})
}

// checkpointHeld is for callers that already hold the repository write lock
// (the Git auto-sync tick).
func (n *nativeVCS) checkpointHeld(repo *Repo, message, actor string) (*nativeRevision, error) {
	if err := n.ensureRepoLocked(repo); err != nil {
		return nil, err
	}
	return n.commitLocked(repo, nativeCommit{Message: message, Actor: actor, Action: "checkpoint", AllowProtected: true})
}

func (n *nativeVCS) status(repo *Repo) (*nativeStatus, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
	return n.statusLocked(repo)
}

func (n *nativeVCS) statusLocked(repo *Repo) (*nativeStatus, error) {
	branch, err := n.currentBranch(repo)
	if err != nil {
		return nil, err
	}
	head := n.readRef(repo, branch, false)
	snap, err := n.snapshotLocked(repo, false)
	if err != nil {
		return nil, err
	}
	dirty := true
	if head != "" {
		rev, e := n.loadRevision(repo, head)
		if e != nil {
			return nil, e
		}
		dirty = rev.Tree != snap.Tree
	}
	count := 0
	if entries, err := os.ReadDir(n.revisionsDir(repo)); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), "tree-") && !strings.HasPrefix(e.Name(), ".tmp-") {
				count++
			}
		}
	}
	return &nativeStatus{Native: true, Branch: branch, Head: head, Dirty: dirty, Revision: count}, nil
}

func (n *nativeVCS) history(repo *Repo, limit int) ([]nativeRevision, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
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

// ─── Branches and tags ──────────────────────────────────────────────────

func (n *nativeVCS) createBranch(repo *Repo, name, start string) error {
	if err := n.ensureRepo(repo); err != nil {
		return err
	}
	if _, err := normaliseRefName(name); err != nil {
		return err
	}
	defer n.lock(repo)()
	if start == "" {
		branch, _ := n.currentBranch(repo)
		start = n.readRef(repo, branch, false)
	}
	if _, err := n.loadRevision(repo, start); err != nil {
		return err
	}
	if fileExists(n.refPath(repo, name, false)) {
		return errors.New("branch already exists")
	}
	return n.moveRefLocked(repo, name, false, true, "", start, nativeRefLogEntry{Action: "branch.create"})
}

func (n *nativeVCS) branches(repo *Repo) (map[string]string, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
	return n.listRefs(repo, false)
}

// switchBranch checks out another branch. Changes that are not in a revision
// travel with the branch they were made on: they are saved as an auto-stash
// revision for the branch being left and put back when that branch is checked
// out again (see native_merge.go).
func (n *nativeVCS) switchBranch(repo *Repo, name string) (*nativeSwitchResult, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	if _, err := normaliseRefName(name); err != nil {
		return nil, err
	}
	defer n.lock(repo)()
	return n.switchBranchLocked(repo, name, "")
}

func normaliseRefName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || strings.Contains(name, "..") || strings.ContainsAny(name, "\\/ ~^:?*[\x00") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasPrefix(name, ".tmp-") {
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
	defer n.lock(repo)()
	if revision == "" {
		branch, _ := n.currentBranch(repo)
		revision = n.readRef(repo, branch, false)
	}
	if _, err := n.loadRevision(repo, revision); err != nil {
		return err
	}
	if fileExists(n.refPath(repo, name, true)) {
		return errors.New("tag already exists")
	}
	return n.moveRefLocked(repo, name, true, true, "", revision, nativeRefLogEntry{Action: "tag.create"})
}

func (n *nativeVCS) tags(repo *Repo) (map[string]string, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
	return n.listRefs(repo, true)
}

// ─── Restore, undo and export ───────────────────────────────────────────

func (n *nativeVCS) restoreRevision(repo *Repo, revision string, only []string) error {
	if err := n.ensureRepo(repo); err != nil {
		return err
	}
	defer n.lock(repo)()
	return n.restoreRevisionLocked(repo, revision, only)
}

// restore applies paths from a revision and, unless workingTreeOnly, records
// the result as a new revision, all under one lock.
func (n *nativeVCS) restore(repo *Repo, revision string, only []string, workingTreeOnly bool, actor string) (*nativeRevision, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.lock(repo)()
	if !workingTreeOnly && n.mergeInProgress(repo) {
		return nil, errNativeMergeInProgress
	}
	if !workingTreeOnly {
		branch, err := n.currentBranch(repo)
		if err != nil {
			return nil, err
		}
		if err := n.guardProtectedLocked(repo, branch); err != nil {
			return nil, err
		}
	}
	if err := n.restoreRevisionLocked(repo, revision, only); err != nil {
		return nil, err
	}
	if workingTreeOnly {
		return nil, nil
	}
	return n.commitLocked(repo, nativeCommit{Message: "Restore " + revision, Actor: actor, Action: "restore"})
}

func (n *nativeVCS) restoreRevisionLocked(repo *Repo, revision string, only []string) error {
	rev, err := n.loadRevision(repo, revision)
	if err != nil {
		return err
	}
	target, err := n.revisionEntries(repo, rev)
	if err != nil {
		return err
	}
	return n.applyEntriesLocked(repo, target, only)
}

// applyEntriesLocked makes the working tree match target (for the selected
// paths, or every path when only is empty). Every target blob must already be
// stored.
func (n *nativeVCS) applyEntriesLocked(repo *Repo, target map[string]nativeEntry, only []string) error {
	current, err := n.snapshotLocked(repo, false)
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	if len(only) == 0 {
		for path := range current.Entries {
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
		have, hasCurrent := current.Entries[path]
		if hasWant && hasCurrent && have == want {
			continue
		}
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
	if len(changes) == 0 {
		return nil
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return applyFileMutations(n.rawStore(), nativeStoreKey(repo), changes)
}

// nativeRefMove is a ref log entry as returned to callers (newest first).
type nativeRefMove struct {
	Index   int    `json:"index"`
	From    string `json:"from,omitempty"`
	To      string `json:"to"`
	Actor   string `json:"actor,omitempty"`
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
	At      string `json:"at"`
}

func (n *nativeVCS) refMoves(repo *Repo, branch string, limit int) (string, []nativeRefMove, error) {
	if err := n.ensureRepo(repo); err != nil {
		return "", nil, err
	}
	defer n.rlock(repo)()
	branch, err := n.resolveBranchLocked(repo, branch)
	if err != nil {
		return "", nil, err
	}
	log, err := n.readRefLog(repo, branch, false)
	if err != nil {
		return "", nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	out := []nativeRefMove{}
	for i := len(log) - 1; i >= 0 && len(out) < limit; i-- {
		e := log[i]
		out = append(out, nativeRefMove{Index: len(log) - 1 - i, From: e.Old, To: e.New, Actor: e.Actor, Action: e.Action, Message: e.Message, At: e.At})
	}
	return branch, out, nil
}

func (n *nativeVCS) resolveBranchLocked(repo *Repo, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return n.currentBranch(repo)
	}
	if _, err := normaliseRefName(branch); err != nil {
		return "", err
	}
	if !fileExists(n.refPath(repo, branch, false)) {
		return "", errors.New("branch not found")
	}
	return branch, nil
}

// undo moves a branch back to a revision it pointed at before (any value in
// its ref log). Moving the checked-out branch also restores the working tree,
// so it requires a clean tree; nothing is lost because the previous head stays
// in the log.
func (n *nativeVCS) undo(repo *Repo, branch, revision, actor string) (string, *nativeRefMove, error) {
	if err := n.ensureRepo(repo); err != nil {
		return "", nil, err
	}
	revision = strings.TrimSpace(revision)
	defer n.lock(repo)()
	if n.mergeInProgress(repo) {
		return "", nil, errNativeMergeInProgress
	}
	branch, err := n.resolveBranchLocked(repo, branch)
	if err != nil {
		return "", nil, err
	}
	if err := n.guardProtectedLocked(repo, branch); err != nil {
		return "", nil, err
	}
	log, err := n.readRefLog(repo, branch, false)
	if err != nil {
		return "", nil, err
	}
	known := false
	for _, e := range log {
		if e.Old == revision || e.New == revision {
			known = true
			break
		}
	}
	if !known {
		return "", nil, fmt.Errorf("revision %s never appears in the history of branch %s; list moves with repos_undo first", revision, branch)
	}
	if _, err := n.loadRevision(repo, revision); err != nil {
		return "", nil, err
	}
	current := n.readRef(repo, branch, false)
	if current == revision {
		return branch, nil, nil
	}
	checkedOut, err := n.currentBranch(repo)
	if err != nil {
		return "", nil, err
	}
	if checkedOut == branch {
		status, err := n.statusLocked(repo)
		if err != nil {
			return "", nil, err
		}
		if status.Dirty {
			return "", nil, errors.New("working tree has changes that are not in a revision; checkpoint them before undoing the checked-out branch")
		}
		if err := n.restoreRevisionLocked(repo, revision, nil); err != nil {
			return "", nil, err
		}
	}
	entry := nativeRefLogEntry{Actor: strings.TrimSpace(actor), Action: "undo", Message: "Undo to " + revision}
	if err := n.moveRefLocked(repo, branch, false, true, current, revision, entry); err != nil {
		return "", nil, err
	}
	return branch, &nativeRefMove{From: current, To: revision, Actor: entry.Actor, Action: entry.Action, Message: entry.Message, At: n.timestamp()}, nil
}

func (n *nativeVCS) exportRef(repo *Repo, revision string) ([]byte, string, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, "", err
	}
	defer n.rlock(repo)()
	rev, err := n.loadRevision(repo, revision)
	if err != nil {
		return nil, "", err
	}
	tree, err := n.revisionEntries(repo, rev)
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

// ─── Diff ───────────────────────────────────────────────────────────────

// changedPaths compares a revision with another revision, or with the working
// tree when to is "". Two format-2 revisions are compared folder by folder so
// unchanged folders are skipped without being read.
func (n *nativeVCS) changedPaths(repo *Repo, from, to string) (string, string, []string, error) {
	if err := n.ensureRepo(repo); err != nil {
		return "", "", nil, err
	}
	defer n.rlock(repo)()
	if from == "" {
		branch, _ := n.currentBranch(repo)
		from = n.readRef(repo, branch, false)
	}
	fromRev, err := n.loadRevision(repo, from)
	if err != nil {
		return "", "", nil, err
	}
	var toRev *nativeRevision
	if to != "" {
		if toRev, err = n.loadRevision(repo, to); err != nil {
			return "", "", nil, err
		}
	}
	changed := map[string]bool{}
	if toRev != nil && fromRev.Root != "" && toRev.Root != "" {
		if err := n.diffFolderTrees(repo, fromRev.Root, toRev.Root, "", changed); err != nil {
			return "", "", nil, err
		}
	} else {
		aTree, err := n.revisionEntries(repo, fromRev)
		if err != nil {
			return "", "", nil, err
		}
		var bTree map[string]nativeEntry
		if toRev == nil {
			snap, err := n.snapshotLocked(repo, false)
			if err != nil {
				return "", "", nil, err
			}
			bTree = snap.Entries
			to = "working-tree"
		} else if bTree, err = n.revisionEntries(repo, toRev); err != nil {
			return "", "", nil, err
		}
		for path, entry := range aTree {
			if other, ok := bTree[path]; !ok || other != entry {
				changed[path] = true
			}
		}
		for path, entry := range bTree {
			if other, ok := aTree[path]; !ok || other != entry {
				changed[path] = true
			}
		}
	}
	paths := make([]string, 0, len(changed))
	for path := range changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return from, to, paths, nil
}

func (n *nativeVCS) diffFolderTrees(repo *Repo, a, b, prefix string, changed map[string]bool) error {
	if a == b {
		return nil
	}
	children := func(hash string) (map[string]nativeTreeEntry, error) {
		out := map[string]nativeTreeEntry{}
		if hash == "" {
			return out, nil
		}
		list, err := n.loadFolderTree(repo, hash)
		if err != nil {
			return nil, err
		}
		for _, c := range list {
			out[c.Name] = c
		}
		return out, nil
	}
	left, err := children(a)
	if err != nil {
		return err
	}
	right, err := children(b)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for name := range left {
		names[name] = true
	}
	for name := range right {
		names[name] = true
	}
	for name := range names {
		path := name
		if prefix != "" {
			path = prefix + "/" + name
		}
		l, hasL := left[name]
		r, hasR := right[name]
		if hasL && hasR && l == r {
			continue
		}
		lTree, rTree := "", ""
		if hasL && l.Kind == "tree" {
			lTree = l.Hash
		} else if hasL {
			changed[path] = true
		}
		if hasR && r.Kind == "tree" {
			rTree = r.Hash
		} else if hasR {
			changed[path] = true
		}
		if lTree != "" || rTree != "" {
			if err := n.diffFolderTrees(repo, lTree, rTree, path, changed); err != nil {
				return err
			}
		}
	}
	return nil
}

// ─── Garbage collection ─────────────────────────────────────────────────

type nativeGCResult struct {
	Revisions int `json:"revisions_removed"`
	Trees     int `json:"trees_removed"`
	Blobs     int `json:"blobs_removed"`
	Kept      int `json:"objects_kept"`
}

// gc removes objects that no ref, ref log entry or index entry can reach and
// that are older than grace. Any read error while marking aborts the sweep, so
// a damaged repository never loses data to collection.
func (n *nativeVCS) gc(repo *Repo, grace time.Duration) (*nativeGCResult, error) {
	defer n.lock(repo)()
	if !fileExists(n.repoDir(repo)) {
		return &nativeGCResult{}, nil
	}
	roots := map[string]bool{}
	heads := 0
	for _, tag := range []bool{false, true} {
		refs, err := n.listRefs(repo, tag)
		if err != nil {
			return nil, err
		}
		for name, id := range refs {
			if id != "" {
				roots[id] = true
				if !tag {
					heads++
				}
			}
			log, err := n.readRefLog(repo, name, tag)
			if err != nil {
				return nil, err
			}
			for _, e := range log {
				roots[e.Old], roots[e.New] = true, true
			}
		}
	}
	extra, err := n.extraRootsLocked(repo)
	if err != nil {
		return nil, err
	}
	for _, id := range extra {
		roots[id] = true
	}
	delete(roots, "")
	if heads == 0 {
		return nil, errors.New("native gc refused: repository has no branches")
	}
	revs, trees, flats, blobs := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, e := range n.loadIndex(repo).Entries {
		blobs[e.Blob] = true
	}
	var markTree func(string) error
	markTree = func(hash string) error {
		if trees[hash] {
			return nil
		}
		trees[hash] = true
		children, err := n.loadFolderTree(repo, hash)
		if err != nil {
			return err
		}
		for _, c := range children {
			if c.Kind == "tree" {
				if err := markTree(c.Hash); err != nil {
					return err
				}
			} else {
				blobs[c.Hash] = true
			}
		}
		return nil
	}
	stack := make([]string, 0, len(roots))
	for id := range roots {
		stack = append(stack, id)
	}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if revs[id] {
			continue
		}
		rev, err := n.loadRevision(repo, id)
		if err != nil {
			return nil, fmt.Errorf("native gc aborted: revision %s: %w", id, err)
		}
		revs[id] = true
		if rev.Root != "" {
			if err := markTree(rev.Root); err != nil {
				return nil, fmt.Errorf("native gc aborted: tree %s: %w", rev.Root, err)
			}
		}
		if entries, err := n.loadTree(repo, rev.Tree); err == nil {
			flats[rev.Tree] = true
			for _, e := range entries {
				blobs[e.Blob] = true
			}
		} else if rev.Root == "" {
			return nil, fmt.Errorf("native gc aborted: tree %s: %w", rev.Tree, err)
		}
		stack = append(stack, rev.Parents...)
	}
	cutoff := n.now().Add(-grace)
	result := &nativeGCResult{}
	sweep := func(dir string, keep func(name string) bool, removed *int) error {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if keep(entry.Name()) {
				result.Kept++
				continue
			}
			info, err := entry.Info()
			if err != nil || info.ModTime().After(cutoff) {
				result.Kept++
				continue
			}
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			*removed++
		}
		return nil
	}
	if err := sweep(n.blobsDir(repo), func(name string) bool { return blobs[name] }, &result.Blobs); err != nil {
		return nil, err
	}
	if err := sweep(n.treesDir(repo), func(name string) bool { return trees[strings.TrimSuffix(name, ".json")] }, &result.Trees); err != nil {
		return nil, err
	}
	if err := sweep(n.revisionsDir(repo), func(name string) bool {
		base := strings.TrimSuffix(name, ".json")
		if strings.HasPrefix(base, "tree-") {
			return flats[strings.TrimPrefix(base, "tree-")]
		}
		return revs[base]
	}, &result.Revisions); err != nil {
		return nil, err
	}
	_ = atomicWrite(n.gcStampPath(repo), []byte(n.timestamp()+"\n"), 0o600)
	return result, nil
}

// maybeGC runs gc at most once per nativeGCInterval per repository.
func (n *nativeVCS) maybeGC(repo *Repo) (*nativeGCResult, error) {
	if info, err := os.Stat(n.gcStampPath(repo)); err == nil && n.now().Sub(info.ModTime()) < nativeGCInterval {
		return nil, nil
	}
	return n.gc(repo, nativeGCGrace)
}

// gcAll collects every repository once; called in the background on mount.
func (n *nativeVCS) gcAll(db *sql.DB, warn func(msg string, args ...any)) {
	repos, err := nativeAllRepos(db)
	if err != nil {
		warn("native gc: list repositories", "err", err)
		return
	}
	for _, repo := range repos {
		if _, err := n.maybeGC(repo); err != nil {
			warn("native gc skipped repository", "slug", repo.Slug, "err", err)
		}
	}
}
