package main

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// CodeCheck runs a project's own test command inside a locked-down
// Workspaces test workspace and reads its JUnit report (and optional lcov
// coverage) from $TESTS_OUT.
type CodeCheck struct {
	Source  CodeSource   `json:"source"`
	Runtime CodeRuntime  `json:"runtime,omitempty"`
	Prepare *CodeCommand `json:"prepare,omitempty"`
	Test    CodeCommand  `json:"test"`
	Results CodeResults  `json:"results"`
	// TimeoutS bounds the whole check: source, prepare, test and export.
	TimeoutS int `json:"timeout_s,omitempty"`
	// RetryFlaky reruns the test command once when cases fail. Cases that pass
	// on the rerun are reported as flaky and the check's status is "flaky".
	RetryFlaky bool `json:"retry_flaky,omitempty"`
	// UseCache shares the dependency cache keyed by the lockfiles (default true).
	UseCache *bool `json:"use_cache,omitempty"`
}

// CodeSource selects the tested source: the suite's Deploy candidate build
// ("candidate") or the current state of a Code repository ("code").
type CodeSource struct {
	From       string `json:"from"`
	Repository string `json:"repository,omitempty"`
	Subdir     string `json:"subdir,omitempty"`
}

type CodeRuntime struct {
	Profile string `json:"profile,omitempty"`
}

