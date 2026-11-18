package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	maxCommandLog   = 128 << 10
	maxResultsFile  = 8 << 20
	maxTextArtifact = 1 << 20
	maxRunFailures  = 20
	maxSlowest      = 10
	workspaceWaitMS = 20000
)

type workspaceCommand struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

func terminalCommand(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "timed_out":
		return true
	}
	return false
}

func workspacesCall(ctx context.Context, app *sdk.AppCtx, tool string, args map[string]any, out any) error {
	platform := app.PlatformAPI()
	if platform == nil {
		return errors.New("code checks need the Workspaces app (0.8.0 or newer) bound to Tests")
	}
	return sdk.CallAppResultContext(ctx, platform, "workspaces", tool, args, out)
}

// commandRun is one finished workspace command.
type commandRun struct {
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Seconds   int64  `json:"seconds"`
	// LogGap is set when Workspaces' 64 KB output window moved past output
	// Tests had not read yet, or the log was cut to its tail.
	LogGap bool `json:"log_gap,omitempty"`
	log    string
}

func (c commandRun) exit() int {
	if c.ExitCode == nil {
		return -1
	}
	return *c.ExitCode
}

// runWorkspaceCommand starts argv and follows its output until it finishes.
func runWorkspaceCommand(ctx context.Context, app *sdk.AppCtx, workspaceID string, cmd *CodeCommand, user string) (commandRun, error) {
	start := time.Now()
	var started struct {
		Command workspaceCommand `json:"command"`
	}
	args := map[string]any{
		"workspace_id": workspaceID, "argv": commandArgv(cmd), "working_directory": workingDirectory(cmd),
		"timeout_s": cmd.TimeoutS, "user": user, "network": cmd.Network,
	}
	if err := workspacesCall(ctx, app, "workspace_command_start", args, &started); err != nil {
		return commandRun{}, fmt.Errorf("Workspaces could not start the %s command: %s", user, deployReason(err))
	}
	if started.Command.ID == "" {
		return commandRun{}, errors.New("Workspaces returned a command without an id")
	}
	result := commandRun{Status: started.Command.Status}
	var log strings.Builder
	cursor := ""
	for {
		var waited struct {
			Command workspaceCommand `json:"command"`
			Output  string           `json:"output"`
			Cursor  string           `json:"cursor"`
			Gap     bool             `json:"gap"`
		}
		err := workspacesCall(ctx, app, "workspace_command_wait", map[string]any{
			"workspace_id": workspaceID, "command_id": started.Command.ID, "cursor": cursor, "wait_ms": workspaceWaitMS,
		}, &waited)
		if err != nil {
			if ctx.Err() != nil {
				cancelCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				_ = workspacesCall(cancelCtx, app, "workspace_command_cancel", map[string]any{"workspace_id": workspaceID, "command_id": started.Command.ID}, nil)
				cancel()
				return result, ctx.Err()
			}
			return result, fmt.Errorf("Workspaces could not report the %s command: %s", user, deployReason(err))
		}
		if waited.Gap {
			result.LogGap = true
			log.WriteString("\n[… earlier output was not captured …]\n")
		}
		log.WriteString(waited.Output)
		if log.Len() > 2*maxCommandLog {
			tail := log.String()[log.Len()-maxCommandLog:]
			log.Reset()
			log.WriteString(tail)
			result.LogGap = true
		}
		cursor = waited.Cursor
		if terminalCommand(waited.Command.Status) {
			result.Status, result.ExitCode, result.ErrorCode = waited.Command.Status, waited.Command.ExitCode, waited.Command.ErrorCode
			break
		}
	}
	result.Seconds = int64(time.Since(start).Seconds())
	result.log = log.String()
	if len(result.log) > maxCommandLog {
		result.log = result.log[len(result.log)-maxCommandLog:]
		result.LogGap = true
	}
	return result, nil
}

