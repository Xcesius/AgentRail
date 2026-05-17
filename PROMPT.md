# AgentRail Bootstrap Prompt

Use `agentrail` for file operations and command execution in `F:\codextool`.

Protocol:

- The normative protocol spec is `F:\codextool\PROTOCOL.md`.
- Treat that file as authoritative for request and response fields.
- Use JSON requests with exactly one object per invocation.
- Keep all `cwd` values inside `F:\codextool`.

Bootstrap requirement:

- Before using `agentrail` by name, prepend the newest `bin\agentrail.exe` directory under `F:\codextool` to `PATH`.

Raw PowerShell:

```powershell
$latest = Get-ChildItem -Path 'F:\codextool' -Recurse -Filter 'agentrail.exe' |
  Where-Object { $_.FullName -match '[\\/]bin[\\/]agentrail\.exe$' } |
  Sort-Object LastWriteTime -Descending |
  Select-Object -First 1

if (-not $latest) {
  throw 'No agentrail.exe found under a bin folder.'
}

$env:PATH = '{0};{1}' -f $latest.Directory.FullName, $env:PATH
Write-Output ('Using AgentRail: {0}' -f $latest.FullName)
Write-Output ('PATH head: {0}' -f $latest.Directory.FullName)
```

AgentRail `exec` request:

```json
{
  "action": "exec",
  "cwd": ".",
  "argv": [
    "C:\\Program Files\\PowerShell\\7\\pwsh.exe",
    "-NoLogo",
    "-NoProfile",
    "-Command",
    "$latest = Get-ChildItem -Path 'F:\\codextool' -Recurse -Filter 'agentrail.exe' | Where-Object { $_.FullName -match '[\\\\/]bin[\\\\/]agentrail\\.exe$' } | Sort-Object LastWriteTime -Descending | Select-Object -First 1; if (-not $latest) { throw 'No agentrail.exe found under a bin folder.' }; $env:PATH = '{0};{1}' -f $latest.Directory.FullName, $env:PATH; Write-Output ('Using AgentRail: {0}' -f $latest.FullName); Write-Output ('PATH head: {0}' -f $latest.Directory.FullName)"
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
5. For edits prefer `patch` or `write` with the protocol rules from `F:\codextool\PROTOCOL.md`.
