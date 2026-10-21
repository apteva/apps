package sdk

import (
	"context"
	"errors"
)

// IntegrationContextClient is optional so existing PlatformClient
// implementations remain source compatible.
type IntegrationContextClient interface {
	ExecuteIntegrationToolContext(context.Context, int64, string, map[string]any) (*ExecuteResult, error)
}

// ExecuteIntegrationToolContext forwards the caller's deadline through the
// platform to the upstream connector. It never silently drops the context.
func ExecuteIntegrationToolContext(ctx context.Context, client PlatformClient, connID int64, tool string, input map[string]any) (*ExecuteResult, error) {
	if api, ok := client.(IntegrationContextClient); ok {
		return api.ExecuteIntegrationToolContext(ctx, connID, tool, input)
	}
	return nil, errors.New("context-aware integration API unavailable")
}
