# AgentRail Protocol Specification (v1.1)

This document is normative for `agentrail` request and response behavior.

Normative keywords are interpreted as: **MUST**, **MUST NOT**, **SHOULD**, **MAY**.

## 1. Transport

- The tool **MUST** emit exactly one JSON object on stdout per invocation.
- The tool **MUST NOT** mix prose with stdout JSON.
- Human diagnostics **MAY** be written to stderr.
- JSON mode (`--json` or auto-detected stdin JSON) **MUST** receive exactly one JSON object.
- Unknown JSON fields **MUST** fail with `invalid_request`.

## 2. Common Envelope

Every response **MUST** include:

- `ok: boolean`
- `action: string`
- `protocol_version: 1`
- `tool_version: string`
- `capabilities: string[]`

If the request was valid JSON and contained `request_id`, the response **MUST** echo `request_id`.
Malformed JSON **CANNOT** echo `request_id`.

### Version and capability semantics

- `tool_version` **MUST** match one of:
  - release: `MAJOR.MINOR.PATCH`
  - dev: `MAJOR.MINOR.PATCH-dev+<shortsha>`
- The default dev fallback is `0.0.0-dev+0000000`.
- `capabilities` **MUST** be a sorted, unique array of stable identifiers.
- Clients **MUST** determine optional behavior support from `capabilities`, not from `tool_version`.
- The minimum v1.1 Windows capability set is:
  - `exec`
  - `exec_output_budget`
  - `exec_process_tree_kill`
  - `files`
  - `files_pagination`
  - `patch`
  - `patch_atomic`
  - `patch_expected_file_tokens`
  - `read`
  - `read_file_token`
  - `search`
  - `write`
- Future versions **MAY** add capability identifiers. Existing identifiers **MUST** retain meaning.

### Error envelope

When `ok=false`, the response **MUST** include:

```json
{
  "error": {
    "code": "invalid_request",
    "message": "human-readable summary",
    "details": {}
  }
}
```

`error.details` **MAY** be omitted when there is nothing structured to report.

## 3. Stable Error Codes

The protocol-level error code set is:

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
- `token_mismatch`
- `cursor_invalid`
- `cursor_stale`
- `commit_failed`
- `rollback_failed`

### Required structured error details

Minimum `error.details` fields when known:

- `invalid_request`: `field`, `reason`
- `path_denied`: `path`, `policy`
- `not_found`: `path`, `kind`
- `too_large`: `field`, `limit_bytes`, `actual_bytes`
- `binary_file`: `path`
- `exec_failed` and `timeout`: `argv0`, `cwd`, `timeout_ms`, `output_bytes`, `process_tree_killed`
- `token_mismatch`: `path`, `expected_file_token`, `actual_file_token`
- `commit_failed` and `rollback_failed`: `phase`, `repository_state`
- `patch_failed`: `path`, `phase`, `hunk`, `expected`, `actual`

## 4. Workspace, Paths, and Deny Rules

Workspace root resolution:

1. `CODEX_TOOL_WORKSPACE` if set
2. otherwise process working directory at startup

Path handling rules:

- Paths **MUST** be resolved through absolute, cleaned, symlink-aware resolution.
- Existing paths **MUST** be resolved through symlinks.
- Non-existing paths **MUST** resolve the nearest existing ancestor through symlinks, then append remaining segments.
- Volume-relative Windows paths such as `C:foo` **MUST NOT** be accepted.

Denied paths:

- `.git` and `node_modules` anywhere under a candidate path are denied.
- Windows system directories (`Windows`, `Program Files`, `Program Files (x86)`, `ProgramData` on the workspace drive) are denied unless that path is itself the workspace root.

Workspace boundaries:

- `write`, `patch`, and `exec.cwd` **MUST** stay inside workspace.
- `read`, `search`, and `files` are workspace-only by default and **MAY** access outside workspace only when explicitly enabled.

### Canonical response paths

- In-workspace paths **MUST** be returned as workspace-relative paths using `/` separators.
- Workspace root **MUST** be represented as `.`.
- Outside-workspace paths **MUST** be returned as canonical absolute paths using `/` separators.
- On Windows, absolute response paths **MUST** use an uppercase drive letter.
- If reliable on-disk casing cannot be recovered cheaply, the runtime **MAY** return its canonical resolved spelling without additional case recovery.
- Clients **MUST** treat outside-workspace paths as case-insensitive on Windows.
- `patch.expected_file_tokens` keys **MUST** use canonical workspace-relative paths.

