package protocol

import "testing"

func TestParseRequestRejectsUnknownField(t *testing.T) {
	_, err := ParseRequest([]byte(`{"action":"read","unknown":true}`))
	if err == nil {
		t.Fatalf("expected invalid request error")
	}
	te, ok := AsToolError(err)
	if !ok || te.Code != CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %v", err)
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
}
