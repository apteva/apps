package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type ReleaseConfig struct {
	Browser             *BrowserConfig      `json:"browser,omitempty"`
	EnvironmentID       string              `json:"environment_id"`
	EnvironmentRevision int                 `json:"environment_revision"`
	AppVersions         map[string]string   `json:"app_versions"`
	GatewayApp          string              `json:"gateway_app"`
	GatewayPath         string              `json:"gateway_path,omitempty"`
	IdentityProbe       ToolProbe           `json:"candidate_probe"`
	Identities          map[string]Identity `json:"identities,omitempty"`
	ReadTools           map[string][]string `json:"read_tools,omitempty"`
	OpenAPI             json.RawMessage     `json:"openapi,omitempty"`
	// Generated marks a configuration Tests derived from a suite target. Its
	// runtime app versions are observed and pinned only where declared.
	Generated bool `json:"generated,omitempty"`
}

type ToolProbe struct {
	App   string         `json:"app"`
	Tool  string         `json:"tool"`
	Input map[string]any `json:"input,omitempty"`
	Path  string         `json:"path"`
}

type Identity struct {
	ToolProbe
	Header string `json:"header"`
	Prefix string `json:"prefix,omitempty"`
}

type ReleaseRecord struct {
	Mode            string `json:"mode,omitempty"`
	CandidateDigest string `json:"candidate_digest"`
	BuildID         int64  `json:"build_id,omitempty"`
	Inconclusive    bool   `json:"inconclusive,omitempty"`
	// Partial marks a run limited by check_names; it never yields a verdict.
	Partial         bool              `json:"partial,omitempty"`
	ObservedDigest  string            `json:"observed_digest,omitempty"`
	SuiteRevision   string            `json:"suite_revision"`
	FixtureRevision string            `json:"fixture_revision"`
	AppVersions     map[string]string `json:"app_versions,omitempty"`
	OpenAPIDigest   string            `json:"openapi_digest,omitempty"`
	EnvironmentRun  string            `json:"environment_run_id,omitempty"`
	RuntimeID       string            `json:"runtime_id,omitempty"`
	Covered         []string          `json:"covered_operations,omitempty"`
	Missing         []string          `json:"missing_operations,omitempty"`
	// SourceDigest is the candidate source a code check tested, verified
	// against Deploy's record and the digest Workspaces computed on import.
	SourceDigest string          `json:"source_digest,omitempty"`
	Source       *codeProvenance `json:"source,omitempty"`
}

type QueueOptions struct {
	CandidateDigest string        `json:"candidate_digest"`
	SuiteRevision   string        `json:"suite_revision"`
	FixtureRevision string        `json:"fixture_revision"`
	Candidate       *CandidateRef `json:"candidate,omitempty"`
	// CheckNames limits a run to these checks (enabled or not). The session
	// setup check is always added. Such runs never produce a release verdict.
	CheckNames []string `json:"check_names,omitempty"`
	// SourceDigest, when given, must match the source the verdict's run tested.
	SourceDigest string `json:"source_digest,omitempty"`
}

type environmentDefinition struct {
	ID          string          `json:"id"`
	SpecVersion int             `json:"spec_version"`
	SpecDigest  string          `json:"spec_digest,omitempty"`
	Spec        json.RawMessage `json:"spec"`
}

type fixturePolicy struct {
	AppInstallIDs      []int64  `json:"app_install_ids"`
	ConnectionIDs      []int64  `json:"connection_ids"`
	ConnectionBindings []any    `json:"connection_bindings"`
	MCPServerIDs       []int64  `json:"mcp_server_ids"`
	AllowHostSuffixes  []string `json:"allow_host_suffixes"`
	TTLSeconds         int      `json:"ttl_seconds"`
	NetworkMode        string   `json:"network_mode"`
	IntegrationMode    string   `json:"integration_mode"`
}

var slugPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func canonicalDigest(v any) string {
	// Decode again to canonicalize arbitrary JSON inputs, including RawMessages.
	var normalized any
	decoder := json.NewDecoder(strings.NewReader(encode(v)))
	decoder.UseNumber()
	if err := decoder.Decode(&normalized); err != nil {
		panic(err) // only validated, JSON-serializable values reach this helper
	}
	sum := sha256.Sum256([]byte(encode(normalized)))
	return hex.EncodeToString(sum[:])
}

