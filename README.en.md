<div align="center">

# codex-usage

**Which machine, model, project, or session used your Codex tokens?**

*本地优先的 Codex 使用分析：从逐电脑归属，到模型、项目、会话与 API 等价成本。*

[Live Demo](https://zjay26.github.io/codex-usage/?lang=en) · [Windows x64](https://github.com/zJay26/codex-usage/releases/latest/download/codex-usage-windows-amd64.exe) · [Linux x64](https://github.com/zJay26/codex-usage/releases/latest/download/codex-usage-linux-amd64) · English / [简体中文](README.md)

[![CI](https://github.com/zJay26/codex-usage/actions/workflows/ci.yml/badge.svg)](https://github.com/zJay26/codex-usage/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/zJay26/codex-usage?display_name=tag)](https://github.com/zJay26/codex-usage/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/github/license/zJay26/codex-usage)](LICENSE)

</div>

![12-second Codex Usage demo: per-machine tokens, date drill-down, filters, and equivalent cost](docs/media/codex-usage-demo.gif)

> The animation and Live Demo use synthetic data only. They do not read your files, set cookies, run analytics, or make external requests.

## Understand it in 30 seconds

Run one single-file binary on each Windows, WSL, or Linux host. It uses Codex session JSONL as the sole accounting source. The background service performs a lightweight file-metadata probe every 30 seconds, incrementally reads appended `token_count` records only after JSONL changes, and keeps a 10-minute fallback scan. The result stays in that machine's SQLite database, so the Dashboard answers **how much this computer used**, not how much the whole account used.

Per-machine attribution is codex-usage's most distinctive entry point, but it is not the endpoint. It breaks the local total down by model and token category, project, Thread, Session, Agent, and local calendar day, then adds Standard API-equivalent cost, pricing coverage, and data-quality records.

Codex's official [`/usage`](https://learn.chatgpt.com/docs/developer-commands.md?surface=cli) is useful for daily, weekly, and cumulative account token activity. codex-usage complements that account view with an explainable local attribution layer: **where these machine-local tokens came from and what drove them**.

The analysis stays local-first: one binary, loopback-only services, local SQLite, and **no access to `auth.json` or conversation content**.

## From totals to explainable usage analytics

| Question | What codex-usage shows |
|---|---|
| Which machine used the tokens? | An independent ledger for each Windows, WSL, or Linux host |
| Which models and token categories drove usage? | Model plus Input, Cached, Cache Write, Output, and Reasoning composition |
| Which work drove it? | Project, Thread, Session, and main task / Subagent / Guardian / Memory attribution |
| When did it happen? | Today, 7 days, 30 days, all time, local calendar days, and single-day drill-down |
| What would it roughly cost at API rates? | Standard API-equivalent cost with explicit token pricing coverage |
| Can the number be audited? | One JSONL source, cumulative differencing, fork-replay deduplication, and data-quality records |

## What it counts / what it does not count

| Counts | Does not count or read |
|---|---|
| Tokens, models, sources, projects, Threads, Sessions, Agents, and local calendar days on this machine | Usage from other machines on the account |
| Existing and newly appended `token_count` records in session JSONL | Account quota, subscription balance, or real bills |
| Standard API text-token equivalent cost and pricing coverage | Prompts, replies, reasoning, tool output, or `auth.json` |
| Audits for fork replay, cumulative resets, malformed records, and rewritten files | State-database `tokens_used`, cloud sync, remote telemetry, or third-party analytics |

> “Machine” means the host running the Codex client and codex-usage, not a remote target used by a shell or tool. Cost is an equivalent estimate, never an OpenAI bill.

## Install directly

Windows amd64 / x64 (no administrator privileges required):

```powershell
Invoke-WebRequest https://github.com/zJay26/codex-usage/releases/latest/download/codex-usage-windows-amd64.exe -OutFile codex-usage.exe
.\codex-usage.exe --lang en install
```

Linux amd64 / x64:

```bash
curl -fL https://github.com/zJay26/codex-usage/releases/latest/download/codex-usage-linux-amd64 -o codex-usage
chmod +x codex-usage
./codex-usage --lang en install
```

Need arm64? Download `windows-arm64.exe` or `linux-arm64` from the [latest Release](https://github.com/zJay26/codex-usage/releases/latest). Verify the file against `SHA256SUMS` on the same page.

The installer creates the local database, scans historical sessions, and starts a user-level background service. It does not configure a Codex telemetry exporter, and Codex does not need to restart. During an upgrade, it removes only the legacy OTel exporter enclosed by codex-usage's own markers and never changes a third-party exporter. Then run `codex-usage` to open the Dashboard.

On a headless Linux server, Codex Usage prints an SSH tunnel command. Run it from your own computer, then open `http://127.0.0.1:43189`.

## Highlights

| Capability | What you get |
|---|---|
| Signature per-machine attribution | A separate `machine_id` and SQLite database on every host |
| Historical + continuous incremental | Scan existing JSONL first, then read changes incrementally with a 10-minute fallback scan |
| Explainable deduplication | Fix one owner session per physical file, recognize copied fork history, and difference cumulative vectors |
| Daily drill-down | Continuous daily pulse, calendar, zero-usage days, and per-day model mix |
| Multi-dimensional usage analytics | Understand usage by model, token category, source, project, Thread, Session, main task, Subagent, Guardian, or Memory |
| Cost insight | Query-time Standard API-equivalent estimate with visible token pricing coverage; unknown usage never looks free |
| Auditable quality | Visible sole source, fork replay, cumulative resets, malformed records, and file rebuilds |
| Local-first | Loopback-only at `127.0.0.1`, embedded assets, and no runtime external requests |
| Single binary | Windows/Linux, amd64/arm64, no CGO or external database service |
| Bilingual | Dashboard and CLI support `zh-CN` / `en` through URL, button, flag, and environment |

<details><summary>View static desktop and 390 × 844 mobile screenshots</summary>

![Codex Usage Dashboard](docs/images/dashboard.png)

![Codex Usage mobile Dashboard](docs/images/dashboard-mobile.png)

</details>

## How it works

`codex-usage` is one Go binary containing the JSONL scanner, SQLite store, local API, and Web Dashboard.

```mermaid
flowchart LR
    A[Codex session JSONL] -->|historical + continuous incremental scan| C[normalized token events]
    S[state SQLite] -->|path discovery and metadata only| C
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

The Codex state database is used only to discover rollout paths and enrich titles, projects, and other metadata. Its `tokens_used` value never changes token totals. OpenAI's [`account/usage/read`](https://learn.chatgpt.com/docs/app-server#7-token-usage-chatgpt) is service-backed account activity; this tool counts only current-machine local JSONL, so the scopes differ.

### JSONL deduplication and fork ownership

- The first `session_meta` fixes the owner session of a physical JSONL file; a copied parent `session_meta` cannot overwrite it.
- In a `forked_from_id` rollout, the “child metadata → copied parent snapshots → parent metadata” prefix establishes a cumulative baseline but is not counted as new child usage.
- A resumed session uses a session-wide cumulative high-water mark, so repeated cumulative snapshots do not create new events.
- If a same-total snapshot corrects Cached Input, Cache Write, Reasoning, or another category, the original event is corrected instead of treating the snapshot as a duplicate.

### File and calendar stability

Every scan unions paths from the state database with `sessions/` and `archived_sessions/`, so a missing state row cannot hide a JSONL file. Ordinary Windows paths and `\\?\` extended paths normalize to one file. Truncation, a rewrite inside the scanned range, or a newly completed fork-replay boundary triggers an automatic rebuild from all JSONL, leaving no stale events.

Each event stores its local date and hour at ingestion, so changing the system timezone later does not move existing history at query time. Repeated data-quality records are grouped by kind and local path; cumulative resets, malformed records, invalid timestamps, and automatic rebuilds remain visible.

### Local service

The service scans once on startup, then checks only JSONL size and modification time every 30 seconds. It runs an incremental scan after a change and a fallback scan every 10 minutes. Dashboard reads use a separate read-only SQLite pool, so ingestion no longer queues every page query behind one connection. There is no central server or cross-machine sync.

The Dashboard has three first-level views: Overview, Daily, and Details. Overview defaults to the last seven local calendar days. Daily fills zero-usage dates and supports calendar drill-down. Details shows one attribution dimension—model, source, agent, project, or thread—at a time.

Display settings in the header use a more comfortable type scale by default and let you adjust font size, display density, color theme, interface motion, and language with an immediate preview. These preferences stay in the current browser and never change usage data or exports.

### Standard API-equivalent cost

The estimator streams the normalized events that already passed source de-duplication and attribution filtering. It runs at query time, writes no cost data to SQLite, and leaves existing token totals unchanged. Arithmetic uses fixed-point nano-USD. Cached Input and Cache Write are removed from regular Input, and Reasoning is already included in Output, so neither is charged twice.

Bundled Standard text prices were checked on **2026-08-04**. All values are USD / 1M tokens:

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

GPT-5.6 Cache Write uses the official 1.25× regular Input rule. To match Codex's current effective context limit of roughly 258K, every recognized model uses the Standard short-context rates shown above and no long-context multiplier is applied. The UI always shows estimated cost together with token pricing coverage; unknown models are never treated as zero-cost.

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
codex-usage doctor                  Check paths, JSONL sources, and service
codex-usage config add-home PATH    Add another CODEX_HOME
codex-usage uninstall               Remove the app, keep the database
codex-usage uninstall --purge       Remove the app and local data
```

The Dashboard supports `?lang=en|zh-CN` and its header language button. The URL wins over the saved locale, followed by the browser locale. The CLI supports global `--lang` and `CODEX_USAGE_LANG`, for example `CODEX_USAGE_LANG=en codex-usage doctor`. `--json` and `--csv` fields never change with language.

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

- This release does not aggregate multiple machines; open each machine's Dashboard separately
- JSONL that is permanently deleted or damaged cannot be fabricated from state `tokens_used` or account usage
- historical sessions cannot be reliably split after users synchronize one Codex Home across machines
- Codex `total` is displayed as reported; it is not an actual bill or account-quota measurement, and API-equivalent cost is only a current-price conversion of local tokens

## License

[MIT](LICENSE) © Codex Usage contributors
