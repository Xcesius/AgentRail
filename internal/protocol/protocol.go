package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"runtime"
	"sort"
	"strings"
)

const ProtocolVersion = 1

var ToolVersion = "0.0.0-dev+0000000"

var (
	baseCapabilities = []string{
		"build_patch",
		"exec",
		"exec_workspace_env",
		"exec_output_budget",
		"files",
		"files_pagination",
		"patch",
		"patch_atomic",
		"patch_expected_file_tokens",
		"read",
		"read_file_token",
		"replace",
		"schema",
		"search",
		"write",
	}
	cachedCapabilities []string
	actionAllowedKeys  = map[string]map[string]struct{}{
		"schema":      {"action": {}, "request_id": {}, "target": {}},
		"files":       {"action": {}, "request_id": {}, "path": {}, "allow_outside_workspace": {}, "limit": {}, "cursor": {}},
		"search":      {"action": {}, "request_id": {}, "query": {}, "path": {}, "case_sensitive": {}, "regex": {}, "glob": {}, "limit": {}, "max_file_bytes": {}, "deterministic": {}, "allow_outside_workspace": {}},
		"read":        {"action": {}, "request_id": {}, "path": {}, "start_line": {}, "end_line": {}, "max_bytes": {}, "allow_outside_workspace": {}},
		"write":       {"action": {}, "request_id": {}, "path": {}, "content": {}, "create_dirs": {}},
		"patch":       {"action": {}, "request_id": {}, "diff": {}, "atomic": {}, "expected_file_tokens": {}},
		"build_patch": {"action": {}, "request_id": {}, "path": {}, "content": {}, "expected_file_token": {}},
		"replace":     {"action": {}, "request_id": {}, "path": {}, "content": {}, "expected_file_token": {}, "create_dirs": {}},
		"exec":        {"action": {}, "request_id": {}, "argv": {}, "cwd": {}, "env": {}, "timeout_ms": {}, "max_output_bytes": {}},
	}
)

type Request struct {
	RequestID             string            `json:"request_id,omitempty"`
	Action                string            `json:"action"`
	Target                string            `json:"target,omitempty"`
	Path                  string            `json:"path,omitempty"`
	Query                 string            `json:"query,omitempty"`
	Content               *string           `json:"content,omitempty"`
	Diff                  *string           `json:"diff,omitempty"`
	Argv                  []string          `json:"argv,omitempty"`
	CWD                   string            `json:"cwd,omitempty"`
	Env                   json.RawMessage   `json:"env,omitempty"`
	TimeoutMS             int               `json:"timeout_ms,omitempty"`
	MaxOutputBytes        *int64            `json:"max_output_bytes,omitempty"`
	StartLine             int               `json:"start_line,omitempty"`
	EndLine               int               `json:"end_line,omitempty"`
	MaxBytes              int64             `json:"max_bytes,omitempty"`
	CaseSensitive         bool              `json:"case_sensitive,omitempty"`
	Regex                 bool              `json:"regex,omitempty"`
	Glob                  string            `json:"glob,omitempty"`
	Limit                 int               `json:"limit,omitempty"`
	Cursor                string            `json:"cursor,omitempty"`
	MaxFileBytes          int64             `json:"max_file_bytes,omitempty"`
	Deterministic         *bool             `json:"deterministic,omitempty"`
	AllowOutsideWorkspace bool              `json:"allow_outside_workspace,omitempty"`
	CreateDirs            bool              `json:"create_dirs,omitempty"`
	Atomic                bool              `json:"atomic,omitempty"`
	ExpectedFileToken     string            `json:"expected_file_token,omitempty"`
	ExpectedFileTokens    map[string]string `json:"expected_file_tokens,omitempty"`
}

type ErrorPayload struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Details ErrorDetails `json:"details,omitempty"`
}

func ParseRequest(data []byte) (Request, error) {
	var req Request
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return Request{}, classifyRequestDecodeError(err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return Request{}, ErrDetails(CodeInvalidRequest, "request must contain exactly one JSON object", ErrorDetails{"reason": "multiple_objects"})
	}
	if strings.TrimSpace(req.Action) == "" {
		return Request{}, ErrDetails(CodeInvalidRequest, "action is required", ErrorDetails{"field": "action", "reason": "required"})
	}
	if err := validateActionFields(strings.ToLower(strings.TrimSpace(req.Action)), data); err != nil {
		return Request{}, err
	}
	return req, nil
}

func validateActionFields(action string, data []byte) error {
	allowed, knownAction := actionAllowedKeys[action]
	if !knownAction {
		return nil
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawFields); err != nil {
		return nil
	}
	var badKeys []string
	for key := range rawFields {
		if _, ok := allowed[key]; !ok {
			badKeys = append(badKeys, key)
		}
	}
	if len(badKeys) > 0 {
		sort.Strings(badKeys)
		return ErrDetails(CodeInvalidRequest, "invalid JSON request", ErrorDetails{"field": badKeys[0], "reason": "unknown_field"})
	}
	return nil
}

