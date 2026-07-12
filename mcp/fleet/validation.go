package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	tenantSlugRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	safeVersionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

func validatedTenantSlug(raw string) (string, error) {
	slug := strings.ToLower(strings.TrimSpace(raw))
	if !tenantSlugRE.MatchString(slug) {
		return "", errors.New("slug must be 1-63 characters, start with a letter or digit, and contain only [a-z0-9_-]")
	}
	return slug, nil
}

func validateAptevaVersion(raw string, allowHost bool) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if allowHost && (v == "host" || v == "system") {
		return v, nil
	}
	if strings.Contains(v, "..") || !safeVersionRE.MatchString(v) {
		return "", fmt.Errorf("invalid apteva version %q", raw)
	}
	return v, nil
}

func validateTenantPort(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("tenant port must be between 1024 and 65535")
	}
	return nil
}

func allowCustomBinary() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FLEET_ALLOW_CUSTOM_BINARY"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func allowInsecurePublicURL() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FLEET_ALLOW_INSECURE_PUBLIC_URL"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func validateLocalTenantDir(slug, dir string) error {
	expected, err := slugDataDir(slug)
	if err != nil {
		return err
	}
	want, err := filepath.Abs(filepath.Clean(expected))
	if err != nil {
		return err
	}
	got, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("refusing unmanaged tenant directory %q; expected %q", got, want)
	}
	return nil
}

func validateHostedTenantDir(slug, dir string) error {
	valid, err := validatedTenantSlug(slug)
	if err != nil {
		return err
	}
	want := remoteFleetRoot + "/" + valid
	if dir != want {
		return fmt.Errorf("refusing unmanaged hosted tenant directory %q; expected %q", dir, want)
	}
	return nil
}
