package main

import (
	"os"
	"path/filepath"
	"testing"

	patchmod "agentrail/internal/patch"
	"agentrail/internal/protocol"
	searchmod "agentrail/internal/search"
	"agentrail/internal/workspace"
)

func TestHandleJSONEchoesRequestID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	resp := handleJSON(manager, false, []byte(`{"request_id":"req-1","action":"read","path":"sample.txt"}`))
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected success response, got %+v", resp)
	}
	if requestID, _ := resp["request_id"].(string); requestID != "req-1" {
		t.Fatalf("expected request_id echo, got %+v", resp)
	}
}

func TestHandleJSONReadIncludesPagingFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	content := "aa\r\nbb\r\ncc\r\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	resp := handleJSON(manager, false, []byte(`{"action":"read","path":"sample.txt","max_bytes":8}`))
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected success response, got %+v", resp)
	}
	if truncated, _ := resp["truncated"].(bool); !truncated {
		t.Fatalf("expected truncated response, got %+v", resp)
	}
	if hasMore, _ := resp["has_more"].(bool); !hasMore {
		t.Fatalf("expected has_more response, got %+v", resp)
	}
	nextStartLine, ok := resp["next_start_line"].(int)
	if !ok || nextStartLine != 3 {
		t.Fatalf("expected next_start_line=3, got %+v", resp)
	}
}

func TestHandleJSONReadBinaryFileUsesCanonicalPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "binary.bin")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte{'n', 0, 'x'}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	resp := handleJSON(manager, false, []byte(`{"action":"read","path":"nested/binary.bin"}`))
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("expected failure response, got %+v", resp)
	}
	errPayload, ok := resp["error"].(protocol.ErrorPayload)
	if !ok {
		t.Fatalf("expected error payload, got %+v", resp)
	}
	if errPayload.Code != protocol.CodeBinaryFile {
		t.Fatalf("expected binary_file, got %+v", errPayload)
	}
	if errPayload.Details["path"] != "nested/binary.bin" {
		t.Fatalf("expected canonical workspace-relative path, got %+v", errPayload)
	}
}

func TestHandleJSONMalformedJSONUsesJSONAction(t *testing.T) {
	manager, err := workspace.NewManagerFromRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	resp := handleJSON(manager, false, []byte("{"))
	if action, _ := resp["action"].(string); action != "json" {
		t.Fatalf("expected json action, got %+v", resp)
	}
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("expected failure response, got %+v", resp)
	}
	errPayload, ok := resp["error"].(protocol.ErrorPayload)
	if !ok {
		t.Fatalf("expected error payload, got %+v", resp)
	}
	if errPayload.Code != protocol.CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %+v", errPayload)
	}
}

func TestHandleJSONExecRejectsInvalidMaxOutputBytes(t *testing.T) {
	manager, err := workspace.NewManagerFromRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	resp := handleJSON(manager, false, []byte(`{"action":"exec","argv":["cmd"],"max_output_bytes":0}`))
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("expected failure response, got %+v", resp)
	}
	errPayload, ok := resp["error"].(protocol.ErrorPayload)
	if !ok {
		t.Fatalf("expected error payload, got %+v", resp)
	}
	if errPayload.Code != protocol.CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %+v", errPayload)
	}
	if errPayload.Details["field"] != "max_output_bytes" {
		t.Fatalf("expected max_output_bytes detail, got %+v", errPayload)
	}
}

func TestHandleJSONPatchResponseIncludesRepositoryState(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	payload := []byte(`{"action":"patch","diff":"--- a/sample.txt\n+++ b/sample.txt\n@@ -1,1 +1,1 @@\n-hello\n+world\n"}`)
	resp := handleJSON(manager, false, payload)
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected success response, got %+v", resp)
	}
	if repositoryState, _ := resp["repository_state"].(string); repositoryState != patchmod.RepositoryStateChanged {
		t.Fatalf("expected changed repository state, got %+v", resp)
	}
}

func TestHandleJSONSchemaPatchReturnsAuthoritativeContract(t *testing.T) {
	manager, err := workspace.NewManagerFromRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	resp := handleJSON(manager, false, []byte(`{"action":"schema","target":"patch"}`))
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected success response, got %+v", resp)
	}
	if action, _ := resp["action"].(string); action != "schema" {
		t.Fatalf("expected schema action, got %+v", resp)
	}
	if target, _ := resp["target"].(string); target != "patch" {
		t.Fatalf("expected patch target, got %+v", resp)
	}
	requestSchema, ok := resp["request_schema"].(map[string]any)
	if !ok {
		t.Fatalf("expected request_schema object, got %+v", resp)
	}
	properties, ok := requestSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected schema properties, got %+v", requestSchema)
	}
	if _, ok := properties["diff"]; !ok {
		t.Fatalf("expected diff property, got %+v", properties)
	}
	notes, ok := resp["notes"].([]string)
	if !ok || len(notes) == 0 {
		t.Fatalf("expected notes, got %+v", resp)
	}
	if notes[0] != "AgentRail JSON patch endpoint accepts unified diff text in diff only." {
		t.Fatalf("unexpected notes: %+v", notes)
	}
}

