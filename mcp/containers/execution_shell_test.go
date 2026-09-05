package main

import (
	"strings"
	"testing"
)

func TestStatefulExecutionKeepsCurrentShellDirectoryByDefault(t *testing.T) {
	workload := &Workload{WorkingDirectory: "/workspace"}
	in, err := normalizeExecutionInput(executionInput{
		ShellCommand: "pwd", SessionKey: "workspace",
	}, workload, 30)
	if err != nil {
		t.Fatal(err)
	}
	if in.WorkingDirectory != "" {
		t.Fatalf("stateful execution reset working directory to %q", in.WorkingDirectory)
	}

	discrete, err := normalizeExecutionInput(executionInput{ShellCommand: "pwd"}, workload, 30)
	if err != nil {
		t.Fatal(err)
	}
	if discrete.WorkingDirectory != "/workspace" {
		t.Fatalf("discrete execution working directory = %q", discrete.WorkingDirectory)
	}
}

func TestPersistentShellCommandQuotesArgumentsAndEnvironment(t *testing.T) {
	argv := shellArgv([]string{"printf", "%s", "it's safe"})
	if argv != `'printf' '%s' 'it'"'"'s safe'` {
		t.Fatalf("arguments were not shell quoted: %s", argv)
	}
	command := persistentShellCommand(executionRuntimeSpec{
		Argv: []string{"printf", "%s", "it's safe"},
		Env:  map[string]string{"VALUE": "a'b"},
	}, "pty_test")
	if !strings.Contains(command, `export VALUE='a'"'"'b'`) {
		t.Fatalf("environment was not shell quoted: %s", command)
	}
}

func TestInvalidPersistentSessionKeyIsRejected(t *testing.T) {
	_, err := normalizeExecutionInput(executionInput{
		ShellCommand: "pwd", SessionKey: "../../escape",
	}, &Workload{WorkingDirectory: "/workspace"}, 30)
	if err == nil {
		t.Fatal("invalid session key was accepted")
	}
}
