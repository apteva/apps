package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// messageTemplateProvider isolates upstream template APIs from Messaging's
// project-owned template model. Provider catalogs are account-wide; rows only
// enter Messaging after an explicit project import.
type messageTemplateProvider interface {
	Key() string
	Label() string
	List(*sdk.AppCtx, int64) ([]providerTemplateInfo, error)
	Create(*sdk.AppCtx, int64, providerTemplateCreateInput) (*providerTemplateCreate, error)
	Submit(*sdk.AppCtx, int64, string, string, string) (string, error)
	Delete(*sdk.AppCtx, int64, string) (deleted bool, alreadyGone bool, err error)
}

type providerTemplateCreateInput struct {
	Name      string
	Language  string
	BodyText  string
	Variables map[string]any
}

func resolveMessageTemplateProvider(ctx *sdk.AppCtx) (messageTemplateProvider, *sdk.BoundIntegration, error) {
	bound := ctx.IntegrationFor("phone_provider")
	if bound == nil {
		return nil, nil, errors.New("no phone_provider bound - install or select a phone provider connection")
	}
	switch strings.ToLower(strings.TrimSpace(bound.AppSlug)) {
	case "", "twilio":
		// Empty is retained for older platform builds that did not expose the
		// bound connection slug. Twilio is currently the only compatible phone
		// provider declared by Messaging.
		return twilioMessageTemplateProvider{}, bound, nil
	default:
		return nil, nil, fmt.Errorf("phone provider %q does not support Messaging template catalogs", bound.AppSlug)
	}
}

type twilioMessageTemplateProvider struct{}

func (twilioMessageTemplateProvider) Key() string   { return "twilio" }
func (twilioMessageTemplateProvider) Label() string { return "Twilio" }

func (twilioMessageTemplateProvider) List(ctx *sdk.AppCtx, connectionID int64) ([]providerTemplateInfo, error) {
	const maxPages = 100
	items := make([]providerTemplateInfo, 0)
	seenProviderIDs := map[string]bool{}
	pageToken := ""
	seenTokens := map[string]bool{}

	for page := 0; page < maxPages; page++ {
		input := map[string]any{"PageSize": 500}
		if pageToken != "" {
			input["PageToken"] = pageToken
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "list_content_templates", input)
		if err != nil {
			return nil, fmt.Errorf("list provider templates: %w", err)
		}
		if res == nil || !res.Success {
			body := ""
			if res != nil {
				body = string(res.Data)
			}
			return nil, fmt.Errorf("list provider templates: provider non-2xx: %s", truncate(body, 400))
		}

		var raw struct {
			Contents []struct {
				Sid          string         `json:"sid"`
				FriendlyName string         `json:"friendly_name"`
				Language     string         `json:"language"`
				Variables    map[string]any `json:"variables"`
				Types        map[string]any `json:"types"`
				Approval     any            `json:"approval_requests"`
			} `json:"contents"`
			Meta struct {
				NextPageURL string `json:"next_page_url"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(res.Data, &raw); err != nil {
			return nil, fmt.Errorf("list provider templates: decode response: %w", err)
		}
		for _, content := range raw.Contents {
			if content.Sid == "" || seenProviderIDs[content.Sid] {
				continue
			}
			seenProviderIDs[content.Sid] = true
			status := "pending"
			category := ""
			approval := approvalInfoFromAny(content.Approval)
			if approval.Status != "" {
				status = approval.Status
				category = approval.Category
			}
			variables := content.Variables
			if variables == nil {
				variables = map[string]any{}
			}
			items = append(items, providerTemplateInfo{
				ProviderTemplateID: content.Sid,
				Name:               content.FriendlyName,
				Language:           content.Language,
				Category:           category,
				Status:             status,
				BodyText:           twilioTemplatePreviewBody(content.Types),
				Variables:          variables,
				LocalState:         "new",
			})
		}

		next := providerPageToken(raw.Meta.NextPageURL)
		if next == "" {
			return items, nil
		}
		if seenTokens[next] {
			return nil, errors.New("list provider templates: repeated pagination token")
		}
		seenTokens[next] = true
		pageToken = next
	}
	return nil, fmt.Errorf("list provider templates: exceeded %d pages", maxPages)
}

func providerPageToken(nextPageURL string) string {
	if strings.TrimSpace(nextPageURL) == "" {
		return ""
	}
	u, err := url.Parse(nextPageURL)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.Query().Get("PageToken"))
}

func twilioTemplatePreviewBody(types map[string]any) string {
	if textType, ok := types["twilio/text"].(map[string]any); ok {
		if body, ok := textType["body"].(string); ok {
			return body
		}
	}
	for _, rawType := range types {
		if contentType, ok := rawType.(map[string]any); ok {
			if body, ok := contentType["body"].(string); ok {
				return body
			}
		}
	}
	return ""
}

func (twilioMessageTemplateProvider) Create(ctx *sdk.AppCtx, connectionID int64, input providerTemplateCreateInput) (*providerTemplateCreate, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "create_content_template", map[string]any{
		"friendly_name": input.Name,
		"language":      input.Language,
		"variables":     input.Variables,
		"types": map[string]any{
			"twilio/text": map[string]any{"body": input.BodyText},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create provider template: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("create provider template: provider non-2xx: %s", truncate(body, 400))
	}
	providerID := extractProviderTemplateID(res.Data)
	if providerID == "" {
		return nil, fmt.Errorf("create provider template: provider response missing template id: %s", truncate(string(res.Data), 400))
	}
	return &providerTemplateCreate{ProviderTemplateID: providerID, ProviderStatus: "created"}, nil
}

func (twilioMessageTemplateProvider) Submit(ctx *sdk.AppCtx, connectionID int64, providerID, name, category string) (string, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "submit_content_template_approval", map[string]any{
		"ContentSid": providerID,
		"name":       providerApprovalName(name),
		"category":   normaliseTemplateCategory(category),
	})
	if err != nil {
		return "", fmt.Errorf("submit provider template: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return "", fmt.Errorf("submit provider template: provider non-2xx: %s", truncate(body, 400))
	}
	if status := extractApprovalStatus(res.Data); status != "" {
		return status, nil
	}
	return "pending", nil
}

func (twilioMessageTemplateProvider) Delete(ctx *sdk.AppCtx, connectionID int64, providerID string) (bool, bool, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "delete_content_template", map[string]any{
		"ContentSid": providerID,
	})
	if err != nil {
		return false, false, fmt.Errorf("delete provider template: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		if providerTemplateDeleteAlreadyGone(res, body) {
			return false, true, nil
		}
		return false, false, fmt.Errorf("delete provider template: provider non-2xx: %s", truncate(body, 400))
	}
	return res.Status == http.StatusNoContent || res.Success, false, nil
}
