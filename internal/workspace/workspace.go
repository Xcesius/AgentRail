package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"agentrail/internal/protocol"
)

type Manager struct {
	Root              string
	WarnSystemRoot    bool
	systemDirectories []string
	rootWithSep       string
	rootLower         string
	rootWithSepLower  string
}

func NewManager() (*Manager, error) {
	root := os.Getenv("CODEX_TOOL_WORKSPACE")
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, protocol.Err(protocol.CodeWorkspaceRequired, "unable to determine current working directory")
		}
		root = cwd
	}
	return NewManagerFromRoot(root)
}

func NewManagerFromRoot(root string) (*Manager, error) {
	if root == "" {
		return nil, protocol.Err(protocol.CodeWorkspaceRequired, "workspace root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, protocol.Err(protocol.CodeWorkspaceRequired, "unable to resolve workspace root")
	}
	canonicalRoot, err := canonicalizePath(absRoot)
	if err != nil {
		return nil, protocol.Err(protocol.CodeWorkspaceRequired, "unable to canonicalize workspace root")
	}
	info, err := os.Stat(canonicalRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, protocol.Err(protocol.CodeWorkspaceRequired, "workspace root does not exist")
		}
		return nil, protocol.Err(protocol.CodeWorkspaceRequired, "unable to inspect workspace root")
	}
	if !info.IsDir() {
		return nil, protocol.Err(protocol.CodeWorkspaceRequired, "workspace root must be a directory")
	}

	cleanRoot := filepath.Clean(canonicalRoot)
	rootWithSep := cleanRoot
	if !strings.HasSuffix(rootWithSep, string(os.PathSeparator)) {
		rootWithSep += string(os.PathSeparator)
	}
	manager := &Manager{
		Root:              cleanRoot,
		systemDirectories: systemDirsForPath(cleanRoot),
		rootWithSep:       rootWithSep,
	}
	if runtime.GOOS == "windows" {
		manager.rootLower = strings.ToLower(cleanRoot)
		manager.rootWithSepLower = strings.ToLower(rootWithSep)
	}
	for _, sys := range manager.systemDirectories {
		if equalPath(manager.Root, sys) {
			manager.WarnSystemRoot = true
			break
		}
	}
	return manager, nil
}

func (m *Manager) ResolveReadPath(input string, allowOutside bool) (string, error) {
	resolved, err := m.resolvePath(input)
	if err != nil {
		return "", err
	}
	if !allowOutside && !m.IsWithinWorkspace(resolved) {
		return "", protocol.ErrDetails(protocol.CodePathDenied, "read outside workspace is not allowed", protocol.ErrorDetails{"path": m.DisplayPath(resolved), "policy": "workspace_only"})
	}
	if m.isSystemDenied(resolved) {
		return "", protocol.ErrDetails(protocol.CodePathDenied, "path is denied", protocol.ErrorDetails{"path": m.DisplayPath(resolved), "policy": "system_directory"})
	}
	if m.IsDeniedPath(resolved) {
		return "", protocol.ErrDetails(protocol.CodePathDenied, "path is denied", protocol.ErrorDetails{"path": m.DisplayPath(resolved), "policy": "deny_rule"})
	}
	return resolved, nil
}

func (m *Manager) ResolveWritePath(input string) (string, error) {
	resolved, err := m.resolvePath(input)
	if err != nil {
		return "", err
	}
	if !m.IsWithinWorkspace(resolved) {
		return "", protocol.ErrDetails(protocol.CodePathDenied, "write outside workspace is not allowed", protocol.ErrorDetails{"path": m.DisplayPath(resolved), "policy": "workspace_only"})
	}
	if m.IsDeniedPath(resolved) {
		return "", protocol.ErrDetails(protocol.CodePathDenied, "path is denied", protocol.ErrorDetails{"path": m.DisplayPath(resolved), "policy": "deny_rule"})
	}
	if m.isSystemDenied(resolved) {
		return "", protocol.ErrDetails(protocol.CodePathDenied, "path is denied", protocol.ErrorDetails{"path": m.DisplayPath(resolved), "policy": "system_directory"})
	}
	return resolved, nil
}

