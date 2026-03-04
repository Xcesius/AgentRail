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
}

func NewManager() (*Manager, error) {
	root := os.Getenv("CODEX_TOOL_WORKSPACE")
	if strings.TrimSpace(root) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, protocol.Err(protocol.CodeWorkspaceRequired, "unable to determine current working directory")
		}
		root = cwd
	}
	return NewManagerFromRoot(root)
}

func NewManagerFromRoot(root string) (*Manager, error) {
	if strings.TrimSpace(root) == "" {
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

	manager := &Manager{
		Root:              filepath.Clean(canonicalRoot),
		systemDirectories: systemDirsForPath(canonicalRoot),
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
		return "", protocol.Err(protocol.CodePathDenied, "read outside workspace is not allowed")
	}
	if m.isSystemDenied(resolved) {
		return "", protocol.Err(protocol.CodePathDenied, "path is denied")
	}
	if m.IsDeniedPath(resolved) {
		return "", protocol.Err(protocol.CodePathDenied, "path is denied")
	}
	return resolved, nil
}

func (m *Manager) ResolveWritePath(input string) (string, error) {
	resolved, err := m.resolvePath(input)
	if err != nil {
		return "", err
	}
	if !m.IsWithinWorkspace(resolved) {
		return "", protocol.Err(protocol.CodePathDenied, "write outside workspace is not allowed")
	}
	if m.IsDeniedPath(resolved) {
		return "", protocol.Err(protocol.CodePathDenied, "path is denied")
	}
	if m.isSystemDenied(resolved) {
		return "", protocol.Err(protocol.CodePathDenied, "path is denied")
	}
	return resolved, nil
}

func (m *Manager) ResolveDirPath(input string, allowOutside bool) (string, error) {
	resolved, err := m.ResolveReadPath(input, allowOutside)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", protocol.Err(protocol.CodeNotFound, "path not found")
		}
		return "", protocol.Err(protocol.CodeInvalidRequest, "unable to inspect directory")
	}
	if !info.IsDir() {
		return "", protocol.Err(protocol.CodeInvalidRequest, "path is not a directory")
	}
	return resolved, nil
}

func (m *Manager) ResolveExecCWD(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return m.Root, nil
	}
	resolved, err := m.resolvePath(input)
	if err != nil {
		return "", err
	}
	if !m.IsWithinWorkspace(resolved) {
		return "", protocol.Err(protocol.CodePathDenied, "exec cwd outside workspace is not allowed")
	}
	if m.IsDeniedPath(resolved) || m.isSystemDenied(resolved) {
		return "", protocol.Err(protocol.CodePathDenied, "path is denied")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", protocol.Err(protocol.CodeNotFound, "cwd not found")
		}
		return "", protocol.Err(protocol.CodeInvalidRequest, "unable to inspect cwd")
	}
	if !info.IsDir() {
		return "", protocol.Err(protocol.CodeInvalidRequest, "cwd must be a directory")
	}
	return resolved, nil
}

func (m *Manager) IsWithinWorkspace(path string) bool {
	path = filepath.Clean(path)
	root := filepath.Clean(m.Root)

	if equalPath(path, root) {
		return true
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

func (m *Manager) RelativePath(path string) string {
	path = filepath.Clean(path)
	if m.IsWithinWorkspace(path) {
		rel, err := filepath.Rel(m.Root, path)
		if err == nil {
			if rel == "." {
				return "."
			}
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
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
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	for _, part := range parts {
		if strings.EqualFold(part, ".git") || strings.EqualFold(part, "node_modules") {
			return true
		}
	}
	return false
}

func (m *Manager) resolvePath(input string) (string, error) {
	candidate := strings.TrimSpace(input)
	if candidate == "" {
		candidate = "."
	}
	if vol := filepath.VolumeName(candidate); vol != "" && !filepath.IsAbs(candidate) {
		return "", protocol.Err(protocol.CodePathDenied, "volume-relative paths are not allowed")
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
	return resolved, nil
}

func (m *Manager) isSystemDenied(path string) bool {
	for _, sys := range m.systemDirectories {
		if isWithin(path, sys) {
			if m.IsWithinWorkspace(path) {
				return false
			}
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
	remainder := []string{}
	cursor := filepath.Clean(path)

	for {
		_, err := os.Lstat(cursor)
		if err == nil {
			return cursor, remainder, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return cursor, remainder, nil
		}
		remainder = append([]string{filepath.Base(cursor)}, remainder...)
		cursor = parent
	}
}

func systemDirsForPath(path string) []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	drive := filepath.VolumeName(path)
	if drive == "" {
		drive = "C:"
	}
	return []string{
		filepath.Join(drive, "Windows"),
		filepath.Join(drive, "Program Files"),
		filepath.Join(drive, "Program Files (x86)"),
		filepath.Join(drive, "ProgramData"),
	}
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

func (m *Manager) WarningMessage() string {
	if !m.WarnSystemRoot {
		return ""
	}
	return fmt.Sprintf("warning: workspace root '%s' is a protected system directory", m.Root)
}
