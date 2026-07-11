package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestLaunchAndroidResolvesLauncherActivity(t *testing.T) {
	var calls [][]string
	run := func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) == 1 {
			return []byte("priority=0 preferredOrder=0 match=0x108000\ncom.example/.MainActivity\n"), nil
		}
		return []byte("Starting: Intent"), nil
	}

	if err := launchAndroidWithADB("com.example", "", run); err != nil {
		t.Fatalf("launchAndroidWithADB: %v", err)
	}
	want := [][]string{
		{"shell", "cmd", "package", "resolve-activity", "--brief", "-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER", "com.example"},
		{"shell", "am", "start", "-n", "com.example/.MainActivity", "-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLaunchAndroidUsesExplicitActivityFirst(t *testing.T) {
	var calls [][]string
	run := func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte("Starting: Intent"), nil
	}

	if err := launchAndroidWithADB("com.example", ".MainActivity", run); err != nil {
		t.Fatalf("launchAndroidWithADB: %v", err)
	}
	if len(calls) != 1 || calls[0][4] != "com.example/.MainActivity" {
		t.Fatalf("unexpected calls: %#v", calls)
	}
}

func TestLaunchAndroidKeepsMonkeyFallback(t *testing.T) {
	var calls [][]string
	run := func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) == 1 {
			return nil, errors.New("resolve unsupported")
		}
		return []byte("Events injected: 1"), nil
	}

	if err := launchAndroidWithADB("com.example", "", run); err != nil {
		t.Fatalf("launchAndroidWithADB: %v", err)
	}
	if len(calls) != 2 || calls[1][1] != "monkey" {
		t.Fatalf("unexpected calls: %#v", calls)
	}
}

func TestResolvedActivityIgnoresMetadata(t *testing.T) {
	got := resolvedActivity("priority=0 preferredOrder=0\ncom.example/.MainActivity\n")
	if got != "com.example/.MainActivity" {
		t.Fatalf("resolvedActivity = %q", got)
	}
}
