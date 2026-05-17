# AgentRail Bootstrap Prompt

Use `agentrail` for file operations and command execution in the current workspace.

Workspace resolution:

1. Use `CODEX_TOOL_WORKSPACE` when it is set.
2. Otherwise use the process current working directory.
3. Keep all AgentRail `cwd` values inside that workspace.

Protocol:

- The normative protocol spec is `PROTOCOL.md` at the workspace root.
- Treat that file as authoritative for request and response fields.
- Use JSON requests with exactly one object per invocation.
- Parse AgentRail stdout as JSON only.

Bootstrap requirement:

- Before using `agentrail` by name, prepend the newest `bin\agentrail.exe` directory under the workspace to `PATH`.
- Do not hardcode drive letters or repository folder names.

Raw PowerShell:

```powershell
$workspace = if ($env:CODEX_TOOL_WORKSPACE) {
  (Resolve-Path -LiteralPath $env:CODEX_TOOL_WORKSPACE).Path
} else {
  (Get-Location).Path
}

$latest = Get-ChildItem -Path $workspace -Recurse -Filter 'agentrail.exe' |
  Where-Object { $_.FullName -match '[\\/]bin[\\/]agentrail\.exe$' } |
  Sort-Object LastWriteTime -Descending |
  Select-Object -First 1

if (-not $latest) {
  throw "No bin\agentrail.exe found under workspace: $workspace"
}

$env:PATH = '{0};{1}' -f $latest.Directory.FullName, $env:PATH
Write-Output ('Workspace: {0}' -f $workspace)
Write-Output ('Using AgentRail: {0}' -f $latest.FullName)
Write-Output ('PATH head: {0}' -f $latest.Directory.FullName)
```

AgentRail `exec` request for bootstrapped sessions:

```json
{
  "action": "exec",
  "cwd": ".",
  "argv": [
    "pwsh",
    "-NoLogo",
    "-NoProfile",
    "-Command",
    "$workspace = if ($env:CODEX_TOOL_WORKSPACE) { (Resolve-Path -LiteralPath $env:CODEX_TOOL_WORKSPACE).Path } else { (Get-Location).Path }; $latest = Get-ChildItem -Path $workspace -Recurse -Filter 'agentrail.exe' | Where-Object { $_.FullName -match '[\\\\/]bin[\\\\/]agentrail\\.exe$' } | Sort-Object LastWriteTime -Descending | Select-Object -First 1; if (-not $latest) { throw \"No bin\\agentrail.exe found under workspace: $workspace\" }; $env:PATH = '{0};{1}' -f $latest.Directory.FullName, $env:PATH; Write-Output ('Workspace: {0}' -f $workspace); Write-Output ('Using AgentRail: {0}' -f $latest.FullName); Write-Output ('PATH head: {0}' -f $latest.Directory.FullName)"
  ],
  "timeout_ms": 20000,
  "max_output_bytes": 32768
}
```

Execution guidance:

1. Run the bootstrap step first.
2. After bootstrap, prefer `agentrail --json` with protocol-compliant requests.
3. For file discovery use `files` or `search`.
4. For reads use bounded `read`.
5. For edits prefer `replace`, `build_patch`, or `patch` with the protocol rules from `PROTOCOL.md`.
6. For validation use `exec -- <argv...>` with direct argv, not shell command strings, unless a shell is explicitly required.
