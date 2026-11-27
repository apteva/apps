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
	"time"

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
		noteContainersProbe(false)
		return &containersUnavailableError{msg: containersMissingError(app).Error(), absent: containersAbsent(err)}
	}
	return err
}

// errContainersAbsent matches (errors.Is) a runtime call refused because the
// Containers app is not installed or not bound for Workspaces, as opposed to
// installed but momentarily unreachable.
var errContainersAbsent = errors.New("the Containers app is not available to Workspaces")

type containersUnavailableError struct {
	msg    string
	absent bool
}

func (e *containersUnavailableError) Error() string { return e.msg }

func (e *containersUnavailableError) Is(target error) bool {
	return e.absent && target == errContainersAbsent
}

// containersAbsent recognises the platform's answers for a call to an app that
// Workspaces cannot use at all: "app not bound: containers" (the optional
// dependency is not installed or not bound, the usual answer) and "not
// installed".
func containersAbsent(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "app not bound") || strings.Contains(text, "not installed")
}

// containersMissing also counts an installed Containers app whose sidecar is
// not answering.
func containersMissing(err error) bool {
	return containersAbsent(err) || strings.Contains(strings.ToLower(err.Error()), "target unreachable")
}

func containersMissingError(app *sdk.AppCtx) error {
	if localDriverAvailable(app) == nil {
		return errors.New("Docker and test workspaces need the Containers app, which is not installed or not running; install it, or create the workspace with driver local")
	}
	return errors.New("Docker and test workspaces need the Containers app, which is not installed or not running; install it, or turn local_driver_enabled back on in the Workspaces settings (it also needs sandbox-exec on macOS or bubblewrap on Linux) to run workspaces as sandboxed folders on this machine")
}

// containersInstalled probes Containers with a lookup that cannot match. The
// answer is kept for containersProbeTTL so creates and /profiles do not each
// make an extra cross-app call.
func containersInstalled(app *sdk.AppCtx) bool {
	containersProbe.mu.Lock()
	if !containersProbe.at.IsZero() && time.Since(containersProbe.at) < containersProbeTTL {
		installed := containersProbe.installed
		containersProbe.mu.Unlock()
		return installed
	}
	containersProbe.mu.Unlock()
	var out map[string]any
	err := app.PlatformAPI().CallAppResult("containers", "containers_get", map[string]any{"workload_id": "probe-workspaces"}, &out)
	installed := err == nil || !containersMissing(err)
	noteContainersProbe(installed)
	return installed
}

// containersProbeTTL is a var so tests can turn the cache off.
var containersProbeTTL = 30 * time.Second

var containersProbe struct {
	mu        sync.Mutex
	at        time.Time
	installed bool
}

func noteContainersProbe(installed bool) {
	containersProbe.mu.Lock()
	containersProbe.at, containersProbe.installed = time.Now(), installed
	containersProbe.mu.Unlock()
}

func resetContainersProbe() {
	containersProbe.mu.Lock()
	containersProbe.at = time.Time{}
	containersProbe.mu.Unlock()
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
	mkdir := func(dir string) error { return os.MkdirAll(dir, 0o700) }
	if kind == sandboxContainer {
		mkdir = mkdirAllTraversable
	}
	if err := mkdir(root); err != nil {
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

// mkdirAllTraversable creates dir and any missing parents as 0711, so each
// workspace's own user can pass through folders the driver created (such as
// /var/lib/apteva-workspaces on a fresh container). Folders that already
// exist keep their mode; prepareContainerRoot reports any that block.
func mkdirAllTraversable(dir string) error {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, d)
		if d == filepath.Dir(d) {
			break
		}
	}
	if err := os.MkdirAll(dir, 0o711); err != nil {
		return err
	}
	for _, d := range missing {
		if err := os.Chmod(d, 0o711); err != nil {
			return err
		}
	}
	return nil
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
