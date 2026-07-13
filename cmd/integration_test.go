package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	buildAgentrailOnce sync.Once
	buildAgentrailPath string
	buildAgentrailErr  error
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

func TestAgentrailBinaryJSONReplaceSuccess(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	resp, stderr := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action":  "replace",
		"path":    "sample.txt",
		"content": "new\n",
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
		t.Fatalf("expected replaced file, got %q", string(updated))
	}
}

func TestAgentrailBinaryJSONReplaceTokenMismatch(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	resp, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action":              "replace",
		"path":                "sample.txt",
		"content":             "new\n",
		"expected_file_token": "sha256:deadbeef",
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

func TestAgentrailBinaryJSONReplaceNoOp(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(target, []byte("same\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	resp, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action":  "replace",
		"path":    "sample.txt",
		"content": "same\n",
	})
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected success response, got %+v", resp)
	}
	if repositoryState, _ := resp["repository_state"].(string); repositoryState != "unchanged" {
		t.Fatalf("expected unchanged repository state, got %+v", resp)
	}
}

func TestAgentrailBinaryJSONBuildPatchOutputCanBeApplied(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	buildResp, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action":  "build_patch",
		"path":    "sample.txt",
		"content": "new\n",
	})
	if ok, _ := buildResp["ok"].(bool); !ok {
		t.Fatalf("expected build_patch success, got %+v", buildResp)
	}
	diff, _ := buildResp["diff"].(string)
	if diff == "" {
		t.Fatalf("expected generated diff, got %+v", buildResp)
	}

	patchResp, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "patch",
		"diff":   diff,
	})
	if ok, _ := patchResp["ok"].(bool); !ok {
		t.Fatalf("expected patch success, got %+v", patchResp)
	}
	updated, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(updated) != "new\n" {
		t.Fatalf("expected patched file, got %q", string(updated))
	}
}

func TestAgentrailBinaryCLIReplaceAndBuildPatch(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	replaceResp, stderr := runAgentrailCLI(t, exePath, workspace, "new\n", "replace", "sample.txt")
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}
	if ok, _ := replaceResp["ok"].(bool); !ok {
		t.Fatalf("expected replace success, got %+v", replaceResp)
	}
	updated, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(updated) != "new\n" {
		t.Fatalf("expected CLI replace to update file, got %q", string(updated))
	}

	buildResp, stderr := runAgentrailCLI(t, exePath, workspace, "newer\n", "build-patch", "sample.txt")
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}
	if ok, _ := buildResp["ok"].(bool); !ok {
		t.Fatalf("expected build-patch success, got %+v", buildResp)
	}
	diff, _ := buildResp["diff"].(string)
	if diff == "" {
		t.Fatalf("expected generated diff, got %+v", buildResp)
	}
}

func TestAgentrailBinaryJSONPatchFailsOnStaleReadToken(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	readResp, stderr := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "read",
		"path":   "sample.txt",
	})
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}
	if ok, _ := readResp["ok"].(bool); !ok {
		t.Fatalf("expected read success, got %+v", readResp)
	}
	fileToken, _ := readResp["file_token"].(string)
	if fileToken == "" {
		t.Fatalf("expected file_token in read response, got %+v", readResp)
	}

	if err := os.WriteFile(target, []byte("external\n"), 0o644); err != nil {
		t.Fatalf("WriteFile external update: %v", err)
	}

	patchResp, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "patch",
		"atomic": true,
		"expected_file_tokens": map[string]any{
			"sample.txt": fileToken,
		},
		"diff": "--- a/sample.txt\n+++ b/sample.txt\n@@ -1,1 +1,1 @@\n-old\n+patched\n",
	})
	if ok, _ := patchResp["ok"].(bool); ok {
		t.Fatalf("expected stale-token patch failure, got %+v", patchResp)
	}
	errorPayload := requireErrorCode(t, patchResp, "token_mismatch")
	if repositoryState, _ := patchResp["repository_state"].(string); repositoryState != "unchanged" {
		t.Fatalf("expected unchanged repository state, got %+v", patchResp)
	}
	details, _ := errorPayload["details"].(map[string]any)
	if details["path"] != "sample.txt" {
		t.Fatalf("expected sample.txt token mismatch details, got %+v", details)
	}
	results := responseResults(t, patchResp)
	if len(results) != 1 {
		t.Fatalf("expected one file result, got %+v", patchResp["results"])
	}
	if results[0]["error_code"] != "token_mismatch" {
		t.Fatalf("expected per-file token_mismatch, got %+v", results[0])
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(after) != "external\n" {
		t.Fatalf("expected external content to remain, got %q", string(after))
	}
}

