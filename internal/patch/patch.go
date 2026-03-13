package patchmod

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"agentrail/internal/filemeta"
	"agentrail/internal/protocol"
	"agentrail/internal/workspace"
	writemod "agentrail/internal/write"
)

const (
	RepositoryStateUnchanged       = "unchanged"
	RepositoryStateChanged         = "changed"
	RepositoryStatePartiallyChange = "partially_changed"
	RepositoryStateAmbiguous       = "ambiguous"
)

type Options struct {
	Atomic             bool
	ExpectedFileTokens map[string]string
	CreateDirs         *bool
}

type FileResult struct {
	Path         string                `json:"path"`
	OK           bool                  `json:"ok"`
	Changed      bool                  `json:"changed"`
	HunksApplied int                   `json:"hunks_applied,omitempty"`
	Error        string                `json:"error,omitempty"`
	ErrorCode    string                `json:"error_code,omitempty"`
	ErrorDetails protocol.ErrorDetails `json:"error_details,omitempty"`
}

type ApplyResult struct {
	RepositoryState string       `json:"repository_state"`
	FilesChanged    []string     `json:"files_changed"`
	HunksApplied    int          `json:"hunks_applied"`
	Results         []FileResult `json:"results"`
}

type filePlan struct {
	DisplayPath    string
	ResolvedPath   string
	Operation      string
	OriginalExists bool
	OriginalBytes  []byte
	OriginalToken  string
	UpdatedBytes   []byte
	HunksApplied   int
	Changed        bool
	Result         FileResult
}

var (
	readFile        = os.ReadFile
	removeFile      = os.Remove
	writeFileAtomic = writemod.WriteFileAtomic
)

func Apply(manager *workspace.Manager, diff string, options Options) (ApplyResult, error) {
	parsed, err := Parse(diff)
	if err != nil {
		return ApplyResult{RepositoryState: RepositoryStateUnchanged, FilesChanged: []string{}, Results: []FileResult{}}, err
	}

	targets := make(map[string]struct{}, len(parsed.Files))
	plans := make([]filePlan, 0, len(parsed.Files))
	results := make([]FileResult, 0, len(parsed.Files))
	anyValidationFailure := false

	for _, filePatch := range parsed.Files {
		plan := buildPlan(manager, filePatch, options.ExpectedFileTokens)
		plans = append(plans, plan)
		results = append(results, plan.Result)
		targets[plan.DisplayPath] = struct{}{}
		if !plan.Result.OK {
			anyValidationFailure = true
		}
	}

	if err := validateExpectedTokenKeys(options.ExpectedFileTokens, targets); err != nil {
		return ApplyResult{RepositoryState: RepositoryStateUnchanged, FilesChanged: []string{}, Results: []FileResult{}}, err
	}

	if options.Atomic {
		if anyValidationFailure {
			result := ApplyResult{RepositoryState: RepositoryStateUnchanged, FilesChanged: []string{}, Results: results}
			return result, topLevelErrorFromResults(result, true)
		}
		return commitAtomic(plans, createDirsEnabled(options))
	}

	return commitNonAtomic(plans, createDirsEnabled(options))
}

