package main

// Native remotes (Code 0.18): fetch, push and clone between Code
// repositories without Git. A remote is another repository in the same
// project ("local") or a repository on another Apteva install reached over
// HTTPS ("apteva"). Objects travel as a bundle; every object is verified by
// its hash on arrival, so a remote can never inject content under a wrong ID.
//
// Remote-tracking refs live in remotes.json next to the native objects and
// resolve as "<remote>/<branch>" anywhere a ref is accepted (repos_merge
// source, repos_diff, repos_branch compare ...). Push is fast-forward only,
// refuses protected branches, and updates a checked-out remote branch only
// when that working tree has no unsaved changes. Pull is fetch + repos_merge.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const nativeBundleMaxBytes = 64 << 20

type nativeBundle struct {
	Revisions []nativeRevision                  `json:"revisions"`
	Trees     map[string][]nativeTreeEntry      `json:"trees"`
	Flats     map[string]map[string]nativeEntry `json:"flats,omitempty"` // revisions without folder trees
	Blobs     map[string]string                 `json:"blobs"`           // base64
}

type nativeRemoteRefs struct {
	Branches map[string]string `json:"branches"`
	Tags     map[string]string `json:"tags"`
	Head     string            `json:"head"`
}

type nativeRefUpdate struct {
	Branch string `json:"branch"`
	Old    string `json:"old"`
	New    string `json:"new"`
}

type nativeReceiveResult struct {
	Updated   []nativeRefUpdate `json:"updated"`
	Revisions int               `json:"revisions_received"`
}

type nativeRemote struct {
	Name      string            `json:"name"`
	Kind      string            `json:"kind"` // local | apteva
	Slug      string            `json:"slug,omitempty"`
	URL       string            `json:"url,omitempty"`
	ProjectID string            `json:"project_id,omitempty"`
	AddedBy   string            `json:"added_by,omitempty"`
	At        string            `json:"added_at"`
	Branches  map[string]string `json:"branches"`
	Tags      map[string]string `json:"tags"`
	FetchedAt string            `json:"fetched_at,omitempty"`
}

// ─── Remote configuration ───────────────────────────────────────────────

func (n *nativeVCS) remotesPath(repo *Repo) string {
	return filepath.Join(n.repoDir(repo), "remotes.json")
}

func (n *nativeVCS) loadRemotesLocked(repo *Repo) (map[string]*nativeRemote, error) {
	body, err := os.ReadFile(n.remotesPath(repo))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]*nativeRemote{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]*nativeRemote{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("remote settings are damaged: %w", err)
	}
	return out, nil
}

func (n *nativeVCS) saveRemotesLocked(repo *Repo, remotes map[string]*nativeRemote) error {
	body, _ := json.Marshal(remotes)
	return atomicWrite(n.remotesPath(repo), body, 0o600)
}

// validateAptevaRemoteURL accepts the base URL of a Code repository's REST
// API on another install, e.g. https://host/api/apps/code/api/repos/<slug>.
// Plain http is allowed only for loopback hosts.
func validateAptevaRemoteURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", errors.New("url must be the remote repository's API base, e.g. https://host/api/apps/code/api/repos/<slug>")
	}
	host := u.Hostname()
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1")) {
		return "", errors.New("remote url must use https (http only for localhost)")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("remote url must not contain credentials, a query or a fragment")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if !strings.Contains(u.Path, "/repos/") {
		return "", errors.New("remote url must end in /repos/<slug>")
	}
	return u.String(), nil
}

