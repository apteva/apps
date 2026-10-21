package sdk

import (
	"fmt"
	"regexp"
	"strings"
)

// RuntimeIngress declares app-owned, authenticated public surfaces. Infrastructure
// must never expose process control or infer public access from a broad HTTP route.
type RuntimeIngress struct {
	Routes      []RuntimeIngressRoute `yaml:"routes" json:"routes"`
	Endpoints   map[string]string     `yaml:"endpoints" json:"endpoints"`
	BrowserPath string                `yaml:"browser_path,omitempty" json:"browser_path,omitempty"`
}
type RuntimeIngressRoute struct {
	Path string `yaml:"path" json:"path"`
}

func ValidateRuntimeIngress(v *RuntimeIngress) error {
	if v == nil {
		return nil
	}
	if len(v.Routes) == 0 || len(v.Routes) > 32 || len(v.Endpoints) > 16 {
		return fmt.Errorf("invalid ingress declaration size")
	}
	pathOK := func(p string) bool {
		return regexp.MustCompile(`^/[A-Za-z0-9/_.-]+(?:/\*)?$`).MatchString(p) && p != "/" && !strings.Contains(p, "..") && !strings.Contains(p, "/_") && p != "/mcp" && p != "/manifest"
	}
	for _, r := range v.Routes {
		if !pathOK(r.Path) || strings.Contains(strings.TrimSuffix(r.Path, "/*"), "*") {
			return fmt.Errorf("invalid public ingress path")
		}
	}
	for name, p := range v.Endpoints {
		if name == "" || len(name) > 64 || strings.ContainsAny(name, "/\\ \r\n") {
			return fmt.Errorf("invalid ingress endpoint name")
		}
		if p != "" && !pathOK(p) {
			return fmt.Errorf("invalid ingress endpoint path")
		}
	}
	if v.BrowserPath != "" && !pathOK(v.BrowserPath) {
		return fmt.Errorf("invalid browser ingress path")
	}
	return nil
}
