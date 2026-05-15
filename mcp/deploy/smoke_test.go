package main

import (
	"database/sql"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Smoke test: bring up the schema in an in-memory DB, run the
// store helpers end-to-end so refactors break loudly.
func TestStoreSmoke(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()

	d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "api", SourceKind: "local", SourceRef: "/tmp/src", Framework: "go",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if d.Name != "api" {
		t.Fatalf("name = %q", d.Name)
	}

	// Duplicate name is rejected by UNIQUE.
	if _, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "api", SourceKind: "local", SourceRef: "/tmp/src",
	}); err == nil {
		t.Fatalf("expected duplicate name to fail")
	}

	got, err := dbGetDeploymentByName(db, "p1", "api")
	if err != nil || got == nil || got.ID != d.ID {
		t.Fatalf("get by name: %v %+v", err, got)
	}

	// Build → release flow.
	b, err := dbCreateBuild(db, d.ID, "go", "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := dbUpdateBuild(db, b.ID, map[string]any{
		"status": "succeeded", "artifact_path": "/data/b1/dist", "artifact_size": int64(1234),
	}); err != nil {
		t.Fatalf("update build: %v", err)
	}
	rel, err := dbCreateRelease(db, d.ID, b.ID)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := dbSetCurrentRelease(db, d.ID, &rel.ID); err != nil {
		t.Fatalf("set current: %v", err)
	}
	again, _ := dbGetDeployment(db, "p1", d.ID)
	if again.CurrentReleaseID == nil || *again.CurrentReleaseID != rel.ID {
		t.Fatalf("current_release_id = %v", again.CurrentReleaseID)
	}

	// Port lease semantics.
	if ok, _ := dbAcquirePortLease(db, 7000, rel.ID); !ok {
		t.Fatal("first lease should succeed")
	}
	if ok, _ := dbAcquirePortLease(db, 7000, rel.ID); ok {
		t.Fatal("second lease on same port should fail")
	}
	held, _ := dbHeldPorts(db)
	if !held[7000] {
		t.Fatal("held should include 7000")
	}
	if err := dbReleasePortLease(db, 7000); err != nil {
		t.Fatalf("release lease: %v", err)
	}

	// Cascade: deleting the deployment wipes builds + releases.
	if err := dbDeleteDeployment(db, "p1", d.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rows, _ := dbListBuilds(db, d.ID, 10)
	if len(rows) != 0 {
		t.Fatalf("builds should cascade, got %d", len(rows))
	}
}

