package main

// Runtime drivers. Workspaces speaks one small vocabulary to whatever runs a
// workspace: the Containers tool names it has always used (containers_run,
// containers_exec_start, containers_volume_import, ...). A driver implements
// that vocabulary. Two exist:
//
//   - containers: the Containers app (local Docker or an Instances host).
//   - local: a folder on the machine running Workspaces, with commands run in
//     the OS sandbox (Seatbelt on macOS, bubblewrap on Linux). No Docker.
//
// Each workspace records its driver at create time. Refs a driver mints carry
// its prefix, so calls that only hold a workload or execution id (reconcile,
// command refresh) still reach the right driver.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

const (
	driverContainers = "containers"
	driverLocal      = "local"
)

// Capabilities says what a workspace's driver supports. Fields are added as
// optional with a false default, so a new driver only fills them in.
type Capabilities struct {
	Isolation string `json:"isolation"` // container | os_sandbox | separate_user
	// Sandbox is how a local workspace is isolated: seatbelt (macOS),
	// bwrap (Linux), or container (inside a container, as its own user;
	// the surrounding container is the boundary).
	Sandbox  string `json:"sandbox,omitempty"`
	Hardened bool   `json:"hardened"` // safe for untrusted code and test workspaces
	Sessions bool   `json:"sessions"` // persistent shell between commands
	Preview  bool   `json:"preview"`
	Images   bool   `json:"images"` // runs any container image
}

func capabilitiesFor(w *Workspace) Capabilities {
	switch {
	case w.Driver == driverLocal:
		if w.Sandbox == string(sandboxContainer) {
			// Not an OS sandbox: a separate user inside Apteva's container.
			return Capabilities{Isolation: "separate_user", Sandbox: w.Sandbox, Preview: true}
		}
		return Capabilities{Isolation: "os_sandbox", Sandbox: w.Sandbox, Preview: true}
	case w.InstanceID > 0:
		return Capabilities{Isolation: "container", Hardened: true, Preview: true, Images: true}
	default:
		return Capabilities{Isolation: "container", Hardened: true, Sessions: true, Preview: true, Images: true}
	}
}

func hostLabelFor(driver string, instanceID int64) string {
	if driver == driverLocal {
		return "This machine (sandbox)"
	}
	return hostLabel(instanceID)
}

func runtimeActorLabel(w *Workspace) string {
	if w != nil && w.Driver == driverLocal {
		return "Local sandbox"
	}
	return "Containers"
}

func normalizeDriver(app *sdk.AppCtx, requested string, instanceID int64) (string, error) {
	driver := strings.ToLower(strings.TrimSpace(requested))
	if driver == "" {
		if instanceID > 0 {
			return driverContainers, nil
		}
		driver = defaultDriver(app)
	}
	switch driver {
	case driverContainers:
		return driverContainers, nil
	case driverLocal:
		if instanceID > 0 {
			return "", errors.New("instance_id applies to the containers driver only; omit it for driver local")
		}
		if err := localDriverAvailable(app); err != nil {
			return "", err
		}
		return driverLocal, nil
	default:
		return "", fmt.Errorf("driver must be %s or %s", driverContainers, driverLocal)
	}
}

// defaultDriver picks the driver for a workspace that names none: Docker when
// the Containers app is installed (unless default_driver is local), otherwise
// the local sandbox when it is available. With neither, it stays containers
// so the create fails with the "Containers is missing" message.
func defaultDriver(app *sdk.AppCtx) string {
	driver := strings.ToLower(configString(app, "default_driver", driverContainers))
	if driver == driverLocal {
		return driverLocal
	}
	if localDriverAvailable(app) == nil && !containersInstalled(app) {
		return driverLocal
	}
	return driverContainers
}

func localDriverAvailable(app *sdk.AppCtx) error {
	if !configBool(app, "local_driver_enabled", true) {
		return errors.New("the local driver was turned off in the Workspaces settings (local_driver_enabled)")
	}
	_, err := localRuntime.get(app)
	return err
}

