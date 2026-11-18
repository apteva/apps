package main

// Tier 1 coverage for Code 0.18 native remotes: clone, fetch + merge (pull),
// fast-forward-only push with its refusals, bundle verification against
// tampering, minimal transfers, remote-tracking refs as GC roots, format-1
// history, and the HTTP transport's wire format.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// twoRepos returns one nativeVCS holding repositories a (with history) and b
// (empty, not yet initialised), sharing a store like a real install.
func twoRepos(t *testing.T) (*nativeVCS, *LocalFileStore, *Repo, *Repo) {
	t.Helper()
	root := t.TempDir()
	store := NewLocalFileStore(filepath.Join(root, "repos"))
	a := &Repo{ID: 1, ProjectID: "p", Slug: "a", Name: "A"}
	b := &Repo{ID: 2, ProjectID: "p", Slug: "b", Name: "B"}
	for _, r := range []*Repo{a, b} {
		if err := store.CreateRepo(repoStoreKey(r)); err != nil {
			t.Fatal(err)
		}
	}
	n := newNativeVCS(filepath.Join(root, "data"), store, newRepoLockSet())
	put(t, store, a, "app.txt", "one\ntwo\nthree\n")
	put(t, store, a, "lib/util.txt", "util\n")
	mustCheckpoint(t, n, a, "A base")
	return n, store, a, b
}

func local(n *nativeVCS, r *Repo) nativeTransport { return &nativeLocalTransport{n: n, repo: r} }

func TestNativeRemote_CloneFetchMergePush(t *testing.T) {
	n, store, a, b := twoRepos(t)
	if _, err := n.setProtection(a, "main", 0, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := n.createBranchRef(a, "feature", "", "t"); err != nil {
		t.Fatal(err)
	}
	if err := n.createTag(a, "v1", ""); err != nil {
		t.Fatal(err)
	}
	res, err := n.cloneInto(b, nativeRemote{Name: "origin", Kind: "local", Slug: "a"}, local(n, a), "cloner")
	if err != nil || res.Revisions < 1 {
		t.Fatalf("clone %+v %v", res, err)
	}
	if n.readRef(b, "main", false) != n.readRef(a, "main", false) || readWorking(t, store, b, "lib/util.txt") != "util\n" {
		t.Fatal("clone did not reproduce main and its files")
	}
	if st, _ := n.status(b); st.Dirty || st.Branch != "main" {
		t.Fatalf("clone status %+v", st)
	}
	if tags, _ := n.tags(b); tags["v1"] == "" {
		t.Fatal("tags not cloned")
	}
	// The remote branch is addressable as origin/feature.
	if cmp, err := n.compareRefs(b, "origin/feature", "main"); err != nil || cmp.Ahead != 0 {
		t.Fatalf("tracking ref compare %+v %v", cmp, err)
	}

	// A moves on: fetch in B then merge origin/main (pull).
	put(t, store, a, "app.txt", "ONE\ntwo\nthree\n")
	n.setProtection(a, "main", -1, "owner")
	aHead := mustCheckpoint(t, n, a, "A edits line 1")
	n.setProtection(a, "main", 0, "owner")
	f, err := n.fetch(b, "origin", local(n, a))
	if err != nil || f.Revisions != 1 || f.Updated["main"].New != aHead.ID {
		t.Fatalf("fetch %+v %v", f, err)
	}
	if again, _ := n.fetch(b, "origin", local(n, a)); again.Revisions != 0 || len(again.Updated) != 0 {
		t.Fatalf("second fetch should be empty: %+v", again)
	}
	put(t, store, b, "app.txt", "one\ntwo\nTHREE\n")
	mustCheckpoint(t, n, b, "B edits line 3")
	m, err := n.mergeStart(b, nativeMergeOptions{Source: "origin/main"})
	if err != nil || m.Status != "merged" || readWorking(t, store, b, "app.txt") != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("pull merge %+v %v", m, err)
	}

	// Pushing to protected main is refused by the remote; a branch is fine.
	if _, err := n.push(b, "origin", "main", "", "pusher", local(n, a)); !errors.Is(err, errNativeProtected) {
		t.Fatalf("push to protected main: %v", err)
	}
	p, err := n.push(b, "origin", "main", "from-b", "pusher", local(n, a))
	if err != nil || p.Status != "pushed" || n.readRef(a, "from-b", false) != n.readRef(b, "main", false) {
		t.Fatalf("push new branch %+v %v", p, err)
	}
	if again, _ := n.push(b, "origin", "main", "from-b", "pusher", local(n, a)); again.Status != "up_to_date" {
		t.Fatalf("repeat push %+v", again)
	}
	_, moves, _ := n.refMoves(a, "from-b", 1)
	if moves[0].Action != "push" || moves[0].Actor != "pusher" {
		t.Fatalf("remote log %+v", moves[0])
	}
	// Non-fast-forward: A moves from-b independently, B pushes again.
	if _, err := n.switchBranch(a, "from-b"); err != nil {
		t.Fatal(err)
	}
	put(t, store, a, "a-only.txt", "x\n")
	mustCheckpoint(t, n, a, "A on from-b")
	put(t, store, b, "b-only.txt", "y\n")
	mustCheckpoint(t, n, b, "B more")
	if _, err := n.push(b, "origin", "main", "from-b", "pusher", local(n, a)); err == nil || !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("non-fast-forward push: %v", err)
	}
	if _, err := n.fetch(b, "origin", local(n, a)); err != nil {
		t.Fatal(err)
	}
	if _, err := n.push(b, "origin", "main", "from-b", "pusher", local(n, a)); err == nil || !strings.Contains(err.Error(), "fast-forward") {
		t.Fatalf("diverged push: %v", err)
	}
	if _, err := n.mergeStart(b, nativeMergeOptions{Source: "origin/from-b"}); err != nil {
		t.Fatal(err)
	}
	// from-b is checked out in A: a dirty tree refuses, a clean one is updated.
	put(t, store, a, "dirty.txt", "unsaved\n")
	if _, err := n.push(b, "origin", "main", "from-b", "pusher", local(n, a)); err == nil || !strings.Contains(err.Error(), "unsaved") {
		t.Fatalf("push into dirty checked-out branch: %v", err)
	}
	if err := store.Delete(repoStoreKey(a), "dirty.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := n.push(b, "origin", "main", "from-b", "pusher", local(n, a)); err != nil {
		t.Fatal(err)
	}
	if readWorking(t, store, a, "b-only.txt") != "y\n" {
		t.Fatal("checked-out remote branch working tree not updated")
	}
	if st, _ := n.status(a); st.Dirty {
		t.Fatal("remote left dirty after push")
	}

	// Remote-tracking refs keep fetched history alive through GC.
	n.now = func() time.Time { return time.Now().Add(30 * 24 * time.Hour) }
	defer func() { n.now = time.Now }()
	if _, err := n.gc(b, nativeGCGrace); err != nil {
		t.Fatal(err)
	}
	if _, err := n.compareRefs(b, "origin/feature", "main"); err != nil {
		t.Fatalf("tracking ref lost after gc: %v", err)
	}
}

