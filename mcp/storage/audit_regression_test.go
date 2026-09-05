package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func auditCtx(t *testing.T, opts ...tk.Option) *sdk.AppCtx {
	opts = append(opts, tk.WithEnv("STORAGE_UPLOADS_DIR", t.TempDir()))
	return newTestCtx(t, opts...)
}
func shaText(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func agentSession(t *testing.T, ctx *sdk.AppCtx, folder string, size int64) string {
	t.Helper()
	out, err := (&App{}).toolUploadInitCtx(context.Background(), ctx, map[string]any{"name": "test.txt", "folder": folder, "size_bytes": size})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["upload_id"].(string)
}
func TestAuditPublicRouteDoesNotTrustUserIdentity(t *testing.T) {
	ctx := auditCtx(t)
	f := mustUpload(t, ctx, "secret.txt", "/secret/", "secret")
	for _, method := range []string{"GET", "HEAD"} {
		r := httptest.NewRequest(method, buildPublicContentURL(f), nil)
		r.Header.Set("X-User-ID", "999")
		w := httptest.NewRecorder()
		(&App{}).handlePublicFilesItem(w, r)
		if w.Code != 403 {
			t.Fatal(method, w.Code, w.Body.String())
		}
	}
}
func TestAuditCanonicalUploadAuthorization(t *testing.T) {
	ctx := auditCtx(t)
	c := withCaller(sdk.Grant{Effect: "allow", Permission: "files.write", Resource: "folder/allowed/**"})
	a := &App{}
	for _, folder := range []string{"allowed/../", "/allowed/../", "allowed/./x"} {
		if _, err := a.toolUploadCtx(c, ctx, map[string]any{"folder": folder, "name": "bad", "content_base64": b64("x")}); err == nil {
			t.Fatal("traversal accepted", folder)
		}
		if _, err := a.toolUploadInitCtx(c, ctx, map[string]any{"folder": folder, "name": "bad", "size_bytes": 1}); err == nil {
			t.Fatal("session traversal accepted")
		}
		if _, err := a.toolCreateFolderCtx(c, ctx, map[string]any{"path": folder}); err == nil {
			t.Fatal("folder traversal accepted")
		}
	}
	if _, err := a.toolUploadCtx(c, ctx, map[string]any{"folder": "/allowed/", "name": "ok", "content_base64": b64("x")}); err != nil {
		t.Fatal(err)
	}
}
func TestAuditAbortRejectsConflictingAliases(t *testing.T) {
	ctx := auditCtx(t)
	allowed := agentSession(t, ctx, "/allowed/", 1)
	secret := agentSession(t, ctx, "/secret/", 1)
	c := withCaller(sdk.Grant{Effect: "allow", Permission: "files.write", Resource: "folder/allowed/**"})
	if _, err := (&App{}).toolAbortUploadCtx(c, ctx, map[string]any{"id": secret, "upload_id": allowed}); err == nil {
		t.Fatal("aliases accepted")
	}
	for _, id := range []string{allowed, secret} {
		if _, err := os.Stat(uploadSessionDir(ctx, id)); err != nil {
			t.Fatal(err)
		}
	}
}
func TestAuditRenameAuthorizesAllDescendants(t *testing.T) {
	ctx := auditCtx(t)
	mustUpload(t, ctx, "safe", "/allowed/", "safe")
	f := mustUpload(t, ctx, "secret", "/allowed/secret/", "secret")
	c := withCaller(sdk.Grant{Effect: "allow", Permission: "files.write", Resource: "folder/**"}, sdk.Grant{Effect: "deny", Permission: "files.write", Resource: "folder/allowed/secret/**"})
	if _, err := (&App{}).toolRenameFolderCtx(c, ctx, map[string]any{"from": "/allowed/", "to": "/moved/"}); err == nil {
		t.Fatal("denied descendant moved")
	}
	got, _ := dbGetByID(ctx.AppDB(), "test-proj", f.ID)
	if got.Folder != f.Folder {
		t.Fatal(got)
	}
}
func TestAuditUnicodeAndLiteralFolderQueries(t *testing.T) {
	ctx := auditCtx(t)
	for _, name := range []string{"café", "📁", "日本", "a_b", "a%b"} {
		mustUpload(t, ctx, "child", "/"+name+"/sub/", name)
		moved, err := dbRenameFolder(ctx.AppDB(), "test-proj", "/"+name+"/", "/archive/"+name+"/")
		if err != nil || len(moved) != 1 || moved[0].Folder != "/archive/"+name+"/sub/" {
			t.Fatal(name, moved, err)
		}
	}
	mustUpload(t, ctx, "other", "/archive/axb/sub/", "other")
	for _, name := range []string{"a_b", "a%b"} {
		got, err := dbListFolder(ctx.AppDB(), "test-proj", "/archive/"+name+"/", true, 10)
		if err != nil || len(got) != 1 {
			t.Fatal(got, err)
		}
	}
}
func TestAuditFolderFiltersAndPagingCompose(t *testing.T) {
	ctx := auditCtx(t)
	for _, name := range []string{"a", "b", "c"} {
		mustUpload(t, ctx, name, "/", name)
	}
	w := httptest.NewRecorder()
	(&App{}).handleFilesCollection(w, httptest.NewRequest("GET", "/files?folder=/&q=absent&limit=1&offset=1", nil))
	var out struct {
		Files   []*File `json:"files"`
		HasMore bool    `json:"has_more"`
		Next    int     `json:"next_offset"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || len(out.Files) != 0 {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	(&App{}).handleFilesCollection(w, httptest.NewRequest("GET", "/files?folder=/&limit=1&offset=1", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Files) != 1 || out.Files[0].Name != "b" || !out.HasMore || out.Next != 2 {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	(&App{}).handleFilesCollection(w, httptest.NewRequest("GET", "/files?folder=/&limit=1&offset=2", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.HasMore {
		t.Fatal("last page claims more")
	}
}
func TestAuditTagSearchIsLiteral(t *testing.T) {
	ctx := auditCtx(t)
	a := mustUpload(t, ctx, "a", "/", "a")
	b := mustUpload(t, ctx, "b", "/", "b")
	_, _ = dbUpdate(ctx.AppDB(), "test-proj", a.ID, map[string]any{"tags": `["a_b"]`})
	_, _ = dbUpdate(ctx.AppDB(), "test-proj", b.ID, map[string]any{"tags": `["axb"]`})
	rows, err := dbSearch(ctx.AppDB(), "test-proj", searchOpts{Tag: "a_b"})
	if err != nil || len(rows) != 1 || rows[0].ID != a.ID {
		t.Fatal(rows, err)
	}
}
func TestAuditMCPPartsEnforceBudgetBeforePersisting(t *testing.T) {
	ctx := auditCtx(t)
	id := agentSession(t, ctx, "/", 1)
	a := &App{}
	for _, raw := range []string{b64("oversized"), strings.Repeat("A", int(mcpUploadPartSize*2))} {
		if _, err := a.toolUploadPartCtx(context.Background(), ctx, map[string]any{"upload_id": id, "part_number": 1, "content_base64": raw}); err == nil {
			t.Fatal("oversize accepted")
		}
	}
	parts, total, err := uploadSessionPartsStatus(ctx, id)
	if err != nil || len(parts) != 0 || total != 0 {
		t.Fatal(parts, total, err)
	}
}
func TestAuditParallelPartsAndCompletionRetry(t *testing.T) {
	ctx := auditCtx(t)
	id := agentSession(t, ctx, "/", 8)
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for n := 1; n <= 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			reader := &gatedReader{ready: ready, release: release, r: strings.NewReader("safe")}
			_, err := writeUploadPart(context.Background(), ctx, id, n, reader, 4, nil)
			errs <- err
		}(n)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			close(release)
			wg.Wait()
			t.Fatal("parts are serialized")
		}
	}
	close(release)
	wg.Wait()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	a := &App{}
	args := map[string]any{"upload_id": id, "sha256": shaText("safesafe")}
	first, err := a.toolUploadCompleteCtx(context.Background(), ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.toolUploadCompleteCtx(context.Background(), ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["file"].(*File).ID != second.(map[string]any)["file"].(*File).ID {
		t.Fatal("retry created a different file")
	}
	if err = writeUploadPartBytes(ctx, id, 1, []byte("evil")); err == nil {
		t.Fatal("completed session still writable")
	}
}

type gatedReader struct {
	once    sync.Once
	ready   chan struct{}
	release chan struct{}
	r       io.Reader
}

func (r *gatedReader) Read(p []byte) (int, error) {
	r.once.Do(func() { r.ready <- struct{}{}; <-r.release })
	return r.r.Read(p)
}
func TestAuditCompletingSnapshotIsImmutable(t *testing.T) {
	ctx := auditCtx(t)
	id := agentSession(t, ctx, "/", 4)
	if err := writeUploadPartBytes(ctx, id, 1, []byte("safe")); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	globalBackend = &hookBackend{Backend: backend(), put: func() { close(entered); <-release }}
	done := make(chan error, 1)
	go func() {
		_, err := completeUploadSessionForTool(ctx, context.Background(), id, shaText("safe"))
		done <- err
	}()
	<-entered
	replaced := make(chan error, 1)
	go func() { replaced <- writeUploadPartBytes(ctx, id, 1, []byte("evil")) }()
	select {
	case <-replaced:
		t.Fatal("writer entered completion snapshot")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-replaced; err == nil {
		t.Fatal("late replacement accepted")
	}
	f, _, err := completedUpload(ctx, id, "test-proj")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := backend().LocalPath(objectKey(f.SHA256, f.StorageKey))
	body, _ := os.ReadFile(p)
	if string(body) != "safe" || f.SHA256 != shaText("safe") {
		t.Fatal(f, string(body))
	}
}

type hookBackend struct {
	Backend
	put       func()
	deleteErr error
}

func (b *hookBackend) Put(c context.Context, k, ct string, r io.Reader, n int64) error {
	if b.put != nil {
		b.put()
	}
	return b.Backend.Put(c, k, ct, r, n)
}
func (b *hookBackend) Delete(c context.Context, k string) error {
	if b.deleteErr != nil {
		return b.deleteErr
	}
	return b.Backend.Delete(c, k)
}
func TestAuditDirectFinalizeAtomicRetryAndImmutableKey(t *testing.T) {
	ctx := auditCtx(t)
	stub := newFakeS3()
	globalBackend = stub
	a := &App{}
	w := httptest.NewRecorder()
	a.handleDirectInit(w, httptest.NewRequest("POST", "/files/init", strings.NewReader(`{"name":"x.txt","size_bytes":3,"sha256":"`+shaText("abc")+`"}`)))
	var init map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &init)
	id, ok := init["upload_id"].(string)
	if !ok {
		t.Fatal(w.Body.String())
	}
	var sk, sha string
	_ = ctx.AppDB().QueryRow(`SELECT storage_key,declared_sha256 FROM pending_uploads WHERE upload_id=?`, id).Scan(&sk, &sha)
	key := objectKey(sha, sk)
	stub.objects[key] = []byte("abc")
	_, err := ctx.AppDB().Exec(`CREATE TRIGGER fail_cleanup BEFORE DELETE ON pending_uploads BEGIN SELECT RAISE(FAIL,'transient'); END`)
	if err != nil {
		t.Fatal(err)
	}
	finalize := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.handleDirectFinalize(w, httptest.NewRequest("POST", "/files/"+id+"/finalize", strings.NewReader(`{}`)), id)
		return w
	}
	if w = finalize(); w.Code == 200 {
		t.Fatal("committed despite failed session transaction")
	}
	var count int
	_ = ctx.AppDB().QueryRow(`SELECT count(*) FROM files`).Scan(&count)
	if count != 0 {
		t.Fatal("partial commit")
	}
	_, _ = ctx.AppDB().Exec(`DROP TRIGGER fail_cleanup`)
	// The client retries its temporary PUT after the failed finalize.
	stub.objects[key] = []byte("abc")
	if w = finalize(); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	f, _, err := completedUpload(ctx, id, "test-proj")
	if err != nil || f == nil {
		t.Fatal(err)
	}
	finalKey := objectKey(f.SHA256, f.StorageKey)
	if finalKey == key {
		t.Fatal("published key remains client-writable")
	}
	stub.objects[key] = []byte("bad")
	if w = finalize(); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if string(stub.objects[finalKey]) != "abc" {
		t.Fatal("published bytes changed")
	}
}
func TestAuditImportVisibilityAndNetworkPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "fixture") }))
	defer server.Close()
	ctx := auditCtx(t, tk.WithConfig(map[string]string{"default_visibility": "public"}))
	if _, err := (&App{}).toolFromURL(ctx, map[string]any{"url": server.URL}); err == nil {
		t.Fatal("loopback import accepted")
	}
	ctx = auditCtx(t, tk.WithConfig(map[string]string{"default_visibility": "public", "import_internal_hosts": "127.0.0.1"}))
	out, err := (&App{}).toolFromURL(ctx, map[string]any{"url": server.URL + "/x.txt", "visibility": "private"})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := dbGetByID(ctx.AppDB(), "test-proj", out.(map[string]any)["id"].(int64))
	if f.Visibility != "private" {
		t.Fatal(f)
	}
	for _, ip := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.0.0.1", "100.64.0.1", "::ffff:127.0.0.1"} {
		if publicImportIP(netip.MustParseAddr(ip)) {
			t.Fatal("private IP allowed", ip)
		}
	}
}
func TestAuditBackendDiscoveryFailsClosed(t *testing.T) {
	ctx := auditCtx(t, tk.WithPlatform(tk.BasePlatformClient{}))
	if _, err := initBackend(ctx); err == nil {
		t.Fatal("discovery failure selected disk")
	}
}
func TestAuditPrivateRevokesSharesAndTokenRotationPreservesCurrentShares(t *testing.T) {
	ctx := auditCtx(t)
	f := mustUpload(t, ctx, "x", "/", "x")
	exp := time.Now().Add(time.Hour).Unix()
	sig := signFile(f.ID, exp)
	proxy := signProxyFile(f.ID, f.ProjectID, exp, DispositionInline)
	t.Setenv("APTEVA_APP_TOKEN", "rotated-test-token")
	if !verifySignature(f.ID, exp, sig) {
		t.Fatal("token rotation invalidated persisted signing key")
	}
	_, err := dbUpdate(ctx.AppDB(), "test-proj", f.ID, map[string]any{"visibility": "private"})
	if err != nil {
		t.Fatal(err)
	}
	if verifySignature(f.ID, exp, sig) || verifyProxySignature(f.ID, f.ProjectID, exp, DispositionInline, proxy) {
		t.Fatal("old share survived revocation")
	}
}
func TestAuditURLsCarryBothScopes(t *testing.T) {
	ctx := auditCtx(t)
	t.Setenv("APTEVA_INSTALL_ID", "42")
	f := mustUpload(t, ctx, "x", "/", "x")
	for _, vis := range []string{"private", "public", "signed"} {
		f.Visibility = vis
		for _, raw := range []string{absoluteContentURL(ctx, f), signedAbsoluteURL(ctx, f, "sig", 1), signedAbsoluteProxyURL(ctx, f, "sig", 1, DispositionInline)} {
			u, e := url.Parse(raw)
			if e != nil || u.Query().Get("project_id") != "test-proj" || u.Query().Get("install_id") != "42" {
				t.Fatal(raw, e)
			}
		}
	}
}
func TestAuditSoftDeletePurgeAndDurableCleanup(t *testing.T) {
	ctx := auditCtx(t)
	f := mustUpload(t, ctx, "x", "/", "x")
	_, _ = deleteFile(ctx, "test-proj", f.ID, true)
	denied := withCaller(sdk.Grant{Effect: "allow", Permission: "files.delete", Resource: "folder/elsewhere/**"})
	if _, err := (&App{}).toolDeleteCtx(denied, ctx, map[string]any{"id": f.ID}); err == nil {
		t.Fatal("tombstone purge bypassed scope")
	}
	original := backend()
	globalBackend = &hookBackend{Backend: original, deleteErr: errors.New("offline")}
	hard, err := deleteFile(ctx, "test-proj", f.ID, false)
	if err != nil || hard {
		t.Fatal(hard, err)
	}
	var queued int
	_ = ctx.AppDB().QueryRow(`SELECT count(*) FROM blob_cleanup`).Scan(&queued)
	if queued != 1 {
		t.Fatal("missing durable cleanup")
	}
	globalBackend = original
	sweepBlobCleanup(ctx)
	if _, err = backend().Stat(context.Background(), objectKey(f.SHA256, f.StorageKey)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestAuditInsertFailureReclaimsBlob(t *testing.T) {
	ctx := auditCtx(t)
	_, _ = ctx.AppDB().Exec(`CREATE TRIGGER fail_insert BEFORE INSERT ON files BEGIN SELECT RAISE(FAIL,'db failed'); END`)
	stub := newFakeS3()
	globalBackend = stub
	if _, _, err := saveBytes(ctx, "test-proj", uploadInput{Name: "x", Folder: "/"}, []byte("x")); err == nil {
		t.Fatal("insert succeeded")
	}
	if len(stub.objects) != 0 {
		t.Fatal("orphan blob", stub.objects)
	}
}
func TestAuditProxyRechecksFinalMIME(t *testing.T) {
	ctx, f, stub := newRemoteFile(t, "video/mp4", "private")
	stub.metadata = &ObjectMetadata{ContentType: "text/html"}
	path := mintProxyPath(t, ctx, f, DispositionInline, 60)
	for _, method := range []string{"GET", "HEAD"} {
		w := httptest.NewRecorder()
		(&App{}).handlePublicFilesItem(w, httptest.NewRequest(method, path, nil))
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
			t.Fatal(method, w.Code, w.Header())
		}
	}
}
func TestAuditNullJSONAndChunkedMint(t *testing.T) {
	ctx := auditCtx(t)
	f := mustUpload(t, ctx, "x", "/", "x")
	a := &App{}
	for _, path := range []string{"/folders", "/files/from-url", fmt.Sprintf("/files/%d/url", f.ID)} {
		r := httptest.NewRequest("POST", path, strings.NewReader("null"))
		w := httptest.NewRecorder()
		if path == "/folders" {
			r.Method = "PATCH"
			a.httpRenameFolder(w, r)
		} else if path == "/files/from-url" {
			a.httpFromURL(w, r)
		} else {
			a.httpMintSignedURL(w, r, f.ID)
		}
		if w.Code != 400 {
			t.Fatal(path, w.Code)
		}
	}
	r := httptest.NewRequest("POST", fmt.Sprintf("/files/%d/url", f.ID), strings.NewReader(`{"ttl_seconds":999999999999,"disposition":"attachment"}`))
	r.ContentLength = -1
	w := httptest.NewRecorder()
	a.httpMintSignedURL(w, r, f.ID)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out["disposition"] != "attachment" || int64(out["expires_at"].(float64)) > time.Now().Add(7*24*time.Hour+time.Second).Unix() {
		t.Fatal(w.Body.String())
	}
}
func TestAuditUploadQuotaAndProjectIsolation(t *testing.T) {
	ctx := auditCtx(t, tk.WithConfig(map[string]string{"max_upload_sessions": "1"}))
	id := agentSession(t, ctx, "/", 1)
	if _, err := (&App{}).toolUploadInitCtx(context.Background(), ctx, map[string]any{"name": "second", "size_bytes": 1}); err == nil {
		t.Fatal("session quota bypassed")
	}
	t.Setenv("APTEVA_PROJECT_ID", "")
	r := httptest.NewRequest("PUT", "/uploads/"+id+"/parts/1?project_id=other", bytes.NewReader([]byte("x")))
	w := httptest.NewRecorder()
	(&App{}).handleUploadPart(w, r, id, 1)
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
}
