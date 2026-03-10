# agentrail

`agentrail` is a Windows-focused execution adapter for AI coding agents.

It provides:

- one self-contained Go binary
- JSON-only stdout
- workspace-aware filesystem controls
- deterministic file and search operations
- direct-argv process execution with no shell parsing
- a normative protocol spec in `PROTOCOL.md`

## Commands

- `agentrail search <query>`
- `agentrail files`
- `agentrail schema <target>`
- `agentrail read <path>`
- `agentrail write <path>`
- `agentrail patch`
- `agentrail exec -- <argv...>`

Global flags:

- `--json` force JSON request mode
- `--allow-outside-workspace` opt-in outside-workspace access for `read`, `search`, and `files`

## Safety Model

Workspace root is resolved from:

1. `CODEX_TOOL_WORKSPACE` if set
2. otherwise the current working directory

Safety rules:

- deny `.git` and `node_modules`
- deny Windows system directories unless they are the workspace root
- keep `write`, `patch`, and `exec.cwd` inside workspace
- default `read`, `search`, and `files` to workspace-only access

## Protocol

Every response is a single JSON object and includes:

- `ok`
- `action`
- `protocol_version`
- `tool_version`
- `capabilities`

Read the full normative contract in `PROTOCOL.md`.

Use `agentrail schema patch` or `{"action":"schema","target":"patch"}` in JSON mode to inspect the live patch request contract.

### Read example

```json
{
  "ok": true,
  "action": "read",
  "protocol_version": 1,
  "tool_version": "0.0.0-dev+0000000",
  "capabilities": ["exec","exec_output_budget","exec_process_tree_kill","files","files_pagination","patch","patch_atomic","patch_expected_file_tokens","read","read_file_token","schema","search","write"],
  "path": "src/main.go",
  "file_token": "sha256:...",
  "content": "...",
  "start_line": 1,
  "end_line": 80,
  "truncated": true,
  "has_more": true,
  "next_start_line": 81
}
```

### Patch example

```json
{
  "ok": true,
  "action": "patch",
  "repository_state": "changed",
  "files_changed": ["src/main.go"],
  "hunks_applied": 1,
  "results": [
    {"path":"src/main.go","ok":true,"changed":true,"hunks_applied":1}
  ]
}
```

### Exec example

```json
{
  "ok": true,
  "action": "exec",
  "exit_code": 0,
  "stdout": "...",
  "stderr": "",
  "stdout_truncated": false,
  "stderr_truncated": false,
  "output_bytes": 1842,
  "timing_ms": 31
}
```

## CLI Examples

List files:

```bash
agentrail files
```

Paginated file enumeration:

```bash
printf '{"action":"files","limit":200}' | agentrail --json
```

Search:

```bash
agentrail search "TODO"
```

Read:

```bash
printf '{"action":"read","path":"README.md","start_line":1,"max_bytes":4096}' | agentrail --json
```

Patch atomically with file tokens:

```bash
printf '{"action":"patch","atomic":true,"expected_file_tokens":{"src/main.go":"sha256:..."},"diff":"--- a/src/main.go\n+++ b/src/main.go\n@@ -1,1 +1,1 @@\n-old\n+new\n"}' | agentrail --json
```

Patch contract schema:

```bash
printf '{"action":"schema","target":"patch"}' | agentrail --json
```

Notes:

- The JSON patch endpoint accepts unified diff text in `diff` only.
- The diff must include `---` and `+++` file headers before any `@@` hunks.
- Fields like `mode`, `patch`, `old_string`, and `new_string` are not part of the CLI JSON contract.

Exec:

```bash
agentrail exec -- go test ./...
```

## Build

Windows single-binary build:

```powershell
set CGO_ENABLED=0
set GOOS=windows
set GOARCH=amd64
go build -trimpath -ldflags "-s -w -buildid=" -o bin/agentrail.exe ./cmd
```

Notes:

- `CGO_ENABLED=0` yields a self-contained `.exe` with no external runtime executable dependencies.
- The Windows build guarantees `exec_process_tree_kill` via Job Object semantics and advertises that capability accordingly.

## Testing

```bash
go test ./...
```

Coverage includes:

- request parsing and envelope fields
- path traversal and deny rules
- file pagination and cursor errors
- paged reads and file tokens
- patch token checks, no-op behavior, and atomic validation
- exec argv-only execution, timeout handling, and output-budget truncation

## License

MIT. See LICENSE.
