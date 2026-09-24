package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunReadsManifestAndWritesJSON(t *testing.T) {
	input := `{"files":["A"],"mappings":[{"source":"A","target":"a"}]}`
	var stdout, stderr bytes.Buffer

	if err := run(nil, strings.NewReader(input), &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v (stderr: %s)", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr, got %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"__planner_tmp_1"`) {
		t.Fatalf("output %q does not contain temporary name", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"rollback_steps"`) {
		t.Fatalf("output %q does not contain rollback steps", stdout.String())
	}
}

func TestRunRejectsInvalidManifest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(nil, strings.NewReader(`{"files":["A","a"],"mappings":[]}`), &stdout, &stderr)
	if err == nil {
		t.Fatal("invalid manifest was accepted")
	}
	if stdout.Len() != 0 {
		t.Fatalf("invalid manifest wrote stdout: %q", stdout.String())
	}
}

func TestRunRejectsExtraArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"one", "two"}, strings.NewReader(""), &stdout, &stderr); err == nil {
		t.Fatal("extra arguments were accepted")
	}
}
