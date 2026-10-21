package sdk

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const (
	SummaryExportContract     = "apteva.summary/v1"
	EntitiesExportContract    = "apteva.entities/v1"
	ContributionKindExport    = "export"
	ContributionKindComponent = "component"
	ContributionKindSurface   = "surface"
)

var (
	exportContractPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]*/[a-z][a-z0-9-]*/v[1-9][0-9]*$`)
	resourceTypePattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)+$`)
)

// AppExport describes an app-relative GET endpoint. Permissions refer to this
// app's provides.permissions; ChangesOn refers to its existing publishes topics.
// Export declarations are discoverable metadata, never an access grant.
type AppExport struct {
	ID          string   `yaml:"id" json:"id"`
	Contract    string   `yaml:"contract" json:"contract"`
	Label       string   `yaml:"label" json:"label"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Endpoint    string   `yaml:"endpoint" json:"endpoint"`
	Permissions []string `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	ChangesOn   []string `yaml:"changes_on,omitempty" json:"changes_on,omitempty"`
}

// ContributionRequirement requests discovery by contract or resource renderer.
// Kind is export, component, or surface. Export consumers specify Contract;
// component consumers specify Slot and optionally ResourceType; surface consumers
// specify Slot. Optional contributions must not auto-install a producer.
type ContributionRequirement struct {
	Kind         string `yaml:"kind" json:"kind"`
	Contract     string `yaml:"contract,omitempty" json:"contract,omitempty"`
	ResourceType string `yaml:"resource_type,omitempty" json:"resource_type,omitempty"`
	Slot         string `yaml:"slot,omitempty" json:"slot,omitempty"`
	Description  string `yaml:"description,omitempty" json:"description,omitempty"`
	Optional     bool   `yaml:"optional,omitempty" json:"optional,omitempty"`
}

// AppContribution is the shared discovery envelope for existing UI declarations
// and new data exports. Exactly one declaration corresponds to Kind. A server
// must filter this catalog using project, caller, visibility and grants before
// returning it; disabled or inaccessible installations must not be advertised.
type AppContribution struct {
	InstallID int64        `json:"install_id"`
	ProjectID string       `json:"project_id,omitempty"`
	App       string       `json:"app"`
	Version   string       `json:"version"`
	Kind      string       `json:"kind"`
	ID        string       `json:"id"`
	Export    *AppExport   `json:"export,omitempty"`
	Component *UIComponent `json:"component,omitempty"`
	Surface   *UISurface   `json:"surface,omitempty"`
}

func (c AppContribution) Validate() error {
	if c.InstallID <= 0 || !isSlug(c.App) || c.Version == "" || !isSlug(c.ID) {
		return errors.New("contribution requires installation, app, version and ID")
	}
	count := 0
	if c.Export != nil {
		count++
	}
	if c.Component != nil {
		count++
	}
	if c.Surface != nil {
		count++
	}
	if count != 1 {
		return errors.New("contribution requires exactly one declaration")
	}
	switch c.Kind {
	case ContributionKindExport:
		if c.Export == nil || c.ID != c.Export.ID || strings.TrimSpace(c.Export.Label) == "" || !exportContractPattern.MatchString(c.Export.Contract) || !validExportPath(c.Export.Endpoint) {
			return errors.New("invalid export contribution")
		}
	case ContributionKindComponent:
		if c.Component == nil || c.ID != c.Component.Name {
			return errors.New("invalid component contribution")
		}
		if err := validateUIComponents([]UIComponent{*c.Component}); err != nil {
			return err
		}
		for _, name := range c.Component.ResourceTypes {
			if !resourceTypePattern.MatchString(name) {
				return errors.New("invalid component resource_type")
			}
		}
		if len(c.Component.ResourceTypes) > 0 && !validExportPath(c.Component.Entry) {
			return errors.New("resource renderer entry must be app-relative")
		}
	case ContributionKindSurface:
		if c.Surface == nil || c.ID != c.Surface.ID {
			return errors.New("invalid surface contribution")
		}
		if err := validateUISurfaces([]UISurface{*c.Surface}); err != nil {
			return err
		}
	default:
		return errors.New("unknown contribution kind")
	}
	return nil
}