func buildPlan(manager *workspace.Manager, filePatch FilePatch, expectedFileTokens map[string]string) filePlan {
	opType := patchType(filePatch)
	target := filePatch.NewPath
	if opType == "delete" {
		target = filePatch.OldPath
	}

	plan := filePlan{Operation: opType}
	resolved, err := manager.ResolveWritePath(target)
	if err != nil {
		payload := protocol.GetErrorPayload(err, protocol.CodePatchFailed)
		plan.DisplayPath = canonicalResultPath(target, payload.Details)
		plan.Result = failureResult(plan.DisplayPath, payload)
		return plan
	}

	plan.ResolvedPath = resolved
	plan.DisplayPath = manager.DisplayPath(resolved)
	original, exists, err := readOriginalFile(resolved)
	if err != nil {
		payload := protocol.GetErrorPayload(err, protocol.CodePatchFailed)
		plan.Result = failureResult(plan.DisplayPath, payload)
		return plan
	}
	plan.OriginalExists = exists
	plan.OriginalBytes = original
	if exists {
		plan.OriginalToken = filemeta.TokenFromBytes(original)
	}

	if opType == "create" && exists {
		plan.Result = failureResult(plan.DisplayPath, protocol.ErrorPayload{Code: protocol.CodePatchFailed, Message: "target file already exists", Details: protocol.ErrorDetails{"path": plan.DisplayPath, "phase": "validation"}})
		return plan
	}
	if opType != "create" && !exists {
		plan.Result = failureResult(plan.DisplayPath, protocol.ErrorPayload{Code: protocol.CodePatchFailed, Message: "target file does not exist", Details: protocol.ErrorDetails{"path": plan.DisplayPath, "phase": "validation"}})
		return plan
	}

	if expectedToken, ok := expectedFileTokens[plan.DisplayPath]; ok {
		actualToken := plan.OriginalToken
		if expectedToken != actualToken {
			plan.Result = failureResult(plan.DisplayPath, protocol.ErrorPayload{Code: protocol.CodeTokenMismatch, Message: "file token mismatch", Details: protocol.ErrorDetails{"path": plan.DisplayPath, "expected_file_token": expectedToken, "actual_file_token": actualToken}})
			return plan
		}
	}

	updatedText, hunksApplied, err := applyToContent(string(original), filePatch)
	if err != nil {
		payload := protocol.GetErrorPayload(err, protocol.CodePatchFailed)
		if payload.Details == nil {
			payload.Details = protocol.ErrorDetails{}
		}
		payload.Details["path"] = plan.DisplayPath
		payload.Details["phase"] = "validation"
		plan.Result = failureResult(plan.DisplayPath, payload)
		return plan
	}

	plan.UpdatedBytes = []byte(updatedText)
	plan.HunksApplied = hunksApplied
	if opType == "delete" && len(plan.UpdatedBytes) != 0 {
		plan.Result = failureResult(plan.DisplayPath, protocol.ErrorPayload{Code: protocol.CodePatchFailed, Message: "delete patch did not remove all content", Details: protocol.ErrorDetails{"path": plan.DisplayPath, "phase": "validation"}})
		return plan
	}
	plan.Changed = determineChanged(opType, exists, original, plan.UpdatedBytes)
	plan.Result = FileResult{
		Path:         plan.DisplayPath,
		OK:           true,
		Changed:      false,
		HunksApplied: 0,
	}
	return plan
}

func commitNonAtomic(plans []filePlan, createDirs bool) (ApplyResult, error) {
	result := ApplyResult{
		RepositoryState: RepositoryStateUnchanged,
		FilesChanged:    []string{},
		Results:         make([]FileResult, 0, len(plans)),
	}
	anyFailure := false
	ambiguous := false
	anyChanged := false

	for _, plan := range plans {
		if !plan.Result.OK {
			anyFailure = true
			result.Results = append(result.Results, plan.Result)
			continue
		}

		fileResult, err := applyCommittedPlan(plan, createDirs)
		result.Results = append(result.Results, fileResult)
		if err != nil {
			anyFailure = true
			changed, known := snapshotDiffersFromOriginal(plan)
			if !known {
				ambiguous = true
			} else if changed {
				anyChanged = true
			}
			continue
		}
		result.HunksApplied += plan.HunksApplied
		if plan.Changed {
			result.FilesChanged = append(result.FilesChanged, plan.DisplayPath)
			anyChanged = true
		}
	}

	if anyFailure {
		switch {
		case ambiguous:
			result.RepositoryState = RepositoryStateAmbiguous
		case anyChanged:
			result.RepositoryState = RepositoryStatePartiallyChange
		default:
			result.RepositoryState = RepositoryStateUnchanged
		}
		return result, topLevelErrorFromResults(result, false)
	}

	if anyChanged {
		result.RepositoryState = RepositoryStateChanged
	} else {
		result.RepositoryState = RepositoryStateUnchanged
	}
	return result, nil
}

func commitAtomic(plans []filePlan, createDirs bool) (ApplyResult, error) {
	result := ApplyResult{
		RepositoryState: RepositoryStateUnchanged,
		FilesChanged:    []string{},
		Results:         make([]FileResult, 0, len(plans)),
	}
	committed := make([]filePlan, 0, len(plans))

	for _, plan := range plans {
		if !plan.Changed {
			continue
		}
		fileResult, err := applyCommittedPlan(plan, createDirs)
		if err != nil {
			rollbackErrs := rollbackCommittedPlans(committed)
			repositoryState := determineAtomicFailureState(append(committed, plan), rollbackErrs)
			result.RepositoryState = repositoryState
			result.Results = atomicResults(plans, repositoryState)
			if repositoryState == RepositoryStateUnchanged {
				return result, protocol.ErrDetails(protocol.CodeCommitFailed, "atomic patch commit failed", protocol.ErrorDetails{"phase": "commit", "repository_state": repositoryState})
			}
			return result, protocol.ErrDetails(protocol.CodeRollbackFailed, "atomic patch commit failed and rollback was incomplete", protocol.ErrorDetails{"phase": "rollback", "repository_state": repositoryState})
		}
		committed = append(committed, plan)
		result.FilesChanged = append(result.FilesChanged, plan.DisplayPath)
		result.HunksApplied += plan.HunksApplied
		_ = fileResult
	}

	for _, plan := range plans {
		fileResult := FileResult{
			Path:         plan.DisplayPath,
			OK:           true,
			Changed:      plan.Changed,
			HunksApplied: plan.HunksApplied,
		}
		result.Results = append(result.Results, fileResult)
	}

	if len(result.FilesChanged) == 0 {
		result.RepositoryState = RepositoryStateUnchanged
	} else {
		result.RepositoryState = RepositoryStateChanged
	}
	return result, nil
}

