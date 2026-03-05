# AgentRail Protocol Specification (v1)

This specification is normative for `agentrail` request/response behavior.

Normative keywords are interpreted as in RFC-style specs: **MUST**, **MUST NOT**, **MAY**.

## 1. Transport Contract

- The tool **MUST** emit exactly one JSON object on stdout per invocation.
- The tool **MAY** emit diagnostics to stderr.
- In JSON mode (`--json` or auto-detected stdin JSON), request input **MUST** be a single JSON object.
- Unknown JSON request fields **MUST** fail with `invalid_request`.

## 2. Common Envelope

## Request envelope

```json
{
  "request_id": "optional-client-id",
  "action": "read"
}
```

Fields:
- `request_id` (optional string): client correlation id.
- `action` (required string): one of `files|search|read|write|patch|exec`.

## Success envelope

```json
{
  "ok": true,
  "action": "read",
  "request_id": "optional-client-id"
}
```

## Error envelope

```json
{
  "ok": false,
  "action": "read",
  "request_id": "optional-client-id",
  "error": {
    "code": "path_denied",
    "message": "read outside workspace is not allowed"
  }
}
```

Envelope rules:
- `ok` and `action` **MUST** be present in every response.
- `error` **MUST** be present when `ok=false`.
- `request_id` **MUST** be echoed on JSON-mode responses when provided and parsed successfully.
- For malformed JSON (cannot parse request envelope), `request_id` **CANNOT** be echoed.

## 3. Stable Error Codes

The tool **MUST** use only these stable codes for protocol-level errors:

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

## 4. Workspace and Path Rules

Workspace root resolution (startup):
1. `CODEX_TOOL_WORKSPACE` env var if set.
2. Otherwise process working directory.

Path resolution and safety:
- Paths **MUST** be normalized via absolute + clean + symlink-aware resolution.
- Existing paths **MUST** resolve through symlinks.
- Non-existing paths **MUST** resolve nearest existing ancestor symlink, then append remaining segments.
- Volume-relative paths (e.g. `C:foo`) **MUST NOT** be accepted.

Denied paths:
- Paths containing `.git` or `node_modules` **MUST** be denied.
- Windows system directories (`Windows`, `Program Files`, `Program Files (x86)`, `ProgramData` on workspace drive) **MUST** be denied unless path is inside workspace.

Workspace boundary:
- `write`, `patch`, and `exec.cwd` **MUST** stay inside workspace.
- `read`, `search`, and `files` **MUST** default to workspace-only and **MAY** access outside only with explicit opt-in (`--allow-outside-workspace` or `allow_outside_workspace=true`).

## 5. Determinism and Limits

Determinism:
- `files.paths` **MUST** be lexicographically sorted.
- `search.matches` **MUST** be sorted by `(path,line,col)` when deterministic mode is enabled.
- JSON `search.deterministic` defaults to `true` when omitted.

Runtime limits:
- stdin payload read cap: 64 MiB. At/above cap **MUST** return `too_large`.
- `read.max_bytes` default: 1 MiB when omitted or `<= 0`.
- `search.max_file_bytes` (if set): files above the limit are skipped; stream scan stops when read bytes exceed limit.

## 6. Action Schemas

## `files`

Request:
```json
{
  "request_id": "optional",
  "action": "files",
  "path": ".",
  "allow_outside_workspace": false
}
```

Response:
```json
{
  "ok": true,
  "action": "files",
  "paths": ["README.md", "cmd/main.go"]
}
```

Rules:
- `path` is optional, default `.`.
- Resolved `path` **MUST** be a directory.

## `search`

Request:
```json
{
  "request_id": "optional",
  "action": "search",
  "query": "TODO",
  "path": ".",
  "case_sensitive": false,
  "regex": false,
  "glob": "*.go",
  "limit": 100,
  "max_file_bytes": 1048576,
  "deterministic": true,
  "allow_outside_workspace": false
}
```

Response:
```json
{
  "ok": true,
  "action": "search",
  "matches": [
    {"path":"cmd/main.go","line":10,"col":5,"preview":"func main() {"}
  ]
}
```

Rules:
- `query` is required.
- `col` is 1-based byte index.
- Binary files **MUST** be skipped silently.
- `preview` is the matching line truncated to 512 bytes.
- When `limit>0`, returned matches **MUST NOT** exceed `limit`.

## `read`

Request:
```json
{
  "request_id": "optional",
  "action": "read",
  "path": "README.md",
  "start_line": 1,
  "end_line": 0,
  "max_bytes": 1048576,
  "allow_outside_workspace": false
}
```