// ManifestContributions flattens declarations without replacing their identity.
// It is for platform implementers, not an authorization filter. ProjectID is
// the installation's owning project (empty for a global installation).
func ManifestContributions(m *Manifest, installID int64, projectID string) ([]AppContribution, error) {
	if m == nil || installID <= 0 {
		return nil, errors.New("contributions require manifest and positive install ID")
	}
	// Validate using the standard manifest rules and defaults.
	copyManifest := *m
	if err := ValidateManifest(&copyManifest); err != nil {
		return nil, err
	}
	out := make([]AppContribution, 0, len(m.Provides.Exports)+len(m.Provides.UIComponents)+len(m.Provides.UISurfaces))
	base := AppContribution{InstallID: installID, ProjectID: projectID, App: m.Name, Version: m.Version}
	for _, value := range m.Provides.Exports {
		item := base
		item.Kind, item.ID, item.Export = ContributionKindExport, value.ID, &value
		out = append(out, item)
	}
	for _, value := range m.Provides.UIComponents {
		item := base
		item.Kind, item.ID, item.Component = ContributionKindComponent, value.Name, &value
		out = append(out, item)
	}
	for _, value := range m.Provides.UISurfaces {
		item := base
		item.Kind, item.ID, item.Surface = ContributionKindSurface, value.ID, &value
		out = append(out, item)
	}
	return out, nil
}