func TestAgentrailBinaryJSONPatchAtomicRollbackOnValidationFailure(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	files := map[string]string{
		"one.txt":   "old-one\n",
		"two.txt":   "old-two\n",
		"three.txt": "old-three\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	resp, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "patch",
		"atomic": true,
		"diff": strings.Join([]string{
			"--- a/one.txt\n+++ b/one.txt\n@@ -1,1 +1,1 @@\n-old-one\n+new-one\n",
			"--- a/two.txt\n+++ b/two.txt\n@@ -1,1 +1,1 @@\n-missing-two\n+new-two\n",
			"--- a/three.txt\n+++ b/three.txt\n@@ -1,1 +1,1 @@\n-old-three\n+new-three\n",
		}, ""),
	})
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("expected atomic validation failure, got %+v", resp)
	}
	requireErrorCode(t, resp, "patch_failed")
	if repositoryState, _ := resp["repository_state"].(string); repositoryState != "unchanged" {
		t.Fatalf("expected unchanged repository state, got %+v", resp)
	}
	if changed := asStringSlice(t, resp["files_changed"]); len(changed) != 0 {
		t.Fatalf("expected no changed files, got %+v", changed)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(workspace, name))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("expected %s to remain unchanged, got %q", name, string(got))
		}
	}
	for _, result := range responseResults(t, resp) {
		if changed, _ := result["changed"].(bool); changed {
			t.Fatalf("did not expect changed=true after atomic rollback, got %+v", result)
		}
	}
}

func TestAgentrailBinaryJSONExecReportsTruncationAndExitMetadata(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()

	resp, stderr := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "exec",
		"argv": []string{
			os.Args[0],
			"-test.run=TestAgentrailExecHelperProcess",
			"--",
			"stdout-bytes=8",
			"stderr-bytes=8",
			"exit-code=7",
		},
		"env": map[string]string{
			"GO_WANT_AGENTRAIL_HELPER_PROCESS": "1",
		},
		"max_output_bytes": 10,
	})
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected exec success response, got %+v", resp)
	}
	if exitCode, _ := resp["exit_code"].(float64); int(exitCode) != 7 {
		t.Fatalf("expected exit code 7, got %+v", resp)
	}
	if outputBytes, _ := resp["output_bytes"].(float64); int(outputBytes) != 10 {
		t.Fatalf("expected output_bytes=10, got %+v", resp)
	}
	stdout, _ := resp["stdout"].(string)
	stderrText, _ := resp["stderr"].(string)
	if len(stdout)+len(stderrText) != 10 {
		t.Fatalf("expected 10 combined output bytes, got stdout=%q stderr=%q", stdout, stderrText)
	}
	stdoutTruncated, _ := resp["stdout_truncated"].(bool)
	stderrTruncated, _ := resp["stderr_truncated"].(bool)
	if !stdoutTruncated && !stderrTruncated {
		t.Fatalf("expected at least one truncated stream, got %+v", resp)
	}
}

func TestAgentrailBinaryJSONReplacePreservesExactCRLFBytes(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	desired := "one\r\ntwo\r\n"

	resp, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action":  "replace",
		"path":    "crlf.txt",
		"content": desired,
	})
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected replace success, got %+v", resp)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "crlf.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != desired {
		t.Fatalf("replace changed requested bytes: got %q want %q", data, desired)
	}

	lf := "one\ntwo\n"
	resp, _ = runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action":  "replace",
		"path":    "crlf.txt",
		"content": lf,
	})
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected line-ending-only replace success, got %+v", resp)
	}
	data, err = os.ReadFile(filepath.Join(workspace, "crlf.txt"))
	if err != nil || string(data) != lf {
		t.Fatalf("line-ending-only replace failed: data=%q err=%v", data, err)
	}
}

