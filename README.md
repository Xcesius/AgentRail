# agentrail

`agentrail` is a high-performance command-line execution adapter for AI coding agents.

- Single self-contained Go binary for Windows
- JSON-only stdout protocol
- Safe workspace-aware filesystem controls
- Direct argv process execution (no shell)
- Normative protocol spec: `PROTOCOL.md`

## Commands

- `agentrail search <query>`
- `agentrail files`
- `agentrail read <path>`
- `agentrail write <path>`
- `agentrail patch`
- `agentrail exec -- <argv...>`

Global flags:

- `--json` force JSON request mode (stdin request object)
- `--allow-outside-workspace` opt-in outside-workspace access for read/search/files

## Workspace and Safety

Workspace root is resolved at startup by:

1. `CODEX_TOOL_WORKSPACE` (if set)
2. current working directory

All path checks are absolute, cleaned, and symlink-resolved where possible.

Safety rules:

- Deny traversal into `.git` and `node_modules`
- Deny Windows system directories (`C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`, `C:\ProgramData`) unless equal to workspace root
- `write` and `patch` must stay inside workspace
- `read/search/files` are workspace-only by default, with explicit opt-in for outside access

## JSON Protocol

All stdout responses are a single JSON object. Human diagnostics go to stderr.

### Request example

```json
{
  "action": "read",
  "path": "src/index.js"
}
```

### Success response example

```json
{
  "ok": true,
  "action": "read",
  "path": "src/index.js",
  "content": "...",
  "start_line": 1,
  "end_line": 12,
  "truncated": false
}
```

### Error response example

```json
{
  "ok": false,
  "action": "write",
  "error": {
    "code": "path_denied",
    "message": "write outside workspace is not allowed"
  }
}
```

### Stable error codes

- `invalid_request`
- `path_denied`
- `not_found`
- `binary_file`
- `too_large`
- `search_error`
- `patch_failed`
- `exec_failed`
- `timeout`
- `workspace_required`

## CLI Examples

### files

```bash
agentrail files
```

### search

```bash
agentrail search "TODO"
```

### read

```bash
agentrail read "README.md"
```

### write

```bash
echo "hello" | agentrail write "notes.txt"
```

### patch

```bash
cat change.diff | agentrail patch
```

### exec

```bash
agentrail exec -- git status
```

## JSON Examples

### search request

```json
{
  "action": "search",
  "query": "func main",
  "regex": false,
  "case_sensitive": false,
  "limit": 50,
  "max_file_bytes": 1048576,
  "deterministic": true
}
```

### write request

```json
{
  "action": "write",
  "path": "out.txt",
  "create_dirs": true,
  "content": "new file contents"
}
```

### exec request

```json
{
  "action": "exec",
  "argv": ["git", "status"],
  "cwd": ".",
  "timeout_ms": 5000,
  "env": {
    "CI": "1"
  }
}
```

## Build (Windows single binary)

```powershell
set CGO_ENABLED=0
set GOOS=windows
set GOARCH=amd64
go build -trimpath -ldflags "-s -w -buildid=" -o bin/agentrail.exe ./cmd
```

Notes:

- `CGO_ENABLED=0` produces a self-contained `.exe` with no external runtime executable dependencies.
- Windows static-linking semantics differ from Linux; this still yields a single deployable binary.

## Testing

```bash
go test ./...
```

Included unit tests cover:

- path traversal attempts
- denied directory writes
- partial reads
- huge file safeguards
- binary file detection
- patch context mismatch
- exec argv-only behavior and timeout handling