// AppResourceRef can be included in a tool result or a chat attachment. It is
// a locator, never proof of ownership/access. Hosts resolve only against this
// installation and must authorize the referenced record on every fetch/action.
type AppResourceRef struct {
	InstallID    int64  `json:"app_install_id"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
}

func (r AppResourceRef) Validate() error {
	if r.InstallID <= 0 || !resourceTypePattern.MatchString(r.ResourceType) || strings.TrimSpace(r.ResourceID) == "" || len(r.ResourceID) > 1024 {
		return errors.New("resource reference requires positive app_install_id, namespaced resource_type and resource_id (up to 1024 bytes)")
	}
	return nil
}

// ResourceRenderers returns every matching component from an already authorized
// catalog. It never silently chooses the first ambiguous match. Hosts may let
// the user choose or fall back to a standard link. Existing explicit props-based
// attachments remain valid and do not need a resource reference.
func ResourceRenderers(items []AppContribution, ref AppResourceRef, slot string) ([]AppContribution, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if slot == "" {
		return nil, errors.New("renderer matching requires a slot")
	}
	out := []AppContribution{}
	for _, item := range items {
		if err := item.Validate(); err != nil {
			return nil, err
		}
		if item.Kind == ContributionKindComponent && item.InstallID == ref.InstallID && item.Component != nil &&
			containsContributionString(item.Component.Slots, slot) && containsContributionString(item.Component.ResourceTypes, ref.ResourceType) {
			out = append(out, item)
		}
	}
	return out, nil
}

func containsContributionString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func validateContributions(m *Manifest) error {
	if m == nil {
		return errors.New("manifest required")
	}
	permissions := map[string]bool{}
	for _, p := range m.Provides.ProvidedPermissions {
		permissions[p.Name] = true
	}
	checkPermissions := func(values []string, prefix string) error {
		seen := map[string]bool{}
		for _, value := range values {
			if !permissions[value] {
				return fmt.Errorf("%s.permissions %q not declared in provides.permissions", prefix, value)
			}
			if seen[value] {
				return fmt.Errorf("%s.permissions contains duplicate %q", prefix, value)
			}
			seen[value] = true
		}
		return nil
	}
	seen := map[string]bool{}
	for i, export := range m.Provides.Exports {
		prefix := fmt.Sprintf("provides.exports[%d]", i)
		if !isSlug(export.ID) {
			return fmt.Errorf("%s.id must be a lowercase slug", prefix)
		}
		if seen[export.ID] {
			return fmt.Errorf("provides.exports: duplicate id %q", export.ID)
		}
		seen[export.ID] = true
		if !exportContractPattern.MatchString(export.Contract) {
			return fmt.Errorf("%s.contract must be namespace/name/vN", prefix)
		}
		if strings.TrimSpace(export.Label) == "" {
			return fmt.Errorf("%s.label required", prefix)
		}
		if !validExportPath(export.Endpoint) {
			return fmt.Errorf("%s.endpoint must be a traversal-free app-relative path without query or fragment", prefix)
		}
		if err := checkPermissions(export.Permissions, prefix); err != nil {
			return err
		}
		topics := map[string]bool{}
		for _, topic := range export.ChangesOn {
			declared := false
			for _, event := range m.Provides.Publishes {
				if topic == event.Name || (event.Dynamic && strings.HasSuffix(event.Name, ".*") && strings.HasPrefix(topic, strings.TrimSuffix(event.Name, "*"))) {
					declared = true
					break
				}
			}
			if strings.TrimSpace(topic) == "" || !declared {
				return fmt.Errorf("%s.changes_on %q not declared in provides.publishes", prefix, topic)
			}
			if topics[topic] {
				return fmt.Errorf("%s.changes_on contains duplicate %q", prefix, topic)
			}
			topics[topic] = true
		}
	}
	for i, component := range m.Provides.UIComponents {
		prefix := fmt.Sprintf("provides.ui_components[%d]", i)
		types := map[string]bool{}
		for _, name := range component.ResourceTypes {
			if !resourceTypePattern.MatchString(name) {
				return fmt.Errorf("%s.resource_types requires namespaced types (e.g. crm.ticket)", prefix)
			}
			if types[name] {
				return fmt.Errorf("%s.resource_types contains duplicate %q", prefix, name)
			}
			types[name] = true
		}
		if len(types) > 0 && !validExportPath(component.Entry) {
			return fmt.Errorf("%s resource renderer entry must be app-relative and traversal-free", prefix)
		}
		if err := checkPermissions(component.Permissions, prefix); err != nil {
			return err
		}
	}
	requirements := map[string]bool{}
	for i, req := range m.Requires.Contributions {
		prefix := fmt.Sprintf("requires.contributions[%d]", i)
		if err := validateContributionSelector(req.Kind, req.Contract, req.ResourceType, req.Slot); err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
		key := req.Kind + "|" + req.Contract + "|" + req.ResourceType + "|" + req.Slot
		if requirements[key] {
			return fmt.Errorf("%s duplicates a contribution requirement", prefix)
		}
		requirements[key] = true
	}
	if len(m.Requires.Contributions) > 0 {
		allowed := false
		for _, permission := range m.Requires.Permissions {
			if permission == PermContributionsRead {
				allowed = true
			}
		}
		if !allowed {
			return errors.New("requires.contributions needs platform.contributions.read permission")
		}
	}
	return nil
}

func validateContributionSelector(kind, contract, resourceType, slot string) error {
	switch kind {
	case ContributionKindExport:
		if !exportContractPattern.MatchString(contract) || resourceType != "" || slot != "" {
			return errors.New("export selector requires contract only (namespace/name/vN)")
		}
	case ContributionKindComponent:
		if contract != "" || !knownComponentContributionSlot(slot) || (resourceType != "" && !resourceTypePattern.MatchString(resourceType)) {
			return errors.New("component selector requires supported slot and optional namespaced resource_type")
		}
	case ContributionKindSurface:
		if contract != "" || resourceType != "" || slot != UISurfaceSlotMobileProjectApp {
			return errors.New("surface selector requires supported slot only")
		}
	default:
		return errors.New("contribution kind must be export, component or surface")
	}
	return nil
}

func knownComponentContributionSlot(slot string) bool {
	switch slot {
	case UIComponentSlotChatMessageAttachment, UIComponentSlotDashboardHome, UIComponentSlotDashboardBuild,
		UIComponentSlotDashboardAgentCard, UIComponentSlotDashboardAgentDetail, UIComponentSlotDashboardThreadSidebar, UIComponentSlotToolDetailsPopover:
		return true
	}
	return false
}

func validExportPath(value string) bool {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || hasPathTraversal(value) {
		return false
	}
	// Reject encoded separators/queries as well as literal external URLs.
	for range 4 {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "" || u.Host != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(value, "?#\\") || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || path.Clean(u.Path) != u.Path {
			return false
		}
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return false
		}
		if decoded == value {
			return true
		}
		value = decoded
	}
	return false
}
