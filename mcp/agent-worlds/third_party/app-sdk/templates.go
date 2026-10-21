package sdk

import (
	"encoding/json"
	"errors"
	"strings"
)

const ProjectSetupTemplateKind = "project_setup"

// ProjectTemplate is the stable generic envelope returned by the platform.
// Definition is intentionally raw so future template kinds do not require an
// SDK release. DecodeProjectSetup provides the typed contract for today's
// project_setup kind.
type ProjectTemplate struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Source         string          `json:"source"`
	OwnerProjectID string          `json:"owner_project_id,omitempty"`
	SchemaVersion  int             `json:"schema_version"`
	Revision       int             `json:"revision,omitempty"`
	Definition     json.RawMessage `json:"definition"`
}

type ProjectTemplateListOptions struct {
	IncludeSystem bool
}

type ProjectSetupTemplateAgent struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Directive   string   `json:"directive"`
	Mode        string   `json:"mode"`
	Unconscious bool     `json:"unconscious,omitempty"`
	Apps        []string `json:"apps,omitempty"`
}

type ProjectSetupTemplateWidget struct {
	ID        string         `json:"id"`
	Component string         `json:"component"`
	Size      string         `json:"size"`
	Settings  map[string]any `json:"settings,omitempty"`
}

type ProjectSetupTemplateDefinition struct {
	Category        string                       `json:"category"`
	Match           []string                     `json:"match,omitempty"`
	Agents          []ProjectSetupTemplateAgent  `json:"agents"`
	Dashboard       []string                     `json:"dashboard,omitempty"`
	DashboardLayout []ProjectSetupTemplateWidget `json:"dashboard_layout,omitempty"`
}

func (t ProjectTemplate) DecodeProjectSetup() (*ProjectSetupTemplateDefinition, error) {
	if strings.TrimSpace(t.Kind) != ProjectSetupTemplateKind {
		return nil, errors.New("template is not a project_setup template")
	}
	var definition ProjectSetupTemplateDefinition
	if err := json.Unmarshal(t.Definition, &definition); err != nil {
		return nil, err
	}
	return &definition, nil
}

// ProjectTemplateClient is an optional read-only capability. Keeping it out
// of PlatformClient preserves compatibility with existing app test doubles.
type ProjectTemplateClient interface {
	ListProjectTemplates(projectID string, options ProjectTemplateListOptions) ([]ProjectTemplate, error)
	GetProjectTemplate(projectID, templateID string) (*ProjectTemplate, error)
}

type projectScopedTemplateClient struct {
	inner     ProjectTemplateClient
	projectID string
}

func (c *projectScopedTemplateClient) scopedProject(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return c.projectID, nil
	}
	if requested != c.projectID {
		return "", errors.New("project template request is outside the current project scope")
	}
	return requested, nil
}

func (c *projectScopedTemplateClient) ListProjectTemplates(projectID string, options ProjectTemplateListOptions) ([]ProjectTemplate, error) {
	projectID, err := c.scopedProject(projectID)
	if err != nil {
		return nil, err
	}
	return c.inner.ListProjectTemplates(projectID, options)
}

func (c *projectScopedTemplateClient) GetProjectTemplate(projectID, templateID string) (*ProjectTemplate, error) {
	projectID, err := c.scopedProject(projectID)
	if err != nil {
		return nil, err
	}
	return c.inner.GetProjectTemplate(projectID, templateID)
}
