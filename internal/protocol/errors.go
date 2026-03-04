package protocol

import "fmt"

const (
	CodeInvalidRequest    = "invalid_request"
	CodePathDenied        = "path_denied"
	CodeNotFound          = "not_found"
	CodeBinaryFile        = "binary_file"
	CodeTooLarge          = "too_large"
	CodeSearchError       = "search_error"
	CodePatchFailed       = "patch_failed"
	CodeExecFailed        = "exec_failed"
	CodeTimeout           = "timeout"
	CodeWorkspaceRequired = "workspace_required"
)

type ToolError struct {
	Code    string
	Message string
}

func (e *ToolError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func Err(code, message string) *ToolError {
	return &ToolError{Code: code, Message: message}
}

func AsToolError(err error) (*ToolError, bool) {
	if err == nil {
		return nil, false
	}
	te, ok := err.(*ToolError)
	return te, ok
}