func TestNativeRemote_BundleVerificationRejectsTampering(t *testing.T) {
	n, store, a, b := twoRepos(t)
	if err := n.ensureRepo(b); err != nil {
		t.Fatal(err)
	}
	head := n.readRef(a, "main", false)
	fresh := func() *nativeBundle {
		bundle, err := n.serveFetch(a, []string{head}, nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(bundle)
		var copy nativeBundle
		_ = json.Unmarshal(raw, &copy)
		return &copy
	}
	importInto := func(bundle *nativeBundle) error {
		defer n.lock(b)()
		_, err := n.importBundleLocked(b, bundle)
		return err
	}
	cases := map[string]func(*nativeBundle){
		"blob content": func(x *nativeBundle) {
			for h := range x.Blobs {
				x.Blobs[h] = base64.StdEncoding.EncodeToString([]byte("evil"))
			}
		},
		"tree entry": func(x *nativeBundle) {
			for h := range x.Trees {
				x.Trees[h][0].Name = "renamed"
			}
		},
		"revision message": func(x *nativeBundle) { x.Revisions[0].Message = "forged" },
		"revision tree": func(x *nativeBundle) {
			x.Revisions[0].Tree = strings.Repeat("0", 64)
		},
		"missing parent": func(x *nativeBundle) {
			if len(x.Revisions) > 1 {
				x.Revisions = x.Revisions[:1]
			}
			x.Revisions[0].Parents = []string{strings.Repeat("a", 64)}
		},
		"path traversal": func(x *nativeBundle) {
			for h, children := range x.Trees {
				children[0].Name = ".."
				delete(x.Trees, h)
				x.Trees[folderTreeHash(children)] = children
			}
		},
	}
	for name, mutate := range cases {
		bundle := fresh()
		mutate(bundle)
		if err := importInto(bundle); err == nil {
			t.Fatalf("%s: tampered bundle accepted", name)
		}
	}
	if err := importInto(fresh()); err != nil {
		t.Fatalf("clean bundle rejected: %v", err)
	}
	_ = store
}

func TestNativeRemote_TransfersOnlyWhatIsMissing(t *testing.T) {
	n, store, a, b := twoRepos(t)
	if _, err := n.cloneInto(b, nativeRemote{Name: "origin", Kind: "local", Slug: "a"}, local(n, a), "c"); err != nil {
		t.Fatal(err)
	}
	put(t, store, a, "app.txt", "changed\n")
	head := mustCheckpoint(t, n, a, "one file changed")
	bundle, err := n.serveFetch(a, []string{head.ID}, []string{n.readRef(b, "main", false)})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Revisions) != 1 || len(bundle.Blobs) != 1 {
		t.Fatalf("incremental bundle: %d revisions, %d blobs", len(bundle.Revisions), len(bundle.Blobs))
	}
	for hash := range bundle.Trees {
		if children, _ := n.loadFolderTree(a, hash); len(children) > 0 && children[0].Name == "util.txt" {
			t.Fatal("unchanged lib/ folder tree was sent again")
		}
	}
}

