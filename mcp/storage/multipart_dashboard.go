package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const dashboardUploadRegistrationKey = "storage-backend"

type dashboardUploadBackend interface {
	PrepareDashboardUpload(context.Context, *sdk.AppCtx) error
}

// A backend change restarts the sidecar and calls this with the new backend.
// Disk must clear the same registration key used by a prior S3 configuration.
func reconcileDashboardUploadOrigin(c context.Context, app *sdk.AppCtx) error {
	if be, ok := backend().(dashboardUploadBackend); ok {
		return be.PrepareDashboardUpload(c, app)
	}
	api := app.DashboardConnectAPI()
	if api == nil {
		return errors.New("dashboard connection registration unsupported")
	}
	if backend().Kind() != "disk" {
		return errors.New("backend has no dashboard upload destination")
	}
	_, err := api.ReplaceDashboardConnectOrigins(dashboardUploadRegistrationKey, []string{})
	return err
}

func (s *s3Backend) browserUploadOrigin(c context.Context) (string, error) {
	// Derive from the exact signing path rather than guessing whether the provider
	// uses bucket virtual hosting, path-style addressing, or a different port.
	signed, err := s.SignMultipartPart(c, "00/apteva-csp-check", "csp-check", 1, 1)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(signed)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return "", errors.New("direct dashboard uploads require an HTTPS bucket origin")
	}
	// Never pass a path, object key, signature, or credential to the platform.
	return "https://" + strings.ToLower(u.Host), nil
}

func (s *s3Backend) PrepareDashboardUpload(c context.Context, app *sdk.AppCtx) error {
	s.dashboardMu.Lock()
	defer s.dashboardMu.Unlock()
	if time.Now().Before(s.dashboardUntil) {
		return s.dashboardErr
	}
	err := s.registerDashboardUpload(c, app)
	if c.Err() != nil {
		return c.Err()
	}
	ttl := 5 * time.Minute
	if err != nil {
		ttl = time.Minute
	}
	s.dashboardUntil, s.dashboardErr = time.Now().Add(ttl), err
	if err != nil {
		app.Logger().Warn("dashboard upload destination unavailable; using multipart relay", "err", err)
	} else {
		app.Logger().Info("dashboard upload destination registered; reload dashboard to apply CSP")
	}
	return err
}
func (s *s3Backend) registerDashboardUpload(c context.Context, app *sdk.AppCtx) error {
	api := app.DashboardConnectAPI()
	if api == nil {
		return errors.New("dashboard connection registration unsupported")
	}
	origin, err := s.browserUploadOrigin(c)
	if err != nil {
		return err
	}
	_, err = api.ReplaceDashboardConnectOrigins(dashboardUploadRegistrationKey, []string{origin})
	return err
}