Response:
```json
{
  "ok": true,
  "action": "read",
  "path": "README.md",
  "content": "...",
  "start_line": 1,
  "end_line": 42,
  "truncated": false,
  "has_more": false,
  "next_start_line": 0
}
```

Rules:
- `path` is required.
- `start_line <= 0` is coerced to `1`.
- `end_line == 0` means read to EOF.
- `max_bytes` is evaluated against raw on-disk bytes, including `\n` or `\r\n`.
- Returned `content` **MUST** contain only complete lines.
- If the next selected unread line would exceed `max_bytes`, the tool **MUST** stop before that line, set `truncated=true`, `has_more=true`, and `next_start_line` to the first unread line.
- If the first selected unread line alone exceeds `max_bytes`, the tool **MUST** return `too_large`.
- `has_more=true` means additional readable content remains beyond the returned slice.
- `next_start_line` **MUST** be `0` when `has_more=false`.
- Binary file reads **MUST** return `binary_file`. Unsupported encodings such as UTF-16 are treated as binary in v1.

## `write`

Request:
```json
{
  "request_id": "optional",
  "action": "write",
  "path": "notes.txt",
  "content": "hello",
  "create_dirs": false
}
```

Response:
```json
{
  "ok": true,
  "action": "write",
  "path": "notes.txt",
  "bytes_written": 5
}
```

Rules:
- `path` is required.
- JSON mode: `content` is required.
- CLI mode: content is stdin bytes.
- Write **MUST** use temp-file + sync + replace strategy.
- Parent dirs **MUST NOT** be created unless `create_dirs=true`.

## `patch`

Request:
```json
{
  "request_id": "optional",
  "action": "patch",
  "diff": "--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n"
}
```

Success response:
```json
{
  "ok": true,
  "action": "patch",
  "files_changed": ["a.txt"],
  "hunks_applied": 1,
  "results": [{"path":"a.txt","ok":true,"hunks_applied":1}]
}
```

Failure response (example):
```json
{
  "ok": false,
  "action": "patch",
  "error": {"code":"patch_failed","message":"one or more file patches failed"},
  "files_changed": ["a.txt"],
  "hunks_applied": 1,
  "results": [
    {"path":"a.txt","ok":true,"hunks_applied":1},
    {"path":"b.txt","ok":false,"error":"patch context mismatch"}
  ]
}
```

Rules:
- JSON mode requires `diff`; CLI mode reads diff from stdin.
- Accepted operations: modify/create/delete.
- Rename/copy metadata **MUST** fail (`patch_failed`).
- Hunk context/deletions **MUST** match strictly.
- Multi-file patch application is **per-file**; partial apply across files **MAY** occur and **MUST** be represented in `results`.

## `exec`

Request:
```json
{
  "request_id": "optional",
  "action": "exec",
  "argv": ["git", "status"],
  "cwd": ".",
  "env": {"CI":"1"},
  "timeout_ms": 5000
}
```

Response:
```json
{
  "ok": true,
  "action": "exec",
  "exit_code": 0,
  "stdout": "...",
  "stderr": "",
  "timing_ms": 31
}
```

Rules:
- `argv` is required.
- Command **MUST** execute as direct argv (no shell).
- `cwd` defaults to workspace root and **MUST** pass workspace validation.
- `env` object => merge with process env.
- `env` array (`KEY=VALUE`) => full replacement env.
- Non-zero process exit **MUST** return `ok=true` with `exit_code != 0`.
- Start failure **MUST** return `exec_failed`.
- Timeout **MUST** return `timeout`, `exit_code=-1`, with partial stdout/stderr if any.

## 7. Malformed and Edge-Case Behavior

- Malformed JSON => `{"ok":false,"action":"json","error":{"code":"invalid_request",...}}`
- Missing `action` => `invalid_request`.
- Reading a directory => `invalid_request`.
- Path traversal (`..`) outside workspace:
  - read/search/files: `path_denied` unless outside enabled
  - write/patch/exec cwd: always `path_denied`
- Symlink escape outside workspace follows same policy as above after canonical resolution.
- File exactly at `read.max_bytes` returns `truncated=false` if fully included.
- `read.start_line` beyond EOF returns empty `content`, `end_line=start_line-1`, `has_more=false`, and `next_start_line=0`.
- Paginated reads assume the target file is immutable or append-only across requests; no consistency token is provided.
- Search on binary file returns no match entry and no error entry.
- Patch stale context returns per-file failure and top-level `patch_failed`.
- Timeout during exec after partial output returns `timeout` with partial captured output.