// readResults unpacks an exported $TESTS_OUT archive.
func readResults(encoded string) (map[string][]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("Workspaces returned an undecodable results archive")
	}
	files := map[string][]byte{}
	if len(data) == 0 {
		return files, nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open results archive: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read results archive: %v", err)
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			continue
		}
		name, err := normalizeEntryPath(h.Name)
		if err != nil || name == "" {
			continue
		}
		if h.Size > maxResultsFile {
			files[name] = nil // present but too large to read
			continue
		}
		total += h.Size
		if total > 4*maxResultsFile {
			return nil, errors.New("results archive is too large")
		}
		body, err := io.ReadAll(io.LimitReader(tr, maxResultsFile))
		if err != nil {
			return nil, fmt.Errorf("read results archive: %v", err)
		}
		files[name] = body
	}
	return files, nil
}

// resultFile finds a $TESTS_OUT-relative path, whether or not the archive
// prefixes entries with the results folder name.
func resultFile(files map[string][]byte, want string) ([]byte, bool) {
	for _, name := range []string{want, "out/" + want} {
		if body, ok := files[name]; ok {
			return body, true
		}
	}
	return nil, false
}

func exportResults(ctx context.Context, app *sdk.AppCtx, workspaceID string, destroy bool) (map[string][]byte, bool, error) {
	var out struct {
		ArchiveBase64 string `json:"archive_base64"`
		Destroyed     bool   `json:"destroyed"`
		DestroyError  string `json:"destroy_error"`
	}
	if err := workspacesCall(ctx, app, "workspace_results_export", map[string]any{"workspace_id": workspaceID, "destroy": destroy}, &out); err != nil {
		return nil, false, fmt.Errorf("Workspaces could not export the results: %s", deployReason(err))
	}
	files, err := readResults(out.ArchiveBase64)
	return files, out.Destroyed, err
}

// attempt is one run of the test command and what it reported.
type attempt struct {
	command commandRun
	cases   []TestCase
	report  []byte
	lcov    []byte
	problem string // why the report could not be used
}

func readAttempt(files map[string][]byte, results CodeResults, cmd commandRun) attempt {
	a := attempt{command: cmd}
	body, ok := resultFile(files, results.JUnit)
	switch {
	case !ok:
		a.problem = fmt.Sprintf("the test command exited %d and wrote no JUnit report at $TESTS_OUT/%s", cmd.exit(), results.JUnit)
	case body == nil:
		a.problem = fmt.Sprintf("the JUnit report $TESTS_OUT/%s exceeds 8 MB", results.JUnit)
	default:
		cases, err := parseJUnit(body)
		if err != nil {
			a.problem = err.Error()
		} else if len(cases) == 0 {
			a.problem = "the JUnit report has no test cases"
		}
		a.cases, a.report = cases, body
	}
	if results.Coverage != "" {
		a.lcov, _ = resultFile(files, results.Coverage)
	}
	return a
}

func failing(c TestCase) bool { return c.Status == "failed" || c.Status == "error" }

