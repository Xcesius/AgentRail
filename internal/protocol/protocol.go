package protocol

import (
	"bytes"
	"encoding/json"
	"io"
)

type Request struct {
	RequestID             string          `json:"request_id,omitempty"`
	Action                string          `json:"action"`
	Path                  string          `json:"path,omitempty"`
	Query                 string          `json:"query,omitempty"`
	Content               *string         `json:"content,omitempty"`
	Diff                  *string         `json:"diff,omitempty"`
	Argv                  []string        `json:"argv,omitempty"`
	CWD                   string          `json:"cwd,omitempty"`
	Env                   json.RawMessage `json:"env,omitempty"`
	TimeoutMS             int             `json:"timeout_ms,omitempty"`
	StartLine             int             `json:"start_line,omitempty"`
	EndLine               int             `json:"end_line,omitempty"`
	MaxBytes              int64           `json:"max_bytes,omitempty"`
	CaseSensitive         bool            `json:"case_sensitive,omitempty"`
	Regex                 bool            `json:"regex,omitempty"`
	Glob                  string          `json:"glob,omitempty"`
	Limit                 int             `json:"limit,omitempty"`
	MaxFileBytes          int64           `json:"max_file_bytes,omitempty"`
	Deterministic         *bool           `json:"deterministic,omitempty"`
	AllowOutsideWorkspace bool            `json:"allow_outside_workspace,omitempty"`
	CreateDirs            bool            `json:"create_dirs,omitempty"`
}

type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func ParseRequest(data []byte) (Request, error) {
	var req Request
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return Request{}, Err(CodeInvalidRequest, "invalid JSON request")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return Request{}, Err(CodeInvalidRequest, "request must contain exactly one JSON object")
	}
	if req.Action == "" {
		return Request{}, Err(CodeInvalidRequest, "action is required")
	}
	return req, nil
}

func Success(action string, fields map[string]any) map[string]any {
	resp := map[string]any{
		"ok":     true,
		"action": action,
	}
	for k, v := range fields {
		resp[k] = v
	}
	return resp
}

func Failure(action, code, message string, fields map[string]any) map[string]any {
	resp := map[string]any{
		"ok":     false,
		"action": action,
		"error": ErrorPayload{
			Code:    code,
			Message: message,
		},
	}
	for k, v := range fields {
		resp[k] = v
	}
	return resp
}

func WriteJSON(w io.Writer, payload any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(payload)
}

func GetCodeAndMessage(err error, fallbackCode string) (string, string) {
	if err == nil {
		return "", ""
	}
	if te, ok := AsToolError(err); ok {
		return te.Code, te.Message
	}
	return fallbackCode, err.Error()
}
