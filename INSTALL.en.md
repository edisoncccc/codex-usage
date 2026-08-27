# Codex Usage Dashboard Installation Guide

[English](INSTALL.en.md) · [简体中文](INSTALL.md)

This is the English installation specification for `codex-usage`. Human users and local AI agents use the same Go CLI, current-user paths, confirmation semantics, and receipts.

## 1. Current status

This repository is currently **source-only**: `binary_release_enabled` is `false` and the Windows `publisher_subject` is `null` in [`install-policy.json`](install-policy.json). There is no trusted binary Release and no EXE download.

Installation therefore requires an explicit choice to build from source. Do not infer a future download URL from asset names, and do not obtain binaries from the upstream Release page, mirrors, file-sharing sites, or search results. See [CODE_SIGNING.en.md](CODE_SIGNING.en.md) for the current signing status and future gates.

## 2. Supported platforms and prerequisites

- The only canonical repository is <https://github.com/edisoncccc/codex-usage>.
- Windows and Linux on `amd64` and `arm64` are supported; macOS is not currently supported.
- Go 1.26 or newer is required; [`go.mod`](go.mod) is authoritative.
- Git is required to obtain source; stop if Git is unavailable. No unverified source-download alternative is supported.
- Installation uses current-user privileges only. It never requests Administrator rights or `sudo`.
- The current user needs write access to the program, state, and local temporary directories.

The source build uses an embedded pure-Go SQLite driver. It does not require a separate SQLite installation, Python, Docker, or a C compiler.

## 3. Install with AI

Give the one canonical repository URL to a local AI and ask it to follow this section. The AI must not infer success from README prose; it must read the machine policy and the JSON Lines terminal event.

### 3.1 The only trusted entry point

The AI must perform these read-only checks first:

1. Confirm that the user-provided URL is exactly `https://github.com/edisoncccc/codex-usage`; reject lookalike repositories, mirrors, and third-party download pages.
2. Read [`install-policy.json`](install-policy.json) from that repository root and inspect `canonical_repository`, the current OS/architecture, installation scope, and `binary_release_enabled`.
3. When the current policy says `binary_release_enabled=false`, explain that only a source build is available, require Go 1.26+, and then request the one confirmation.
4. If a future policy enables prebuilt binaries but download, digest, signature, immutable Release, or Attestation verification fails, stop. A verification failure must never trigger an automatic source-build fallback.

### 3.2 AI execution protocol

After the user confirms, the AI follows this order:

1. Obtain source from the canonical repository in a new dedicated directory. Stop if the directory exists, cloning fails, the Git remote is not an exact match, or the commit cannot be read; never reuse an old same-named directory. Then record and display the actual commit.
2. Require Go 1.26+ with `go version`, run `go test ./...`, then build the native binary with `CGO_ENABLED=0`.
3. Run `version --json` and record version, commit, dirty state, OS, and architecture. A direct source build may truthfully report `commit=dev` and `dirty=true`; never rewrite that as a verified Release identity.
4. Only after obtaining the one confirmation described below, run `install --yes --json`. `--yes` asserts that the caller already obtained confirmation; it is not permission to skip confirmation.
5. Decode stdout one line at a time. Decide from the single `result` or `error` terminal event and stable `code`, never by matching Chinese or English prose.
6. After a successful install, run `doctor --json` and verify the service identity, state directory, database, and `http://127.0.0.1:43189`.
7. Report the actual write paths, service mode, verification states, scan summary, and uninstall commands. Preserve the original stable code on any failure; never claim success without a successful terminal event.

The AI must not disable security controls, elevate privileges, install a background updater, or pass a network response directly to a shell or PowerShell expression evaluator.

### 3.3 One-confirmation checklist

A normal source installation requires one confirmation. Before asking, the AI must show all of the following together:

- the canonical repository, current commit, target OS/architecture, and source-only build status;
- that source and Go module acquisition will contact GitHub and sources configured by the local Go tool; the current installer, service, and `update` command do not make external requests;
- the exact test, build, `install --yes --json`, and `doctor --json` commands;
- the Windows or Linux program and state paths;
- the Windows `HKCU` current-user startup entry or Linux `systemd --user` service (the receipt may report a current-user detached fallback when unavailable);
- loopback-only listening on `127.0.0.1:43189`;
- every local `CODEX_HOME` included in the first session JSONL scan;
- that default uninstall keeps the database and config, while purge requires separate confirmation for the canonical state path;
- that no Administrator rights or `sudo` will be used.