// runtimeCall sends one runtime operation to the driver that owns it.
func runtimeCall(app *sdk.AppCtx, tool string, input map[string]any, out any) error {
	if ownedByLocal(input) {
		driver, err := localRuntime.get(app)
		if err != nil {
			return err
		}
		return driver.Call(tool, input, out)
	}
	err := app.PlatformAPI().CallAppResult("containers", tool, input, out)
	if err != nil && containersMissing(err) {
		return containersMissingError(app)
	}
	return err
}

// containersMissing recognises the platform's answer for a call to an app
// that is not installed (or not running) in this project.
func containersMissing(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "target unreachable") || strings.Contains(text, "not installed")
}

func containersMissingError(app *sdk.AppCtx) error {
	if localDriverAvailable(app) == nil {
		return errors.New("Docker and test workspaces need the Containers app, which is not installed or not running; install it, or create the workspace with driver local")
	}
	return errors.New("Docker and test workspaces need the Containers app, which is not installed or not running; install it, or turn local_driver_enabled back on in the Workspaces settings (it also needs sandbox-exec on macOS or bubblewrap on Linux) to run workspaces as sandboxed folders on this machine")
}

// containersInstalled probes Containers with a lookup that cannot match.
func containersInstalled(app *sdk.AppCtx) bool {
	var out map[string]any
	err := app.PlatformAPI().CallAppResult("containers", "containers_get", map[string]any{"workload_id": "probe-workspaces"}, &out)
	return err == nil || !containersMissing(err)
}

func ownedByLocal(input map[string]any) bool {
	if driver, _ := input["driver"].(string); driver == driverLocal {
		return true
	}
	if id, _ := input["workload_id"].(string); strings.HasPrefix(id, localWorkloadPrefix) {
		return true
	}
	if id, _ := input["execution_id"].(string); strings.HasPrefix(id, localExecPrefix) {
		return true
	}
	return false
}

// localRuntime is created on first use, rooted in the app's data directory.
var localRuntime localRegistry

type localRegistry struct {
	mu     sync.Mutex
	driver *localDriver
}

func (r *localRegistry) get(app *sdk.AppCtx) (*localDriver, error) {
	network := configString(app, "local_network", "open")
	kind, err := resolveSandbox(configString(app, "local_sandbox", sandboxModeAuto), network)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.driver != nil {
		// Settings can change while running: new workspaces use the current
		// sandbox; existing ones keep the one recorded when they were made.
		r.driver.setMode(kind, network)
		return r.driver, nil
	}
	root := configString(app, "local_root", "")
	home, _ := os.UserHomeDir()
	if root == "" && kind == sandboxContainer {
		// Outside /root (mode 0700): each workspace's own user must be able
		// to reach its folder.
		root = filepath.Join("/var/lib/apteva-workspaces", "install-"+firstNonEmpty(os.Getenv("APTEVA_INSTALL_ID"), "0"))
	}
	if root == "" {
		// A plain folder in the home directory, outside the hidden folders
		// the sandbox blocks (the app data dir is under ~/.apteva).
		if home == "" {
			return nil, errors.New("the local driver needs a home directory or local_root")
		}
		root = filepath.Join(home, "apteva-workspaces", "install-"+firstNonEmpty(os.Getenv("APTEVA_INSTALL_ID"), "0"))
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("local_root %q must be an absolute path", root)
	}
	if strings.ContainsAny(root, " \t\n'\"\\$`") {
		return nil, fmt.Errorf("local driver root %q must not contain spaces, quotes or shell characters; set local_root", root)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if hidden := rootInsideHidden(root, home); hidden != "" {
		return nil, fmt.Errorf("local_root %s is inside %s, which sandboxed commands cannot read; choose another folder", root, hidden)
	}
	if kind == sandboxContainer {
		if err := prepareContainerRoot(root); err != nil {
			return nil, err
		}
	}
	r.driver = newLocalDriver(root, kind, network)
	return r.driver, nil
}

// prepareContainerRoot makes the driver root traversable but not listable
// (0711), and checks every parent lets other users through, since each
// workspace runs as its own user.
func prepareContainerRoot(root string) error {
	if err := os.Chmod(root, 0o711); err != nil {
		return err
	}
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o001 == 0 {
			return fmt.Errorf("local_root %s is below %s, which other users cannot enter (mode %o); choose another local_root", root, dir, info.Mode().Perm())
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}
