package main

import (
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Apps by name (0.14.0). Environments checks the shape; the platform resolves
// each name to a source and checks the version constraint when the runtime
// starts.

// maxRunApps matches the platform's limit on apps in one runtime, counting
// app_install_ids and apps together.
const maxRunApps = 16

func validateRunApps(spec EnvironmentSpec) error {
	if len(spec.AppInstallIDs)+len(spec.Apps) > maxRunApps {
		return fmt.Errorf("app_install_ids and apps may name at most %d apps together", maxRunApps)
	}
	seen := map[string]bool{}
	for i, app := range spec.Apps {
		name := strings.ToLower(strings.TrimSpace(app.Name))
		if name == "" {
			return fmt.Errorf("apps %d: name required", i)
		}
		if seen[name] {
			return fmt.Errorf("apps %d: %q is listed twice", i, app.Name)
		}
		seen[name] = true
	}
	return nil
}

// runAppsMissing lists the apps a spec named that the created runtime does
// not report. A server without apps by name ignores the field, and the run
// would otherwise start without them.
func runAppsMissing(apps []sdk.RuntimeAppSource, summary *sdk.RuntimeSummary) []string {
	if len(apps) == 0 {
		return nil
	}
	present := map[string]bool{}
	if summary != nil {
		for _, app := range summary.Apps {
			present[strings.ToLower(strings.TrimSpace(app.Name))] = true
		}
	}
	var missing []string
	for _, app := range apps {
		if !present[strings.ToLower(strings.TrimSpace(app.Name))] {
			missing = append(missing, app.Name)
		}
	}
	return missing
}
