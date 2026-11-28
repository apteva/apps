package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"syscall"
)

// localProcess is one process of a local workload, as workspace_processes
// lists it.
type localProcess struct {
	PID            int    `json:"pid"`
	PPID           int    `json:"ppid"`
	PGID           int    `json:"pgid"`
	UID            int    `json:"uid"`
	State          string `json:"state,omitempty"`
	RSSKB          int64  `json:"rss_kb"`
	RunningSeconds int64  `json:"running_seconds,omitempty"`
	Command        string `json:"command"`
}

// workloadProcesses lists the processes that belong to a workload. In
// container mode that is every process of the workload's own uid, including
// daemons that left their process group. In the OS-sandbox modes it is the
// processes in the groups of the workload's running commands; a process that
// started its own session is no longer counted there.
func (d *localDriver) workloadProcesses(wl *localWorkload) ([]localProcess, error) {
	all, err := listProcesses()
	if err != nil {
		return nil, err
	}
	kind, _ := d.workloadMode(wl)
	keep := func(p localProcess) bool { return false }
	if kind == sandboxContainer && wl.UID >= localUIDBase {
		keep = func(p localProcess) bool { return p.UID == wl.UID }
	} else {
		groups := map[int]bool{}
		d.mu.Lock()
		for execID, proc := range d.running {
			if m := localExecIDRe.FindStringSubmatch(execID); m != nil && localWorkloadPrefix+m[1] == wl.ID && proc.cmd.Process != nil {
				groups[proc.cmd.Process.Pid] = true
			}
		}
		d.mu.Unlock()
		keep = func(p localProcess) bool { return groups[p.PGID] }
	}
	out := []localProcess{}
	for _, p := range all {
		if keep(p) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

func (d *localDriver) processes(input map[string]any) (any, error) {
	wl, err := d.loadWorkload(input)
	if err != nil {
		return nil, err
	}
	procs, err := d.workloadProcesses(wl)
	if err != nil {
		return nil, err
	}
	kind, _ := d.workloadMode(wl)
	scope := "process groups of running commands"
	if kind == sandboxContainer {
		scope = "every process of the workspace user"
	}
	return map[string]any{"processes": procs, "scope": scope}, nil
}

var processSignals = map[string]syscall.Signal{
	"TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "INT": syscall.SIGINT,
	"HUP": syscall.SIGHUP, "QUIT": syscall.SIGQUIT, "USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2,
}

// processKill signals processes of a workload: one pid, or all of them. A pid
// that does not belong to the workload is refused.
func (d *localDriver) processKill(input map[string]any) (any, error) {
	wl, err := d.loadWorkload(input)
	if err != nil {
		return nil, err
	}
	name := strings.TrimPrefix(strings.ToUpper(firstNonEmpty(stringIn(input, "signal"), "TERM")), "SIG")
	sig, ok := processSignals[name]
	if !ok {
		return nil, errors.New("signal must be TERM, KILL, INT, HUP, QUIT, USR1 or USR2")
	}
	procs, err := d.workloadProcesses(wl)
	if err != nil {
		return nil, err
	}
	pid := intIn(input, "pid", 0)
	all := input["all"] == true
	if pid <= 0 && !all {
		return nil, errors.New("pid or all is required")
	}
	var signalled []int
	for _, p := range procs {
		if all || p.PID == pid {
			if err := syscall.Kill(p.PID, sig); err == nil {
				signalled = append(signalled, p.PID)
			}
		}
	}
	if !all && len(signalled) == 0 {
		return nil, fmt.Errorf("process %d is not a process of this workspace (list them with workspace_processes)", pid)
	}
	return map[string]any{"signalled": signalled, "signal": "SIG" + name}, nil
}