If preflight discovers required elevation, an unknown existing installation, a path/port conflict, invalid state ownership, or a request to erase data, stop the normal flow and report it separately. Do not fold it into the one confirmation.

## 4. Manual installation

Manual and AI installation call the same CLI. The commands below never execute a remote script from a network response. They create a new dedicated source directory under the current directory and refuse to reuse an existing target.

### 4.1 Windows PowerShell

```powershell
$ErrorActionPreference = "Stop"
$Repository = "https://github.com/edisoncccc/codex-usage"
$SourceDir = Join-Path (Get-Location) "codex-usage-source"

if (Test-Path -LiteralPath $SourceDir) {
    throw "Refusing to reuse existing source directory: $SourceDir"
}

git clone --origin origin -- $Repository $SourceDir
if ($LASTEXITCODE -ne 0) { throw "git clone failed with exit code $LASTEXITCODE" }

Set-Location -LiteralPath $SourceDir
$Origin = git remote get-url origin
if ($LASTEXITCODE -ne 0) { throw "reading origin failed with exit code $LASTEXITCODE" }
if ($Origin -cne $Repository) { throw "origin mismatch: $Origin" }

$Commit = git rev-parse --verify HEAD
if ($LASTEXITCODE -ne 0) { throw "reading commit failed with exit code $LASTEXITCODE" }
Write-Output "Commit: $Commit"

Get-Content -LiteralPath .\install-policy.json
go version
if ($LASTEXITCODE -ne 0) { throw "go version failed with exit code $LASTEXITCODE" }
go test ./...
if ($LASTEXITCODE -ne 0) { throw "go test failed with exit code $LASTEXITCODE" }
$env:CGO_ENABLED = "0"
go build -trimpath -o codex-usage.exe ./cmd/codex-usage
if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
& .\codex-usage.exe version --json
if ($LASTEXITCODE -ne 0) { throw "version failed with exit code $LASTEXITCODE" }
& .\codex-usage.exe install
if ($LASTEXITCODE -ne 0) { throw "install failed with exit code $LASTEXITCODE" }
& .\codex-usage.exe doctor --json
if ($LASTEXITCODE -ne 0) { throw "doctor failed with exit code $LASTEXITCODE" }
```

PowerShell checks `$LASTEXITCODE` immediately after every native `git`, `go`, and `codex-usage.exe` command, so no failed step can continue into testing, building, or installation. `install` prints the complete preflight checklist and reads one `yes`. To use English CLI text, place the global option anywhere, for example `.\codex-usage.exe --lang en install`.

### 4.2 Linux bash

```bash
set -euo pipefail

repository='https://github.com/edisoncccc/codex-usage'
source_dir="${PWD}/codex-usage-source"

if [[ -e "$source_dir" ]]; then
  printf 'Refusing to reuse existing source directory: %s\n' "$source_dir" >&2
  exit 1
fi

git clone --origin origin -- "$repository" "$source_dir"
cd -- "$source_dir"

origin="$(git remote get-url origin)"
if [[ "$origin" != "$repository" ]]; then
  printf 'origin mismatch: %s\n' "$origin" >&2
  exit 1
fi

commit="$(git rev-parse --verify HEAD)"
printf 'Commit: %s\n' "$commit"

cat ./install-policy.json
go version
go test ./...
CGO_ENABLED=0 go build -trimpath -o codex-usage ./cmd/codex-usage
./codex-usage version --json
./codex-usage install
./codex-usage doctor --json
```

`set -euo pipefail` stops immediately if cloning, source verification, testing, building, or program execution fails. `install` prints the complete preflight checklist and reads one `yes`. On a headless host, the program provides an SSH tunnel hint; the Dashboard still listens only on the server's loopback interface.

## 5. Build from source

If the source is already present, do not clone it again. Check `git remote get-url origin` and `git rev-parse HEAD`, then use the platform-specific commands.

### 5.1 Windows build

