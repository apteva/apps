//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func platformSandboxSupported() bool { return true }
func sandboxRequiredByDefault() bool { return true }

func runSandboxHelper(args []string) error {
	spec, target, targetArgs, err := parseSandboxArgs(args)
	if err != nil {
		return err
	}
	if err := applyProcessLimits(spec); err != nil {
		return err
	}
	if err := attachCgroup(spec); err != nil {
		if spec.RequireCgroup {
			return fmt.Errorf("cgroup: %w", err)
		}
		_, _ = fmt.Fprintln(os.Stderr, "functions sandbox: cgroup unavailable; using runtime memory limits:", err)
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if err := applyLandlock(spec); err != nil {
		if spec.RequireSandbox {
			return fmt.Errorf("landlock: %w", err)
		}
		_, _ = fmt.Fprintln(os.Stderr, "functions sandbox: Landlock unavailable; continuing with resource limits:", err)
	}
	if err := applySeccomp(); err != nil {
		if spec.RequireSandbox {
			return fmt.Errorf("seccomp: %w", err)
		}
		_, _ = fmt.Fprintln(os.Stderr, "functions sandbox: seccomp unavailable:", err)
	}
	resolved, err := execLookPath(target)
	if err != nil {
		return err
	}
	return syscall.Exec(resolved, append([]string{resolved}, targetArgs...), os.Environ())
}

func applySeccomp() error {
	blocked := []uint32{
		unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT,
		unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_SETNS, unix.SYS_UNSHARE,
		unix.SYS_SETSID, unix.SYS_SETPGID,
		unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN, unix.SYS_USERFAULTFD,
		unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY,
		unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE,
		unix.SYS_KEXEC_LOAD, unix.SYS_OPEN_BY_HANDLE_AT,
	}
	filters := make([]unix.SockFilter, 0, 2+len(blocked)*2)
	// seccomp_data.nr is the first uint32 in struct seccomp_data.
	filters = append(filters, unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0})
	for _, nr := range blocked {
		filters = append(filters,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: nr},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		)
	}
	filters = append(filters, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	prog := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
		return err
	}
	return nil
}

func attachCgroup(spec sandboxSpec) error {
	root := strings.TrimSpace(spec.CgroupRoot)
	if root == "" {
		root = "/sys/fs/cgroup/apteva-functions"
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		return errors.New("cgroup v2 is not mounted")
	}
	dir := filepath.Join(root, fmt.Sprintf("%s-%d", spec.Mode, os.Getpid()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.Remove(dir)
		}
	}()
	memoryMB := spec.MemoryMB
	if memoryMB <= 0 {
		memoryMB = defaultMemoryMB
	}
	pids := "32"
	cpu := "100000 100000"
	if spec.Mode == sandboxBuild {
		pids = "128"
		cpu = "200000 100000"
	}
	settings := []struct{ name, value string }{
		{"memory.max", fmt.Sprintf("%d", int64(memoryMB)<<20)},
		{"memory.swap.max", "0"},
		{"pids.max", pids},
		{"cpu.max", cpu},
		{"cgroup.procs", fmt.Sprintf("%d", os.Getpid())},
	}
	for _, setting := range settings {
		if err := os.WriteFile(filepath.Join(dir, setting.name), []byte(setting.value), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", setting.name, err)
		}
	}
	failed = false
	return nil
}

func cleanupSandboxProcess(pid int, mode sandboxMode) {
	if pid <= 0 {
		return
	}
	root := strings.TrimSpace(os.Getenv("APTEVA_FUNCTIONS_CGROUP_ROOT"))
	if root == "" {
		root = "/sys/fs/cgroup/apteva-functions"
	}
	dir := filepath.Join(root, fmt.Sprintf("%s-%d", mode, pid))
	_ = os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0o600)
	for i := 0; i < 20; i++ {
		if err := os.Remove(dir); err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func execLookPath(target string) (string, error) {
	if filepath.IsAbs(target) {
		return target, nil
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		candidate := filepath.Join(dir, target)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("sandbox target %q not found", target)
}

func applyProcessLimits(spec sandboxSpec) error {
	limits := []struct {
		resource int
		cur      uint64
	}{
		{unix.RLIMIT_NOFILE, 256},
		{unix.RLIMIT_FSIZE, 256 << 20},
		{unix.RLIMIT_CORE, 0},
	}
	for _, item := range limits {
		lim := &unix.Rlimit{Cur: item.cur, Max: item.cur}
		if err := unix.Setrlimit(item.resource, lim); err != nil {
			return fmt.Errorf("setrlimit(%d): %w", item.resource, err)
		}
	}
	return nil
}

func applyLandlock(spec sandboxSpec) error {
	allFS := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
		unix.LANDLOCK_ACCESS_FS_REFER |
		unix.LANDLOCK_ACCESS_FS_TRUNCATE)

	attr := unix.LandlockRulesetAttr{Access_fs: allFS}
	// Scope signals and abstract Unix sockets when supported. Kernels with
	// an older Landlock ABI reject these bits; retry without them below.
	attr.Scoped = unix.LANDLOCK_SCOPE_SIGNAL | unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET
	ruleset, err := landlockCreate(&attr)
	if err != nil {
		attr.Scoped = 0
		ruleset, err = landlockCreate(&attr)
	}
	if err != nil {
		return err
	}
	defer unix.Close(ruleset)

	readExec := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR)
	for _, path := range []string{"/bin", "/sbin", "/usr", "/lib", "/lib64"} {
		if err := landlockAllow(ruleset, path, readExec); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, path := range []string{"/etc/ssl", "/etc/pki", "/etc/ca-certificates", "/etc/ld.so.conf.d"} {
		if err := landlockAllow(ruleset, path, readExec); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	readOnly := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE)
	for _, path := range []string{
		"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/gai.conf",
		"/etc/localtime", "/etc/passwd", "/etc/group", "/etc/ld.so.cache", "/etc/ld.so.conf",
	} {
		if err := landlockAllow(ruleset, path, readOnly); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	deviceRW := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE)
	for _, path := range []string{"/dev/null", "/dev/zero", "/dev/tty"} {
		if err := landlockAllow(ruleset, path, deviceRW); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, path := range []string{"/dev/random", "/dev/urandom"} {
		if err := landlockAllow(ruleset, path, uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	rootAccess := readExec
	if spec.Mode == sandboxBuild {
		rootAccess = allFS
	}
	if err := landlockAllow(ruleset, spec.Root, rootAccess); err != nil {
		return err
	}
	if spec.TempDir != "" {
		if err := landlockAllow(ruleset, spec.TempDir, allFS); err != nil {
			return err
		}
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0); errno != 0 {
		return errno
	}
	return nil
}

func landlockCreate(attr *unix.LandlockRulesetAttr) (int, error) {
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(attr)), unsafe.Sizeof(*attr), 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func landlockAllow(ruleset int, path string, access uint64) error {
	if envBool("APTEVA_FUNCTIONS_SANDBOX_DEBUG", false) {
		_, _ = fmt.Fprintf(os.Stderr, "functions sandbox: allow path=%s access=%#x\n", path, access)
	}
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	attr := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(fd)}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset),
		uintptr(unix.LANDLOCK_RULE_PATH_BENEATH), uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
