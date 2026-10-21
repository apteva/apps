package sdk

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// SetupSpec describes optional guided setup. Roles and fields remain in their
// existing manifest declarations; these references never grant permissions.
type SetupSpec struct {
	Features []SetupFeature `yaml:"features" json:"features"`
}
type SetupFeature struct {
	ID          string             `yaml:"id" json:"id"`
	Label       string             `yaml:"label" json:"label"`
	Description string             `yaml:"description,omitempty" json:"description,omitempty"`
	Default     bool               `yaml:"default,omitempty" json:"default,omitempty"`
	Requires    []SetupRequirement `yaml:"requires,omitempty" json:"requires,omitempty"`
	Fields      []string           `yaml:"fields,omitempty" json:"fields,omitempty"`
	Configure   *SetupConfigure    `yaml:"configure,omitempty" json:"configure,omitempty"`
	Readiness   *SetupReadiness    `yaml:"readiness,omitempty" json:"readiness,omitempty"`
}
type SetupRequirement struct {
	Role    string `yaml:"role" json:"role"`
	Feature string `yaml:"feature,omitempty" json:"feature,omitempty"`
}
type SetupConfigure struct {
	Entry string `yaml:"entry" json:"entry"`
}
type SetupReadiness struct {
	Route string `yaml:"route" json:"route"`
}

// SetupStatus is returned by an app's read-only readiness GET route. Do not
// return secrets. Missing/failed checks never mean ready. Checks must not mutate.
type SetupStatus struct {
	Status  string `json:"status"` // ready | needs_setup | pending | error
	Message string `json:"message,omitempty"`
}

// ValidSetupPath restricts app-owned setup destinations to local HTTP routes.
func ValidSetupPath(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") &&
		!strings.ContainsAny(raw, "\\\r\n") && u.Host == "" && u.Scheme == "" && u.RawQuery == "" && u.Fragment == "" &&
		u.Path == raw && u.RawPath == "" && path.Clean(u.Path) == u.Path && u.Path != "/"
}

var setupID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func validateSetup(m *Manifest) error {
	roles := map[string]string{}
	for _, d := range m.Requires.Integrations {
		roles[d.Role] = d.Kind
	}
	for _, d := range m.Requires.Apps {
		if _, exists := roles[d.Name]; !exists {
			roles[d.Name] = "app"
		}
	}
	for _, c := range m.ConfigSchema {
		if c.AppRole != "" && (c.Type != "select_from_app" || roles[c.AppRole] != "app") {
			return fmt.Errorf("config field %q: app_role must reference an app role for select_from_app", c.Name)
		}
	}
	if m.Setup == nil {
		return nil
	}
	if len(m.Setup.Features) == 0 || len(m.Setup.Features) > 32 {
		return fmt.Errorf("setup.features must contain 1–32 features")
	}
	fields := map[string]bool{}
	for _, f := range m.ConfigSchema {
		fields[f.Name] = true
	}
	seen := map[string]bool{}
	for _, f := range m.Setup.Features {
		if !setupID.MatchString(f.ID) || seen[f.ID] || strings.TrimSpace(f.Label) == "" {
			return fmt.Errorf("setup feature %q needs a unique valid id and label", f.ID)
		}
		seen[f.ID] = true
		refs := map[string]bool{}
		for _, r := range f.Requires {
			kind, exists := roles[r.Role]
			if !exists || refs[r.Role] {
				return fmt.Errorf("setup feature %q references unknown or repeated role %q", f.ID, r.Role)
			}
			refs[r.Role] = true
			if r.Feature != "" && (kind != "app" || !setupID.MatchString(r.Feature)) {
				return fmt.Errorf("setup feature %q: dependency feature requires an app role and valid feature id", f.ID)
			}
		}
		for _, name := range f.Fields {
			if !fields[name] {
				return fmt.Errorf("setup feature %q references unknown field %q", f.ID, name)
			}
		}
		for _, route := range []string{setupConfigurePath(f), setupReadinessPath(f)} {
			if route != "" && !ValidSetupPath(route) {
				return fmt.Errorf("setup feature %q has an invalid local route", f.ID)
			}
		}
		if f.Configure != nil && f.Configure.Entry == "" || f.Readiness != nil && f.Readiness.Route == "" {
			return fmt.Errorf("setup feature %q has an empty route", f.ID)
		}
	}
	return nil
}
func setupConfigurePath(f SetupFeature) string {
	if f.Configure == nil {
		return ""
	}
	return f.Configure.Entry
}
func setupReadinessPath(f SetupFeature) string {
	if f.Readiness == nil {
		return ""
	}
	return f.Readiness.Route
}
