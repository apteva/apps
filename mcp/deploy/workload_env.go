package main

import (
	"os"
	"sort"
	"strings"
)

// Host credentials are never ambient workload credentials. Explicit workload
// values are allowed, except the platform namespace reserved for the sidecar.
func workloadEnv(user map[string]string, extra ...string) []string {
	values := map[string]string{}
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "TZ", "SYSTEMROOT", "USER", "SHELL", "GOPATH", "GOCACHE", "GOMODCACHE", "GOROOT", "JAVA_HOME", "ANDROID_HOME", "ANDROID_SDK_ROOT", "DEVELOPER_DIR", "SDKROOT", "BUN_INSTALL", "PNPM_HOME"} {
		if v, ok := os.LookupEnv(key); ok {
			values[key] = v
		}
	}
	for _, entry := range extra {
		k, v, ok := strings.Cut(entry, "=")
		if ok && !strings.HasPrefix(k, "APTEVA_") {
			values[k] = v
		}
	}
	for k, v := range user {
		if !strings.HasPrefix(k, "APTEVA_") {
			values[k] = v
		}
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+values[k])
	}
	return out
}