func suiteRevision(v Suite, checks []Check) string {
	if v.Target == nil && v.Session == nil && len(v.Credentials) == 0 {
		// Unchanged from 0.4.0 so existing Deploy gate pins stay valid.
		return canonicalDigest(struct {
			ID      int64
			Release *ReleaseConfig
			Checks  []Check
		}{v.ID, v.Release, checks})
	}
	return canonicalDigest(struct {
		ID          int64
		Release     *ReleaseConfig
		Target      *TargetConfig
		Credentials map[string]CredentialRef
		Session     *SessionConfig
		Checks      []Check
	}{v.ID, v.Release, v.Target, v.Credentials, v.Session, checks})
}

func (r *ReleaseConfig) allowsRead(app, tool string) bool {
	for _, permitted := range r.ReadTools[app] {
		if permitted == tool {
			return true
		}
	}
	return false
}

func validProbe(p ToolProbe) bool {
	return slugPattern.MatchString(p.App) && slugPattern.MatchString(p.Tool) &&
		(p.Path == "" || strings.HasPrefix(p.Path, "/"))
}

func (r *ReleaseConfig) validate() error {
	if r.Browser != nil {
		if err := r.Browser.validate(); err != nil {
			return err
		}
	}
	if !slugPattern.MatchString(r.EnvironmentID) || r.EnvironmentRevision < 1 ||
		!slugPattern.MatchString(r.GatewayApp) || !validProbe(r.IdentityProbe) ||
		!r.allowsRead(r.IdentityProbe.App, r.IdentityProbe.Tool) {
		return errors.New("release needs a pinned environment, Gateway app and allowlisted candidate digest probe")
	}
	if r.GatewayPath == "" || r.GatewayPath == "/" || len(r.AppVersions) == 0 || len(r.AppVersions) > 24 || r.AppVersions[r.GatewayApp] == "" {
		return errors.New("release requires a Gateway path prefix and exact versions for its runtime apps")
	}
	for app, version := range r.AppVersions {
		if !slugPattern.MatchString(app) || strings.TrimSpace(version) != version || version == "" || len(version) > 64 {
			return errors.New("invalid runtime app version pin")
		}
	}
	if err := validateGatewayPath(r.GatewayPath); err != nil {
		return err
	}
	for name, id := range r.Identities {
		if name == "anonymous" || !slugPattern.MatchString(name) || !validProbe(id.ToolProbe) {
			return fmt.Errorf("invalid identity %q", name)
		}
		switch id.Header {
		case "Authorization", "X-API-Key", "Cookie":
		default:
			return fmt.Errorf("unsupported identity header for %s", name)
		}
		if strings.ContainsAny(id.Prefix, "\r\n") || len(id.Prefix) > 32 {
			return fmt.Errorf("invalid identity prefix for %s", name)
		}
	}
	if len(r.OpenAPI) > 0 {
		_, err := parseContract(r.OpenAPI)
		return err
	}
	return nil
}

func checkFixture(def environmentDefinition, release *ReleaseConfig) error {
	switch {
	case def.ID != release.EnvironmentID:
		return fmt.Errorf("environment ID mismatch: pinned %q, Environments returned %q", release.EnvironmentID, def.ID)
	case def.SpecVersion != release.EnvironmentRevision:
		return fmt.Errorf("environment %s: pinned revision v%d, found v%d", release.EnvironmentID, release.EnvironmentRevision, def.SpecVersion)
	case len(def.Spec) == 0 || string(def.Spec) == "null":
		return fmt.Errorf("environment %s v%d has an empty spec", release.EnvironmentID, def.SpecVersion)
	case len(def.Spec) > maxPayload:
		return fmt.Errorf("environment %s v%d spec is %d KiB; hand-written release suites accept up to %d KiB (use a suite target to pin by digest instead)", release.EnvironmentID, def.SpecVersion, (len(def.Spec)+1023)>>10, maxPayload>>10)
	}
	var policy fixturePolicy
	if err := json.Unmarshal(def.Spec, &policy); err != nil {
		return errors.New("invalid environment fixture")
	}
	if len(policy.AppInstallIDs) == 0 || len(policy.AppInstallIDs) > len(release.AppVersions) || policy.NetworkMode != "block" ||
		policy.IntegrationMode != "mock" || len(policy.AllowHostSuffixes) > 0 ||
		len(policy.ConnectionIDs) > 0 || len(policy.ConnectionBindings) > 0 ||
		len(policy.MCPServerIDs) > 0 || policy.TTLSeconds < 60 || policy.TTLSeconds > 900 {
		return errors.New("release environment requires app installs, block networking, mock integrations, no live connections or MCP servers, and a TTL of 60–900 seconds")
	}
	return nil
}
