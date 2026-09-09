package instanceworker

import (
	"golang.org/x/sys/unix"
	"testing"
)

func makeFIFO(t *testing.T, p string) {
	t.Helper()
	if e := unix.Mkfifo(p, 0600); e != nil {
		t.Fatal(e)
	}
}
