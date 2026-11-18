package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

type fakeFile struct {
	body string
	exec bool
	link string
}

// independentDigest computes workspaces-tree-v1 the way Deploy and
// Workspaces document it, separately from the code under test.
func independentDigest(files map[string]fakeFile) string {
	var lines []string
	for name, f := range files {
		kind, mode, content := "f", "644", f.body
		if f.link != "" {
			kind, mode, content = "l", "777", f.link
		} else if f.exec {
			mode = "755"
		}
		sum := sha256.Sum256([]byte(content))
		lines = append(lines, name+"\x00"+kind+"\x00"+mode+"\x00"+hex.EncodeToString(sum[:])+"\n")
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, line := range lines {
		io.WriteString(h, line)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func zipOf(t *testing.T, files map[string]fakeFile) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, f := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		mode := fs.FileMode(0o644)
		body := f.body
		if f.exec {
			mode = 0o755
		}
		if f.link != "" {
			mode, body = fs.ModeSymlink|0o777, f.link
		}
		h.SetMode(mode)
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(fw, body)
	}
	// Dependency folders are skipped like Deploy and Workspaces skip them.
	fw, _ := w.Create("node_modules/left-pad/index.js")
	io.WriteString(fw, "module.exports = 1")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarGzOf(files map[string]string) string {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: "./" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		io.WriteString(tw, body)
	}
	tw.Close()
	gz.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// codeWorld fakes Deploy, Code and Workspaces for code checks.
type codeWorld struct {
	mu            sync.Mutex
	t             *testing.T
	files         map[string]fakeFile
	zip           []byte
	deployDigest  string // what Deploy recorded; defaults to the true digest
	candidate     string
	attempt       int
	results       func(attempt int) (exit int, out map[string]string)
	commands      []map[string]any
	created       int
	destroyed     int
	importedFiles map[string]fakeFile
}

func newCodeWorld(t *testing.T) *codeWorld {
	files := map[string]fakeFile{
		"package.json":         {body: `{"name":"web"}`},
		"bun.lock":             {body: "lock"},
		"src/math.ts":          {body: "export const add = (a, b) => a + b"},
		"src/math.test.ts":     {body: "test()"},
		"scripts/check.sh":     {body: "#!/bin/sh\necho ok", exec: true},
		"src/current-math.ts":  {link: "math.ts"},
		"docs/nested/notes.md": {body: "notes"},
	}
	w := &codeWorld{t: t, files: files, candidate: strings.Repeat("c", 64)}
	w.zip = zipOf(t, files)
	w.deployDigest = independentDigest(files)
	return w
}

func reply(out any, v any) error {
	raw, _ := json.Marshal(v)
	return json.Unmarshal(raw, out)
}

func (w *codeWorld) call(_ context.Context, app, tool string, raw map[string]any, out any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var args map[string]any
	b, _ := json.Marshal(raw)
	json.Unmarshal(b, &args)
	sum := sha256.Sum256(w.zip)
	switch app + "/" + tool {
	case "deploy/deploy_candidate_digest":
		if args["candidate_digest"] != nil && args["candidate_digest"] != w.candidate {
			return errors.New("no build with that digest")
		}
		return reply(out, map[string]any{"build_id": 7, "candidate_digest": w.candidate, "source_digest": w.deployDigest})
	case "deploy/deploy_status":
		return reply(out, map[string]any{"current_release": map[string]any{"build_id": 7}})
	case "deploy/deploy_build_source":
		if args["build_id"] != float64(7) || args["_project_id"] != "project-a" {
			return fmt.Errorf("unexpected build source args %v", args)
		}
		return reply(out, map[string]any{"source_digest": w.deployDigest, "digest_method": "workspaces-tree-v1", "file_count": len(w.files),
			"source_kind": "code", "build_id": 7, "candidate_digest": w.candidate,
			"code": map[string]any{"snapshot_id": "snap-1", "source_revision": "sha256:snap-1", "repository": "web"}})
	case "code/repos_export":
		if args["slug"] != "web" || (args["snapshot_id"] != nil && args["snapshot_id"] != "snap-1") {
			return fmt.Errorf("unexpected export args %v", args)
		}
		return reply(out, map[string]any{"snapshot_id": "snap-1", "source_revision": "sha256:snap-1", "sha256": hex.EncodeToString(sum[:]), "size": len(w.zip), "format": "zip-v1"})
	case "code/repos_snapshot_read":
		offset := int(args["offset"].(float64))
		end := offset + 100 // small chunks exercise the loop
		if end > len(w.zip) {
			end = len(w.zip)
		}
		return reply(out, map[string]any{"data_b64": base64.StdEncoding.EncodeToString(w.zip[offset:end]), "next_offset": end, "eof": end == len(w.zip)})
	case "workspaces/workspace_test_create":
		w.created++
		w.attempt = 0
		archive, _ := base64.StdEncoding.DecodeString(args["source_archive_base64"].(string))
		w.importedFiles = untar(w.t, archive)
		return reply(out, map[string]any{"workspace": map[string]any{"id": "ws-1", "image": "oven/bun:1-debian@sha256:93d5"}, "source_digest": independentDigest(w.importedFiles)})
	case "workspaces/workspace_source_digest":
		return reply(out, map[string]any{"source_digest": independentDigest(w.importedFiles), "method": "workspaces-tree-v1", "cache_key": "lock-key", "image": "oven/bun:1-debian@sha256:93d5"})
	case "workspaces/workspace_command_start":
		w.commands = append(w.commands, args)
		return reply(out, map[string]any{"command": map[string]any{"id": fmt.Sprintf("cmd-%d", len(w.commands)), "status": "running"}})
	case "workspaces/workspace_command_wait":
		cmd := w.commands[len(w.commands)-1]
		status, exit := "succeeded", 0
		if cmd["user"] == "runner" {
			w.attempt++
			exit, _ = w.results(w.attempt)
			if exit != 0 {
				status = "failed"
			}
		}
		return reply(out, map[string]any{"command": map[string]any{"id": args["command_id"], "status": status, "exit_code": exit}, "output": fmt.Sprintf("%s output\n", cmd["user"]), "cursor": "c1"})
	case "workspaces/workspace_results_export":
		_, files := w.results(w.attempt)
		destroyed := args["destroy"] != false
		if destroyed {
			w.destroyed++
		}
		return reply(out, map[string]any{"format": "tar.gz", "archive_base64": tarGzOf(files), "destroyed": destroyed})
	case "workspaces/workspace_destroy":
		w.destroyed++
		return reply(out, map[string]any{"destroyed": true})
	}
	return fmt.Errorf("unexpected call %s/%s", app, tool)
}

func untar(t *testing.T, archive []byte) map[string]fakeFile {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files := map[string]fakeFile{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		switch h.Typeflag {
		case tar.TypeSymlink:
			files[h.Name] = fakeFile{link: h.Linkname}
		default:
			files[h.Name] = fakeFile{body: string(body), exec: h.Mode&0o111 != 0}
		}
	}
	return files
}

const junitOneFailure = `<testsuites><testsuite name="src/math.test.ts" file="src/math.test.ts">
<testcase name="adds" classname="math" time="0.004" file="src/math.test.ts"/>
<testcase name="flaky clock" classname="math" time="0.250" file="src/math.test.ts"><failure message="expected 1 to be 2" type="AssertionError">at math.test.ts:5</failure></testcase>
<testcase name="later" classname="math" time="0.001" file="src/math.test.ts"><skipped/></testcase>
</testsuite></testsuites>`

var junitAllPass = strings.Replace(junitOneFailure, `<failure message="expected 1 to be 2" type="AssertionError">at math.test.ts:5</failure>`, "", 1)

const failingKey = "src/math.test.ts :: math > flaky clock"

func codeFixture(t *testing.T, world *codeWorld, check CodeCheck, blocking bool) (*App, store, Suite, func() Run) {
	t.Helper()
	platform := &scenarioPlatform{call: world.call}
	a, app, s := fixture(t, tk.WithPlatform(platform))
	suite, err := s.saveSuite(Suite{Name: "Unit tests", ReleaseBlocking: blocking, Target: &TargetConfig{Mode: "source", Candidate: &CandidateRef{DeploymentID: 3, Build: "latest_succeeded"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.saveCheck(Check{SuiteID: suite.ID, Name: "bun test", Kind: "code", Enabled: true, Definition: Definition{Code: &check}}); err != nil {
		t.Fatal(err)
	}
	runs := 0
	run := func() Run {
		t.Helper()
		runs++
		r, err := a.queue(context.Background(), app, suite.ID, "tool", fmt.Sprintf("k%d", runs), QueueOptions{CandidateDigest: world.candidate})
		if err != nil {
			t.Fatal(err)
		}
		if err = a.work(context.Background(), app); err != nil {
			t.Fatal(err)
		}
		r, err = s.run(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	return a, s, suite, run
}

func bunCheck() CodeCheck {
	return CodeCheck{Source: CodeSource{From: "candidate"},
		Prepare: &CodeCommand{Argv: []string{"bun", "install", "--frozen-lockfile"}},
		Test:    CodeCommand{Argv: []string{"bun", "test", "--reporter=junit", "--reporter-outfile=$TESTS_OUT/junit.xml", "--coverage", "--coverage-reporter=lcov", "--coverage-dir=$TESTS_OUT/coverage"}, Env: map[string]string{"TZ": "UTC"}},
		Results: CodeResults{JUnit: "junit.xml", Coverage: "coverage/lcov.info"}}
}

func TestCodeCheckOnCandidateSource(t *testing.T) {
	world := newCodeWorld(t)
	world.results = func(int) (int, map[string]string) {
		return 1, map[string]string{"junit.xml": junitOneFailure, "coverage/lcov.info": "SF:src/math.ts\nLF:4\nLH:3\nend_of_record\n"}
	}
	a, s, suite, run := codeFixture(t, world, bunCheck(), true)

	r := run()
	if r.Status != "failed" || len(r.Results) != 1 || r.Results[0].Status != "failed" || !strings.Contains(r.Results[0].Error, "1 of 3 test cases failed") {
		t.Fatalf("%+v", r)
	}
	// The tested source is exactly the build's, with modes and symlinks kept
	// and node_modules left out.
	if len(world.importedFiles) != len(world.files) || !world.importedFiles["scripts/check.sh"].exec || world.importedFiles["src/current-math.ts"].link != "math.ts" {
		t.Fatalf("imported %+v", world.importedFiles)
	}
	prepare, test := world.commands[0], world.commands[1]
	if prepare["user"] != "builder" || prepare["network"] != "registry_only" || test["user"] != "runner" || test["network"] != "none" || test["working_directory"] != "/workspace/src" {
		t.Fatalf("commands %+v", world.commands)
	}
	argv := fmt.Sprint(test["argv"])
	if !strings.HasPrefix(argv, "[env TZ=UTC bun test") || !strings.Contains(argv, "--reporter-outfile=/workspace/out/junit.xml") {
		t.Fatal(argv)
	}
	if world.destroyed != 1 {
		t.Fatalf("destroyed %d", world.destroyed)
	}
	out := r.Results[0].Output.(map[string]any)
	if out["coverage"].(map[string]any)["lines_pct"] != float64(75) || out["summary"].(map[string]any)["failed"] != float64(1) {
		t.Fatalf("%+v", out)
	}
	source := out["source"].(map[string]any)
	if source["source_digest"] != world.deployDigest || source["cache_key"] != "lock-key" || source["snapshot_id"] != "snap-1" || source["build_id"] != float64(7) {
		t.Fatalf("%+v", source)
	}
	if r.Release == nil || r.Release.Mode != "source" || r.Release.SourceDigest != world.deployDigest || r.Release.ObservedDigest != world.candidate {
		t.Fatalf("%+v", r.Release)
	}
	cases, err := s.cases(caseFilter{RunID: r.ID, Status: "failing"})
	if err != nil || len(cases) != 1 || cases[0].Key != failingKey || cases[0].Message != "expected 1 to be 2" {
		t.Fatalf("%+v %v", cases, err)
	}
	var artifacts int
	s.db.QueryRow(`SELECT count(*) FROM test_artifacts WHERE run_id=? AND mime='text/plain'`, r.ID).Scan(&artifacts)
	if artifacts != 3 {
		t.Fatalf("artifacts %d", artifacts)
	}

	// Quarantine the failing case: the next run passes and earns a verdict.
	if _, err = s.setQuarantine(quarantineRequest{SuiteID: suite.ID, CaseKey: failingKey, Reason: "clock-dependent, tracked separately", Days: 7}, "user"); err != nil {
		t.Fatal(err)
	}
	world.results = func(int) (int, map[string]string) {
		return 1, map[string]string{"junit.xml": junitOneFailure, "coverage/lcov.info": "SF:src/math.ts\nLF:4\nLH:4\nend_of_record\n"}
	}
	r = run()
	if r.Status != "passed" || r.Results[0].Status != "passed" {
		t.Fatalf("%+v", r)
	}
	out = r.Results[0].Output.(map[string]any)
	if out["summary"].(map[string]any)["quarantined_failures"] != float64(1) || out["coverage"].(map[string]any)["delta_lines_pct"] != float64(25) {
		t.Fatalf("%+v", out)
	}
	history, err := s.caseHistories(suite.ID, failingKey, false, 0)
	if err != nil || len(history) != 1 || history[0].Runs != 2 || history[0].Failures != 2 || history[0].Recent != "ff" || !history[0].Quarantined {
		t.Fatalf("%+v %v", history, err)
	}

	spec, err := a.releaseSpec(context.Background(), a.ctx.WithProject("project-a"), suite.ID)
	if err != nil {
		t.Fatal(err)
	}
	opt := QueueOptions{CandidateDigest: world.candidate, SuiteRevision: spec["suite_revision"].(string), FixtureRevision: spec["fixture_revision"].(string), SourceDigest: world.deployDigest}
	v, err := s.verdict(suite.ID, opt)
	if err != nil || !v.Allowed || v.State != "passed" || v.Release.SourceDigest != world.deployDigest {
		t.Fatalf("%+v %v", v, err)
	}
	opt.SourceDigest = "sha256:" + strings.Repeat("0", 64)
	if v, _ = s.verdict(suite.ID, opt); v.Allowed || v.State != "source_mismatch" {
		t.Fatalf("%+v", v)
	}
	all, err := a.releaseVerdicts(context.Background(), a.ctx.WithProject("project-a"), world.candidate, world.deployDigest)
	if err != nil || all["allowed"] != true || len(all["suites"].([]suiteVerdict)) != 1 {
		t.Fatalf("%+v %v", all, err)
	}
	other, _ := a.releaseVerdicts(context.Background(), a.ctx.WithProject("project-a"), strings.Repeat("d", 64), "")
	if other["allowed"] != false {
		t.Fatalf("%+v", other)
	}
}

func TestCodeCheckFlakyRetry(t *testing.T) {
	world := newCodeWorld(t)
	world.results = func(attempt int) (int, map[string]string) {
		if attempt == 1 {
			return 1, map[string]string{"junit.xml": junitOneFailure}
		}
		return 0, map[string]string{"junit.xml": junitAllPass}
	}
	check := bunCheck()
	check.RetryFlaky, check.Results.Coverage = true, ""
	_, s, suite, run := codeFixture(t, world, check, false)
	r := run()
	if r.Status != "failed" || r.Results[0].Status != "flaky" || !strings.Contains(r.Results[0].Error, "passed on the retry") {
		t.Fatalf("%+v", r)
	}
	if world.attempt != 2 || world.destroyed != 1 {
		t.Fatalf("attempts %d destroyed %d", world.attempt, world.destroyed)
	}
	history, _ := s.caseHistories(suite.ID, "", true, 0)
	if len(history) != 1 || history[0].Flaky != 1 || history[0].Recent != "y" {
		t.Fatalf("%+v", history)
	}

	// A case that fails twice is a failure, not flakiness.
	world.results = func(int) (int, map[string]string) { return 1, map[string]string{"junit.xml": junitOneFailure} }
	r = run()
	if r.Results[0].Status != "failed" || !strings.Contains(r.Results[0].Error, "also on the retry") {
		t.Fatalf("%+v", r.Results[0])
	}
}

func TestCodeCheckRejectsWrongSource(t *testing.T) {
	world := newCodeWorld(t)
	world.deployDigest = "sha256:" + strings.Repeat("e", 64)
	world.results = func(int) (int, map[string]string) { return 0, map[string]string{"junit.xml": junitAllPass} }
	_, _, _, run := codeFixture(t, world, bunCheck(), false)
	r := run()
	if r.Status != "failed" || r.Results[0].Status != "error" || !strings.Contains(r.Results[0].Error, "Deploy recorded") || world.created != 0 {
		t.Fatalf("%+v", r)
	}
	if r.Release.SourceDigest != "" || r.Release.Inconclusive != true {
		t.Fatalf("%+v", r.Release)
	}
}

func TestCodeCheckReportProblems(t *testing.T) {
	world := newCodeWorld(t)
	world.results = func(int) (int, map[string]string) { return 0, map[string]string{} }
	_, _, _, run := codeFixture(t, world, bunCheck(), false)
	r := run()
	if r.Results[0].Status != "failed" || !strings.Contains(r.Results[0].Error, "wrote no JUnit report at $TESTS_OUT/junit.xml") {
		t.Fatalf("%+v", r.Results[0])
	}
	world.results = func(int) (int, map[string]string) { return 2, map[string]string{"junit.xml": junitAllPass} }
	r = run()
	if r.Results[0].Status != "failed" || !strings.Contains(r.Results[0].Error, "exited 2, but its JUnit report shows no failures") {
		t.Fatalf("%+v", r.Results[0])
	}
}

func TestCodeCheckValidation(t *testing.T) {
	valid := func() Check {
		c := bunCheck()
		return Check{SuiteID: 1, Name: "unit", Kind: "code", Definition: Definition{Code: &c}}
	}
	c := valid()
	if err := validateCheck(&c); err != nil || c.Definition.Code.TimeoutS != 900 || c.Definition.Code.Test.Network != "none" || c.Definition.Code.Runtime.Profile != "bun-test" {
		t.Fatalf("%v %+v", err, c.Definition.Code)
	}
	for name, mutate := range map[string]func(*CodeCheck){
		"secret env":     func(k *CodeCheck) { k.Test.Env = map[string]string{"API_TOKEN": "x"} },
		"credential ref": func(k *CodeCheck) { k.Test.Env = map[string]string{"USER_NAME": "credentials.operator.email"} },
		"reserved env":   func(k *CodeCheck) { k.Test.Env = map[string]string{"TESTS_OUT": "/tmp"} },
		"test network":   func(k *CodeCheck) { k.Test.Network = "default" },
		"escape results": func(k *CodeCheck) { k.Results.JUnit = "../junit.xml" },
		"absolute dir":   func(k *CodeCheck) { k.Test.WorkingDirectory = "/etc" },
		"no argv":        func(k *CodeCheck) { k.Test.Argv = nil },
		"bad source":     func(k *CodeCheck) { k.Source = CodeSource{From: "git"} },
		"repo on cand":   func(k *CodeCheck) { k.Source.Repository = "web" },
		"long timeout":   func(k *CodeCheck) { k.TimeoutS = 7200 },
		"profile":        func(k *CodeCheck) { k.Runtime.Profile = "node" },
	} {
		c := valid()
		mutate(c.Definition.Code)
		if err := validateCheck(&c); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	source := Suite{Target: &TargetConfig{Mode: "source", Candidate: &CandidateRef{DeploymentID: 1}}}
	c = valid()
	validateCheck(&c)
	if err := validateScenarioBinding(c, source); err != nil {
		t.Fatal(err)
	}
	if err := validateScenarioBinding(Check{Kind: "http", Definition: Definition{URL: "https://example.com"}}, source); err == nil {
		t.Fatal("source suites accepted an HTTP check")
	}
	if err := validateScenarioBinding(c, Suite{}); err == nil {
		t.Fatal("candidate source accepted without a candidate")
	}
	repo := valid()
	repo.Definition.Code.Source = CodeSource{From: "code", Repository: "web"}
	validateCheck(&repo)
	if err := validateScenarioBinding(repo, Suite{}); err != nil {
		t.Fatal(err)
	}
	blocking := source
	blocking.ReleaseBlocking = true
	if err := validateScenarioBinding(repo, blocking); err == nil {
		t.Fatal("release-blocking suite accepted repository source")
	}
	for _, bad := range []*TargetConfig{
		{Mode: "source"},
		{Mode: "source", Candidate: &CandidateRef{DeploymentID: 1}, Targets: map[string]TargetEndpoint{"api": {BaseURL: "https://x.example"}}},
	} {
		if err := bad.validate(); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if err := validateSuiteExtensions(&Suite{ReleaseBlocking: true}); err == nil {
		t.Fatal("release_blocking accepted without a verdict source")
	}
}

func TestQuarantineRules(t *testing.T) {
	_, _, s := fixture(t)
	suite, _ := s.saveSuite(Suite{Name: "q"})
	if _, err := s.setQuarantine(quarantineRequest{SuiteID: suite.ID, CaseKey: "a :: b", Reason: "flaky network"}, "user"); err == nil {
		t.Fatal("quarantined a case that never ran")
	}
	if _, err := s.db.Exec(`INSERT INTO test_runs(id,project_id,suite_id,suite_name,environment,status,trigger,snapshot,created_at) VALUES(900,?,?,'q','test','passed','tool','[]','1')`, s.project, suite.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.saveCases(suite.ID, 900, 1, []TestCase{{Key: "a :: b", Name: "b", Status: "failed"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.setQuarantine(quarantineRequest{SuiteID: suite.ID, CaseKey: "a :: b", Reason: "no"}, "user"); err == nil {
		t.Fatal("accepted a short reason")
	}
	if _, err := s.setQuarantine(quarantineRequest{SuiteID: suite.ID, CaseKey: "a :: b", Reason: "flaky network", Days: 91}, "user"); err == nil {
		t.Fatal("accepted 91 days")
	}
	entry, err := s.setQuarantine(quarantineRequest{SuiteID: suite.ID, CaseKey: "a :: b", Reason: "flaky network"}, "agent 4")
	if err != nil || entry.AddedBy != "agent 4" {
		t.Fatal(entry, err)
	}
	active, _ := s.activeQuarantine(suite.ID)
	if active["a :: b"] != "flaky network" {
		t.Fatal(active)
	}
	s.db.Exec(`UPDATE test_quarantine SET expires_at='2000-01-01T00:00:00Z'`)
	if active, _ = s.activeQuarantine(suite.ID); len(active) != 0 {
		t.Fatal("expired quarantine still active")
	}
	if list, _ := s.quarantineList(suite.ID, true); len(list) != 1 || !list[0].Expired {
		t.Fatal(list)
	}
	if _, err = s.setQuarantine(quarantineRequest{SuiteID: suite.ID, CaseKey: "a :: b", Remove: true}, "user"); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateSourceFromDeployArchive(t *testing.T) {
	files := map[string]fakeFile{"index.ts": {body: "export {}"}, "run.sh": {body: "#!/bin/sh", exec: true}}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"index.ts", "run.sh"} {
		mode := int64(0o644)
		if files[name].exec {
			mode = 0o755
		}
		tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(files[name].body)), Typeflag: tar.TypeReg})
		io.WriteString(tw, files[name].body)
	}
	tw.Close()
	gz.Close()
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(archive) }))
	defer server.Close()
	expiredNote := false
	platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
		src := map[string]any{"source_digest": independentDigest(files), "digest_method": "workspaces-tree-v1", "source_kind": "local", "build_id": 9}
		if expiredNote {
			src["archive_note"] = "the source copy was removed with the build's artifact by retention"
		} else {
			src["archive"] = map[string]any{"format": "tar.gz", "sha256": hex.EncodeToString(sum[:]), "size_bytes": len(archive), "url": server.URL + "/source.tar.gz"}
		}
		return reply(out, src)
	}}
	_, app, _ := fixture(t, tk.WithPlatform(platform))
	source, err := fetchCandidateSource(context.Background(), app, 9)
	if err != nil {
		t.Fatal(err)
	}
	if source.provenance.SourceDigest != independentDigest(files) || source.provenance.SourceKind != "local" || !untar(t, source.archive)["run.sh"].exec {
		t.Fatalf("%+v", source.provenance)
	}
	expiredNote = true
	if _, err = fetchCandidateSource(context.Background(), app, 9); err == nil || !strings.Contains(err.Error(), "removed with the build's artifact") {
		t.Fatal(err)
	}
}
