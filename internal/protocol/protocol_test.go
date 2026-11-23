package protocol

import (
	"fmt"
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

func TestParseRequestRejectsWhitespaceOnlyAction(t *testing.T) {
	_, err := ParseRequest([]byte(`{"action":"   "}`))
	if err == nil {
		t.Fatal("expected whitespace-only action to fail")
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

func TestParseRequestRejectsCrossActionFields(t *testing.T) {
	cases := []struct {
		name      string
		payload   string
		wantField string
	}{
		{"singular token on patch", `{"action":"patch","diff":"x","expected_file_token":"sha256:abc"}`, "expected_file_token"},
		{"plural tokens on replace", `{"action":"replace","path":"a.txt","content":"x","expected_file_tokens":{"a.txt":"sha256:abc"}}`, "expected_file_tokens"},
		{"limit on read", `{"action":"read","path":"a.txt","limit":10}`, "limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRequest([]byte(tc.payload))
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			te, ok := AsToolError(err)
			if !ok || te.Code != CodeInvalidRequest {
				t.Fatalf("expected invalid_request, got %v", err)
			}
			if te.Details["field"] != tc.wantField || te.Details["reason"] != "unknown_field" {
				t.Fatalf("unexpected error details: %+v", te.Details)
			}
		})
	}
}

func TestAsToolErrorUnwrapsWrappedErrors(t *testing.T) {
	inner := Err(CodeNotFound, "missing")
	wrapped := fmt.Errorf("outer: %w", inner)
	te, ok := AsToolError(wrapped)
	if !ok || te.Code != CodeNotFound {
		t.Fatalf("expected wrapped ToolError to unwrap, got ok=%v te=%v", ok, te)
	}
}