// RevalidateWritePath repeats canonical resolution immediately before a
// mutation and rejects a path whose resolved target changed after planning.
func (m *Manager) RevalidateWritePath(resolved string) error {
	current, err := m.ResolveWritePath(resolved)
	if err != nil {
		return err
	}
	if !equalPath(current, resolved) {
		return protocol.ErrDetails(protocol.CodePathDenied, "resolved write target changed before commit", protocol.ErrorDetails{
			"path":   m.DisplayPath(current),
			"policy": "path_changed",
		})
	}
	return nil
}

func (m *Manager) ResolveDirPath(input string, allowOutside bool) (string, error) {
	resolved, err := m.ResolveReadPath(input, allowOutside)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", protocol.ErrDetails(protocol.CodeNotFound, "path not found", protocol.ErrorDetails{"path": m.DisplayPath(resolved), "kind": "directory"})
		}
		return "", protocol.Err(protocol.CodeInvalidRequest, "unable to inspect directory")
	}
	if !info.IsDir() {
		return "", protocol.ErrDetails(protocol.CodeInvalidRequest, "path is not a directory", protocol.ErrorDetails{"field": "path", "reason": "not_directory"})
	}
	return resolved, nil
}

func (m *Manager) ResolveExecCWD(input string) (string, error) {
	if input == "" {
		return m.Root, nil
	}
	resolved, err := m.resolvePath(input)
	if err != nil {
		return "", err
	}
	if !m.IsWithinWorkspace(resolved) {
		return "", protocol.ErrDetails(protocol.CodePathDenied, "exec cwd outside workspace is not allowed", protocol.ErrorDetails{"path": m.DisplayPath(resolved), "policy": "workspace_only"})
	}
	if m.IsDeniedPath(resolved) {
		return "", protocol.ErrDetails(protocol.CodePathDenied, "path is denied", protocol.ErrorDetails{"path": m.DisplayPath(resolved), "policy": "deny_rule"})
	}
	if m.isSystemDenied(resolved) {
		return "", protocol.ErrDetails(protocol.CodePathDenied, "path is denied", protocol.ErrorDetails{"path": m.DisplayPath(resolved), "policy": "system_directory"})
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", protocol.ErrDetails(protocol.CodeNotFound, "cwd not found", protocol.ErrorDetails{"path": m.DisplayPath(resolved), "kind": "directory"})
		}
		return "", protocol.Err(protocol.CodeInvalidRequest, "unable to inspect cwd")
	}
	if !info.IsDir() {
		return "", protocol.ErrDetails(protocol.CodeInvalidRequest, "cwd must be a directory", protocol.ErrorDetails{"field": "cwd", "reason": "not_directory"})
	}
	return resolved, nil
}

func (m *Manager) IsWithinWorkspace(path string) bool {
	_, within := m.relativeIfWithin(filepath.Clean(path))
	return within
}

func (m *Manager) DisplayPath(path string) string {
	clean := filepath.Clean(path)
	if rel, within := m.relativeIfWithin(clean); within {
		if rel == "" || rel == "." {
			return "."
		}
		return filepath.ToSlash(rel)
	}
	return canonicalAbsoluteDisplay(clean)
}

