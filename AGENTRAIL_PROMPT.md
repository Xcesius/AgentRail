# Codex Prompt For AgentRail

You are Codex operating inside a repository that provides `./agentrail.exe` as the execution adapter.

Use AgentRail first for repository operations.

Required behavior:

- Start with `./agentrail.exe files` unless you already know the exact target path.
- In larger repos, prefer `files` pagination instead of full enumeration.
- Use `./agentrail.exe search <query>` to narrow scope before broad reads.
- Use JSON `read` requests with `max_bytes` and continue with `next_start_line` when `has_more=true`.
- Preserve `file_token` from `read` when a later patch depends on the current file state.
- Use `patch` for targeted edits to existing files.
- For multi-file edits, prefer `atomic=true` unless partial apply is intentionally acceptable.
- Include `expected_file_tokens` when patching files that were previously read.
- Use `write` for new files or full-file replacements.
- Use `exec -- <argv...>` for builds, tests, and direct program execution.
- Parse stdout as JSON only.
- Do not rely on shell prose, shell quoting tricks, `cmd /c`, or PowerShell file commands when AgentRail can do the job.
- After every edit, verify with `read`, then run the relevant validation command.

Execution discipline:

1. Discover with `files` and `search`.
2. Read only the required regions.
3. Preserve `file_token` when coherence matters.
4. Make the smallest viable edit.
5. Inspect `repository_state` on every patch response.
6. Inspect `stdout_truncated`, `stderr_truncated`, `output_bytes`, and `exit_code` on every exec response.
7. Report exact outcomes from JSON fields.

Useful commands:

List files:

    ./agentrail.exe files

Paginated files:

    '{"action":"files","limit":200}' | ./agentrail.exe --json

Search:

    ./agentrail.exe search "main"

Read:

    '{"action":"read","path":"src/main.go","start_line":1,"max_bytes":4096}' | ./agentrail.exe --json

Patch with token protection:

    '{"action":"patch","atomic":true,"expected_file_tokens":{"src/main.go":"sha256:..."},"diff":"--- a/src/main.go\n+++ b/src/main.go\n@@ -1,1 +1,1 @@\n-old\n+new\n"}' | ./agentrail.exe --json

Patch schema:

    '{"action":"schema","target":"patch"}' | ./agentrail.exe --json

Exec:

    ./agentrail.exe exec -- go test ./...

If AgentRail returns a structured error, handle that error directly instead of bypassing the adapter.
If a patch response reports `repository_state="ambiguous"`, stop normal editing flow and surface that state clearly.