func (n *nativeVCS) addRemote(repo *Repo, r nativeRemote) (*nativeRemote, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	if _, err := normaliseRefName(r.Name); err != nil {
		return nil, errors.New("remote name must be a simple name like origin")
	}
	switch {
	case r.Slug != "" && r.URL == "":
		r.Kind = "local"
	case r.URL != "" && r.Slug == "":
		clean, err := validateAptevaRemoteURL(r.URL)
		if err != nil {
			return nil, err
		}
		r.Kind, r.URL = "apteva", clean
	default:
		return nil, errors.New("add needs exactly one of slug (a repository in this project) or url (a repository on another Apteva install)")
	}
	defer n.lock(repo)()
	remotes, err := n.loadRemotesLocked(repo)
	if err != nil {
		return nil, err
	}
	if remotes[r.Name] != nil {
		return nil, fmt.Errorf("remote %s already exists", r.Name)
	}
	if fileExists(n.refPath(repo, r.Name, false)) {
		return nil, fmt.Errorf("a branch is already called %s; pick another remote name", r.Name)
	}
	r.At, r.Branches, r.Tags = n.timestamp(), map[string]string{}, map[string]string{}
	remotes[r.Name] = &r
	return &r, n.saveRemotesLocked(repo, remotes)
}

func (n *nativeVCS) removeRemote(repo *Repo, name string) error {
	if err := n.ensureRepo(repo); err != nil {
		return err
	}
	defer n.lock(repo)()
	remotes, err := n.loadRemotesLocked(repo)
	if err != nil {
		return err
	}
	if remotes[name] == nil {
		return fmt.Errorf("remote %s not found", name)
	}
	delete(remotes, name)
	return n.saveRemotesLocked(repo, remotes)
}