func TestAgentrailBinaryJSONExecTimeoutKillsProcessTreeOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific process-tree kill behavior")
	}

	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	markerPath := filepath.Join(t.TempDir(), "marker.txt")

	resp, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "exec",
		"argv": []string{
			os.Args[0],
			"-test.run=TestAgentrailExecHelperProcess",
			"--",
			"spawn-marker=" + markerPath,
		},
		"env": map[string]string{
			"GO_WANT_AGENTRAIL_HELPER_PROCESS": "1",
		},
		"timeout_ms": 100,
	})
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("expected timeout response, got %+v", resp)
	}
	errorPayload := requireErrorCode(t, resp, "timeout")
	if exitCode, _ := resp["exit_code"].(float64); int(exitCode) != -1 {
		t.Fatalf("expected timeout exit code -1, got %+v", resp)
	}
	details, _ := errorPayload["details"].(map[string]any)
	if processTreeKilled, _ := details["process_tree_killed"].(bool); !processTreeKilled {
		t.Fatalf("expected process_tree_killed=true, got %+v", details)
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("expected marker to stay absent, got %v", err)
	}
}

func TestAgentrailBinaryJSONFilesPaginationAndCursorErrors(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	page1, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "files",
		"limit":  2,
	})
	if ok, _ := page1["ok"].(bool); !ok {
		t.Fatalf("expected files page 1 success, got %+v", page1)
	}
	if got := asStringSlice(t, page1["paths"]); strings.Join(got, ",") != "a.txt,b.txt" {
		t.Fatalf("unexpected page 1 paths: %+v", got)
	}
	if hasMore, _ := page1["has_more"].(bool); !hasMore {
		t.Fatalf("expected page 1 continuation, got %+v", page1)
	}
	cursor, _ := page1["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("expected page 1 cursor, got %+v", page1)
	}

	page2, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "files",
		"limit":  2,
		"cursor": cursor,
	})
	if ok, _ := page2["ok"].(bool); !ok {
		t.Fatalf("expected files page 2 success, got %+v", page2)
	}
	if got := asStringSlice(t, page2["paths"]); strings.Join(got, ",") != "c.txt" {
		t.Fatalf("unexpected page 2 paths: %+v", got)
	}
	if hasMore, _ := page2["has_more"].(bool); hasMore {
		t.Fatalf("expected final page, got %+v", page2)
	}

	invalid, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "files",
		"limit":  1,
		"cursor": "not-a-cursor",
	})
	if ok, _ := invalid["ok"].(bool); ok {
		t.Fatalf("expected invalid cursor failure, got %+v", invalid)
	}
	requireErrorCode(t, invalid, "cursor_invalid")

	stalePage, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "files",
		"limit":  2,
	})
	staleCursor, _ := stalePage["next_cursor"].(string)
	if staleCursor == "" {
		t.Fatalf("expected stale test cursor, got %+v", stalePage)
	}
	if err := os.Remove(filepath.Join(workspace, "b.txt")); err != nil {
		t.Fatalf("Remove(b.txt): %v", err)
	}
	stale, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "files",
		"limit":  2,
		"cursor": staleCursor,
	})
	if ok, _ := stale["ok"].(bool); ok {
		t.Fatalf("expected stale cursor failure, got %+v", stale)
	}
	requireErrorCode(t, stale, "cursor_stale")
}

func TestAgentrailBinaryJSONReadPaginationBoundariesAndOversizedLine(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "sample.txt")
	content := "one\r\ntwo\r\nthree\r\n"
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(sample.txt): %v", err)
	}

	page1, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action":     "read",
		"path":       "sample.txt",
		"start_line": 1,
		"max_bytes":  len("one\r\ntwo\r\n"),
	})
	if ok, _ := page1["ok"].(bool); !ok {
		t.Fatalf("expected read page 1 success, got %+v", page1)
	}
	if got, _ := page1["content"].(string); got != "one\r\ntwo\r\n" {
		t.Fatalf("unexpected page 1 content: %q", got)
	}
	if truncated, _ := page1["truncated"].(bool); !truncated {
		t.Fatalf("expected truncated page 1, got %+v", page1)
	}
	if nextStart, _ := page1["next_start_line"].(float64); int(nextStart) != 3 {
		t.Fatalf("expected next_start_line=3, got %+v", page1)
	}
	token1, _ := page1["file_token"].(string)
	if token1 == "" {
		t.Fatalf("expected page 1 file token, got %+v", page1)
	}

	page2, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action":     "read",
		"path":       "sample.txt",
		"start_line": 3,
		"max_bytes":  1024,
	})
	if ok, _ := page2["ok"].(bool); !ok {
		t.Fatalf("expected read page 2 success, got %+v", page2)
	}
	if got, _ := page2["content"].(string); got != "three\r\n" {
		t.Fatalf("unexpected page 2 content: %q", got)
	}
	if token2, _ := page2["file_token"].(string); token2 != token1 {
		t.Fatalf("expected stable file token across pages, got %q vs %q", token1, token2)
	}
	if hasMore, _ := page2["has_more"].(bool); hasMore {
		t.Fatalf("expected final page, got %+v", page2)
	}

	longLine := strings.Repeat("a", 32) + "\nrest\n"
	if err := os.WriteFile(filepath.Join(workspace, "wide.txt"), []byte(longLine), 0o644); err != nil {
		t.Fatalf("WriteFile(wide.txt): %v", err)
	}
	tooLarge, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action":     "read",
		"path":       "wide.txt",
		"start_line": 1,
		"max_bytes":  8,
	})
	if ok, _ := tooLarge["ok"].(bool); ok {
		t.Fatalf("expected too_large failure, got %+v", tooLarge)
	}
	requireErrorCode(t, tooLarge, "too_large")
}