// runCode runs a code check: fetch the source, create a test workspace,
// prepare as builder, test as runner without network, read the reports.
func (a *App) runCode(ctx context.Context, c Check, run *appRunContext) (result Result) {
	result = Result{CheckID: c.ID, Name: c.Name, Kind: c.Kind, Status: "error", Assertions: []AssertionResult{}}
	start := time.Now()
	defer func() {
		result.DurationMS = time.Since(start).Milliseconds()
		if p := recover(); p != nil {
			result.Status, result.Error = "error", "runner panic"
		}
	}()
	code := c.Definition.Code
	if code == nil {
		result.Error = "code check has no definition.code"
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(code.TimeoutS)*time.Second)
	defer cancel()
	app := run.ctx
	output := map[string]any{}
	fail := func(status, message string) Result {
		result.Status, result.Error = status, scrubText(message, run.secrets)
		result.Output = normalizeOutput(output)
		return result
	}

	source, err := run.codeSource(ctx, code)
	if err != nil {
		return fail("error", err.Error())
	}
	proof := source.provenance
	output["source"] = &proof

	useCache := code.UseCache == nil || *code.UseCache
	ttl := code.TimeoutS/60 + 15
	if ttl > 120 {
		ttl = 120
	}
	var created struct {
		Workspace struct {
			ID    string `json:"id"`
			Image string `json:"image"`
		} `json:"workspace"`
		SourceDigest string `json:"source_digest"`
	}
	name := fmt.Sprintf("Tests run %d: %s", run.runID, c.Name)
	if len(name) > 120 {
		name = name[:120]
	}
	err = workspacesCall(ctx, app, "workspace_test_create", map[string]any{
		"name": name, "purpose": "Tests code check", "source_archive_base64": base64.StdEncoding.EncodeToString(source.archive),
		"profile": code.Runtime.Profile, "ttl_minutes": ttl, "use_cache": useCache,
		"idempotency_key": fmt.Sprintf("tests:%s:run:%d:check:%d", app.CurrentProject(), run.runID, c.ID),
		"owner_label":     "Tests", "resource_kind": "test_run", "resource_id": fmt.Sprint(run.runID),
	}, &created)
	if err != nil {
		return fail("error", "Workspaces could not create the test workspace (Workspaces 0.8.0 or newer, with Tests allowed in test_workspace_apps): "+deployReason(err))
	}
	workspaceID := created.Workspace.ID
	if workspaceID == "" {
		return fail("error", "Workspaces returned a test workspace without an id")
	}
	proof.WorkspaceID, proof.Image = workspaceID, created.Workspace.Image
	destroyed := false
	defer func() {
		if destroyed {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := workspacesCall(cleanup, app, "workspace_destroy", map[string]any{"workspace_id": workspaceID, "confirm": true}, nil); err != nil {
			run.warn(fmt.Sprintf("check %q: test workspace %s was not destroyed (%s); it expires with its TTL", c.Name, workspaceID, deployReason(err)))
		}
	}()
	if created.SourceDigest != proof.SourceDigest {
		return fail("error", fmt.Sprintf("Workspaces imported source %s, but Tests sent %s", created.SourceDigest, proof.SourceDigest))
	}
	var digest struct {
		SourceDigest string `json:"source_digest"`
		Method       string `json:"method"`
		CacheKey     string `json:"cache_key"`
		Image        string `json:"image"`
	}
	if err = workspacesCall(ctx, app, "workspace_source_digest", map[string]any{"workspace_id": workspaceID}, &digest); err == nil {
		if digest.SourceDigest != proof.SourceDigest || digest.Method != sourceDigestMethod {
			return fail("error", fmt.Sprintf("Workspaces reports source %s (%s), expected %s", digest.SourceDigest, digest.Method, proof.SourceDigest))
		}
		proof.CacheKey = digest.CacheKey
		if digest.Image != "" {
			proof.Image = digest.Image
		}
	}
	if code.Source.From == "candidate" {
		verified := proof
		run.candidateSource = &verified
	}

	logs := &strings.Builder{}
	commands := map[string]any{}
	output["commands"] = commands
	defer func() {
		if logs.Len() > 0 {
			text := scrubText(logs.String(), run.secrets)
			if len(text) > maxTextArtifact {
				text = text[len(text)-maxTextArtifact:]
			}
			if id, err := saveTextArtifact(app, run.runID, c.ID, "logs", []byte(text)); err == nil {
				artifactsOf(output)["logs"] = id
				result.Output = normalizeOutput(output)
			}
		}
	}()
	record := func(label string, cmd commandRun) {
		commands[label] = cmd
		fmt.Fprintf(logs, "== %s: %s, exit %d, %ds ==\n%s\n", label, cmd.Status, cmd.exit(), cmd.Seconds, cmd.log)
	}
	if code.Prepare != nil {
		prepared, err := runWorkspaceCommand(ctx, app, workspaceID, code.Prepare, "builder")
		if err != nil {
			return fail("error", err.Error())
		}
		record("prepare", prepared)
		if prepared.Status != "succeeded" || prepared.exit() != 0 {
			return fail("failed", fmt.Sprintf("prepare %s (exit %d): %s", prepared.Status, prepared.exit(), lastLines(prepared.log, 5)))
		}
	}

	quarantine, err := run.store().activeQuarantine(run.suiteID)
	if err != nil {
		return fail("error", "could not read quarantined cases: "+err.Error())
	}
	runTest := func(label string, destroy bool) (attempt, error) {
		cmd, err := runWorkspaceCommand(ctx, app, workspaceID, &code.Test, "runner")
		if err != nil {
			return attempt{}, err
		}
		record(label, cmd)
		files, gone, err := exportResults(ctx, app, workspaceID, destroy)
		if err != nil {
			return attempt{}, err
		}
		destroyed = destroyed || gone
		at := readAttempt(files, code.Results, cmd)
		for i := range at.cases {
			if _, ok := quarantine[at.cases[i].Key]; ok && failing(at.cases[i]) {
				at.cases[i].Quarantined = true
			}
		}
		return at, nil
	}
	first, err := runTest("test", !code.RetryFlaky)
	if err != nil {
		return fail("error", err.Error())
	}
	final := first
	attempts := 1
	unquarantinedFailures := func(cases []TestCase) []string {
		var keys []string
		for _, tc := range cases {
			if failing(tc) && !tc.Quarantined {
				keys = append(keys, tc.Key)
			}
		}
		return keys
	}
	firstFailures := unquarantinedFailures(first.cases)
	flaky := map[string]bool{}
	if code.RetryFlaky && first.problem == "" && len(firstFailures) > 0 {
		second, err := runTest("test retry", true)
		if err != nil {
			return fail("error", err.Error())
		}
		attempts = 2
		if second.problem == "" {
			passedAgain := map[string]bool{}
			for _, tc := range second.cases {
				if tc.Status == "passed" {
					passedAgain[tc.Key] = true
				}
			}
			for _, key := range firstFailures {
				if passedAgain[key] {
					flaky[key] = true
				}
			}
			for i := range second.cases {
				if flaky[second.cases[i].Key] {
					second.cases[i].Status = "flaky"
				}
			}
		}
		final = second
	}
	output["attempts"] = attempts

	// Evidence: the report, coverage and the per-case table.
	if final.report != nil {
		if len(final.report) <= maxTextArtifact {
			if id, err := saveTextArtifact(app, run.runID, c.ID, "junit", final.report); err == nil {
				artifactsOf(output)["junit"] = id
			}
		} else {
			artifactsOf(output)["junit_skipped"] = "report exceeds 1 MB"
		}
	}
	if code.Results.Coverage != "" {
		switch {
		case final.lcov == nil:
			output["coverage_error"] = "no coverage report at $TESTS_OUT/" + code.Results.Coverage
		default:
			cov, err := parseLcov(final.lcov)
			if err != nil {
				output["coverage_error"] = err.Error()
			} else {
				if previous, ok := run.store().previousCoverage(run.suiteID, c.Name, run.runID); ok {
					delta := float64(int64((cov.LinesPct-previous)*100)) / 100
					cov.DeltaLinesPct = &delta
				}
				output["coverage"] = cov
			}
			if len(final.lcov) <= maxTextArtifact {
				if id, err := saveTextArtifact(app, run.runID, c.ID, "coverage", final.lcov); err == nil {
					artifactsOf(output)["coverage"] = id
				}
			}
		}
	}
	summary := summarize(final.cases)
	output["summary"] = summary
	output["exit_code"] = final.command.exit()
	output["failures"] = topFailures(final.cases)
	output["slowest"] = slowest(final.cases)
	if len(final.cases) > 0 {
		if err := run.store().saveCases(run.suiteID, run.runID, c.ID, final.cases, run.secrets); err != nil {
			return fail("error", "could not save test cases: "+err.Error())
		}
	}

	var problem string
	stillFailing := unquarantinedFailures(final.cases)
	switch {
	case final.command.Status == "timed_out":
		problem = fmt.Sprintf("the test command timed out after %ds", code.Test.TimeoutS)
	case final.command.Status == "cancelled":
		problem = "the test command was cancelled"
	case final.problem != "":
		problem = final.problem
	case len(stillFailing) > 0:
		problem = fmt.Sprintf("%d of %d test cases failed", len(stillFailing), summary.Tests)
		if attempts == 2 {
			problem += " (also on the retry)"
		}
	case final.command.exit() != 0 && summary.QuarantinedFailures == 0:
		problem = fmt.Sprintf("the test command exited %d, but its JUnit report shows no failures", final.command.exit())
	}
	if problem != "" {
		return fail("failed", problem)
	}
	result.Output = normalizeOutput(output)
	result.Status = "passed"
	if len(flaky) > 0 {
		result.Status = "flaky"
		result.Error = fmt.Sprintf("%d test cases failed, then passed on the retry", len(flaky))
	}
	if len(c.Definition.Assertions) > 0 {
		result.Assertions = evaluate(result.Output, c.Definition.Assertions)
		for _, v := range result.Assertions {
			if !v.Passed {
				result.Status = "failed"
			}
		}
	}
	return result
}

func artifactsOf(output map[string]any) map[string]any {
	artifacts, ok := output["artifacts"].(map[string]any)
	if !ok {
		artifacts = map[string]any{}
		output["artifacts"] = artifacts
	}
	return artifacts
}

// normalizeOutput turns the output into plain JSON values for assertions
// and storage.
func normalizeOutput(output map[string]any) any {
	raw, err := json.Marshal(output)
	if err != nil {
		return map[string]any{"error": "unserializable output"}
	}
	var generic any
	_ = json.Unmarshal(raw, &generic)
	return generic
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return truncateText(strings.Join(lines, "\n"), 2000)
}

func topFailures(cases []TestCase) []map[string]any {
	out := []map[string]any{}
	for _, c := range cases {
		if !failing(c) || len(out) >= maxRunFailures {
			continue
		}
		item := map[string]any{"case_key": c.Key, "status": c.Status, "message": truncateText(c.Message, 2000)}
		if c.Quarantined {
			item["quarantined"] = true
		}
		out = append(out, item)
	}
	return out
}

func slowest(cases []TestCase) []map[string]any {
	sorted := append([]TestCase(nil), cases...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].DurationMS > sorted[j].DurationMS })
	out := []map[string]any{}
	for _, c := range sorted {
		if len(out) >= maxSlowest || c.DurationMS <= 0 {
			break
		}
		out = append(out, map[string]any{"case_key": c.Key, "duration_ms": c.DurationMS})
	}
	return out
}

// codeSource fetches each source once per run.
func (r *appRunContext) codeSource(ctx context.Context, code *CodeCheck) (*codeSource, error) {
	key := "candidate"
	if code.Source.From == "code" {
		key = "code:" + code.Source.Repository + ":" + code.Source.Subdir
	}
	if r.sources == nil {
		r.sources = map[string]*codeSource{}
	}
	if cached := r.sources[key]; cached != nil {
		return cached, nil
	}
	var source *codeSource
	var err error
	if code.Source.From == "candidate" {
		if r.record == nil || r.record.BuildID == 0 {
			return nil, errors.New(`code.source "candidate" needs a Deploy build: queue the run with a candidate (target.candidate, candidate or candidate_digest)`)
		}
		source, err = fetchCandidateSource(ctx, r.ctx, r.record.BuildID)
	} else {
		source, err = fetchRepositorySource(ctx, r.ctx, code.Source.Repository, code.Source.Subdir)
	}
	if err != nil {
		return nil, err
	}
	r.sources[key] = source
	return source, nil
}

func (r *appRunContext) store() store {
	return store{r.ctx.AppDB(), r.ctx.CurrentProject()}
}

func (r *appRunContext) warn(text string) {
	r.warnings = append(r.warnings, text)
}