## 5. Determinism and Global Limits

- `files.paths` **MUST** be lexicographically sorted.
- `search.matches` **MUST** be sorted by `(path, line, col)` when deterministic mode is enabled.
- JSON `search.deterministic` defaults to `true` when omitted.
- Stdin payloads are capped at 64 MiB. Payloads at or above that cap **MUST** return `too_large`.

## 6. Actions

## `files`

Request fields:

- `action: "files"`
- `path` optional, default `.`
- `allow_outside_workspace` optional, default `false`
- `limit` optional
- `cursor` optional

Success fields:

- `paths: string[]`
- `has_more: boolean`
- `next_cursor: string`

Rules:

- Resolved `path` **MUST** be a directory.
- `limit <= 0` or omitted returns the full list for compatibility.
- New clients **SHOULD** send `limit`.
- `cursor` is opaque and currently encodes base64url JSON with fields `{v, root, after}`.
- `root` is the canonical resolved root path for the enumeration, expressed as an absolute slash path with uppercase drive letter on Windows.
- `after` is the last emitted canonical response path.
- `cursor_invalid` means malformed cursor, version mismatch, or root mismatch.
- `cursor_stale` means the cursor root matches but the `after` anchor no longer exists in the current sorted file set.
- When the cursor is valid, pagination resumes strictly after the anchor path.

## `search`

Request fields:

- `action: "search"`
- `query` required
- `path` optional, default `.`
- `case_sensitive` optional
- `regex` optional
- `glob` optional
- `limit` optional
- `max_file_bytes` optional
- `deterministic` optional, default `true`
- `allow_outside_workspace` optional, default `false`

Success fields:

- `matches: [{ path, line, col, preview }]`

Rules:

- `col` is a 1-based byte index.
- Binary files are skipped silently.
- `preview` is the matched line truncated to the implementation preview limit.
- If `limit > 0`, returned matches **MUST NOT** exceed `limit`.

## `read`

Request fields:

- `action: "read"`
- `path` required
- `start_line` optional, default `1`
- `end_line` optional, `0` means no explicit line bound
- `max_bytes` optional, default `1048576`
- `allow_outside_workspace` optional, default `false`

Success fields:

- `path`
- `content`
- `file_token`
- `start_line`
- `end_line`
- `truncated`
- `has_more`
- `next_start_line`

Rules:

- `max_bytes` is measured against raw file bytes, including `\n` or `\r\n`.
- Returned `content` **MUST** contain only complete lines.
- If appending the next selected line would exceed `max_bytes`, the tool **MUST** stop before that line, set `truncated=true`, `has_more=true`, and set `next_start_line` to the first unread line.
- If the first selected unread line alone exceeds `max_bytes`, the tool **MUST** return `too_large`.
- `has_more=true` means additional readable content remains.
- `next_start_line` **MUST** be `0` when `has_more=false`.
- `file_token` **MUST** be `sha256:<hex>` of the full current raw file bytes after path resolution and before page extraction.
- For unchanged files, paged reads **MUST** return the same `file_token`.
- Unsupported encodings such as UTF-16 are treated as `binary_file` in v1.1.
- Paged reads assume the file is immutable or append-only across requests.

## `write`

Request fields:

- `action: "write"`
- `path` required
- `content` required in JSON mode
- `create_dirs` optional, default `false`

Success fields:

- `path`
- `bytes_written`

Rules:

- CLI mode reads content from stdin.
- Write **MUST** use temp-file + sync + replace semantics.
- Parent directories **MUST NOT** be created unless `create_dirs=true`.

## `patch`

Request fields:

- `action: "patch"`
- `diff` required in JSON mode
- `atomic` optional, default `false`
- `expected_file_tokens` optional map of canonical workspace-relative path to `sha256:<hex>`

Every patch response, success or failure, **MUST** include:

- `repository_state`
- `files_changed`
- `hunks_applied`
- `results`

`repository_state` **MUST** be one of:

- `unchanged`
- `changed`
- `partially_changed`
- `ambiguous`

Top-level `ok` semantics:

- `ok=true` only when the requested patch operation completed fully as requested.
- `ok=false` for validation failure, token mismatch, mixed-result non-atomic patch, commit failure, or rollback failure.

Rules:

- Accepted operations are modify, create, and delete.
- Rename and copy metadata **MUST** fail.
- Hunk context and deletions **MUST** match strictly.
- Result entries include `path`, `ok`, `changed`, `hunks_applied`, and optional `error`, `error_code`, `error_details`.
- Successful no-op patch semantics:
  - syntactically valid patch
  - all hunks validate
  - zero repository byte changes
  - `ok=true`
  - `repository_state="unchanged"`
  - `files_changed=[]`
  - result entries use `changed=false`
- Empty diff / no file patches is **not** a successful no-op and returns `patch_failed`.

### `patch.expected_file_tokens`

- Keys **MUST** match canonical workspace-relative target paths.
- A token mismatch **MUST** fail deterministically with `token_mismatch`.
- Token mismatch before any writes yields `repository_state="unchanged"`.

### `patch.atomic`

When `atomic=false`:

- Patch application is per-file.
- Partial apply across files **MAY** occur.
- Mixed success/failure **MUST** return `ok=false` and `repository_state="partially_changed"` unless final state cannot be proven, in which case `repository_state="ambiguous"`.

When `atomic=true`:

- The tool **MUST** complete parse, path resolution, token validation, and in-memory patch application for all targets before any repository write occurs.
- Any pre-commit validation failure **MUST** perform zero repository writes and **MUST** return `repository_state="unchanged"`.
- Commit uses staged temp writes plus rollback metadata. This is logical atomicity with rollback, not a filesystem transaction.
- If commit fails and rollback fully restores prior state, the response **MUST** use `commit_failed` and `repository_state="unchanged"`.
- If rollback fails and a changed subset is known, the response **MUST** use `rollback_failed` and `repository_state="partially_changed"`.
- If rollback fails and final state cannot be proven, the response **MUST** use `rollback_failed` and `repository_state="ambiguous"`.
- Once file-level processing has begun, `results` **MUST** be included even when `ok=false`.

## `exec`

Request fields:

- `action: "exec"`
- `argv` required
- `cwd` optional, default workspace root
- `env` optional object or array
- `timeout_ms` optional
- `max_output_bytes` optional

Success fields:

- `exit_code`
- `stdout`
- `stderr`
- `stdout_truncated`
- `stderr_truncated`
- `output_bytes`
- `timing_ms`

Rules:

- `argv` **MUST** execute as direct argv. No shell parsing is performed.
- `cwd` **MUST** pass workspace validation.
- `env` object means merge with the process environment.
- `env` array of `KEY=VALUE` strings means full replacement environment.
- Non-zero exit code is still a successful transport response: `ok=true`, `exit_code != 0`.
- Start failure returns `exec_failed`.

### Output budget

- If `max_output_bytes` is omitted, the default is `262144`.
- `max_output_bytes <= 0` or `max_output_bytes > 4194304` **MUST** return `invalid_request` with `error.details.field="max_output_bytes"`.
- The runtime **MUST** enforce one combined captured-output budget across stdout and stderr.
- After the capture budget is exhausted, the runtime **MUST** continue draining both streams until exit or timeout and **MUST** discard uncaptured bytes.
- `output_bytes` reports captured bytes only.
- `stdout_truncated=true` iff at least one stdout byte was discarded.
- `stderr_truncated=true` iff at least one stderr byte was discarded.
- Fairness is first-arrival capture under one shared budget; there is no per-stream reservation.

### Timeout and process-tree semantics

- Timeout returns `timeout`, `exit_code=-1`, and any partial captured output.
- On supported Windows builds, `exec_process_tree_kill` means descendant-tree termination is guaranteed on timeout or cancel via Job Object kill-on-close semantics.
- If a future non-Windows build cannot provide the same guarantee, it **MUST NOT** advertise `exec_process_tree_kill`.

## 7. Malformed and Edge Cases

- Malformed JSON request => `ok=false`, `action="json"`, `error.code="invalid_request"`.
- Multiple JSON objects on stdin => `invalid_request`.
- Reading a directory => `invalid_request`.
- Path traversal outside workspace:
  - `read`, `search`, `files` => `path_denied` unless outside-workspace access was explicitly enabled
  - `write`, `patch`, `exec.cwd` => always `path_denied`
- Symlink escape is evaluated after canonical resolution and follows the same rules.
- File exactly at `read.max_bytes` is not truncated if it fully fits.
- `read.start_line` beyond EOF returns empty `content`, `end_line=start_line-1`, `has_more=false`, and `next_start_line=0`.
- Search encountering a binary file emits no match and no per-file warning.
- Patch with stale context returns per-file failure and a top-level patch failure.