```powershell
$ErrorActionPreference = "Stop"
$Repository = "https://github.com/edisoncccc/codex-usage"
$Origin = git remote get-url origin
if ($LASTEXITCODE -ne 0) { throw "reading origin failed with exit code $LASTEXITCODE" }
if ($Origin -cne $Repository) { throw "origin mismatch: $Origin" }
$Commit = git rev-parse --verify HEAD
if ($LASTEXITCODE -ne 0) { throw "reading commit failed with exit code $LASTEXITCODE" }
Write-Output "Commit: $Commit"
go version
if ($LASTEXITCODE -ne 0) { throw "go version failed with exit code $LASTEXITCODE" }
go test ./...
if ($LASTEXITCODE -ne 0) { throw "go test failed with exit code $LASTEXITCODE" }
$env:CGO_ENABLED = "0"
go build -trimpath -o codex-usage.exe ./cmd/codex-usage
if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
& .\codex-usage.exe version --json
if ($LASTEXITCODE -ne 0) { throw "version failed with exit code $LASTEXITCODE" }
```

### 5.2 Linux build

```bash
set -euo pipefail

repository='https://github.com/edisoncccc/codex-usage'
origin="$(git remote get-url origin)"
if [[ "$origin" != "$repository" ]]; then
  printf 'origin mismatch: %s\n' "$origin" >&2
  exit 1
fi
commit="$(git rev-parse --verify HEAD)"
printf 'Commit: %s\n' "$commit"
go version
go test ./...
CGO_ENABLED=0 go build -trimpath -o codex-usage ./cmd/codex-usage
./codex-usage version --json
```

Stop installation and preserve the error when testing, building, or first execution fails. Never report a successful compilation as a passed test run.

## 6. Installation, progress, and machine receipt

### 6.1 Human interaction

A human runs:

```text
codex-usage install
```

The CLI shows the canonical repository, source, program/state paths, current-user service, loopback address, scan scope, and data-retention semantics, then asks once.

### 6.2 AI or automation

After obtaining the one confirmation, the canonical machine invocation is:

```text
codex-usage install --yes --json
```

JSON mode without `--yes` never reads stdin. It exits nonzero with a `confirmation_required` terminal event containing the preflight details.

### 6.3 Progress and terminal event

With `--json`, stdout is JSON Lines: one complete JSON object per line. Long phases such as scanning emit progress or a heartbeat at most about every 4 seconds, including home, file, record, event, and warning counts. The terminal never appears as only a blinking cursor.

Each command attempts to emit exactly one terminal event: `event=result` on success or `event=error` on failure. A successful installation uses code `install_complete`; its receipt includes build identity, absolute paths, service mode, Dashboard URL, scan summary, data retention, three-state verification results, and canonical uninstall commands. Release, Attestation, and Authenticode checks for a source build must be `not_applicable`. A local candidate-copy SHA256 check is `verified` only when it actually matched.

## 7. Health check

For a human-readable report:

```text
codex-usage doctor
```

For a machine-readable report:

```text
codex-usage doctor --json
```

`doctor` checks config, the state marker, database, Codex Homes, loopback listening, and the running service's complete build identity. Only the machine terminal code `health_check_complete` means no check has level `error`; warnings remain present in `checks`. The browser URL is <http://127.0.0.1:43189>.

## 8. Updates

The Phase A update channel is disabled. Both commands exit nonzero with one `release_channel_disabled` terminal event and retain `checked=false` and `modified=false`:

```text
codex-usage update --check --json
codex-usage update --yes --json
```

They create no HTTP client, download nothing, modify nothing, and do not fall back to a source build. To update a source installation, explicitly obtain a new commit from the canonical repository, then repeat testing, building, and installation confirmation.

## 9. Uninstall and data retention

### 9.1 Keep data by default

A human runs `codex-usage uninstall` and confirms. After already obtaining confirmation, a machine uses:

```text
codex-usage uninstall --yes --json
```

Default uninstall stops and removes this project's current-user service and program while preserving `usage.sqlite` and `config.json`. A running Windows program may return `removal_scheduled=true`; removal happens only after exit, so the path must not be reported as already absent. A normal synchronous Linux removal reports `false`.

### 9.2 Explicitly purge data

Purge permanently removes this project's database and configuration inside the canonical state directory. A human first runs `codex-usage uninstall --purge` to review the absolute target and confirm separately. An AI must show the same absolute path and obtain separate confirmation before running:

```text
codex-usage uninstall --purge --yes --json
```

Uninstall stops before deletion if path, marker, install-record, or executable-SHA256 validation fails. Purge never touches Codex session JSONL, `auth.json`, other applications' data, or third-party startup entries.

## 10. Default paths and CODEX_USAGE_HOME override

**Default layout (without `CODEX_USAGE_HOME`)**