func (n *nativeVCS) listRemotes(repo *Repo) ([]*nativeRemote, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
	remotes, err := n.loadRemotesLocked(repo)
	if err != nil {
		return nil, err
	}
	out := make([]*nativeRemote, 0, len(remotes))
	for _, r := range remotes {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (n *nativeVCS) getRemote(repo *Repo, name string) (*nativeRemote, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
	remotes, err := n.loadRemotesLocked(repo)
	if err != nil {
		return nil, err
	}
	r := remotes[strings.TrimSpace(name)]
	if r == nil {
		return nil, fmt.Errorf("remote %s not found; add it with repos_remote action=add", name)
	}
	return r, nil
}

// trackingRefLocked resolves "<remote>/<branch>".
func (n *nativeVCS) trackingRefLocked(repo *Repo, ref string) (string, bool) {
	remote, branch, ok := strings.Cut(ref, "/")
	if !ok || remote == "" || branch == "" {
		return "", false
	}
	remotes, err := n.loadRemotesLocked(repo)
	if err != nil || remotes[remote] == nil {
		return "", false
	}
	id := remotes[remote].Branches[branch]
	return id, id != ""
}

// ─── Bundles ────────────────────────────────────────────────────────────

func (n *nativeVCS) hasRevision(repo *Repo, id string) bool {
	return nativeRevisionID.MatchString(id) && fileExists(filepath.Join(n.revisionsDir(repo), id+".json"))
}

// bundleLocked packs every revision reachable from wants that is not
// reachable from haves (those the receiver already has), with the trees and
// blobs they need that are not in the haves' own trees.
func (n *nativeVCS) bundleLocked(repo *Repo, wants, haves []string) (*nativeBundle, error) {
	excluded := map[string]bool{}
	haveObjects := map[string]bool{}
	for _, h := range haves {
		if !n.hasRevision(repo, h) {
			continue
		}
		anc, err := n.ancestorsLocked(repo, h)
		if err != nil {
			return nil, err
		}
		for id := range anc {
			excluded[id] = true
		}
		rev, err := n.loadRevision(repo, h)
		if err != nil {
			return nil, err
		}
		if rev.Root != "" {
			if err := n.markTreeObjects(repo, rev.Root, haveObjects); err != nil {
				return nil, err
			}
		}
	}
	b := &nativeBundle{Trees: map[string][]nativeTreeEntry{}, Flats: map[string]map[string]nativeEntry{}, Blobs: map[string]string{}}
	size := 0
	addBlob := func(hash string) error {
		if haveObjects[hash] || b.Blobs[hash] != "" {
			return nil
		}
		body, err := n.readBlob(repo, hash)
		if err != nil {
			return err
		}
		size += len(body) * 4 / 3
		if size > nativeBundleMaxBytes {
			return fmt.Errorf("transfer exceeds %d MiB; fetch or push one branch at a time", nativeBundleMaxBytes>>20)
		}
		b.Blobs[hash] = base64.StdEncoding.EncodeToString(body)
		return nil
	}
	var addTree func(hash string) error
	addTree = func(hash string) error {
		if haveObjects[hash] || b.Trees[hash] != nil {
			return nil
		}
		children, err := n.loadFolderTree(repo, hash)
		if err != nil {
			return err
		}
		b.Trees[hash] = children
		for _, c := range children {
			if c.Kind == "tree" {
				err = addTree(c.Hash)
			} else {
				err = addBlob(c.Hash)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	seen := map[string]bool{}
	stack := append([]string(nil), wants...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[id] || excluded[id] {
			continue
		}
		seen[id] = true
		rev, err := n.loadRevision(repo, id)
		if err != nil {
			return nil, fmt.Errorf("revision %s: %w", shortID(id), err)
		}
		b.Revisions = append(b.Revisions, *rev)
		if rev.Root != "" {
			if err := addTree(rev.Root); err != nil {
				return nil, err
			}
		} else {
			entries, err := n.loadTree(repo, rev.Tree)
			if err != nil {
				return nil, err
			}
			b.Flats[rev.Tree] = entries
			for _, e := range entries {
				if err := addBlob(e.Blob); err != nil {
					return nil, err
				}
			}
		}
		stack = append(stack, rev.Parents...)
	}
	return b, nil
}

func (n *nativeVCS) markTreeObjects(repo *Repo, hash string, out map[string]bool) error {
	if out[hash] {
		return nil
	}
	out[hash] = true
	children, err := n.loadFolderTree(repo, hash)
	if err != nil {
		return err
	}
	for _, c := range children {
		if c.Kind == "tree" {
			if err := n.markTreeObjects(repo, c.Hash, out); err != nil {
				return err
			}
		} else {
			out[c.Hash] = true
		}
	}
	return nil
}

func revisionIDFor(rev *nativeRevision) string {
	seed, _ := json.Marshal(struct {
		Tree      string   `json:"tree"`
		Parents   []string `json:"parents,omitempty"`
		Message   string   `json:"message"`
		Actor     string   `json:"actor,omitempty"`
		CreatedAt string   `json:"created_at"`
	}{rev.Tree, rev.Parents, rev.Message, rev.Actor, rev.CreatedAt})
	return hashBytes(seed)
}

// importBundleLocked verifies and stores a bundle. Nothing is referenced by a
// ref yet, so a rejected bundle only leaves unreachable objects for GC.
func (n *nativeVCS) importBundleLocked(repo *Repo, b *nativeBundle) (int, error) {
	if b == nil {
		return 0, nil
	}
	if err := n.ensureDirs(repo); err != nil {
		return 0, err
	}
	for hash, enc := range b.Blobs {
		body, err := base64.StdEncoding.DecodeString(enc)
		if err != nil || hashBytes(body) != hash {
			return 0, fmt.Errorf("blob %s failed verification", shortID(hash))
		}
		if err := n.writeBlob(repo, hash, body); err != nil {
			return 0, err
		}
	}
	for hash, children := range b.Trees {
		if folderTreeHash(children) != hash {
			return 0, fmt.Errorf("tree %s failed verification", shortID(hash))
		}
		for _, c := range children {
			if c.Kind != "tree" && c.Kind != "blob" || c.Name == "" || strings.ContainsAny(c.Name, "/\x00") || c.Name == "." || c.Name == ".." {
				return 0, fmt.Errorf("tree %s has an invalid entry", shortID(hash))
			}
		}
		path := filepath.Join(n.treesDir(repo), hash+".json")
		if !fileExists(path) {
			body, _ := json.Marshal(children)
			if err := atomicWrite(path, body, 0o600); err != nil {
				return 0, err
			}
		}
	}
	for hash, entries := range b.Flats {
		if flatTreeHash(entries) != hash {
			return 0, fmt.Errorf("flat tree %s failed verification", shortID(hash))
		}
		path := filepath.Join(n.revisionsDir(repo), "tree-"+hash+".json")
		if !fileExists(path) {
			body, _ := json.Marshal(entries)
			if err := atomicWrite(path, body, 0o600); err != nil {
				return 0, err
			}
		}
	}
	incoming := map[string]bool{}
	for i := range b.Revisions {
		incoming[b.Revisions[i].ID] = true
	}
	for i := range b.Revisions {
		rev := &b.Revisions[i]
		if !nativeRevisionID.MatchString(rev.ID) || revisionIDFor(rev) != rev.ID {
			return 0, fmt.Errorf("revision %s failed verification", shortID(rev.ID))
		}
		for _, p := range rev.Parents {
			if !incoming[p] && !n.hasRevision(repo, p) {
				return 0, fmt.Errorf("revision %s is missing its parent %s", shortID(rev.ID), shortID(p))
			}
		}
		var entries map[string]nativeEntry
		if rev.Root != "" {
			entries = map[string]nativeEntry{}
			if err := n.readFolderTree(repo, rev.Root, "", entries); err != nil {
				return 0, fmt.Errorf("revision %s: %w", shortID(rev.ID), err)
			}
			if flatTreeHash(entries) != rev.Tree {
				return 0, fmt.Errorf("revision %s tree failed verification", shortID(rev.ID))
			}
			flat := filepath.Join(n.revisionsDir(repo), "tree-"+rev.Tree+".json")
			if !fileExists(flat) {
				body, _ := json.Marshal(entries)
				if err := atomicWrite(flat, body, 0o600); err != nil {
					return 0, err
				}
			}
		} else {
			var err error
			if entries, err = n.loadTree(repo, rev.Tree); err != nil {
				return 0, fmt.Errorf("revision %s has no tree", shortID(rev.ID))
			}
		}
		for path, e := range entries {
			if clean, err := normalisePath(path); err != nil || clean != path {
				return 0, fmt.Errorf("revision %s has an invalid path", shortID(rev.ID))
			}
			if !fileExists(filepath.Join(n.blobsDir(repo), e.Blob)) {
				return 0, fmt.Errorf("revision %s is missing blob %s", shortID(rev.ID), shortID(e.Blob))
			}
		}
	}
	for i := range b.Revisions {
		rev := &b.Revisions[i]
		path := filepath.Join(n.revisionsDir(repo), rev.ID+".json")
		if fileExists(path) {
			continue
		}
		body, _ := json.Marshal(rev)
		if err := atomicWrite(path, body, 0o600); err != nil {
			return 0, err
		}
	}
	return len(b.Revisions), nil
}

// ─── Serving side ───────────────────────────────────────────────────────

func (n *nativeVCS) serveRefs(repo *Repo) (*nativeRemoteRefs, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
	branches, err := n.listRefs(repo, false)
	if err != nil {
		return nil, err
	}
	tags, err := n.listRefs(repo, true)
	if err != nil {
		return nil, err
	}
	head, _ := n.currentBranch(repo)
	return &nativeRemoteRefs{Branches: branches, Tags: tags, Head: head}, nil
}

func (n *nativeVCS) serveFetch(repo *Repo, wants, haves []string) (*nativeBundle, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
	for _, w := range wants {
		if !n.hasRevision(repo, w) {
			return nil, fmt.Errorf("revision %s not found", shortID(w))
		}
	}
	return n.bundleLocked(repo, wants, haves)
}

// serveReceive stores pushed objects and fast-forwards branches.
func (n *nativeVCS) serveReceive(repo *Repo, b *nativeBundle, updates []nativeRefUpdate, actor string) (*nativeReceiveResult, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	if len(updates) == 0 {
		return nil, errors.New("push needs at least one branch update")
	}
	defer n.lock(repo)()
	count, err := n.importBundleLocked(repo, b)
	if err != nil {
		return nil, err
	}
	current, _ := n.currentBranch(repo)
	for _, u := range updates {
		if _, err := normaliseRefName(u.Branch); err != nil {
			return nil, err
		}
		if err := n.guardProtectedLocked(repo, u.Branch); err != nil {
			return nil, err
		}
		if !n.hasRevision(repo, u.New) {
			return nil, fmt.Errorf("pushed revision %s was not received", shortID(u.New))
		}
		if have := n.readRef(repo, u.Branch, false); have != u.Old {
			return nil, fmt.Errorf("%w: remote branch %s is at %s, not %s; fetch and merge first", errNativeRefMoved, u.Branch, shortID(have), shortID(u.Old))
		}
		if u.Old != "" {
			anc, err := n.ancestorsLocked(repo, u.New)
			if err != nil {
				return nil, err
			}
			if !anc[u.Old] {
				return nil, fmt.Errorf("push to %s is not a fast-forward; fetch, merge, then push", u.Branch)
			}
		}
		if u.Branch == current {
			if n.mergeInProgress(repo) {
				return nil, errNativeMergeInProgress
			}
			status, err := n.statusLocked(repo)
			if err != nil {
				return nil, err
			}
			if status.Dirty {
				return nil, fmt.Errorf("remote branch %s is checked out with unsaved changes; it cannot be updated by a push now", u.Branch)
			}
		}
	}
	out := &nativeReceiveResult{Revisions: count}
	for _, u := range updates {
		if u.Branch == current {
			if err := n.restoreRevisionLocked(repo, u.New, nil); err != nil {
				return nil, err
			}
		}
		entry := nativeRefLogEntry{Actor: strings.TrimSpace(actor), Action: "push", Message: "Push"}
		if err := n.moveRefLocked(repo, u.Branch, false, true, u.Old, u.New, entry); err != nil {
			return nil, err
		}
		out.Updated = append(out.Updated, u)
	}
	return out, nil
}

// ─── Transports ─────────────────────────────────────────────────────────

type nativeTransport interface {
	Refs() (*nativeRemoteRefs, error)
	Fetch(wants, haves []string) (*nativeBundle, error)
	Receive(b *nativeBundle, updates []nativeRefUpdate, actor string) (*nativeReceiveResult, error)
}

type nativeLocalTransport struct {
	n    *nativeVCS
	repo *Repo
}

func (t *nativeLocalTransport) Refs() (*nativeRemoteRefs, error) { return t.n.serveRefs(t.repo) }
func (t *nativeLocalTransport) Fetch(w, h []string) (*nativeBundle, error) {
	return t.n.serveFetch(t.repo, w, h)
}
func (t *nativeLocalTransport) Receive(b *nativeBundle, u []nativeRefUpdate, actor string) (*nativeReceiveResult, error) {
	return t.n.serveReceive(t.repo, b, u, actor)
}

type nativeHTTPTransport struct {
	base      string
	token     string
	projectID string
	client    *http.Client
}

func (t *nativeHTTPTransport) call(method, path string, in, out any) error {
	u := t.base + "/version/sync/" + path
	if t.projectID != "" {
		u += "?project_id=" + url.QueryEscape(t.projectID)
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	if t.projectID != "" {
		req.Header.Set("X-Apteva-Project-ID", t.projectID)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("remote unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, nativeBundleMaxBytes+(1<<20)))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := strings.TrimSpace(e.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("remote refused the credentials (HTTP %d); pass a valid token", resp.StatusCode)
		}
		return fmt.Errorf("remote error (HTTP %d): %s", resp.StatusCode, msg)
	}
	return json.Unmarshal(raw, out)
}

func (t *nativeHTTPTransport) Refs() (*nativeRemoteRefs, error) {
	var out nativeRemoteRefs
	return &out, t.call(http.MethodGet, "refs", nil, &out)
}
func (t *nativeHTTPTransport) Fetch(w, h []string) (*nativeBundle, error) {
	var out nativeBundle
	return &out, t.call(http.MethodPost, "fetch", map[string]any{"wants": w, "haves": h}, &out)
}
func (t *nativeHTTPTransport) Receive(b *nativeBundle, u []nativeRefUpdate, _ string) (*nativeReceiveResult, error) {
	var out nativeReceiveResult
	return &out, t.call(http.MethodPost, "receive", map[string]any{"bundle": b, "updates": u}, &out)
}

func newNativeHTTPTransport(remote *nativeRemote, token string) *nativeHTTPTransport {
	return &nativeHTTPTransport{base: remote.URL, token: strings.TrimSpace(token), projectID: remote.ProjectID, client: &http.Client{Timeout: 5 * time.Minute}}
}

// ─── Client side ────────────────────────────────────────────────────────

type nativeFetchResult struct {
	Remote    string                     `json:"remote"`
	Updated   map[string]nativeRefUpdate `json:"updated"`
	Revisions int                        `json:"revisions_received"`
	Branches  map[string]string          `json:"branches"`
}

func (n *nativeVCS) localHeadsLocked(repo *Repo, extra map[string]string) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] && n.hasRevision(repo, id) {
			seen[id] = true
			out = append(out, id)
		}
	}
	if refs, err := n.listRefs(repo, false); err == nil {
		for _, id := range refs {
			add(id)
		}
	}
	for _, id := range extra {
		add(id)
	}
	return out
}

func (n *nativeVCS) fetch(repo *Repo, name string, t nativeTransport) (*nativeFetchResult, error) {
	remote, err := n.getRemote(repo, name)
	if err != nil {
		return nil, err
	}
	refs, err := t.Refs()
	if err != nil {
		return nil, err
	}
	wants, haves := []string{}, []string(nil)
	func() {
		defer n.rlock(repo)()
		for _, m := range []map[string]string{refs.Branches, refs.Tags} {
			for _, id := range m {
				if id != "" && !n.hasRevision(repo, id) {
					wants = append(wants, id)
				}
			}
		}
		haves = n.localHeadsLocked(repo, remote.Branches)
	}()
	out := &nativeFetchResult{Remote: remote.Name, Updated: map[string]nativeRefUpdate{}, Branches: refs.Branches}
	var bundle *nativeBundle
	if len(wants) > 0 {
		if bundle, err = t.Fetch(wants, haves); err != nil {
			return nil, err
		}
	}
	defer n.lock(repo)()
	if out.Revisions, err = n.importBundleLocked(repo, bundle); err != nil {
		return nil, err
	}
	remotes, err := n.loadRemotesLocked(repo)
	if err != nil {
		return nil, err
	}
	cfg := remotes[remote.Name]
	if cfg == nil {
		return nil, fmt.Errorf("remote %s was removed during fetch", remote.Name)
	}
	for b, id := range refs.Branches {
		if !n.hasRevision(repo, id) {
			return nil, fmt.Errorf("remote branch %s was not received", b)
		}
		if cfg.Branches[b] != id {
			out.Updated[b] = nativeRefUpdate{Branch: b, Old: cfg.Branches[b], New: id}
		}
	}
	for b, id := range cfg.Branches {
		if _, ok := refs.Branches[b]; !ok {
			out.Updated[b] = nativeRefUpdate{Branch: b, Old: id}
		}
	}
	cfg.Branches, cfg.Tags, cfg.FetchedAt = refs.Branches, refs.Tags, n.timestamp()
	return out, n.saveRemotesLocked(repo, remotes)
}

type nativePushResult struct {
	Remote       string            `json:"remote"`
	Branch       string            `json:"branch"`
	RemoteBranch string            `json:"remote_branch"`
	Old          string            `json:"old,omitempty"`
	New          string            `json:"new"`
	Status       string            `json:"status"` // pushed, up_to_date
	Revisions    int               `json:"revisions_sent"`
	Updated      []nativeRefUpdate `json:"updated,omitempty"`
}

func (n *nativeVCS) push(repo *Repo, name, branch, remoteBranch, actor string, t nativeTransport) (*nativePushResult, error) {
	remote, err := n.getRemote(repo, name)
	if err != nil {
		return nil, err
	}
	var local string
	func() {
		defer n.rlock(repo)()
		if strings.TrimSpace(branch) == "" {
			branch, _ = n.currentBranch(repo)
		}
		local = n.readRef(repo, branch, false)
	}()
	if local == "" {
		return nil, fmt.Errorf("branch %s not found", branch)
	}
	if strings.TrimSpace(remoteBranch) == "" {
		remoteBranch = branch
	}
	if _, err := normaliseRefName(remoteBranch); err != nil {
		return nil, err
	}
	refs, err := t.Refs()
	if err != nil {
		return nil, err
	}
	old := refs.Branches[remoteBranch]
	out := &nativePushResult{Remote: remote.Name, Branch: branch, RemoteBranch: remoteBranch, Old: old, New: local}
	if old == local {
		out.Status = "up_to_date"
		return out, nil
	}
	var bundle *nativeBundle
	err = func() error {
		defer n.rlock(repo)()
		if old != "" {
			if !n.hasRevision(repo, old) {
				return fmt.Errorf("remote %s has revisions you have not fetched; fetch, merge %s/%s, then push", remote.Name, remote.Name, remoteBranch)
			}
			anc, err := n.ancestorsLocked(repo, local)
			if err != nil {
				return err
			}
			if !anc[old] {
				return fmt.Errorf("push to %s/%s is not a fast-forward; merge %s/%s into %s first", remote.Name, remoteBranch, remote.Name, remoteBranch, branch)
			}
		}
		haves := []string{}
		for _, id := range refs.Branches {
			if n.hasRevision(repo, id) {
				haves = append(haves, id)
			}
		}
		var e error
		bundle, e = n.bundleLocked(repo, []string{local}, haves)
		return e
	}()
	if err != nil {
		return nil, err
	}
	res, err := t.Receive(bundle, []nativeRefUpdate{{Branch: remoteBranch, Old: old, New: local}}, actor)
	if err != nil {
		return nil, err
	}
	out.Status, out.Revisions, out.Updated = "pushed", len(bundle.Revisions), res.Updated
	defer n.lock(repo)()
	if remotes, err := n.loadRemotesLocked(repo); err == nil && remotes[remote.Name] != nil {
		remotes[remote.Name].Branches[remoteBranch] = local
		_ = n.saveRemotesLocked(repo, remotes)
	}
	return out, nil
}

// cloneInto fills a brand-new repository (no native state yet) from a
// remote: every branch becomes a remote-tracking ref of "origin", local main
// starts at the remote main, and the working tree is checked out.
func (n *nativeVCS) cloneInto(repo *Repo, remote nativeRemote, t nativeTransport, actor string) (*nativeFetchResult, error) {
	refs, err := t.Refs()
	if err != nil {
		return nil, err
	}
	main := refs.Branches["main"]
	if main == "" {
		return nil, errors.New("the source repository has no main branch")
	}
	wants := []string{}
	for _, m := range []map[string]string{refs.Branches, refs.Tags} {
		for _, id := range m {
			wants = append(wants, id)
		}
	}
	bundle, err := t.Fetch(wants, nil)
	if err != nil {
		return nil, err
	}
	defer n.lock(repo)()
	if fileExists(n.refPath(repo, "main", false)) {
		return nil, errors.New("clone target already has history")
	}
	count, err := n.importBundleLocked(repo, bundle)
	if err != nil {
		return nil, err
	}
	for tag, id := range refs.Tags {
		if _, err := normaliseRefName(tag); err == nil && n.hasRevision(repo, id) {
			_ = n.moveRefLocked(repo, tag, true, true, "", id, nativeRefLogEntry{Actor: actor, Action: "clone"})
		}
	}
	if err := n.moveRefLocked(repo, "main", false, true, "", main, nativeRefLogEntry{Actor: actor, Action: "clone", Message: "Clone"}); err != nil {
		return nil, err
	}
	if err := atomicWrite(n.headPath(repo), []byte("main\n"), 0o600); err != nil {
		return nil, err
	}
	if err := atomicWrite(n.formatPath(repo), []byte(fmt.Sprintf("%d\n", nativeFormatVersion)), 0o600); err != nil {
		return nil, err
	}
	remote.At, remote.Branches, remote.Tags, remote.FetchedAt = n.timestamp(), refs.Branches, refs.Tags, n.timestamp()
	if err := n.saveRemotesLocked(repo, map[string]*nativeRemote{remote.Name: &remote}); err != nil {
		return nil, err
	}
	if err := n.restoreRevisionLocked(repo, main, nil); err != nil {
		return nil, err
	}
	return &nativeFetchResult{Remote: remote.Name, Revisions: count, Branches: refs.Branches, Updated: map[string]nativeRefUpdate{}}, nil
}