func TestDeploymentDomainLink(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()

	d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "api", SourceKind: "local", SourceRef: "/tmp/src",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if d.Domain != "" || d.DomainRecordID != "" || d.DomainAttachedAt != "" {
		t.Fatalf("expected empty domain fields, got %+v", d)
	}

	if err := dbSetDeploymentDomain(db, d.ID, "app.acme.com", "acme.com|CNAME", nowUTC()); err != nil {
		t.Fatalf("set domain: %v", err)
	}
	got, _ := dbGetDeployment(db, "p1", d.ID)
	if got.Domain != "app.acme.com" || got.DomainRecordID != "acme.com|CNAME" || got.DomainAttachedAt == "" {
		t.Fatalf("attached row mismatched: %+v", got)
	}

	apex, rtype, ok := splitRecordID(got.DomainRecordID)
	if !ok || apex != "acme.com" || rtype != "CNAME" {
		t.Fatalf("splitRecordID: ok=%v apex=%q rtype=%q", ok, apex, rtype)
	}

	if err := dbSetDeploymentDomain(db, d.ID, "", "", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = dbGetDeployment(db, "p1", d.ID)
	if got.Domain != "" || got.DomainRecordID != "" || got.DomainAttachedAt != "" {
		t.Fatalf("expected cleared, got %+v", got)
	}
}

func TestValidateName(t *testing.T) {
	cases := map[string]bool{
		"":         false,
		"ok":       true,
		"a-b_c123": true,
		"BAD":      false, // uppercase rejected
		"has space": false,
		"slash/x":  false,
	}
	for in, want := range cases {
		err := validateName(in)
		if (err == nil) != want {
			t.Errorf("validateName(%q): err=%v, want ok=%v", in, err, want)
		}
	}
}

func TestTailLines(t *testing.T) {
	body := "a\nb\nc\nd\ne\n"
	if got := tailLines(body, 2); got != "d\ne\n" {
		t.Errorf("tailLines(2) = %q", got)
	}
	if got := tailLines(body, 100); got != body {
		t.Errorf("tailLines(100) = %q", got)
	}
}

func TestDetectFramework(t *testing.T) {
	tmp := t.TempDir()
	if detectFramework(tmp) != "" {
		t.Fatal("empty dir should detect nothing")
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectFramework(tmp); got != "go" {
		t.Fatalf("detectFramework after go.mod = %q", got)
	}
}

// TestDetectFramework_Node guards the package.json → "node" branch
// that v0.3.0 turned from a stub into a real builder. Also covers
// static (index.html) and python (requirements.txt) so refactors of
// the detect order can't silently regress.
func TestDetectFramework_Node(t *testing.T) {
	cases := []struct {
		file string
		body string
		want string
	}{
		{"package.json", `{"name":"x"}`, "node"},
		{"index.html", "<html></html>", "static"},
		{"requirements.txt", "flask\n", "python"},
		{"pyproject.toml", "[project]\n", "python"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.file, func(t *testing.T) {
			tmp := t.TempDir()
			if err := os.WriteFile(filepath.Join(tmp, tc.file), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := detectFramework(tmp); got != tc.want {
				t.Fatalf("detectFramework(%s) = %q, want %q", tc.file, got, tc.want)
			}
		})
	}
}

// TestDetectPackageManager pins the lockfile precedence: bun.lockb
// outranks pnpm-lock.yaml outranks yarn.lock outranks npm-default.
// If this order shifts, both builder and runtime would silently pick
// different tools and `<pm> install` could collide with the wrong
// lockfile.
func TestDetectPackageManager(t *testing.T) {
	cases := []struct {
		name      string
		lockfiles []string
		want      string
	}{
		{"empty dir → npm fallback", nil, "npm"},
		{"bun lockfile → bun", []string{"bun.lockb"}, "bun"},
		{"pnpm lockfile → pnpm", []string{"pnpm-lock.yaml"}, "pnpm"},
		{"yarn lockfile → yarn", []string{"yarn.lock"}, "yarn"},
		{"npm lockfile → npm", []string{"package-lock.json"}, "npm"},
		{"bun wins over pnpm", []string{"pnpm-lock.yaml", "bun.lockb"}, "bun"},
		{"pnpm wins over yarn", []string{"yarn.lock", "pnpm-lock.yaml"}, "pnpm"},
		{"yarn wins over package-lock", []string{"package-lock.json", "yarn.lock"}, "yarn"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			for _, f := range tc.lockfiles {
				if err := os.WriteFile(filepath.Join(tmp, f), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := detectPackageManager(tmp); got != tc.want {
				t.Fatalf("detectPackageManager() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHasNpmScript ensures the build / start auto-detect logic only
// fires when the script is actually declared. Missing package.json,
// invalid JSON, and absent script must all return false (otherwise
// the runtime would try to exec `<pm> run start` against nothing).
func TestHasNpmScript(t *testing.T) {
	t.Run("missing package.json", func(t *testing.T) {
		if hasNpmScript(t.TempDir(), "build") {
			t.Fatal("missing package.json should report no script")
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmp, "package.json"), []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if hasNpmScript(tmp, "build") {
			t.Fatal("invalid package.json should report no script")
		}
	})
	t.Run("script absent", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmp, "package.json"), []byte(`{"scripts":{"test":"x"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if hasNpmScript(tmp, "build") {
			t.Fatal("absent script should report false")
		}
	})
	t.Run("script present", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmp, "package.json"), []byte(`{"scripts":{"build":"next build","start":"next start"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if !hasNpmScript(tmp, "build") || !hasNpmScript(tmp, "start") {
			t.Fatal("declared scripts should report true")
		}
	})
}

// TestBuilderFor pins the framework → builder dispatch. The error
// message lists the supported set; the test asserts the new "node"
// case is wired in alongside the existing ones.
func TestBuilderFor(t *testing.T) {
	cases := map[string]string{
		"go":     "go",
		"node":   "node",
		"static": "static",
		"blank":  "blank",
	}
	for input, wantFramework := range cases {
		b, err := builderFor(input)
		if err != nil {
			t.Errorf("builderFor(%q) err = %v", input, err)
			continue
		}
		if b.Framework() != wantFramework {
			t.Errorf("builderFor(%q).Framework() = %q", input, b.Framework())
		}
	}
	if _, err := builderFor("rust"); err == nil {
		t.Error("builderFor(rust) should error")
	} else if !strings.Contains(err.Error(), "node") {
		t.Errorf("error should advertise node as supported; got %q", err.Error())
	}
	if _, err := builderFor(""); err == nil {
		t.Error("builderFor(empty) should error with detection hint")
	}
}

// TestResolveCommand_Node verifies the node default-start path:
//
//	package.json with start script  → <pm> run start
//	package.json without start      → error (set start_cmd)
//	start_cmd override               → sh -c <override>
//
// Mirrors the lockfile-precedence test by using bun.lockb so we know
// the "<pm>" detection actually fires and the runtime + builder are
// in sync.
func TestResolveCommand_Node(t *testing.T) {
	t.Run("with start script picks pm", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmp, "package.json"),
			[]byte(`{"scripts":{"start":"next start"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "bun.lockb"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		bin, args, err := resolveCommand(ReleaseSpec{Framework: "node", ArtifactDir: tmp})
		if err != nil {
			t.Fatalf("resolveCommand: %v", err)
		}
		if bin != "bun" || len(args) != 2 || args[0] != "run" || args[1] != "start" {
			t.Fatalf("got bin=%q args=%v, want bun run start", bin, args)
		}
	})

	t.Run("missing start script errors", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmp, "package.json"),
			[]byte(`{"scripts":{"build":"x"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := resolveCommand(ReleaseSpec{Framework: "node", ArtifactDir: tmp})
		if err == nil || !strings.Contains(err.Error(), "start_cmd") {
			t.Fatalf("err = %v, want hint about start_cmd", err)
		}
	})

	t.Run("start_cmd override wins", func(t *testing.T) {
		bin, args, err := resolveCommand(ReleaseSpec{
			Framework:   "node",
			ArtifactDir: t.TempDir(), // no package.json — override skips detection
			StartCmd:    "node server.js",
		})
		if err != nil {
			t.Fatalf("resolveCommand: %v", err)
		}
		if bin != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != "node server.js" {
			t.Fatalf("got bin=%q args=%v, want sh -c \"node server.js\"", bin, args)
		}
	})
}

// TestBuildUpdate_PersistsFramework guards against the "auto-detected
// framework silently dropped" regression: dbCreateBuild stores
// whatever the deployment had (often empty for auto-detect), and the
// build runner only learns the real framework after fetching source.
// The post-success update has to include framework — otherwise the
// release path's resolveCommand falls through default with "no
// default start command for framework \"\"".
func TestBuildUpdate_PersistsFramework(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()

	d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "auto", SourceKind: "local", SourceRef: "/tmp/x", Framework: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := dbCreateBuild(db, d.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Framework != "" {
		t.Fatalf("create with empty framework leaked %q", b.Framework)
	}
	if err := dbUpdateBuild(db, b.ID, map[string]any{
		"status":    "succeeded",
		"framework": "node",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := dbGetBuild(db, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Framework != "node" {
		t.Fatalf("framework after update = %q, want node — dbUpdateBuild's allowlist must include 'framework'", got.Framework)
	}
}

// TestPortFreeForServer_RejectsWildcardSquatter pins the v0.3.2
// allocator fix: when a foreign listener holds [::]:p (the wildcard
// IPv6 bind real servers like next start use), the older 127.0.0.1
// smoke test would say "free" and the supervised process would crash
// with EADDRINUSE on its own bind. The new probe binds both wildcards
// so this can't slip through again.
func TestPortFreeForServer_RejectsWildcardSquatter(t *testing.T) {
	// Free port from the OS so the test isn't tied to a hardcoded
	// number that another process might have grabbed.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	if !portFreeForServer(port) {
		t.Skipf("port %d became unavailable before the test could grab it", port)
	}

	squatter, err := net.Listen("tcp", "[::]:"+itoa(port))
	if err != nil {
		t.Skipf("can't bind [::]:%d on this host (%v); skipping", port, err)
	}
	defer squatter.Close()

	if portFreeForServer(port) {
		t.Fatalf("portFreeForServer(%d) said free with [::] squatter held — wildcard probe regressed", port)
	}
}

// itoa is a tiny strconv-free int → string used only by the port
// probe test above; lets the test stay self-contained.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// openSchemaDB opens an in-memory SQLite and applies the v1 migration
// inline. Simpler than wiring the SDK's migration runner for unit
// tests; just keep them in sync.
func openSchemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	for _, mig := range []string{"migrations/001_init.sql", "migrations/002_domain_link.sql"} {
		body, err := os.ReadFile(mig)
		if err != nil {
			t.Fatal(err)
		}
		cleaned := stripSQLComments(string(body))
		for _, stmt := range strings.Split(cleaned, ";") {
			s := strings.TrimSpace(stmt)
			if s == "" {
				continue
			}
			if _, err := db.Exec(s); err != nil {
				t.Fatalf("%s: %v\n%s", mig, err, s)
			}
		}
	}
	return db
}

// TestDetectStaticOutput pins the priority chain for the runtime's
// static-output routing (Next.js export / Vite / Astro / Gatsby).
// Order matters: "out" must beat "dist" must beat "public", and a
// subdir without index.html doesn't count (a server project might
// emit a JS-only dist/ that must not be misrouted to FileServer).
func TestDetectStaticOutput(t *testing.T) {
	t.Run("empty dir → none", func(t *testing.T) {
		if got := detectStaticOutput(t.TempDir()); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
	t.Run("out/index.html → out", func(t *testing.T) {
		tmp := t.TempDir()
		mkFile(t, filepath.Join(tmp, "out", "index.html"), "<html></html>")
		if got := detectStaticOutput(tmp); got != "out" {
			t.Fatalf("got %q, want out", got)
		}
	})
	t.Run("dist/index.html → dist", func(t *testing.T) {
		tmp := t.TempDir()
		mkFile(t, filepath.Join(tmp, "dist", "index.html"), "<html></html>")
		if got := detectStaticOutput(tmp); got != "dist" {
			t.Fatalf("got %q, want dist", got)
		}
	})
	t.Run("public/index.html → public", func(t *testing.T) {
		tmp := t.TempDir()
		mkFile(t, filepath.Join(tmp, "public", "index.html"), "<html></html>")
		if got := detectStaticOutput(tmp); got != "public" {
			t.Fatalf("got %q, want public", got)
		}
	})
	t.Run("out beats dist beats public", func(t *testing.T) {
		tmp := t.TempDir()
		mkFile(t, filepath.Join(tmp, "out", "index.html"), "<html></html>")
		mkFile(t, filepath.Join(tmp, "dist", "index.html"), "<html></html>")
		mkFile(t, filepath.Join(tmp, "public", "index.html"), "<html></html>")
		if got := detectStaticOutput(tmp); got != "out" {
			t.Fatalf("got %q, want out", got)
		}
	})
	t.Run("dist without index.html does not count", func(t *testing.T) {
		// A Bun server project may bundle a JS-only dist/ with no
		// index.html. Must NOT be routed to FileServer — that would
		// turn a working server into a 404 page.
		tmp := t.TempDir()
		mkFile(t, filepath.Join(tmp, "dist", "server.js"), "// bundle")
		if got := detectStaticOutput(tmp); got != "" {
			t.Fatalf("got %q, want empty (no index.html)", got)
		}
	})
}

// TestDetectNextStaticExport covers the build-time warning's config
// scanner. It's a string match, not a parser — assert the variants
// it has to catch (single/double quotes, ts/js/mjs filenames) and
// the cases it must miss (no output line, output: 'standalone').
func TestDetectNextStaticExport(t *testing.T) {
	cases := []struct {
		name string
		file string
		body string
		want string
	}{
		{"single-quote export ts", "next.config.ts", "export default { output: 'export' }", "next.config.ts"},
		{"double-quote export js", "next.config.js", `module.exports = { output: "export" }`, "next.config.js"},
		{"mjs", "next.config.mjs", "export default { output: 'export' }\n", "next.config.mjs"},
		{"cjs", "next.config.cjs", "module.exports = { output: 'export' }\n", "next.config.cjs"},
		{"standalone is not export", "next.config.ts", "export default { output: 'standalone' }", ""},
		{"no output key", "next.config.ts", "export default {}\n", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			mkFile(t, filepath.Join(tmp, tc.file), tc.body)
			if got := detectNextStaticExport(tmp); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("no config files → empty", func(t *testing.T) {
		if got := detectNextStaticExport(t.TempDir()); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
	t.Run("ts wins over js", func(t *testing.T) {
		// Reported filename should be deterministic if both exist.
		tmp := t.TempDir()
		mkFile(t, filepath.Join(tmp, "next.config.ts"), "export default { output: 'export' }")
		mkFile(t, filepath.Join(tmp, "next.config.js"), "module.exports = { output: 'export' }")
		if got := detectNextStaticExport(tmp); got != "next.config.ts" {
			t.Fatalf("got %q, want next.config.ts", got)
		}
	})
}

// TestLocalRuntime_StaticOutputRouting is the end-to-end guard for
// the bug this fix exists for: a Next.js export deployed under
// framework=bun (or node) would crash on `next start` because the
// runtime's resolveCommand fell back to `bun run start`. With the
// fix, the same artifact shape gets served by the in-process
// FileServer — no child process, no `bunx serve` per host.
func TestLocalRuntime_StaticOutputRouting(t *testing.T) {
	for _, framework := range []string{"bun", "node"} {
		framework := framework
		t.Run(framework, func(t *testing.T) {
			artifactDir := t.TempDir()
			// Mimic a finished Next.js export: package.json with a
			// "start" script that would fail (resolveCommand picks it
			// up), and an out/ with the actual content.
			mkFile(t, filepath.Join(artifactDir, "package.json"),
				`{"scripts":{"start":"next start"}}`)
			mkFile(t, filepath.Join(artifactDir, "out", "index.html"),
				"<!doctype html><title>hi</title>")

			port := freePortForTest(t)
			rt := NewLocalRuntime(t.TempDir(), &App{})
			rr, err := rt.Start(ReleaseSpec{
				ReleaseID:   1,
				Framework:   framework,
				ArtifactDir: artifactDir,
				Port:        port,
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer func() { _ = rt.Stop(rr) }()

			if rr.server == nil {
				t.Fatal("expected in-process server (server!=nil), got child process")
			}
			if rr.cmd != nil {
				t.Fatal("expected no cmd handle; static-output should not spawn a process")
			}

			// Fetch / and confirm we get our index.html.
			body := httpGet(t, port, "/")
			if !strings.Contains(body, "<title>hi</title>") {
				t.Fatalf("body did not contain index.html content; got %q", body)
			}
		})
	}
}

func mkFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func freePortForTest(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func httpGet(t *testing.T, port int, path string) string {
	t.Helper()
	url := "http://127.0.0.1:" + itoa(port) + path
	// Server starts asynchronously inside startStatic; retry briefly.
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	t.Fatalf("GET %s: %v", url, lastErr)
	return ""
}

// stripSQLComments removes -- line comments. The migration has no
// /* */ blocks so this is enough.
func stripSQLComments(s string) string {
	out := make([]string, 0)
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