func applyCommittedPlan(plan filePlan, createDirs bool) (FileResult, error) {
	result := FileResult{Path: plan.DisplayPath, OK: true, Changed: plan.Changed, HunksApplied: plan.HunksApplied}
	if !plan.Changed {
		return result, nil
	}

	if plan.Operation == "delete" {
		if err := removeFile(plan.ResolvedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			payload := protocol.ErrorPayload{Code: protocol.CodePatchFailed, Message: "unable to delete file", Details: protocol.ErrorDetails{"path": plan.DisplayPath, "phase": "commit"}}
			return failureResult(plan.DisplayPath, payload), protocol.ErrDetails(payload.Code, payload.Message, payload.Details)
		}
		return result, nil
	}

	if _, err := writeFileAtomic(plan.ResolvedPath, plan.UpdatedBytes, createDirs); err != nil {
		payload := protocol.GetErrorPayload(err, protocol.CodePatchFailed)
		if payload.Details == nil {
			payload.Details = protocol.ErrorDetails{}
		}
		payload.Details["path"] = plan.DisplayPath
		payload.Details["phase"] = "commit"
		return failureResult(plan.DisplayPath, payload), protocol.ErrDetails(payload.Code, payload.Message, payload.Details)
	}
	return result, nil
}

func rollbackCommittedPlans(committed []filePlan) []error {
	errs := make([]error, 0, len(committed))
	for i := len(committed) - 1; i >= 0; i-- {
		if err := restoreOriginal(committed[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func restoreOriginal(plan filePlan) error {
	if !plan.Changed {
		return nil
	}
	if !plan.OriginalExists {
		if err := removeFile(plan.ResolvedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	_, err := writeFileAtomic(plan.ResolvedPath, plan.OriginalBytes, true)
	return err
}

func createDirsEnabled(options Options) bool {
	if options.CreateDirs == nil {
		return true
	}
	return *options.CreateDirs
}

func determineAtomicFailureState(plans []filePlan, rollbackErrs []error) string {
	anyDifferent := false
	unknown := false
	for _, plan := range plans {
		different, known := snapshotDiffersFromOriginal(plan)
		if !known {
			unknown = true
			continue
		}
		if different {
			anyDifferent = true
		}
	}
	if unknown {
		return RepositoryStateAmbiguous
	}
	if anyDifferent {
		return RepositoryStatePartiallyChange
	}
	if len(rollbackErrs) > 0 {
		return RepositoryStateAmbiguous
	}
	return RepositoryStateUnchanged
}

func atomicResults(plans []filePlan, repositoryState string) []FileResult {
	results := make([]FileResult, 0, len(plans))
	for _, plan := range plans {
		changed := false
		if repositoryState != RepositoryStateUnchanged {
			if different, known := snapshotDiffersFromOriginal(plan); known && different {
				changed = true
			}
		}
		results = append(results, FileResult{
			Path:         plan.DisplayPath,
			OK:           plan.Result.OK,
			Changed:      changed,
			HunksApplied: 0,
			Error:        plan.Result.Error,
			ErrorCode:    plan.Result.ErrorCode,
			ErrorDetails: plan.Result.ErrorDetails,
		})
	}
	return results
}

func snapshotDiffersFromOriginal(plan filePlan) (bool, bool) {
	data, exists, err := readOriginalFile(plan.ResolvedPath)
	if err != nil {
		return false, false
	}
	if !plan.OriginalExists {
		return exists, true
	}
	if !exists {
		return true, true
	}
	return !bytes.Equal(data, plan.OriginalBytes), true
}

func determineChanged(opType string, originalExists bool, original, updated []byte) bool {
	switch opType {
	case "create":
		return true
	case "delete":
		return originalExists
	default:
		return !bytes.Equal(original, updated)
	}
}

func validateExpectedTokenKeys(expected map[string]string, targets map[string]struct{}) error {
	for key := range expected {
		if _, ok := targets[key]; !ok {
			return protocol.ErrDetails(protocol.CodeInvalidRequest, "unexpected expected_file_tokens entry", protocol.ErrorDetails{"field": "expected_file_tokens", "reason": "unexpected_path"})
		}
	}
	return nil
}

func topLevelErrorFromResults(result ApplyResult, atomic bool) error {
	codes := make([]string, 0, len(result.Results))
	messages := make([]string, 0, len(result.Results))
	seen := map[string]struct{}{}
	details := protocol.ErrorDetails{"repository_state": result.RepositoryState}

	for _, fileResult := range result.Results {
		if fileResult.OK {
			continue
		}
		if fileResult.ErrorCode != "" {
			if _, ok := seen[fileResult.ErrorCode]; !ok {
				seen[fileResult.ErrorCode] = struct{}{}
				codes = append(codes, fileResult.ErrorCode)
			}
		}
		if fileResult.Error != "" {
			messages = append(messages, fileResult.Error)
		}
		for key, value := range fileResult.ErrorDetails {
			if _, exists := details[key]; !exists {
				details[key] = value
			}
		}
	}

	code := protocol.CodePatchFailed
	message := "one or more file patches failed"
	if len(codes) == 1 {
		code = codes[0]
	}
	if atomic && result.RepositoryState == RepositoryStateUnchanged && len(messages) == 1 {
		message = messages[0]
	}
	if !atomic && len(messages) == 1 {
		message = messages[0]
	}
	return protocol.ErrDetails(code, message, details)
}

func failureResult(path string, payload protocol.ErrorPayload) FileResult {
	return FileResult{
		Path:         path,
		OK:           false,
		Changed:      false,
		Error:        payload.Message,
		ErrorCode:    payload.Code,
		ErrorDetails: payload.Details,
	}
}

func readOriginalFile(path string) ([]byte, bool, error) {
	data, err := readFile(path)
	if err == nil {
		return data, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, protocol.Err(protocol.CodePatchFailed, "unable to read target file")
}

func patchType(filePatch FilePatch) string {
	if filePatch.OldPath == "/dev/null" && filePatch.NewPath != "/dev/null" {
		return "create"
	}
	if filePatch.NewPath == "/dev/null" && filePatch.OldPath != "/dev/null" {
		return "delete"
	}
	if filePatch.OldPath != filePatch.NewPath {
		return "rename"
	}
	return "modify"
}

func applyToContent(content string, filePatch FilePatch) (string, int, error) {
	if patchType(filePatch) == "rename" {
		return "", 0, protocol.Err(protocol.CodePatchFailed, "rename patches are not supported")
	}

	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	hasTrailingNewline := strings.HasSuffix(normalized, "\n")
	lines := splitLines(normalized)

	result := make([]string, 0, len(lines)+8)
	cursor := 0
	hunksApplied := 0
	finalHasTrailingNewline := hasTrailingNewline

	for _, hunk := range filePatch.Hunks {
		expected := hunk.OldStart - 1
		if hunk.OldStart == 0 {
			expected = 0
		}
		if expected < cursor || expected > len(lines) {
			return "", hunksApplied, protocol.Err(protocol.CodePatchFailed, fmt.Sprintf("hunk start out of range for %s", filePatch.NewPath))
		}

		result = append(result, lines[cursor:expected]...)
		idx := expected

		for _, line := range hunk.Lines {
			switch line.Kind {
			case ' ':
				if idx >= len(lines) || lines[idx] != line.Text {
					return "", hunksApplied, protocol.ErrDetails(protocol.CodePatchFailed, "patch context mismatch", protocol.ErrorDetails{"hunk": hunksApplied + 1, "expected": line.Text, "actual": actualLine(lines, idx)})
				}
				result = append(result, lines[idx])
				idx++
			case '-':
				if idx >= len(lines) || lines[idx] != line.Text {
					return "", hunksApplied, protocol.ErrDetails(protocol.CodePatchFailed, "patch deletion mismatch", protocol.ErrorDetails{"hunk": hunksApplied + 1, "expected": line.Text, "actual": actualLine(lines, idx)})
				}
				idx++
			case '+':
				result = append(result, line.Text)
			default:
				return "", hunksApplied, protocol.Err(protocol.CodePatchFailed, "invalid hunk line")
			}
		}

		hunkTouchesEOF := idx == len(lines)
		cursor = idx
		hunksApplied++
		if hunkTouchesEOF && len(result) > 0 {
			finalHasTrailingNewline = !hunk.NewNoTrailingNL
		}
	}

	result = append(result, lines[cursor:]...)
	joined := strings.Join(result, "\n")
	if len(result) > 0 && finalHasTrailingNewline {
		joined += "\n"
	}
	if len(result) == 0 {
		joined = ""
	}
	return joined, hunksApplied, nil
}

func splitLines(content string) []string {
	if content == "" {
		return []string{}
	}
	parts := strings.Split(content, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func actualLine(lines []string, idx int) string {
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	return lines[idx]
}

func canonicalResultPath(target string, details protocol.ErrorDetails) string {
	if details != nil {
		if path, ok := details["path"].(string); ok {
			if strings.TrimSpace(path) != "" {
				return path
			}
		}
	}
	return cleanPatchPath(target)
}

func cleanPatchPath(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}