func (m *Manager) relativeIfWithin(cleanPath string) (string, bool) {
	if runtime.GOOS == "windows" {
		rootLower := m.rootLower
		rootWithSepLower := m.rootWithSepLower
		if rootLower == "" {
			cleanRoot := filepath.Clean(m.Root)
			rootLower = strings.ToLower(cleanRoot)
			rootWithSepLower = rootLower
			if !strings.HasSuffix(rootWithSepLower, string(os.PathSeparator)) {
				rootWithSepLower += string(os.PathSeparator)
			}
		}
		if len(cleanPath) == len(rootLower) && strings.EqualFold(cleanPath, rootLower) {
			return ".", true
		}
		if len(cleanPath) > len(rootWithSepLower) && strings.EqualFold(cleanPath[:len(rootWithSepLower)], rootWithSepLower) {
			return cleanPath[len(rootWithSepLower):], true
		}
		return "", false
	}

	root := m.Root
	rootWithSep := m.rootWithSep
	if rootWithSep == "" {
		root = filepath.Clean(root)
		rootWithSep = root
		if !strings.HasSuffix(rootWithSep, string(os.PathSeparator)) {
			rootWithSep += string(os.PathSeparator)
		}
	}
	if cleanPath == root {
		return ".", true
	}
	if strings.HasPrefix(cleanPath, rootWithSep) {
		return cleanPath[len(rootWithSep):], true
	}
	return "", false
}

func (m *Manager) RelativePath(path string) string {
	return m.DisplayPath(path)
}

func (m *Manager) ShouldSkipDir(path string) bool {
	clean := filepath.Clean(path)
	if m.IsDeniedPath(clean) {
		return true
	}
	if m.isSystemDenied(clean) {
		return true
	}
	return false
}

func (m *Manager) IsDeniedPath(path string) bool {
	clean := filepath.Clean(path)
	target := clean
	if rel, within := m.relativeIfWithin(clean); within {
		if rel == "." || rel == "" {
			return false
		}
		target = rel
	}
	return hasDeniedSegment(target)
}

func hasDeniedSegment(path string) bool {
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '/' || path[i] == '\\' {
			part := path[start:i]
			if isDeniedSegment(part) {
				return true
			}
			start = i + 1
		}
	}
	return false
}

func isDeniedSegment(part string) bool {
	switch len(part) {
	case 4:
		return strings.EqualFold(part, ".git")
	case 10:
		return strings.EqualFold(part, ".agentrail")
	case 12:
		return strings.EqualFold(part, "node_modules")
	default:
		return false
	}
}

func (m *Manager) resolvePath(input string) (string, error) {
	candidate := input
	if candidate == "" {
		candidate = "."
	}
	if runtime.GOOS == "windows" {
		normalized, err := normalizeWindowsPrefix(candidate)
		if err != nil {
			return "", err
		}
		candidate = normalized
	}
	if vol := filepath.VolumeName(candidate); vol != "" && !filepath.IsAbs(candidate) {
		return "", protocol.ErrDetails(protocol.CodePathDenied, "volume-relative paths are not allowed", protocol.ErrorDetails{"path": filepath.ToSlash(candidate), "policy": "volume_relative"})
	}

	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(m.Root, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", protocol.Err(protocol.CodeInvalidRequest, "unable to resolve path")
	}
	resolved, err := canonicalizePath(abs)
	if err != nil {
		return "", protocol.Err(protocol.CodeInvalidRequest, "unable to canonicalize path")
	}
	if runtime.GOOS == "windows" {
		normalized, normErr := normalizeWindowsPrefix(resolved)
		if normErr != nil {
			return "", normErr
		}
		resolved = normalized
	}
	return resolved, nil
}

