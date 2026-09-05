package main

// Race-detector test for the lockedWriter that fixes the dropped-
// output bug in runSSH. Pre-v0.3.2 the SSH path pointed both
// session.Stdout and session.Stderr at the same *bytes.Buffer;
// crypto/ssh delivers the two streams from independent goroutines,
// and concurrent unsynchronised writes to bytes.Buffer silently lose
// data. Symptom in production: instance_metrics returning "no JSON
// in vitals script output" intermittently (4 of 5 calls observed
// dropping an entire stream's worth of output).
//
// This test reproduces the concurrent-write workload and asserts
// no bytes are lost. Run with -race to also catch any future
// refactor that drops the mutex.

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestLockedWriter_NoLostBytesUnderConcurrentWrites(t *testing.T) {
	w := &lockedWriter{}
	const writers = 4
	const writesPer = 250
	const linesPerWrite = 10

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			// Each writer emits a recognisable prefix so we can
			// count its lines back in the combined output. Multi-
			// line writes per call to stress the buffer-grow path.
			for j := 0; j < writesPer; j++ {
				var lines strings.Builder
				for k := 0; k < linesPerWrite; k++ {
					lines.WriteString("w")
					lines.WriteString(itoa(id))
					lines.WriteString("-")
					lines.WriteString(itoa(j*linesPerWrite + k))
					lines.WriteByte('\n')
				}
				if _, err := w.Write([]byte(lines.String())); err != nil {
					t.Errorf("writer %d: %v", id, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	got := w.String()
	for i := 0; i < writers; i++ {
		want := writesPer * linesPerWrite
		gotN := strings.Count(got, "w"+itoa(i)+"-")
		if gotN != want {
			t.Errorf("writer %d: got %d lines, want %d (total buf=%d bytes)",
				i, gotN, want, len(got))
		}
	}
}

func TestLockedWriter_BoundsOutputWhileWriting(t *testing.T) {
	w := &lockedWriter{max: 8}
	if n, err := w.Write([]byte("0123456789abcdef")); err != nil || n != 16 {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	if got := w.String(); got != "01234567" {
		t.Fatalf("bounded output=%q", got)
	}
	if !w.Truncated() {
		t.Fatal("writer did not report truncation")
	}
}

func TestIsSSHConnError_Classification(t *testing.T) {
	// Connection-class errors should redial; exit-code errors should
	// not (those are the user's command saying "no").
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"closed network", &stringErr{"use of closed network connection"}, true},
		{"eof", &stringErr{"EOF"}, true},
		{"channel reset", &stringErr{"ssh: channel open failed"}, true},
		{"random", &stringErr{"some other error"}, false},
	}
	for _, c := range cases {
		if got := isSSHConnError(c.err); got != c.want {
			t.Errorf("%s: isSSHConnError(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}

func TestResolveSSHRunResult_RecoversMissingExitStatusWithMarker(t *testing.T) {
	marker := "__APTEVA_EXIT_0123456789abcdef__="
	out, exit, err := resolveSSHRunResult("installed\n"+marker+"0\n", marker, &ssh.ExitMissingError{})
	if err != nil || exit != 0 || out != "installed" {
		t.Fatalf("out=%q exit=%d err=%v", out, exit, err)
	}
}

func TestResolveSSHRunResult_DoesNotGuessWithoutMarker(t *testing.T) {
	missing := &ssh.ExitMissingError{}
	out, exit, err := resolveSSHRunResult("installed", "__APTEVA_EXIT_missing__=", missing)
	if out != "installed" || exit != -1 || !errors.Is(err, missing) {
		t.Fatalf("out=%q exit=%d err=%v", out, exit, err)
	}
}

func TestResolveSSHRunResult_PreservesNonZeroExit(t *testing.T) {
	marker := "__APTEVA_EXIT_0123456789abcdef__="
	out, exit, err := resolveSSHRunResult("bad input\n"+marker+"42\n", marker, &ssh.ExitMissingError{})
	if out != "bad input" || exit != 42 || err == nil || !strings.Contains(err.Error(), "42") {
		t.Fatalf("out=%q exit=%d err=%v", out, exit, err)
	}
}

func TestWrapSSHCommand_QuotesArbitraryCommand(t *testing.T) {
	marker := "__APTEVA_EXIT_test__="
	wrapped := wrapSSHCommand(`printf '%s' "$HOME"`, marker)
	if !strings.Contains(wrapped, marker) || !strings.Contains(wrapped, `'"'"'`) {
		t.Fatalf("wrapped command is not safely quoted: %q", wrapped)
	}
}

// ─── helpers ───────────────────────────────────────────────────────

type stringErr struct{ s string }

func (e *stringErr) Error() string { return e.s }

// itoa avoids strconv import noise in the hot loop; small positive
// ints only, which is all the test uses.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