// CodeCommand is an argv run without a shell. "$TESTS_OUT" in argv and env
// values is replaced by the results directory.
type CodeCommand struct {
	Argv             []string          `json:"argv"`
	Env              map[string]string `json:"env,omitempty"`
	Network          string            `json:"network,omitempty"`
	TimeoutS         int               `json:"timeout_s,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
}

// CodeResults are paths relative to $TESTS_OUT.
type CodeResults struct {
	JUnit    string `json:"junit"`
	Coverage string `json:"coverage,omitempty"`
}

const (
	codeProfileBun      = "bun-test"
	codeSourceDir       = "/workspace/src"
	codeResultsDir      = "/workspace/out"
	defaultCodeTimeoutS = 900
	maxCodeTimeoutS     = 3600
)

var codeEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Workspaces sets these for every test command; a check cannot override them.
var reservedCodeEnv = map[string]bool{
	"HOME": true, "TMPDIR": true, "XDG_CACHE_HOME": true, "CI": true, "TESTS_OUT": true,
	"BUN_INSTALL_CACHE_DIR": true, "PATH": true, "USER": true, "LD_PRELOAD": true, "LD_LIBRARY_PATH": true,
	"NODE_OPTIONS": true,
}

func validateCode(c *Check) error {
	d := &c.Definition
	code := d.Code
	if code == nil {
		return errors.New("code checks need definition.code")
	}
	if d.Function != "" || d.URL != "" || d.Target != "" || len(d.Steps) > 0 || d.Session != "" || d.Diagnostics != nil {
		return errors.New("code checks take only definition.code and optional assertions")
	}
	switch code.Source.From {
	case "candidate":
		if code.Source.Repository != "" || code.Source.Subdir != "" {
			return errors.New("code.source candidate reads the Deploy build's own source; omit repository and subdir")
		}
	case "code":
		if !slugPattern.MatchString(code.Source.Repository) {
			return errors.New("code.source.repository must name a Code repository")
		}
		if code.Source.Subdir != "" {
			if _, err := cleanRelative(code.Source.Subdir); err != nil {
				return fmt.Errorf("code.source.subdir: %w", err)
			}
		}
	default:
		return errors.New(`code.source.from must be "candidate" or "code"`)
	}
	if code.Runtime.Profile == "" {
		code.Runtime.Profile = codeProfileBun
	}
	if code.Runtime.Profile != codeProfileBun {
		return errors.New(`code.runtime.profile must be "bun-test"`)
	}
	if code.TimeoutS == 0 {
		code.TimeoutS = defaultCodeTimeoutS
	}
	if code.TimeoutS < 60 || code.TimeoutS > maxCodeTimeoutS {
		return fmt.Errorf("code.timeout_s must be 60–%d", maxCodeTimeoutS)
	}
	if code.Prepare != nil {
		if code.Prepare.Network == "" {
			code.Prepare.Network = "registry_only"
		}
		switch code.Prepare.Network {
		case "none", "registry_only", "default":
		default:
			return errors.New("code.prepare.network must be none, registry_only or default")
		}
		if err := validateCodeCommand("prepare", code.Prepare, code.TimeoutS); err != nil {
			return err
		}
	}
	if code.Test.Network != "" && code.Test.Network != "none" {
		return errors.New("code.test always runs without network; omit code.test.network")
	}
	code.Test.Network = "none"
	if err := validateCodeCommand("test", &code.Test, code.TimeoutS); err != nil {
		return err
	}
	junit, err := cleanRelative(code.Results.JUnit)
	if err != nil {
		return fmt.Errorf("code.results.junit (relative to $TESTS_OUT): %w", err)
	}
	code.Results.JUnit = junit
	if code.Results.Coverage != "" {
		if code.Results.Coverage, err = cleanRelative(code.Results.Coverage); err != nil {
			return fmt.Errorf("code.results.coverage (relative to $TESTS_OUT): %w", err)
		}
	}
	d.TimeoutMS = code.TimeoutS * 1000
	if len(d.Assertions) > 0 {
		return validateAssertions(d.Assertions)
	}
	return nil
}

func validateCodeCommand(name string, c *CodeCommand, total int) error {
	if len(c.Argv) == 0 || len(c.Argv) > 64 {
		return fmt.Errorf("code.%s.argv needs 1–64 arguments", name)
	}
	size := 0
	for _, arg := range c.Argv {
		size += len(arg)
		if strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("code.%s.argv contains a control character", name)
		}
		if looksSecret(arg) {
			return fmt.Errorf("code.%s.argv must not carry secrets or credential references", name)
		}
	}
	if size > 8192 {
		return fmt.Errorf("code.%s.argv exceeds 8 KiB", name)
	}
	if len(c.Env) > 32 {
		return fmt.Errorf("code.%s.env accepts at most 32 variables", name)
	}
	for key, value := range c.Env {
		if !codeEnvName.MatchString(key) || reservedCodeEnv[strings.ToUpper(key)] {
			return fmt.Errorf("code.%s.env: %q is not an allowed variable name", name, key)
		}
		if sensitiveName(key) || looksSecret(value) {
			return fmt.Errorf("code.%s.env.%s looks like a secret; code checks never receive credentials", name, key)
		}
		if len(value) > 2048 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("code.%s.env.%s is too long or has a line break", name, key)
		}
	}
	if c.TimeoutS == 0 {
		c.TimeoutS = total
	}
	if c.TimeoutS < 10 || c.TimeoutS > total {
		return fmt.Errorf("code.%s.timeout_s must be between 10 and code.timeout_s", name)
	}
	if c.WorkingDirectory != "" {
		dir, err := cleanRelative(c.WorkingDirectory)
		if err != nil {
			return fmt.Errorf("code.%s.working_directory (relative to the source): %w", name, err)
		}
		c.WorkingDirectory = dir
	}
	return nil
}

// looksSecret rejects values that reference credentials or templates. Code
// checks run project code; they never receive suite credentials.
func looksSecret(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "credentials.") || strings.Contains(value, "{{") ||
		strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "ghp_") || strings.HasPrefix(lower, "bearer ")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// cleanRelative accepts a slash path inside a root, without "..".
func cleanRelative(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" || len(p) > 512 || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\r\n") {
		return "", errors.New("needs a relative path")
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("must stay inside its directory")
	}
	return clean, nil
}

// expandResults replaces "$TESTS_OUT" with the results directory. There is no
// shell, so this is the only substitution.
func expandResults(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "${TESTS_OUT}", codeResultsDir), "$TESTS_OUT", codeResultsDir)
}

// commandArgv prefixes env assignments; Workspaces has no env argument.
func commandArgv(c *CodeCommand) []string {
	argv := make([]string, 0, len(c.Argv)+len(c.Env)+1)
	if len(c.Env) > 0 {
		argv = append(argv, "env")
		for _, key := range sortedKeys(c.Env) {
			argv = append(argv, key+"="+expandResults(c.Env[key]))
		}
	}
	for _, arg := range c.Argv {
		argv = append(argv, expandResults(arg))
	}
	return argv
}

func workingDirectory(c *CodeCommand) string {
	if c.WorkingDirectory == "" {
		return codeSourceDir
	}
	return codeSourceDir + "/" + c.WorkingDirectory
}

// codeTimeout is the time code checks may add to a run's deadline.
func codeTimeout(checks []Check) int {
	total := 0
	for _, c := range checks {
		if c.Kind == "code" && c.Definition.Code != nil {
			total += c.Definition.Code.TimeoutS
		}
	}
	return total
}

// validateCodeBinding checks a code check against its suite.
func validateCodeBinding(c Check, suite Suite) error {
	code := c.Definition.Code
	if code == nil {
		return errors.New("code checks need definition.code")
	}
	if suite.Release != nil || suite.Target != nil && suite.Target.Mode == "isolated" {
		return errors.New("code checks run in source-mode, existing-mode or target-less suites, not isolated environments")
	}
	if code.Source.From == "candidate" && (suite.Target == nil || suite.Target.Mode != "source" && suite.Target.Candidate == nil) {
		return errors.New(`code.source "candidate" needs a suite target with a Deploy candidate (mode "source", or existing mode with target.candidate)`)
	}
	if suite.ReleaseBlocking && code.Source.From != "candidate" {
		return errors.New(`release-blocking suites test the candidate's own source; use code.source.from "candidate"`)
	}
	return nil
}