func normalizeWindowsPrefix(candidate string) (string, error) {
	// Normalize leading slashes so both \\?\ and //?/ are caught.
	slashNorm := strings.ReplaceAll(candidate, "/", `\`)
	switch {
	case strings.HasPrefix(slashNorm, `\\.\`), strings.HasPrefix(slashNorm, `\??\`):
		return "", protocol.ErrDetails(protocol.CodePathDenied, "Windows device paths are not allowed", protocol.ErrorDetails{"path": filepath.ToSlash(candidate), "policy": "device_path"})
	case strings.HasPrefix(slashNorm, `\\?\`):
		rest := slashNorm[4:]
		if len(rest) > 4 && strings.EqualFold(rest[:4], `UNC\`) {
			uncTarget := rest[4:]
			if idx := strings.IndexByte(uncTarget, '\\'); idx > 0 && idx+1 < len(uncTarget) {
				return `\\` + uncTarget, nil
			}
			return "", protocol.ErrDetails(protocol.CodePathDenied, "Windows device paths are not allowed", protocol.ErrorDetails{"path": filepath.ToSlash(candidate), "policy": "device_path"})
		}
		if len(rest) >= 3 && isASCIIAlpha(rest[0]) && rest[1] == ':' && rest[2] == '\\' {
			return rest, nil
		}
		if len(rest) == 2 && isASCIIAlpha(rest[0]) && rest[1] == ':' {
			return rest + `\`, nil
		}
		return "", protocol.ErrDetails(protocol.CodePathDenied, "Windows device paths are not allowed", protocol.ErrorDetails{"path": filepath.ToSlash(candidate), "policy": "device_path"})
	}
	return candidate, nil
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func (m *Manager) isSystemDenied(path string) bool {
	for _, sys := range m.systemDirectories {
		if isWithin(path, sys) {
			if equalPath(m.Root, sys) {
				return false
			}
			return true
		}
	}
	return false
}

func canonicalizePath(path string) (string, error) {
	path = filepath.Clean(path)
	if _, err := os.Lstat(path); err == nil {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		return filepath.Clean(resolved), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	existing, remainder, err := splitExistingPrefix(path)
	if err != nil {
		return "", err
	}
	resolvedBase := existing
	if _, err := os.Lstat(existing); err == nil {
		resolvedBase, err = filepath.EvalSymlinks(existing)
		if err != nil {
			return "", err
		}
	}
	resolved := filepath.Clean(resolvedBase)
	for _, part := range remainder {
		resolved = filepath.Join(resolved, part)
	}
	return filepath.Clean(resolved), nil
}

func splitExistingPrefix(path string) (string, []string, error) {
	remainder := make([]string, 0, 8)
	cursor := filepath.Clean(path)

	for {
		_, err := os.Lstat(cursor)
		if err == nil {
			reverseStrings(remainder)
			return cursor, remainder, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			reverseStrings(remainder)
			return cursor, remainder, nil
		}
		remainder = append(remainder, filepath.Base(cursor))
		cursor = parent
	}
}

func reverseStrings(items []string) {
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
}

func systemDirsForPath(path string) []string {
	if runtime.GOOS != "windows" {
		return nil
	}

	candidates := []string{
		os.Getenv("SystemRoot"),
		os.Getenv("windir"),
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramW6432"),
		os.Getenv("ProgramFiles(x86)"),
		os.Getenv("ProgramData"),
	}

	drives := []string{filepath.VolumeName(path), os.Getenv("SystemDrive")}
	if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
		drives = append(drives, filepath.VolumeName(systemRoot))
	}
	for _, drive := range drives {
		drive = strings.TrimSpace(drive)
		if drive == "" {
			continue
		}
		driveRoot := drive
		if !strings.HasSuffix(driveRoot, string(os.PathSeparator)) {
			driveRoot += string(os.PathSeparator)
		}
		candidates = append(candidates,
			filepath.Join(driveRoot, "Windows"),
			filepath.Join(driveRoot, "Program Files"),
			filepath.Join(driveRoot, "Program Files (x86)"),
			filepath.Join(driveRoot, "ProgramData"),
		)
	}

	seen := make(map[string]struct{}, len(candidates))
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" || !filepath.IsAbs(candidate) {
			continue
		}
		clean := filepath.Clean(candidate)
		key := strings.ToLower(clean)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, clean)
	}
	return result
}

func isWithin(path, parent string) bool {
	path = filepath.Clean(path)
	parent = filepath.Clean(parent)
	if equalPath(path, parent) {
		return true
	}
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return rel != "."
}

func equalPath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func canonicalAbsoluteDisplay(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	if runtime.GOOS == "windows" && len(clean) >= 2 && clean[1] == ':' {
		clean = strings.ToUpper(clean[:1]) + clean[1:]
	}
	return clean
}

func (m *Manager) WarningMessage() string {
	if !m.WarnSystemRoot {
		return ""
	}
	return fmt.Sprintf("warning: workspace root '%s' is a protected system directory", m.Root)
}
