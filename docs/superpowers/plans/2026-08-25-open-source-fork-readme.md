# Public Fork and Bilingual README Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish the verified local changes as the public `edisoncccc/codex-usage` MIT Fork with an attractive, structurally equivalent Chinese and English README.

**Architecture:** Preserve the upstream Git history and license, commit the existing local fixes in three reviewable units, then replace the two README entry points with a product-first bilingual presentation that reuses only synthetic repository media. Create the GitHub Fork only after tests, privacy checks, and documentation are complete; push over the already verified GitHub SSH identity.

**Tech Stack:** Go 1.26, SQLite, vanilla HTML/CSS/JavaScript, Playwright, Git, GitHub CLI, GitHub SSH.

---

## File map

| File | Responsibility in this plan |
|---|---|
| `.gitignore` | Keep local visual-brainstorm artifacts out of the public repository. |
| `internal/store/store.go` | Derive readable untitled Subagent labels from role and ancestor task title. |
| `internal/store/store_test.go` | Verify parent, nested, orphaned, and explicitly titled Subagent cases. |
| `internal/usage/scanner.go` | Keep modern fork replay data pending until a real child task begins. |
| `internal/usage/scanner_test.go` | Verify repeated parent metadata is not counted as child usage. |
| `internal/web/static/app.js` | Render Cached Rate, cost components, and quiet auto-handled reset records. |
| `internal/web/static/i18n.js` | Supply equivalent Chinese and English labels. |
| `internal/web/static/index.html` | Add the cost-breakdown and handled-record containers. |
| `internal/web/static/styles.css` | Style the new compact cost and provenance rows. |
| `tests/dashboard.spec.mjs` | Verify Cached Rate, cost components, and warning-priority behavior. |
| `README.md` | Chinese product-first repository homepage. |
| `README.en.md` | English homepage with the same structure and visuals. |
| `docs/sessions/2026-08-25-open-source-fork.md` | Human- and AI-readable execution handoff required by repository instructions. |

### Task 1: Isolate local-only artifacts

**Files:**
- Modify: `.gitignore`

- [ ] **Step 1: Add the visual companion directory to the ignore list**

Append this exact entry after the existing test-report ignores:

```gitignore
/.superpowers/
```

- [ ] **Step 2: Verify the local mockups no longer appear as publishable files**

Run:

```powershell
git status --short
git check-ignore -v .superpowers/brainstorm/*/content/*.html
```

Expected: `.superpowers/` is absent from the first command and the second command cites `/.superpowers/`.

- [ ] **Step 3: Commit only the ignore rule**

```powershell
git add .gitignore
git diff --cached --name-only
git commit -m "chore: ignore local brainstorm artifacts"
```

Expected staged file: `.gitignore` only.

### Task 2: Commit readable Subagent thread titles

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

- [ ] **Step 1: Inspect the exact implementation pair**

Confirm that `ReadStateThreads` parses `source.subagent.thread_spawn`, follows parent IDs with a cycle guard, preserves explicit child titles, and truncates inherited titles at the first line.

- [ ] **Step 2: Run the focused regression test**

```powershell
go test ./internal/store -run TestReadStateThreadsLabelsUntitledSubagentsFromTheirParentTask -count=1
```

Expected: `ok` for `internal/store`.

- [ ] **Step 3: Run the complete store package tests**

```powershell
go test ./internal/store -count=1
```

Expected: `ok` for `internal/store`.

- [ ] **Step 4: Commit the implementation and its test atomically**

```powershell
git add internal/store/store.go internal/store/store_test.go
git diff --cached --check
git commit -m "fix: label subagents from their parent tasks"
```

### Task 3: Commit modern fork replay accounting protection

**Files:**
- Modify: `internal/usage/scanner.go`
- Test: `internal/usage/scanner_test.go`

- [ ] **Step 1: Verify the guard is minimal**

The implementation must change only the legacy-offset branch from:

```go
} else if out.legacyOffset == 0 {
```

to:

```go
} else if !out.modernPrefix && out.legacyOffset == 0 {
```

- [ ] **Step 2: Run the focused replay regression test**

```powershell
go test ./internal/usage -run TestModernForkRepeatedParentMetadataStaysPendingWithoutChildTask -count=1
```

Expected: `ok` and no parent replay tokens exposed as child usage.

- [ ] **Step 3: Run the complete usage package tests**

```powershell
go test ./internal/usage -count=1
```

Expected: `ok` for `internal/usage`.

- [ ] **Step 4: Commit the accounting fix and regression test**

```powershell
git add internal/usage/scanner.go internal/usage/scanner_test.go
git diff --cached --check
git commit -m "fix: keep modern fork replay history pending"
```

