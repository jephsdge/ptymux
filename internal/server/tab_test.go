package server

import (
	"strings"
	"testing"
)

func TestPTYRunnerPreservesShellState(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	if _, err := runner.Run("cd /tmp"); err != nil {
		t.Fatalf("cd command returned error: %v", err)
	}

	result, err := runner.Run("pwd")
	if err != nil {
		t.Fatalf("pwd command returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0; output=%q", result.ExitCode, result.Output)
	}
	if strings.TrimSpace(result.Output) != "/tmp" {
		t.Fatalf("Output = %q, want /tmp", result.Output)
	}
}