func TestHandleJSONSchemaBuildPatchAndReplaceReturnContracts(t *testing.T) {
	manager, err := workspace.NewManagerFromRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	for _, target := range []string{"build_patch", "replace"} {
		resp := handleJSON(manager, false, []byte(`{"action":"schema","target":"`+target+`"}`))
		if ok, _ := resp["ok"].(bool); !ok {
			t.Fatalf("%s: expected success response, got %+v", target, resp)
		}
		if got, _ := resp["target"].(string); got != target {
			t.Fatalf("%s: expected target echo, got %+v", target, resp)
		}
		requestSchema, ok := resp["request_schema"].(map[string]any)
		if !ok {
			t.Fatalf("%s: expected request schema, got %+v", target, resp)
		}
		properties, ok := requestSchema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: expected properties, got %+v", target, requestSchema)
		}
		if _, ok := properties["content"]; !ok {
			t.Fatalf("%s: expected content property, got %+v", target, properties)
		}
		if _, ok := properties["expected_file_token"]; !ok {
			t.Fatalf("%s: expected expected_file_token property, got %+v", target, properties)
		}
	}
}

func TestHandleJSONBuildPatchAndReplaceRequireContent(t *testing.T) {
	manager, err := workspace.NewManagerFromRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	for _, action := range []string{"build_patch", "replace"} {
		resp := handleJSON(manager, false, []byte(`{"action":"`+action+`","path":"sample.txt"}`))
		if ok, _ := resp["ok"].(bool); ok {
			t.Fatalf("%s: expected failure response, got %+v", action, resp)
		}
		errPayload, ok := resp["error"].(protocol.ErrorPayload)
		if !ok {
			t.Fatalf("%s: expected error payload, got %+v", action, resp)
		}
		if errPayload.Code != protocol.CodeInvalidRequest {
			t.Fatalf("%s: expected invalid_request, got %+v", action, errPayload)
		}
		if errPayload.Details["field"] != "content" {
			t.Fatalf("%s: expected content detail, got %+v", action, errPayload)
		}
	}
}

func TestHandleJSONBuildPatchReturnsGeneratedDiff(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	resp := handleJSON(manager, false, []byte(`{"action":"build_patch","path":"sample.txt","content":"world\n"}`))
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected success response, got %+v", resp)
	}
	if changed, _ := resp["changed"].(bool); !changed {
		t.Fatalf("expected changed response, got %+v", resp)
	}
	diff, _ := resp["diff"].(string)
	if diff == "" {
		t.Fatalf("expected generated diff, got %+v", resp)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(current) != "hello\n" {
		t.Fatalf("expected build_patch to be non-mutating, got %q", string(current))
	}
}

func TestHandleJSONReplaceNoOpReturnsUnchangedRepositoryState(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	resp := handleJSON(manager, false, []byte(`{"action":"replace","path":"sample.txt","content":"hello\n"}`))
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected success response, got %+v", resp)
	}
	if repositoryState, _ := resp["repository_state"].(string); repositoryState != patchmod.RepositoryStateUnchanged {
		t.Fatalf("expected unchanged repository state, got %+v", resp)
	}
	results, ok := resp["results"].([]patchmod.FileResult)
	if !ok || len(results) != 1 || !results[0].OK {
		t.Fatalf("expected successful no-op result, got %+v", resp)
	}
}

func TestHandleJSONUsesConsistentCanonicalPathsAcrossActions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "sample.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manager, err := workspace.NewManagerFromRoot(root)
	if err != nil {
		t.Fatalf("NewManagerFromRoot: %v", err)
	}

	filesResp := handleJSON(manager, false, []byte(`{"action":"files"}`))
	paths, ok := filesResp["paths"].([]string)
	if !ok || len(paths) != 1 || paths[0] != "nested/sample.txt" {
		t.Fatalf("unexpected files response: %+v", filesResp)
	}

	readResp := handleJSON(manager, false, []byte(`{"action":"read","path":"nested/sample.txt"}`))
	if got, _ := readResp["path"].(string); got != "nested/sample.txt" {
		t.Fatalf("unexpected read path: %+v", readResp)
	}

	searchResp := handleJSON(manager, false, []byte(`{"action":"search","query":"needle"}`))
	matches, ok := searchResp["matches"].([]searchmod.Match)
	if !ok || len(matches) != 1 || matches[0].Path != "nested/sample.txt" {
		t.Fatalf("unexpected search response: %+v", searchResp)
	}

	patchResp := handleJSON(manager, false, []byte(`{"action":"patch","diff":"--- a/nested/sample.txt\n+++ b/nested/sample.txt\n@@ -1,1 +1,1 @@\n-needle\n+thread\n"}`))
	if ok, _ := patchResp["ok"].(bool); !ok {
		t.Fatalf("expected patch success, got %+v", patchResp)
	}
	results, ok := patchResp["results"].([]patchmod.FileResult)
	if !ok || len(results) != 1 || results[0].Path != "nested/sample.txt" {
		t.Fatalf("unexpected patch results: %+v", patchResp)
	}
}
