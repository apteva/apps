package main

import (
	"bufio"
	"os/exec"
	"strings"
	"sync"

	"github.com/creack/pty"
)

// scrcpy buffers its ready message when stdout is a pipe. A private terminal
// gives us line-buffered startup/progress and a foreground process group for
// shutdown; --no-window/--no-control keep capture independent of desktop UI.
func startScrcpyProcess(bin string, args []string) (*process, <-chan struct{}, error) {
	cmd := exec.Command(bin, args...)
	terminal, err := pty.Start(cmd)
	if err != nil {
		return nil, nil, err
	}
	p := &process{cmd: cmd, terminal: terminal, stderr: &limitedBuffer{limit: 16 << 10}, done: make(chan struct{})}
	ready := make(chan struct{})
	var once sync.Once
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		scanner := bufio.NewScanner(terminal)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = p.stderr.Write([]byte(line + "\n"))
			if strings.Contains(line, "Recording started to") {
				once.Do(func() { close(ready) })
			}
		}
	}()
	go func() {
		p.err = cmd.Wait()
		// Closing the terminal also releases any inherited ADB descriptors.
		_ = terminal.Close()
		<-readDone
		close(p.done)
	}()
	return p, ready, nil
}
