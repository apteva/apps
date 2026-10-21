package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var (
	ErrContributionsUnsupported = errors.New("platform contribution API unsupported")
	ErrContributionAccessDenied = errors.New("contribution access denied")
	ErrContributionUnavailable  = errors.New("contribution unavailable")
)

// ContributionClient is optional to keep PlatformClient and app mocks source
// compatible. Its HTTP protocol requires server support; older servers return
// an explicit unsupported error. Type assertion alone does not prove support.
type ContributionClient interface {
	DiscoverContributions(context.Context, ContributionQuery) (*ContributionPage, error)
	ReadAppExport(context.Context, AppExportReadRequest, any) error
}

type ContributionQuery struct {
	ProjectID    string `json:"project_id"`
	Kind         string `json:"kind,omitempty"`
	Contract     string `json:"contract,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	Slot         string `json:"slot,omitempty"`
	Cursor       string `json:"cursor,omitempty"`
	Limit        int    `json:"limit,omitempty"` // 0 = server default; maximum 200
}

type ContributionPage struct {
	Items      []AppContribution `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func (p ContributionPage) Validate() error {
	if p.Items == nil || len(p.Items) > AppExportMaxItems {
		return errors.New("invalid contribution page")
	}
	seen := map[string]bool{}
	for _, item := range p.Items {
		if err := item.Validate(); err != nil {
			return err
		}
		key := fmt.Sprintf("%d/%s/%s", item.InstallID, item.Kind, item.ID)
		if seen[key] {
			return errors.New("duplicate contribution identity")
		}
		seen[key] = true
	}
	return nil
}

// AppExportReadRequest selects a declaration by identity, never an arbitrary
// URL. The server resolves its GET endpoint, contract and required grants.
// Resource is an optional exact entity filter; apps must authorize it separately.
type AppExportReadRequest struct {
	ProjectID string           `json:"project_id"`
	InstallID int64            `json:"app_install_id"`
	ExportID  string           `json:"export_id"`
	Cursor    string           `json:"cursor,omitempty"`
	Limit     int              `json:"limit,omitempty"`
	Resource  *ExportEntityRef `json:"resource,omitempty"`
}

func (c *AppCtx) ContributionsAPI() ContributionClient {
	if c == nil || c.platform == nil {
		return nil
	}
	if scoped, ok := c.platform.(*projectScopedClient); ok {
		if _, ok := scoped.inner.(ContributionClient); !ok {
			return nil
		}
	}
	api, _ := c.platform.(ContributionClient)
	return api
}

func validateContributionPageRequest(project string, limit int) error {
	if strings.TrimSpace(project) == "" {
		return errors.New("contribution request requires project_id")
	}
	if limit < 0 || limit > AppExportMaxItems {
		return errors.New("contribution limit must be between 0 and 200")
	}
	return nil
}

func (c *httpPlatformClient) DiscoverContributions(ctx context.Context, q ContributionQuery) (*ContributionPage, error) {
	if err := validateContributionPageRequest(q.ProjectID, q.Limit); err != nil {
		return nil, err
	}
	if q.Kind != "" || q.Contract != "" || q.ResourceType != "" || q.Slot != "" {
		if err := validateContributionSelector(q.Kind, q.Contract, q.ResourceType, q.Slot); err != nil {
			return nil, err
		}
	}
	params := url.Values{"project_id": {q.ProjectID}}
	for key, value := range map[string]string{"kind": q.Kind, "contract": q.Contract, "resource_type": q.ResourceType, "slot": q.Slot, "cursor": q.Cursor} {
		if value != "" {
			params.Set(key, value)
		}
	}
	if q.Limit > 0 {
		params.Set("limit", strconv.Itoa(q.Limit))
	}
	var out ContributionPage
	err := c.contributionRequest(ctx, http.MethodGet, "/api/apps/callback/contributions?"+params.Encode(), nil, &out, true)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ReadAppExport(ctx context.Context, req AppExportReadRequest, out any) error {
	if err := validateContributionPageRequest(req.ProjectID, req.Limit); err != nil {
		return err
	}
	if req.InstallID <= 0 || !isSlug(req.ExportID) || out == nil {
		return errors.New("export read requires positive app_install_id, export_id and output destination")
	}
	if req.Resource != nil {
		if _, err := req.Resource.InInstallation(req.InstallID); err != nil {
			return err
		}
	}
	return c.contributionRequest(ctx, http.MethodPost, "/api/apps/callback/contributions/exports/read", req, out, false)
}

func (c *httpPlatformClient) contributionRequest(ctx context.Context, method, endpoint string, body, out any, discovery bool) error {
	if ctx == nil {
		return errors.New("contribution request requires context")
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, reader)
	if err != nil {
		return err
	}
	c.addAuth(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	// Do not follow redirects with an installation token/user delegation.
	client := *c.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrContributionAccessDenied
	case http.StatusNotImplemented:
		return ErrContributionsUnsupported
	case http.StatusNotFound:
		if discovery {
			return ErrContributionsUnsupported
		}
		return ErrContributionUnavailable
	case http.StatusGone, http.StatusServiceUnavailable:
		return ErrContributionUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("contribution API returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, AppExportMaxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > AppExportMaxBytes {
		return errors.New("contribution response exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	switch out.(type) {
	case *SummaryExport, *EntitiesExport:
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("contribution response must contain exactly one JSON value")
	}
	if validator, ok := out.(interface{ Validate() error }); ok {
		return validator.Validate()
	}
	return nil
}

func (p *projectScopedClient) contributionProject(project string) (string, error) {
	if project != "" && project != p.projectID {
		return "", errors.New("contribution request is outside the current project")
	}
	return p.projectID, nil
}

func (p *projectScopedClient) DiscoverContributions(ctx context.Context, q ContributionQuery) (*ContributionPage, error) {
	api, ok := p.inner.(ContributionClient)
	if !ok {
		return nil, ErrContributionsUnsupported
	}
	var err error
	q.ProjectID, err = p.contributionProject(q.ProjectID)
	if err != nil {
		return nil, err
	}
	return api.DiscoverContributions(ctx, q)
}

func (p *projectScopedClient) ReadAppExport(ctx context.Context, req AppExportReadRequest, out any) error {
	api, ok := p.inner.(ContributionClient)
	if !ok {
		return ErrContributionsUnsupported
	}
	var err error
	req.ProjectID, err = p.contributionProject(req.ProjectID)
	if err != nil {
		return err
	}
	return api.ReadAppExport(ctx, req, out)
}
