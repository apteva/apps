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
	CandidateDigest string            `json:"candidate_digest"`
	ObservedDigest  string            `json:"observed_digest,omitempty"`
	SuiteRevision   string            `json:"suite_revision"`
	FixtureRevision string            `json:"fixture_revision"`
	AppVersions     map[string]string `json:"app_versions,omitempty"`
	OpenAPIDigest   string            `json:"openapi_digest,omitempty"`
	EnvironmentRun  string            `json:"environment_run_id,omitempty"`
	RuntimeID       string            `json:"runtime_id,omitempty"`
	Covered         []string          `json:"covered_operations,omitempty"`
	Missing         []string          `json:"missing_operations,omitempty"`
}

type QueueOptions struct {
	CandidateDigest string `json:"candidate_digest"`
	SuiteRevision   string `json:"suite_revision"`
	FixtureRevision string `json:"fixture_revision"`
}

type environmentDefinition struct {
	ID          string          `json:"id"`
	SpecVersion int             `json:"spec_version"`
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
	return canonicalDigest(struct {
		ID      int64
		Release *ReleaseConfig
		Checks  []Check
	}{v.ID, v.Release, checks})
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
	if def.ID != release.EnvironmentID || def.SpecVersion != release.EnvironmentRevision ||
		len(def.Spec) == 0 || len(def.Spec) > maxPayload {
		return errors.New("environment definition does not match the pinned fixture revision")
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
