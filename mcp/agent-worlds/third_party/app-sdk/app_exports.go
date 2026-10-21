package sdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	AppExportMaxBytes = 1 << 20
	AppExportMaxItems = 200
)

// ExportMetadata identifies the contract and freshness of one snapshot/page.
// Revision is an app-owned opaque value, suitable for equality checks. Events
// invalidate snapshots; the latest snapshot is authoritative after reconnect.
type ExportMetadata struct {
	Contract  string `json:"contract"`
	Revision  string `json:"revision,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"` // RFC3339
}

type SummaryExport struct {
	ExportMetadata
	Sections []ExportSection `json:"sections"`
}

type ExportSection struct {
	ID      string         `json:"id"`
	Label   string         `json:"label"`
	Kind    string         `json:"kind,omitempty"` // semantic hint; unknown kinds use a generic renderer
	Icon    string         `json:"icon,omitempty"` // semantic icon, not HTML or executable code
	Status  string         `json:"status,omitempty"`
	Metrics []ExportMetric `json:"metrics,omitempty"`
	Links   []ExportLink   `json:"links,omitempty"`
}

type ExportMetric struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Value  any    `json:"value"` // JSON scalar (string, number, boolean or null)
	Format string `json:"format,omitempty"`
	Unit   string `json:"unit,omitempty"`
}

// UnmarshalJSON distinguishes an explicit null value from a missing required
// value, while keeping Value convenient for app authors constructing metrics.
func (m *ExportMetric) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID     string          `json:"id"`
		Label  string          `json:"label"`
		Value  json.RawMessage `json:"value"`
		Format string          `json:"format,omitempty"`
		Unit   string          `json:"unit,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if len(wire.Value) == 0 {
		return errors.New("metric value required")
	}
	var value any
	decoder = json.NewDecoder(bytes.NewReader(wire.Value))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*m = ExportMetric{ID: wire.ID, Label: wire.Label, Value: value, Format: wire.Format, Unit: wire.Unit}
	return nil
}

// ExportLink is navigation only. Path is relative to the owning app; a host
// resolves it through the authenticated app proxy and adds trusted scope.
// Mutation buttons continue to use the app's existing UI/action contracts.
type ExportLink struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// ExportEntityRef is local to the producing installation. Consumers construct
// AppResourceRef using the trusted discovery envelope's InstallID, never a
// different installation asserted inside a producer payload.
type ExportEntityRef struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
}

func (r ExportEntityRef) InInstallation(installID int64) (AppResourceRef, error) {
	ref := AppResourceRef{InstallID: installID, ResourceType: r.ResourceType, ResourceID: r.ResourceID}
	return ref, ref.Validate()
}

type EntitiesExport struct {
	ExportMetadata
	Items      []ExportEntity `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

type ExportEntity struct {
	Ref           ExportEntityRef      `json:"ref"`
	Label         string               `json:"label"`
	Kind          string               `json:"kind,omitempty"`
	Icon          string               `json:"icon,omitempty"`
	Status        string               `json:"status,omitempty"`
	Parent        *ExportEntityRef     `json:"parent,omitempty"`
	Relationships []ExportRelationship `json:"relationships,omitempty"`
	Metrics       []ExportMetric       `json:"metrics,omitempty"`
	Links         []ExportLink         `json:"links,omitempty"`
}

type ExportRelationship struct {
	Kind   string          `json:"kind"`
	Target ExportEntityRef `json:"target"`
}

func validateExportMetadata(meta ExportMetadata, contract string) error {
	if meta.Contract != contract {
		return fmt.Errorf("expected export contract %s", contract)
	}
	if meta.UpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339, meta.UpdatedAt); err != nil {
			return errors.New("updated_at must be RFC3339")
		}
	}
	return nil
}

func (s SummaryExport) Validate() error {
	if err := validateExportMetadata(s.ExportMetadata, SummaryExportContract); err != nil {
		return err
	}
	if s.Sections == nil || len(s.Sections) > AppExportMaxItems {
		return errors.New("sections must be an array with at most 200 entries")
	}
	seen := map[string]bool{}
	for _, section := range s.Sections {
		if !isSlug(section.ID) || strings.TrimSpace(section.Label) == "" || seen[section.ID] {
			return errors.New("sections require unique slug IDs and labels")
		}
		seen[section.ID] = true
		if err := validateExportDetails(section.Metrics, section.Links); err != nil {
			return err
		}
	}
	return nil
}