### Task 4: Commit cache-aware cost and warning-priority UI

**Files:**
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/static/i18n.js`
- Modify: `internal/web/static/index.html`
- Modify: `internal/web/static/styles.css`
- Test: `tests/dashboard.spec.mjs`

- [ ] **Step 1: Verify Cached Rate semantics**

The overview must compute:

```javascript
const input = Math.max(0, Number(usage.input) || 0);
const cached = Math.max(0, Number(usage.cached_input) || 0);
const cachedRate = input > 0 ? formatPercent(cached / input) : "—";
```

Expected meaning: Cached Input divided by total Input; zero Input displays an em dash rather than `0%`.

- [ ] **Step 2: Verify the Standard API-equivalent cost components**

The overview must render Regular Input, Cached Input, Cache Write, and Output from the existing `estimate` fields without recomputing prices in JavaScript.

- [ ] **Step 3: Verify warning priority is conservative**

Only `cumulative_reset` belongs in `AUTO_HANDLED_WARNING_KINDS`. API failure must fall back to treating all warnings as actionable, and the handled records must remain accessible from the provenance section.

- [ ] **Step 4: Run the complete Dashboard test suite**

```powershell
npm test
```

Expected: Playwright reports all tests passed, including:

- `cached rate is unavailable without input tokens`
- `auto-handled counter resets stay quiet and reviewable`
- `actionable warnings stay prominent beside quiet handled records`

- [ ] **Step 5: Commit the UI and its browser tests atomically**

```powershell
git add internal/web/static/app.js internal/web/static/i18n.js internal/web/static/index.html internal/web/static/styles.css tests/dashboard.spec.mjs
git diff --cached --check
git commit -m "feat: clarify cached usage and warning priority"
```

### Task 5: Rewrite the bilingual product-first README

**Files:**
- Modify: `README.md`
- Modify: `README.en.md`

- [ ] **Step 1: Replace the Chinese hero with the approved product-first header**

Use this structure, with no Release badge or binary download link:

```markdown
<div align="center">

# codex-usage

**看清 Codex Token 花在哪里、缓存如何利用，以及折算成 Standard API 价格大约是多少。**

*See where your Codex tokens went, how caching helped, and what the same usage would cost at Standard API prices.*

[简体中文](README.md) · [English](README.en.md)