func TestNativeRemote_Format1HistoryTravels(t *testing.T) {
	root := t.TempDir()
	store := NewLocalFileStore(filepath.Join(root, "repos"))
	a := &Repo{ID: 7, ProjectID: "p", Slug: "old", Name: "Old"}
	b := &Repo{ID: 8, ProjectID: "p", Slug: "new", Name: "New"}
	for _, r := range []*Repo{a, b} {
		if err := store.CreateRepo(repoStoreKey(r)); err != nil {
			t.Fatal(err)
		}
	}
	n := newNativeVCS(filepath.Join(root, "data"), store, newRepoLockSet())
	ids := writeFormat1Repo(t, n, a, []map[string]string{{"x.txt": "v1\n"}, {"x.txt": "v2\n", "y.txt": "y\n"}})
	put(t, store, a, "x.txt", "v2\n")
	put(t, store, a, "y.txt", "y\n")
	if _, err := n.cloneInto(b, nativeRemote{Name: "origin", Kind: "local", Slug: "old"}, local(n, a), "c"); err != nil {
		t.Fatal(err)
	}
	if n.readRef(b, "main", false) != ids[1] || readWorking(t, store, b, "y.txt") != "y\n" {
		t.Fatal("format-1 history not cloned")
	}
	hist, err := n.history(b, 0)
	if err != nil || len(hist) != 2 {
		t.Fatalf("history %d %v", len(hist), err)
	}
}

func TestNativeRemote_HTTPTransportWireFormat(t *testing.T) {
	n, store, a, b := twoRepos(t)
	var sawAuth, sawProject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth, sawProject = r.Header.Get("Authorization"), r.URL.Query().Get("project_id")
		if !strings.HasPrefix(r.URL.Path, "/api/apps/code/api/repos/a/version/sync/") {
			http.NotFound(w, r)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/api/apps/code/api/repos/a/version/sync/") {
		case "refs":
			refs, _ := n.serveRefs(a)
			_ = json.NewEncoder(w).Encode(refs)
		case "fetch":
			var body struct{ Wants, Haves []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			bundle, err := n.serveFetch(a, body.Wants, body.Haves)
			if err != nil {
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(bundle)
		case "receive":
			var body struct {
				Bundle  *nativeBundle
				Updates []nativeRefUpdate
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			res, err := n.serveReceive(a, body.Bundle, body.Updates, "http")
			if err != nil {
				w.WriteHeader(409)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(res)
		}
	}))
	defer srv.Close()
	base, err := validateAptevaRemoteURL(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/api/apps/code/api/repos/a")
	if err != nil {
		t.Fatal(err)
	}
	remote := &nativeRemote{Name: "origin", Kind: "apteva", URL: base, ProjectID: "proj-1"}
	tr := newNativeHTTPTransport(remote, "sk-test")
	if _, err := n.cloneInto(b, *remote, tr, "c"); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer sk-test" || sawProject != "proj-1" {
		t.Fatalf("auth %q project %q", sawAuth, sawProject)
	}
	put(t, store, b, "from-http.txt", "hi\n")
	mustCheckpoint(t, n, b, "over http")
	if _, err := n.push(b, "origin", "main", "via-http", "c", tr); err != nil {
		t.Fatal(err)
	}
	if n.readRef(a, "via-http", false) != n.readRef(b, "main", false) {
		t.Fatal("http push did not land")
	}
	if _, err := n.push(b, "origin", "main", "main", "c", tr); err != nil {
		t.Fatalf("fast-forward main over http: %v", err)
	}
	for _, bad := range []string{"http://example.com/api/apps/code/api/repos/a", "https://user:pw@host/api/repos/a", "https://host/api/repos/a?x=1", "https://host/elsewhere", "ftp://host/repos/a"} {
		if _, err := validateAptevaRemoteURL(bad); err == nil {
			t.Fatalf("accepted remote url %q", bad)
		}
	}
	_ = os.Getenv
}
