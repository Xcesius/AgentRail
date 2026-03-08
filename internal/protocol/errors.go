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
	CodeTokenMismatch     = "token_mismatch"
	CodeCursorInvalid     = "cursor_invalid"
	CodeCursorStale       = "cursor_stale"
	CodeCommitFailed      = "commit_failed"
	CodeRollbackFailed    = "rollback_failed"
)

type ErrorDetails map[string]any

type ToolError struct {
	Code    string
	Message string
	Details ErrorDetails
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

func ErrDetails(code, message string, details ErrorDetails) *ToolError {
	if len(details) == 0 {
		return Err(code, message)
	}
	return &ToolError{Code: code, Message: message, Details: cloneDetails(details)}
}

func AsToolError(err error) (*ToolError, bool) {
	if err == nil {
		return nil, false
	}
	te, ok := err.(*ToolError)
	return te, ok
}

func cloneDetails(details ErrorDetails) ErrorDetails {
	if len(details) == 0 {
		return nil
	}
	cloned := make(ErrorDetails, len(details))
	for key, value := range details {
		cloned[key] = value
	}
	return cloned
}
