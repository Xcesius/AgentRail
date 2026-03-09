package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentrailBinaryJSONPatchSingleFile(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	resp, stderr := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "patch",
		"diff":   "--- a/sample.txt\n+++ b/sample.txt\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	})
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected success response, got %+v", resp)
	}
	if repositoryState, _ := resp["repository_state"].(string); repositoryState != "changed" {
		t.Fatalf("expected changed repository state, got %+v", resp)
	}
	updated, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(updated) != "new\n" {
		t.Fatalf("expected patched file, got %q", string(updated))
	}
}

func TestAgentrailBinaryJSONPatchAtomicMultiFile(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	onePath := filepath.Join(workspace, "one.txt")
	twoPath := filepath.Join(workspace, "two.txt")
	if err := os.WriteFile(onePath, []byte("old-one\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(one.txt): %v", err)
	}
	if err := os.WriteFile(twoPath, []byte("old-two\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(two.txt): %v", err)
	}

	resp, stderr := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "patch",
		"atomic": true,
		"diff":   "--- a/one.txt\n+++ b/one.txt\n@@ -1,1 +1,1 @@\n-old-one\n+new-one\n--- a/two.txt\n+++ b/two.txt\n@@ -1,1 +1,1 @@\n-old-two\n+new-two\n",
	})
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected success response, got %+v", resp)
	}
	if repositoryState, _ := resp["repository_state"].(string); repositoryState != "changed" {
		t.Fatalf("expected changed repository state, got %+v", resp)
	}
	oneBytes, err := os.ReadFile(onePath)
	if err != nil {
		t.Fatalf("ReadFile(one.txt): %v", err)
	}
	twoBytes, err := os.ReadFile(twoPath)
	if err != nil {
		t.Fatalf("ReadFile(two.txt): %v", err)
	}
	if string(oneBytes) != "new-one\n" || string(twoBytes) != "new-two\n" {
		t.Fatalf("expected both files patched, got one=%q two=%q", string(oneBytes), string(twoBytes))
	}
}

func TestAgentrailBinaryJSONPatchTokenMismatch(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	resp, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "patch",
		"expected_file_tokens": map[string]any{
			"sample.txt": "sha256:deadbeef",
		},
		"diff": "--- a/sample.txt\n+++ b/sample.txt\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	})
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("expected failure response, got %+v", resp)
	}
	errorPayload, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error payload, got %+v", resp)
	}
	if code, _ := errorPayload["code"].(string); code != "token_mismatch" {
		t.Fatalf("expected token_mismatch, got %+v", errorPayload)
	}
	unchanged, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(unchanged) != "old\n" {
		t.Fatalf("expected file to remain unchanged, got %q", string(unchanged))
	}
}

func TestAgentrailBinaryJSONPatchMalformedDiff(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()

	resp, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "patch",
		"diff":   "@@ -1,1 +1,1 @@\n-old\n+new\n",
	})
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("expected failure response, got %+v", resp)
	}
	errorPayload, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error payload, got %+v", resp)
	}
	if code, _ := errorPayload["code"].(string); code != "patch_failed" {
		t.Fatalf("expected patch_failed, got %+v", errorPayload)
	}
	message, _ := errorPayload["message"].(string)
	if !strings.Contains(message, "no file headers") {
		t.Fatalf("expected missing file headers guidance, got %+v", errorPayload)
	}
	details, ok := errorPayload["details"].(map[string]any)
	if !ok {
		t.Fatalf("expected structured error details, got %+v", errorPayload)
	}
	if details["field"] != "diff" || details["reason"] != "missing_file_headers" {
		t.Fatalf("expected diff-specific details, got %+v", details)
	}
}

func buildAgentrailBinary(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	name := "agentrail-test"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	exePath := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", exePath, ".")
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(output))
	}
	return exePath
}

func runAgentrailJSON(t *testing.T, exePath, workspace string, payload map[string]any) (map[string]any, string) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	cmd := exec.Command(exePath, "--json")
	cmd.Dir = workspace
	cmd.Stdin = bytes.NewReader(data)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("agentrail run failed: %v", err)
		}
	}

	var resp map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	return resp, strings.TrimSpace(stderr.String())
}
