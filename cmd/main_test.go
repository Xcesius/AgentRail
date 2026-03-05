package main

import (
	"os"
	"path/filepath"
	"testing"

	"agentrail/internal/protocol"
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
