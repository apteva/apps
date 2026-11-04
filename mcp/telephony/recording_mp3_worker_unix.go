//go:build unix

package main

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// On Linux nice is per thread: setting it for the process would only reach the
// calling thread. A new thread copies its creator, so once every thread is low
// all later ones are too. The second pass covers one started during the first.
func lowerRecordingMP3WorkerPriority() error {
	var result error
	for range 2 {
		tasks, err := os.ReadDir("/proc/self/task")
		if err != nil {
			return syscall.Setpriority(syscall.PRIO_PROCESS, 0, recordingMP3WorkerNice)
		}
		for _, task := range tasks {
			tid, err := strconv.Atoi(task.Name())
			if err != nil {
				continue
			}
			if err := syscall.Setpriority(syscall.PRIO_PROCESS, tid, recordingMP3WorkerNice); err != nil && !errors.Is(err, syscall.ESRCH) {
				result = err
			}
		}
	}
	return result
}

func recordingMP3WorkerThreadNice() (int, bool) {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		prio, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
		if err != nil {
			return 0, false
		}
		if runtime.GOOS == "linux" {
			prio = 20 - prio // the raw Linux syscall returns 20-nice
		}
		return prio, true
	}
	lowest, known := 0, false
	for _, task := range tasks {
		stat, err := os.ReadFile("/proc/self/task/" + task.Name() + "/stat")
		if err != nil {
			continue
		}
		// Field 19. Fields are counted after the command name, which can hold spaces.
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) < 17 {
			continue
		}
		nice, err := strconv.Atoi(fields[16])
		if err != nil {
			continue
		}
		if !known || nice < lowest {
			lowest, known = nice, true
		}
	}
	return lowest, known
}
