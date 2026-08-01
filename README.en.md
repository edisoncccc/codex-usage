<div align="center">

# codex-usage

**Turn scattered local Codex records into clear, trustworthy per-machine token usage.**

English | [简体中文](README.md)

[![CI](https://github.com/zJay26/codex-usage/actions/workflows/ci.yml/badge.svg)](https://github.com/zJay26/codex-usage/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/zJay26/codex-usage?display_name=tag)](https://github.com/zJay26/codex-usage/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/github/license/zJay26/codex-usage)](LICENSE)

</div>

![Codex Usage Dashboard](docs/images/dashboard.png)

<details><summary>View the 390 × 844 mobile layout</summary>

![Codex Usage mobile Dashboard](docs/images/dashboard-mobile.png)

</details>

## The problem it solves

Account-level usage cannot answer a practical question: **which computer used those tokens?**

When the same Codex account runs on a Windows workstation, a Linux server, or WSL, the account total mixes everything together. `codex-usage` runs independently on each host and shows:

- today's, 7-day, 30-day, and all-time usage for this machine
- local-calendar daily usage, zero-usage days, and per-day model composition
- usage by model, project, thread, source, and agent type
- Standard API-equivalent cost estimates with explicit token pricing coverage
- historical sessions and future real-time metrics in one view
- visible warnings when attribution is incomplete instead of false precision

All data stays local. The tool does not read account credentials, store prompts or responses, or upload usage data.

## Highlights

| Capability | What you get |
|---|---|
| Per-machine accounting | A separate `machine_id` and SQLite database on every host |
| Historical import | Existing local Codex sessions are scanned on first install |
| Real-time metrics | Official Codex OTel metrics, refreshed every 60 seconds by default |
| Useful attribution | Filter by model, source, project, thread, main task, subagent, guardian, or memory |
| Daily drill-down | Continuous daily pulse, calendar view, zero-usage days, and per-day model mix |
| Equivalent cost | Query-time Standard API estimate; unknown tokens remain visibly unpriced |
| Honest gaps | Corrupt records, counter resets, and unattributed history stay visible |
| Local-first | Dashboard binds only to `127.0.0.1`; all frontend assets are embedded |
| Single binary | Windows and Linux, amd64 and arm64, no CGO or external database server |
| Export | JSON and CSV from both the CLI and Dashboard |

## Quick start

Download the correct file from the [latest Release](https://github.com/zJay26/codex-usage/releases/latest):

| Platform | amd64 / x64 | arm64 |
|---|---|---|
| Windows | `codex-usage-windows-amd64.exe` | `codex-usage-windows-arm64.exe` |
| Linux | `codex-usage-linux-amd64` | `codex-usage-linux-arm64` |

Windows:

```powershell
.\codex-usage-windows-amd64.exe install
```

Linux:

```bash
chmod +x codex-usage-linux-amd64
./codex-usage-linux-amd64 install
```

No administrator privileges are required. Installation creates the local database, scans historical sessions, adds a loopback OTel endpoint without overwriting an existing exporter, and starts a user-level background service. Restart Codex afterward so new Codex processes load the real-time metrics configuration.

Windows uses an `HKCU` login startup entry, so the background service returns automatically after a reboot when the same user signs in. Linux prefers an enabled `systemd --user` service. If no user bus is available, the installer still starts the current background process and prints a warning. An unattended Linux host needs linger enabled for the user if the service must continue after every login session ends.

When upgrading an earlier release, the installer stops the previous background service before migrating its configuration, SQLite database, WAL data, and managed OTel block. Historical usage remains available; the new release creates and uses only `usage.sqlite`.

Run `codex-usage` to open the Dashboard. On a headless Linux host, use the printed SSH tunnel command and open `http://127.0.0.1:43189` locally.

## How it works

`codex-usage` is one Go binary containing the collector, SQLite store, local API, and Web Dashboard.

```mermaid
flowchart LR
    A[Codex session JSONL] -->|historical incremental scan| C[normalized token events]
    B[Codex OTel metrics] -->|real-time OTLP/HTTP| C
    S[state SQLite] -->|historical gap fallback only| C
    C --> D[(local SQLite)]
    D --> P[query-time pricing estimator]
    D --> E[127.0.0.1 API]
    P --> E
    E --> F[Dashboard]
    E --> G[CLI / JSON / CSV]
```

### Historical scan

The tool reads the current machine's `CODEX_HOME`. It first discovers canonical session metadata from the Codex state database, then streams JSONL files under `sessions/` and `archived_sessions/`.

Codex session usage is cumulative. At **each** `token_count` record, the scanner subtracts the previous cumulative vector and assigns that delta to the record timestamp's local calendar day. It never moves an entire multi-day session to the session's latest update date. Stable event IDs and cursors keep repeated scans idempotent. Large prompt, response, reasoning, and tool-output records are skipped without loading the entire line into memory or writing content to the database.

State-database differences that have only an aggregate total and no event timestamp remain date-unattributed. They appear only in the all-time total and are never guessed or spread across days. OpenAI's [`account/usage/read`](https://learn.chatgpt.com/docs/app-server#7-token-usage-chatgpt) returns service-backed ChatGPT account token activity and optional daily buckets; this tool uses only current-machine local sources, so the scopes differ, and the public documentation does not define those account buckets as using the same local-calendar timezone.

### Real-time collection

The installer safely configures Codex to send official `turn.token_usage` metrics to:

```text
http://127.0.0.1:43189/v1/metrics
```

The receiver accepts OTLP/HTTP JSON on loopback only. It never exposes a public listener.

### Merge rules

- OTel is the machine total during known OTel coverage windows
- session JSONL fills time before OTel was enabled or while the receiver was offline
- state-database `tokens_used` only fills historical differences that cannot be assigned to a date
- project, thread, and session details are attribution views and are not added again to the machine total

The three sources are never blindly summed.

Repeated data-quality records are grouped by kind and local path while retaining first seen, last seen, and occurrence count. `state_fallback_suppressed_otel` means the de-duplication guard successfully prevented a state total from being added on top of OTel, so it remains informational rather than actionable. Cumulative resets, malformed records, and invalid timestamps remain visible for review.

### Local service

The service scans once on startup and then incrementally every 60 seconds by default. The Dashboard and CLI query the same local SQLite database. There is no central server or cross-machine sync.

The Dashboard has three first-level views: Overview, Daily, and Details. Overview defaults to the last seven local calendar days. Daily fills zero-usage dates and supports calendar drill-down. Details shows one attribution dimension—model, source, agent, project, or thread—at a time.

### Standard API-equivalent cost

The estimator streams the normalized events that already passed source de-duplication and attribution filtering. It runs at query time, writes no cost data to SQLite, and leaves existing token totals unchanged. Arithmetic uses fixed-point nano-USD. Cached Input and Cache Write are removed from regular Input, and Reasoning is already included in Output, so neither is charged twice.

Bundled Standard text prices were checked on **2026-07-31**. All values are USD / 1M tokens:

| Model | Input | Cached | Cache Write | Output |
|---|---:|---:|---:|---:|
| [GPT-5.6 Sol](https://developers.openai.com/api/docs/models/gpt-5.6-sol) | 5.00 | 0.50 | 6.25 | 30.00 |
| [GPT-5.6 Terra](https://developers.openai.com/api/docs/models/gpt-5.6-terra) | 2.00 | 0.20 | 2.50 | 12.00 |
| [GPT-5.6 Luna](https://developers.openai.com/api/docs/models/gpt-5.6-luna) | 0.20 | 0.02 | 0.25 | 1.20 |
| [GPT-5.5](https://developers.openai.com/api/docs/models/gpt-5.5) | 5.00 | 0.50 | not published | 30.00 |
| [GPT-5.4](https://developers.openai.com/api/docs/models/gpt-5.4) | 2.50 | 0.25 | not published | 15.00 |
| [GPT-5.4 mini](https://developers.openai.com/api/docs/models/gpt-5.4-mini) | 0.75 | 0.075 | not published | 4.50 |
| [GPT-5.3-Codex](https://developers.openai.com/api/docs/models/gpt-5.3-codex) | 1.75 | 0.175 | not published | 14.00 |
| [GPT-5.2-Codex](https://developers.openai.com/api/docs/models/gpt-5.2-codex) | 1.75 | 0.175 | not published | 14.00 |

GPT-5.6 Cache Write uses the official 1.25× regular Input rule. Exact GPT-5.4, GPT-5.5, and GPT-5.6 events apply the long-context multipliers when Input exceeds 272K. Aggregate-only records and events whose request boundary cannot be established remain unpriced. The UI always shows estimated cost together with token pricing coverage; unknown models are never treated as zero-cost.

Internal models can be explicitly mapped to one built-in public model or assigned custom rates in the Dashboard. Overrides take effect without restarting:

```json
{
  "pricing_overrides": {
    "codex-auto-review": { "alias_of": "gpt-5.6-luna" },
    "internal-model": {
      "input_usd_per_million": "1.00",
      "cached_input_usd_per_million": "0.10",
      "cache_write_input_usd_per_million": "1.25",
      "output_usd_per_million": "6.00"
    }
  }
}
```

The loopback API exposes `GET /api/v1/cost-estimate`, `GET /api/v1/pricing`, and `PUT /api/v1/pricing/overrides`. Pricing ships inside the binary; the running app never fetches price pages.

## Common commands

```text
codex-usage                         Open the Dashboard
codex-usage summary --since 7d     Show a 7-day summary
codex-usage summary --since 30d --json
codex-usage summary --since all --csv
codex-usage scan                    Incremental historical scan
codex-usage scan --rebuild          Rebuild historical scan data
codex-usage doctor                  Check paths, service, and coverage gaps
codex-usage config add-home PATH    Add another CODEX_HOME
codex-usage uninstall               Remove the app, keep the database
codex-usage uninstall --purge       Remove the app and local data
```

## Local data paths

| Data | Windows | Linux |
|---|---|---|
| Codex Home | `%USERPROFILE%\.codex` | `~/.codex` |
| codex-usage state | `%LOCALAPPDATA%\codex-usage` | `${XDG_DATA_HOME:-~/.local/share}/codex-usage` |
| Installed binary | `%LOCALAPPDATA%\Programs\codex-usage\codex-usage.exe` | `~/.local/bin/codex-usage` |
| SQLite | `...\codex-usage\usage.sqlite` | `.../codex-usage/usage.sqlite` |

`CODEX_USAGE_HOME` overrides the app state directory. Do not synchronize this directory between machines, or the per-machine boundary becomes unreliable.

`usage.sqlite` is created automatically when installation, service startup, scanning, or a query first opens the store, then persists in the state directory above. The embedded pure-Go SQLite driver requires no separately installed SQLite server, Python, Docker, or CGO. The current user only needs write access to the state directory and sufficient disk space. Prefer a local disk; do not place the active database on cloud-sync folders, network shares, or a directory written by multiple machines.

## Privacy boundaries

- never reads or parses `auth.json`
- never stores prompts, responses, reasoning, or tool output
- never stores a Codex account ID
- uses no CDN; frontend assets are embedded
- refuses to listen outside `127.0.0.1`
- never reads actual OpenAI billing or ChatGPT rate-limit/account quota; it only estimates Standard API-equivalent cost for this machine
- embeds its pricing catalog and makes no external network request for estimation

Full local project paths and thread titles are retained for attribution, so JSON/CSV exports may contain that local metadata.

## Build from source

Go 1.26.x is required:

```bash
go test ./...
CGO_ENABLED=0 go build -trimpath -o codex-usage ./cmd/codex-usage
```

Build all four targets with `scripts/build.ps1` on Windows or `scripts/build.sh` on Linux.

Dashboard tests:

```bash
npm ci
npx playwright install chromium
go build -trimpath -o codex-usage ./cmd/codex-usage
CODEX_USAGE_BIN=./codex-usage npm test
```

See [ACCEPTANCE.md](ACCEPTANCE.md) for validation results, [CONTRIBUTING.md](CONTRIBUTING.md) before opening an issue, and [SECURITY.md](SECURITY.md) for private vulnerability reporting.

## Known boundaries

- v1 does not aggregate multiple machines; open each machine's Dashboard separately
- Codex OTel may omit thread IDs or cwd, so exact totals can coexist with JSONL-based attribution
- historical sessions cannot be reliably split after users synchronize one Codex Home across machines
- Codex `total` is displayed as reported; it is not an actual bill or account-quota measurement, and API-equivalent cost is only a current-price conversion of local tokens

## License

[MIT](LICENSE) © Codex Usage contributors
