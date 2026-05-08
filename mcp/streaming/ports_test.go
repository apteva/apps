package main

import "testing"

func TestPortAllocator_Range(t *testing.T) {
	p, err := newPortAllocator("1935-1937")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := p.size(); got != 3 {
		t.Errorf("size=%d, want 3", got)
	}
	a, _ := p.allocate()
	b, _ := p.allocate()
	c, _ := p.allocate()
	if a != 1935 || b != 1936 || c != 1937 {
		t.Errorf("allocations = %d, %d, %d; want 1935, 1936, 1937", a, b, c)
	}
	if _, err := p.allocate(); err == nil {
		t.Error("4th allocate should have failed (range exhausted)")
	}
	p.release(b)
	d, err := p.allocate()
	if err != nil || d != 1936 {
		t.Errorf("after release(1936), allocate = %d / %v", d, err)
	}
}

func TestPortAllocator_SinglePort(t *testing.T) {
	p, err := newPortAllocator("1935")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := p.size(); got != 1 {
		t.Errorf("size=%d, want 1", got)
	}
	a, _ := p.allocate()
	if a != 1935 {
		t.Errorf("allocate=%d, want 1935", a)
	}
}

func TestPortAllocator_BadRange(t *testing.T) {
	cases := []string{"", "abc", "0", "65536", "2000-1000", "1000-abc"}
	for _, s := range cases {
		if _, err := newPortAllocator(s); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
}