| Data | Windows | Linux |
|---|---|---|
| Program | `%LOCALAPPDATA%\Programs\codex-usage\codex-usage.exe` | `~/.local/bin/codex-usage` |
| State directory | `%LOCALAPPDATA%\codex-usage` | `${XDG_DATA_HOME:-~/.local/share}/codex-usage` |
| Database | `%LOCALAPPDATA%\codex-usage\usage.sqlite` | `${XDG_DATA_HOME:-~/.local/share}/codex-usage/usage.sqlite` |
| Config | `%LOCALAPPDATA%\codex-usage\config.json` | `${XDG_DATA_HOME:-~/.local/share}/codex-usage/config.json` |
| Install record | `%LOCALAPPDATA%\codex-usage\install.json` | `${XDG_DATA_HOME:-~/.local/share}/codex-usage/install.json` |
| Default Codex Home | `%USERPROFILE%\.codex` | `~/.codex` |
| Background service | `HKCU` current-user startup | `systemd --user`, with a current-user fallback when necessary |

**Override layout**

With `CODEX_USAGE_HOME=<ABS>`, the state root is `<ABS>` and the installed executable moves with it. No extra `state` subdirectory is added.

| Data | Windows | Linux |
|---|---|---|
| Program | `<ABS>/bin/codex-usage.exe` | `<ABS>/bin/codex-usage` |
| State root | `<ABS>` | `<ABS>` |
| Config | `<ABS>/config.json` | `<ABS>/config.json` |
| Database | `<ABS>/usage.sqlite` | `<ABS>/usage.sqlite` |
| Install record | `<ABS>/install.json` | `<ABS>/install.json` |
| Backups | `<ABS>/backups` | `<ABS>/backups` |

`<ABS>` must be a dedicated absolute directory writable by the current user. It cannot be a filesystem root, the user home, or a directory containing unrelated files; actual separators follow the operating system. `CODEX_HOME` only selects Codex data sources and does not control the program/state layout. Do not synchronize the active state root between machines.

Bare `codex-usage` commands in this guide work only when the resolved program directory is on `PATH`. Otherwise, invoke the absolute `result.install_path` from the installation JSON terminal receipt. With the override, it is the corresponding `<ABS>/bin/...` path above.

## 11. Network and privacy boundaries

- Obtaining source and Go modules contacts GitHub and module sources configured for the local Go tool.
- The current `install`, `doctor`, background service, and disabled-channel `update` command make no external network request.
- The background service has no telemetry, cloud sync, background updater, or pricing fetch, and it listens only on `127.0.0.1`.
- The scanner selectively parses only local JSONL metadata, task boundaries, `token_count`, and other records required for statistics. It neither parses nor persists prompt, response, reasoning, or tool-output bodies.
- The program never reads or parses `auth.json`, and it never reads actual bills, subscription allowances, or account quotas.
- The database retains local project paths and Thread titles for attribution. Redact JSON/CSV exports before sharing them.

## 12. Windows Smart App Control

Windows Smart App Control or an organization's application-control policy may block Go-generated test programs in temporary directories, a source-built executable, or its first launch. If Windows reports a security block, the cursor remains with no progress, or the command fails with an application-control error:

1. Stop the installation and report the exact blocked command and path.
2. Do not disable Smart App Control, Defender, organization policy, or signature checks.
3. Do not evade the control by renaming files, copying them to a special directory, adding exclusions, or using another bypass.
4. Do not report compilation alone as a passed test or successful execution.

In the current source-only phase, there is no supported automatic alternative when security policy prevents local building or execution. Leave the product uninstalled and wait for a future trusted release that has passed every signing gate.

## 13. Failure handling and next action

- `confirmation_required`: show the details and ask the user; do not add `--yes` autonomously.
- `source_build_blocked`: the source build or candidate cannot execute; stop and preserve the error.
- `permission_required`: current-user paths are not writable; do not elevate or use `sudo`.
- `existing_install_untrusted`: an existing binary, record, port, or path identity is untrusted; stop without overwriting it.
- `health_check_failed`: the new service failed identity or health checks; the installer attempts rollback, and the AI must report the original error and rollback details.
- `release_channel_disabled`: the repository remains source-only. This is not a network failure and must not trigger an automatic download or source fallback.

Report installation-chain, path-boundary, or code-signing security issues through the private GitHub vulnerability-reporting process described in [SECURITY.md](SECURITY.md), with only minimal redacted evidence.
