package tests

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	a := append([]string{"run", "../main.go"}, args...)
	cmd := exec.Command("go", a...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func TestCLIVersion(t *testing.T) {
	out, err := runCLI(t, "version")
	if err != nil {
		t.Fatalf("version command failed: %v\nOutput: %s", err, out)
	}
	if !strings.Contains(out, "flagbase") {
		t.Errorf("expected 'flagbase' in output, got: %s", out)
	}
	if !strings.Contains(out, "Version:") {
		t.Errorf("expected 'Version:' in output, got: %s", out)
	}
}

func TestCLIVersionShort(t *testing.T) {
	out, err := runCLI(t, "version", "--short")
	if err != nil {
		t.Fatalf("version --short failed: %v\nOutput: %s", err, out)
	}
	if strings.TrimSpace(out) != "dev" {
		t.Errorf("expected 'dev', got: %s", out)
	}
}

func TestCLIHelp(t *testing.T) {
	out, err := runCLI(t, "--help")
	if err != nil {
		t.Fatalf("--help failed: %v\nOutput: %s", err, out)
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected 'Usage:' in help output, got: %s", out)
	}
	if !strings.Contains(out, "Available Commands:") {
		t.Errorf("expected 'Available Commands:' in help output, got: %s", out)
	}
}

func TestCLIStartHelp(t *testing.T) {
	out, err := runCLI(t, "start", "--help")
	if err != nil {
		t.Fatalf("start --help failed: %v\nOutput: %s", err, out)
	}
	if !strings.Contains(out, "flagbase") {
		t.Errorf("expected 'flagbase' in start help, got: %s", out)
	}
}