func Success(action string, fields map[string]any) map[string]any {
	resp := baseResponse(true, action)
	maps.Copy(resp, fields)
	return resp
}

func Failure(action, code, message string, fields map[string]any) map[string]any {
	return FailureWithDetails(action, code, message, nil, fields)
}

func FailureWithDetails(action, code, message string, details ErrorDetails, fields map[string]any) map[string]any {
	resp := baseResponse(false, action)
	resp["error"] = ErrorPayload{Code: code, Message: message, Details: cloneDetails(details)}
	maps.Copy(resp, fields)
	return resp
}

func WriteJSON(w io.Writer, payload any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(payload)
}

func GetCodeAndMessage(err error, fallbackCode string) (string, string) {
	payload := GetErrorPayload(err, fallbackCode)
	return payload.Code, payload.Message
}

func GetErrorPayload(err error, fallbackCode string) ErrorPayload {
	if err == nil {
		return ErrorPayload{}
	}
	if te, ok := AsToolError(err); ok {
		return ErrorPayload{Code: te.Code, Message: te.Message, Details: cloneDetails(te.Details)}
	}
	return ErrorPayload{Code: fallbackCode, Message: err.Error()}
}

func Capabilities() []string {
	out := make([]string, len(cachedCapabilities))
	copy(out, cachedCapabilities)
	return out
}

func baseResponse(ok bool, action string) map[string]any {
	return map[string]any{
		"ok":               ok,
		"action":           action,
		"protocol_version": ProtocolVersion,
		"tool_version":     ToolVersion,
		"capabilities":     Capabilities(),
	}
}

func classifyRequestDecodeError(err error) error {
	if errors.Is(err, io.EOF) {
		return ErrDetails(CodeInvalidRequest, "invalid JSON request", ErrorDetails{"reason": "empty"})
	}

	if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		field := requestFieldName(typeErr.Field)
		message := "invalid JSON request"
		details := ErrorDetails{"reason": "invalid_type"}
		if field != "" {
			details["field"] = field
		}
		return ErrDetails(CodeInvalidRequest, message, details)
	}

	if _, ok := errors.AsType[*json.SyntaxError](err); ok {
		return ErrDetails(CodeInvalidRequest, "invalid JSON request", ErrorDetails{"reason": "syntax"})
	}

	message := err.Error()
	if after, ok := strings.CutPrefix(message, "json: unknown field "); ok {
		field := strings.Trim(after, "\"")
		return ErrDetails(CodeInvalidRequest, "invalid JSON request", ErrorDetails{"field": field, "reason": "unknown_field"})
	}

	return ErrDetails(CodeInvalidRequest, "invalid JSON request", ErrorDetails{"reason": "decode_error"})
}

func requestFieldName(field string) string {
	if field == "" {
		return ""
	}
	switch field {
	case "RequestID", "request_id":
		return "request_id"
	case "Action", "action":
		return "action"
	case "Target", "target":
		return "target"
	case "Path", "path":
		return "path"
	case "Query", "query":
		return "query"
	case "Content", "content":
		return "content"
	case "Diff", "diff":
		return "diff"
	case "Argv", "argv":
		return "argv"
	case "CWD", "cwd":
		return "cwd"
	case "Env", "env":
		return "env"
	case "TimeoutMS", "timeout_ms":
		return "timeout_ms"
	case "MaxOutputBytes", "max_output_bytes":
		return "max_output_bytes"
	case "StartLine", "start_line":
		return "start_line"
	case "EndLine", "end_line":
		return "end_line"
	case "MaxBytes", "max_bytes":
		return "max_bytes"
	case "CaseSensitive", "case_sensitive":
		return "case_sensitive"
	case "Regex", "regex":
		return "regex"
	case "Glob", "glob":
		return "glob"
	case "Limit", "limit":
		return "limit"
	case "Cursor", "cursor":
		return "cursor"
	case "MaxFileBytes", "max_file_bytes":
		return "max_file_bytes"
	case "Deterministic", "deterministic":
		return "deterministic"
	case "AllowOutsideWorkspace", "allow_outside_workspace":
		return "allow_outside_workspace"
	case "CreateDirs", "create_dirs":
		return "create_dirs"
	case "Atomic", "atomic":
		return "atomic"
	case "ExpectedFileToken", "expected_file_token":
		return "expected_file_token"
	case "ExpectedFileTokens", "expected_file_tokens":
		return "expected_file_tokens"
	default:
		return strings.TrimSpace(field)
	}
}

func init() {
	sort.Strings(baseCapabilities)
	cachedCapabilities = make([]string, 0, len(baseCapabilities)+1)
	cachedCapabilities = append(cachedCapabilities, baseCapabilities...)
	if runtime.GOOS == "windows" {
		cachedCapabilities = append(cachedCapabilities, "exec_process_tree_kill")
	}
	sort.Strings(cachedCapabilities)
}
