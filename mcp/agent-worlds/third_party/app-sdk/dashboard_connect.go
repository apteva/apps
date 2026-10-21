package sdk

import (
	"errors"
	"net/url"
	"strings"
)

// DashboardConnectOriginRegistration extends the whole dashboard document's
// CSP connect-src. It does not configure CORS or grant access to files/APIs.
type DashboardConnectOriginRegistration struct {
	Key     string   `json:"key"`
	Origins []string `json:"origins"`
}

// DashboardConnectClient is optional to preserve PlatformClient compatibility.
// Registrations belong to the authenticated install. Changes require a dashboard
// reload. The server requires an admin-owned install with PermDashboardConnect.
type DashboardConnectClient interface {
	ReplaceDashboardConnectOrigins(key string, origins []string) (*DashboardConnectOriginRegistration, error)
	GetDashboardConnectOrigins(key string) (*DashboardConnectOriginRegistration, error)
	ListDashboardConnectOriginRegistrations() ([]DashboardConnectOriginRegistration, error)
	DeleteDashboardConnectOrigins(key string) error
}

// DashboardConnectAPI returns nil for custom clients without this capability.
// An HTTP client can still talk to an older server: callers must handle endpoint
// errors and retain their proxy/relay fallback in that case.
func (c *AppCtx) DashboardConnectAPI() DashboardConnectClient {
	if c == nil || c.platform == nil {
		return nil
	}
	client := c.platform
	if scoped, ok := client.(*projectScopedClient); ok {
		client = scoped.inner
	}
	api, _ := client.(DashboardConnectClient)
	return api
}

func dashboardConnectPath(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "/\\") {
		return "", errors.New("dashboard-connect registration key is required and cannot contain slashes")
	}
	return "/api/apps/callback/dashboard-connect-origins/" + url.PathEscape(key), nil
}

// ReplaceDashboardConnectOrigins atomically replaces a named destination set.
// Origins must be exact HTTPS origins; an empty set removes the registration.
func (c *httpPlatformClient) ReplaceDashboardConnectOrigins(key string, origins []string) (*DashboardConnectOriginRegistration, error) {
	path, err := dashboardConnectPath(key)
	if err != nil {
		return nil, err
	}
	var out DashboardConnectOriginRegistration
	err = c.put(path, map[string]any{"origins": append([]string{}, origins...)}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *httpPlatformClient) GetDashboardConnectOrigins(key string) (*DashboardConnectOriginRegistration, error) {
	path, err := dashboardConnectPath(key)
	if err != nil {
		return nil, err
	}
	var out DashboardConnectOriginRegistration
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *httpPlatformClient) ListDashboardConnectOriginRegistrations() ([]DashboardConnectOriginRegistration, error) {
	var out struct {
		Registrations []DashboardConnectOriginRegistration `json:"registrations"`
	}
	if err := c.get("/api/apps/callback/dashboard-connect-origins", &out); err != nil {
		return nil, err
	}
	if out.Registrations == nil {
		out.Registrations = []DashboardConnectOriginRegistration{}
	}
	return out.Registrations, nil
}
func (c *httpPlatformClient) DeleteDashboardConnectOrigins(key string) error {
	path, err := dashboardConnectPath(key)
	if err != nil {
		return err
	}
	return c.delete(path, nil)
}
