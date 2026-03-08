package protocol

import (
	"regexp"
	"sort"
	"testing"
)

func TestParseRequestRejectsUnknownField(t *testing.T) {
	_, err := ParseRequest([]byte(`{"action":"read","unknown":true}`))
	if err == nil {
		t.Fatalf("expected invalid request error")
	}
	te, ok := AsToolError(err)
	if !ok || te.Code != CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %v", err)
	}
	if te.Details["field"] != "unknown" {
		t.Fatalf("expected unknown field detail, got %+v", te.Details)
	}
}

func TestParseRequestRejectsMultipleObjects(t *testing.T) {
	_, err := ParseRequest([]byte(`{"action":"read"}{"action":"read"}`))
	if err == nil {
		t.Fatalf("expected invalid request error")
	}
	te, ok := AsToolError(err)
	if !ok || te.Code != CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %v", err)
	}
}

func TestParseRequestRequiresAction(t *testing.T) {
	_, err := ParseRequest([]byte(`{"path":"file.txt"}`))
	if err == nil {
		t.Fatalf("expected invalid request error")
	}
	te, ok := AsToolError(err)
	if !ok || te.Code != CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %v", err)
	}
	if te.Details["field"] != "action" {
		t.Fatalf("expected action field detail, got %+v", te.Details)
	}
}

func TestResponsesIncludeEnvelopeFields(t *testing.T) {
	success := Success("read", map[string]any{"path": "sample.txt"})
	failure := FailureWithDetails("exec", CodeExecFailed, "boom", ErrorDetails{"argv0": "cmd"}, nil)

	for name, payload := range map[string]map[string]any{"success": success, "failure": failure} {
		if _, ok := payload["protocol_version"].(int); !ok {
			t.Fatalf("%s: missing protocol_version: %+v", name, payload)
		}
		toolVersion, ok := payload["tool_version"].(string)
		if !ok {
			t.Fatalf("%s: missing tool_version: %+v", name, payload)
		}
		if !regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(-dev\+[0-9a-fA-F]+)?$`).MatchString(toolVersion) {
			t.Fatalf("%s: unexpected tool_version format %q", name, toolVersion)
		}
		capabilities, ok := payload["capabilities"].([]string)
		if !ok {
			t.Fatalf("%s: missing capabilities: %+v", name, payload)
		}
		if !sort.StringsAreSorted(capabilities) {
			t.Fatalf("%s: capabilities must be sorted: %+v", name, capabilities)
		}
		seen := map[string]struct{}{}
		for _, capability := range capabilities {
			if _, exists := seen[capability]; exists {
				t.Fatalf("%s: duplicate capability %q", name, capability)
			}
			seen[capability] = struct{}{}
		}
	}
}