[![CI](https://github.com/edisoncccc/codex-usage/actions/workflows/ci.yml/badge.svg)](https://github.com/edisoncccc/codex-usage/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Local first](https://img.shields.io/badge/data-local--first-0f766e)](#隐私边界)
[![License](https://img.shields.io/github/license/edisoncccc/codex-usage)](LICENSE)

</div>

![codex-usage：本地 Codex Token 归属与 API 等价成本](Codex-Usage.png)
```

- [ ] **Step 2: Add a concise, visible Fork notice in Chinese**

```markdown
> [!NOTE]
> 这是基于 [zJay26/codex-usage](https://github.com/zJay26/codex-usage) 的社区维护 Fork，依照 MIT License 发布。当前版本加入了更清晰的 Subagent 归属、fork 重放保护、Cached Rate、费用分项和更克制的数据质量提示。
```

- [ ] **Step 3: Put the synthetic 12-second demo directly after the value summary**

```markdown
## 12 秒看懂

![codex-usage 合成数据演示：Token 归属、缓存和等价成本](docs/media/codex-usage-demo.gif)

> 演示只使用合成数据。项目不会读取或保存 prompt、回复、reasoning、工具输出或 `auth.json`。
```

- [ ] **Step 4: Replace direct binary installation with source-first instructions**

The Chinese README must include these exact supported flows:

```powershell
go test ./...
$env:CGO_ENABLED = "0"
go build -trimpath -o codex-usage.exe ./cmd/codex-usage
.\codex-usage.exe install
```

```bash
go test ./...
CGO_ENABLED=0 go build -trimpath -o codex-usage ./cmd/codex-usage
./codex-usage install
```

- [ ] **Step 5: Mirror the same hierarchy in English**

The English hero must use:

```markdown
**See where your Codex tokens went, how caching helped, and what the same usage would cost at Standard API prices.**

*看清 Codex Token 花在哪里、缓存如何利用，以及折算成 Standard API 价格大约是多少。*

[English](README.en.md) · [简体中文](README.md)
```

The English Fork notice must convey the same upstream, license, change list, and source-only status as the Chinese notice.

- [ ] **Step 6: Preserve the technical truth and privacy boundary**

Keep the existing explanations for local JSONL scanning, SQLite, deduplication, Agent attribution, pricing coverage, and the distinction between Standard API-equivalent cost versus actual bill or subscription quota. Do not claim cross-device aggregation or account-level usage.

- [ ] **Step 7: Validate README media and links locally**

```powershell
$required = @(
  'Codex-Usage.png',
  'docs/media/codex-usage-demo.gif',
  'LICENSE',
  'THIRD_PARTY_NOTICES.md',
  'README.md',
  'README.en.md'
)
$required | ForEach-Object { if (-not (Test-Path -LiteralPath $_)) { throw "Missing README asset: $_" } }
rg -n 'zJay26/codex-usage/releases|releases/latest/download' README.md README.en.md
```

Expected: all required files exist; the final `rg` command returns no matches.

- [ ] **Step 8: Commit both READMEs together**

```powershell
git add README.md README.en.md
git diff --cached --check
git commit -m "docs: refresh the bilingual project homepage"
```

### Task 6: Run publication checks and write the session handoff

**Files:**
- Create: `docs/sessions/2026-08-25-open-source-fork.md`

- [ ] **Step 1: Run all Go tests**

```powershell
go test ./...
```

Expected: all Go packages pass.

- [ ] **Step 2: Re-run all Dashboard tests from a clean test invocation**

```powershell
npm test
```

Expected: all Playwright tests pass.

- [ ] **Step 3: Run repository and privacy checks**

```powershell
git diff --check cd6d4fdbff54838aed7e38a8bc4edf022c6ce8c7..HEAD
git status --short
git grep -n -I -E 'C:\\Users\\ediso|D:\\Projects\\codex|Laptop-Chen|edison_c|01a0[0-9a-f]{4}' -- .
git ls-files | rg '(usage\.sqlite|\.jsonl$|^dist/|^node_modules/|^test-results/|^\.superpowers/)'
```

Expected: no whitespace errors; the working tree is clean; both privacy/publication scans return no matches.

- [ ] **Step 4: Write the required session handoff**

Create `docs/sessions/2026-08-25-open-source-fork.md` with these populated sections:

```markdown
# 2026-08-25 Open-source Fork Session

## 工作目标
## 执行步骤
## 修改文件
## 运行命令与结果
## 关键决策
## GitHub 发布结果
## 后续待办
```

Record actual test counts/results, commit hashes, remotes, repository URL, and the explicit decision not to publish binaries. Do not include local usernames, private paths, tokens, or session data.

- [ ] **Step 5: Commit the session handoff**

```powershell
git add docs/sessions/2026-08-25-open-source-fork.md
git diff --cached --check
git commit -m "docs: record the public fork handoff"
```

### Task 7: Create and publish the GitHub Fork over SSH

**Files:**
- Modify: local Git remote configuration only; no repository files.

- [ ] **Step 1: Reconfirm authenticated identities immediately before the external write**

```powershell
gh api user --jq .login
ssh -T -o BatchMode=yes -o ConnectTimeout=10 git@github.com 2>&1
```

Expected: GitHub user `edisoncccc`; SSH message says authentication succeeded. The SSH command may exit with status 1 because GitHub does not provide shell access.

- [ ] **Step 2: Confirm the target repository still does not exist**

```powershell
gh repo view edisoncccc/codex-usage --json nameWithOwner,url 2>$null
```

Expected before creation: repository-not-found response. If it exists, stop and inspect rather than overwriting it.

- [ ] **Step 3: Create the public Fork and configure remotes**

```powershell
gh repo fork zJay26/codex-usage --clone=false --remote
git remote set-url origin git@github.com:edisoncccc/codex-usage.git
git remote -v
```

Expected:

```text
origin   git@github.com:edisoncccc/codex-usage.git
upstream https://github.com/zJay26/codex-usage.git
```

If GitHub CLI chooses an SSH upstream URL, keep it; only the origin ownership must be exact.

- [ ] **Step 4: Push the verified branch as the Fork default branch**

```powershell
git push origin HEAD:main
```

Expected: a fast-forward update of `main`; never use `--force`.

- [ ] **Step 5: Verify the public repository and upstream relationship**

```powershell
gh repo view edisoncccc/codex-usage --json nameWithOwner,url,visibility,isFork,parent,defaultBranchRef
git ls-remote origin refs/heads/main
git rev-parse HEAD
```

Expected: public Fork of `zJay26/codex-usage`, default branch `main`, and remote `main` hash equal to local `HEAD`.

- [ ] **Step 6: Update the session handoff with the real publication result**

Append the verified URL, final commit hash, and remote relationship to `docs/sessions/2026-08-25-open-source-fork.md`, commit the update, then push the resulting fast-forward commit:

```powershell
git add docs/sessions/2026-08-25-open-source-fork.md
git commit -m "docs: record the published fork"
git push origin HEAD:main
```

Expected: the public repository contains the final verified handoff and no Release or binary assets.
