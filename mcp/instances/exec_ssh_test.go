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
	"strings"
	"sync"
	"testing"
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