func (s EntitiesExport) Validate() error {
	if err := validateExportMetadata(s.ExportMetadata, EntitiesExportContract); err != nil {
		return err
	}
	if s.Items == nil || len(s.Items) > AppExportMaxItems {
		return errors.New("items must be an array with at most 200 entries")
	}
	seen := map[ExportEntityRef]bool{}
	for _, item := range s.Items {
		if _, err := item.Ref.InInstallation(1); err != nil {
			return err
		}
		if strings.TrimSpace(item.Label) == "" || seen[item.Ref] {
			return errors.New("entities require labels and unique resource references")
		}
		seen[item.Ref] = true
		if item.Parent != nil {
			if _, err := item.Parent.InInstallation(1); err != nil {
				return err
			}
			if *item.Parent == item.Ref {
				return errors.New("entity cannot be its own parent")
			}
		}
		if len(item.Relationships) > AppExportMaxItems {
			return errors.New("too many entity relationships")
		}
		for _, rel := range item.Relationships {
			if strings.TrimSpace(rel.Kind) == "" {
				return errors.New("relationship kind required")
			}
			if _, err := rel.Target.InInstallation(1); err != nil {
				return err
			}
		}
		if err := validateExportDetails(item.Metrics, item.Links); err != nil {
			return err
		}
	}
	return nil
}

func validateExportDetails(metrics []ExportMetric, links []ExportLink) error {
	if len(metrics) > AppExportMaxItems || len(links) > AppExportMaxItems {
		return errors.New("too many export metrics or links")
	}
	seen := map[string]bool{}
	for _, metric := range metrics {
		if !isSlug(metric.ID) || strings.TrimSpace(metric.Label) == "" || seen[metric.ID] {
			return errors.New("metrics require unique slug IDs and labels")
		}
		seen[metric.ID] = true
		value, err := json.Marshal(metric.Value)
		if err != nil {
			return fmt.Errorf("metric value: %w", err)
		}
		if len(value) == 0 || value[0] == '{' || value[0] == '[' {
			return errors.New("metric value must be a JSON scalar")
		}
	}
	for _, link := range links {
		u, err := url.Parse(link.Path)
		if err != nil || strings.TrimSpace(link.Label) == "" || u.Scheme != "" || u.Host != "" || u.Fragment != "" || !validExportPath(u.EscapedPath()) {
			return errors.New("links require a label and an app-relative path")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return errors.New("invalid export link query")
		}
		for key := range query {
			if key == "project_id" || key == "install_id" || key == "_project_id" {
				return errors.New("export links must not supply platform scope")
			}
		}
	}
	return nil
}

// ParseAppExport decodes a shared contract, rejects unknown fields/trailing JSON,
// and validates its semantics. Custom namespaced contracts use their own parser.
func ParseAppExport(data []byte, contract string) (any, error) {
	if len(data) > AppExportMaxBytes {
		return nil, errors.New("export exceeds 1 MiB")
	}
	var out interface{ Validate() error }
	switch contract {
	case SummaryExportContract:
		out = &SummaryExport{}
	case EntitiesExportContract:
		out = &EntitiesExport{}
	default:
		return nil, fmt.Errorf("unsupported shared export contract %q", contract)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("export must contain exactly one JSON value")
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// AppExportRoute creates an authenticated GET route for use in HTTPRoutes().
// The handler MUST authorize/filter records using the platform-authenticated
// request identity before returning data. This helper does not grant access.
// Register the endpoint under provides.http_routes as with any other app route.
func AppExportRoute(declaration AppExport, handler func(*http.Request) (any, error)) (Route, error) {
	if !validExportPath(declaration.Endpoint) || !exportContractPattern.MatchString(declaration.Contract) || handler == nil {
		return Route{}, errors.New("export route requires app-relative endpoint, contract and handler")
	}
	return Route{Method: http.MethodGet, Pattern: declaration.Endpoint, Handler: func(w http.ResponseWriter, r *http.Request) {
		// Keep exports private even if a broad NoAuth route or webhook carve-out
		// overlaps the endpoint. An unconfigured local token fails closed.
		token := os.Getenv("APTEVA_APP_TOKEN")
		if token == "" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		value, err := handler(r)
		if err != nil {
			// Authorization errors should be explicit without exposing internal data.
			if errors.Is(err, ErrExportAccessDenied) {
				http.Error(w, "export access denied", http.StatusForbidden)
			} else {
				http.Error(w, "export unavailable", http.StatusInternalServerError)
			}
			return
		}
		data, err := json.Marshal(value)
		if err == nil && len(data) > AppExportMaxBytes {
			err = errors.New("export exceeds 1 MiB")
		}
		if err == nil && (declaration.Contract == SummaryExportContract || declaration.Contract == EntitiesExportContract) {
			_, err = ParseAppExport(data, declaration.Contract)
		}
		if err != nil {
			http.Error(w, "invalid export response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}}, nil
}

var ErrExportAccessDenied = errors.New("export access denied")