func TestAgentrailBinaryJSONPathSecurity(t *testing.T) {
	exePath := buildAgentrailBinary(t)
	workspace := t.TempDir()
	insidePath := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(insidePath, []byte("inside\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(sample.txt): %v", err)
	}
	outsidePath := filepath.Join(filepath.Dir(workspace), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(outside.txt): %v", err)
	}

	normalizedRead, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "read",
		"path":   "nested/../sample.txt",
	})
	if ok, _ := normalizedRead["ok"].(bool); !ok {
		t.Fatalf("expected normalized read success, got %+v", normalizedRead)
	}
	if path, _ := normalizedRead["path"].(string); path != "sample.txt" {
		t.Fatalf("expected normalized display path sample.txt, got %+v", normalizedRead)
	}
	if content, _ := normalizedRead["content"].(string); content != "inside\n" {
		t.Fatalf("unexpected normalized read content: %q", content)
	}

	outsideDenied, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "read",
		"path":   outsidePath,
	})
	if ok, _ := outsideDenied["ok"].(bool); ok {
		t.Fatalf("expected outside read denial, got %+v", outsideDenied)
	}
	requireErrorCode(t, outsideDenied, "path_denied")

	outsideAllowed, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action":                  "read",
		"path":                    outsidePath,
		"allow_outside_workspace": true,
	})
	if ok, _ := outsideAllowed["ok"].(bool); !ok {
		t.Fatalf("expected outside read opt-in success, got %+v", outsideAllowed)
	}
	if content, _ := outsideAllowed["content"].(string); content != "outside\n" {
		t.Fatalf("unexpected outside read content: %q", content)
	}

	outsideWrite, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action":  "write",
		"path":    outsidePath,
		"content": "mutated\n",
	})
	if ok, _ := outsideWrite["ok"].(bool); ok {
		t.Fatalf("expected outside write denial, got %+v", outsideWrite)
	}
	requireErrorCode(t, outsideWrite, "path_denied")

	denylistedWrite, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action":  "write",
		"path":    ".git/config",
		"content": "[core]\nrepositoryformatversion = 0\n",
	})
	if ok, _ := denylistedWrite["ok"].(bool); ok {
		t.Fatalf("expected denylisted write failure, got %+v", denylistedWrite)
	}
	requireErrorCode(t, denylistedWrite, "path_denied")

	patchOutside, _ := runAgentrailJSON(t, exePath, workspace, map[string]any{
		"action": "patch",
		"atomic": true,
		"diff":   "--- a/../outside.txt\n+++ b/../outside.txt\n@@ -1,1 +1,1 @@\n-outside\n+mutated\n",
	})
	if ok, _ := patchOutside["ok"].(bool); ok {
		t.Fatalf("expected outside patch denial, got %+v", patchOutside)
	}
	requireErrorCode(t, patchOutside, "path_denied")
	if repositoryState, _ := patchOutside["repository_state"].(string); repositoryState != "unchanged" {
		t.Fatalf("expected unchanged repository state, got %+v", patchOutside)
	}
	outsideBytes, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatalf("ReadFile(outside.txt): %v", err)
	}
	if string(outsideBytes) != "outside\n" {
		t.Fatalf("expected outside file to remain unchanged, got %q", string(outsideBytes))
	}
}

func TestAgentrailExecHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_AGENTRAIL_HELPER_PROCESS") != "1" {
		return
	}
	index := -1
	for i, arg := range os.Args {
		if arg == "--" {
			index = i
			break
		}
	}
	if index == -1 || index+1 > len(os.Args) {
		fmt.Fprint(os.Stderr, "missing helper args")
		os.Exit(2)
	}

	helperArgs := os.Args[index+1:]
	exitCode := 0
	for _, arg := range helperArgs {
		switch {
		case strings.HasPrefix(arg, "stdout-bytes="):
			n, _ := strconv.Atoi(strings.TrimPrefix(arg, "stdout-bytes="))
			fmt.Fprint(os.Stdout, strings.Repeat("o", n))
		case strings.HasPrefix(arg, "stderr-bytes="):
			n, _ := strconv.Atoi(strings.TrimPrefix(arg, "stderr-bytes="))
			fmt.Fprint(os.Stderr, strings.Repeat("e", n))
		case strings.HasPrefix(arg, "exit-code="):
			exitCode, _ = strconv.Atoi(strings.TrimPrefix(arg, "exit-code="))
		case strings.HasPrefix(arg, "write-marker-after="):
			parts := strings.SplitN(strings.TrimPrefix(arg, "write-marker-after="), ":", 2)
			if len(parts) != 2 {
				fmt.Fprint(os.Stderr, "invalid write-marker-after")
				os.Exit(2)
			}
			delayMS, _ := strconv.Atoi(parts[0])
			time.Sleep(time.Duration(delayMS) * time.Millisecond)
			if err := os.WriteFile(parts[1], []byte("marker"), 0o644); err != nil {
				fmt.Fprint(os.Stderr, err.Error())
				os.Exit(2)
			}
		case strings.HasPrefix(arg, "spawn-marker="):
			markerPath := strings.TrimPrefix(arg, "spawn-marker=")
			child := exec.Command(os.Args[0], "-test.run=TestAgentrailExecHelperProcess", "--", "write-marker-after=300:"+markerPath)
			child.Env = append(os.Environ(), "GO_WANT_AGENTRAIL_HELPER_PROCESS=1")
			if err := child.Start(); err != nil {
				fmt.Fprint(os.Stderr, err.Error())
				os.Exit(2)
			}
			time.Sleep(time.Second)
		}
	}
	os.Exit(exitCode)
}

func buildAgentrailBinary(t *testing.T) string {
	t.Helper()
	buildAgentrailOnce.Do(func() {
		cwd, err := os.Getwd()
		if err != nil {
			buildAgentrailErr = err
			return
		}
		name := "agentrail-test"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		buildDir, err := os.MkdirTemp("", "agentrail-build-*")
		if err != nil {
			buildAgentrailErr = err
			return
		}
		buildAgentrailPath = filepath.Join(buildDir, name)
		cmd := exec.Command("go", "build", "-o", buildAgentrailPath, ".")
		cmd.Dir = cwd
		output, err := cmd.CombinedOutput()
		if err != nil {
			buildAgentrailErr = fmt.Errorf("go build failed: %w\n%s", err, string(output))
		}
	})
	if buildAgentrailErr != nil {
		t.Fatalf("buildAgentrailBinary: %v", buildAgentrailErr)
	}
	return buildAgentrailPath
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

func runAgentrailCLI(t *testing.T, exePath, workspace, stdin string, args ...string) (map[string]any, string) {
	t.Helper()

	cmd := exec.Command(exePath, args...)
	cmd.Dir = workspace
	cmd.Stdin = strings.NewReader(stdin)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("agentrail CLI run failed: %v", err)
		}
	}

	var resp map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal CLI response: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	return resp, strings.TrimSpace(stderr.String())
}

func requireErrorCode(t *testing.T, resp map[string]any, want string) map[string]any {
	t.Helper()
	errorPayload, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error payload, got %+v", resp)
	}
	if code, _ := errorPayload["code"].(string); code != want {
		t.Fatalf("expected error code %q, got %+v", want, errorPayload)
	}
	return errorPayload
}

func responseResults(t *testing.T, resp map[string]any) []map[string]any {
	t.Helper()
	raw, ok := resp["results"].([]any)
	if !ok {
		t.Fatalf("expected results array, got %+v", resp)
	}
	results := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("expected result object, got %+v", item)
		}
		results = append(results, entry)
	}
	return results
}

func asStringSlice(t *testing.T, value any) []string {
	t.Helper()
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("expected string slice, got %+v", value)
		}
		out = append(out, text)
	}
	return out
}
