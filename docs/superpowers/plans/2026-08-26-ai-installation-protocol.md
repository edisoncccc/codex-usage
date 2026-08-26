# AI 安装协议第一阶段 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在保持公开 Fork 为 source-only 的前提下，让人工用户和本地 AI 通过同一套 Go CLI 完成可确认、可观察、可验证、可回滚的当前用户安装流程。

**Architecture:** 先以根目录机器策略和 fail-closed workflow 锁死二进制发布，再建立与语言无关的 JSON Lines 事件协议、安装记录和版本决策；扫描器只负责同步进度快照，app 层用独立心跳保证长任务最多四秒有输出；安装通过暂存、备份、身份健康检查和提交/回滚形成事务。Phase A 的 `update` 明确返回渠道关闭，绝不联网或静默降级；SignPath 申请、签名 workflow 和正式 Release 留到取得外部书面批准后的独立计划。

**Tech Stack:** Go 1.26 标准库、SQLite、PowerShell、Bash、GitHub Actions、Playwright、Git。

---

## 范围与完成边界

本计划只实施规格 [2026-08-26-ai-installation-design.md](../specs/2026-08-26-ai-installation-design.md) 的阶段 A：

- `install-policy.json` 保持 `binary_release_enabled=false`。
- `.github/workflows/release.yml` 不监听 tag、不构建、不上传、不创建 Release。
- 不提交 SignPath 申请，不创建 SignPath/GitHub secrets，不预填 publisher Subject。
- 不创建 `v*` tag、GitHub Release 或任何对外二进制资产。
- `update` 只建立稳定 CLI 契约；可信 Release 发现、下载、签名与 Attestation 验证属于阶段 C。
- 不改 Go module 路径，不重写语言，不支持 macOS、管理员安装或后台自动更新。

阶段 A 完成后，阶段 B 只有在用户另行授权对外申请且 SignPath 明确书面接受该公开 Fork 时才能规划；阶段 C 只有在真实 publisher、测试签名和时间戳验证通过后才能规划。未知的 SignPath action 版本、组织 ID、项目 ID、策略 ID 和证书 Subject 不得以占位值写入仓库。

## 文件总览

| 文件 | 职责 |
|---|---|
| `install-policy.json` | AI/人工共同读取的语言无关安装与信任策略。 |
| `internal/installpolicy/policy.go` | 解析并验证策略；向 Phase A `update` 提供编译期关闭状态。 |
| `internal/installpolicy/policy_test.go` | 锁定规范仓库、四个平台资产和 source-only workflow。 |
| `.github/workflows/release.yml` | 改成手动、只读、不可发布的 source-only 哨兵。 |
| `internal/app/events.go` | 单行 JSON 事件、稳定 code、唯一终态与三态验证结果。 |
| `internal/app/version.go` | 人类/机器版本输出和完整构建身份。 |
| `internal/install/state.go` | 安装记录、文件 SHA256、严格稳定版本比较和安装决策。 |
| `internal/usage/progress.go` | 扫描进度快照与 observer；保留现有 `Scan` API。 |
| `internal/app/progress.go` | 人类/JSON 心跳与线程安全快照，生产间隔四秒。 |
| `internal/app/scan.go` | `scan --json` JSON Lines 迁移。 |
| `internal/server/server.go` | `/healthz` 返回完整 version/commit/build/os/arch 身份。 |
| `internal/app/doctor.go` | typed doctor report 和稳定 JSON Lines 终态。 |
| `internal/app/lifecycle.go` | 可注入的安装事务、身份健康检查和失败回滚。 |
| `internal/app/install.go` | `install --yes --json`、幂等、进度与最终回执。 |
| `internal/app/update.go` | Phase A 的确定性 `release_channel_disabled` 契约。 |
| `internal/app/uninstall.go` | 确认、默认保留数据、purge 路径和异步删除回执。 |
| `INSTALL.md` / `INSTALL.en.md` | 同构的 AI/人工安装权威说明。 |
| `CODE_SIGNING.md` / `CODE_SIGNING.en.md` | 当前 source-only 状态、未来角色和签名门禁，不声称已获批。 |
| `README.md` / `README.en.md` | 同等可见的“让 AI 安装 / 手动安装”入口。 |
| `CONTRIBUTING.md` / `SECURITY.md` | 贡献与安全边界同步到新的安装协议。 |
| `tests/install-windows.ps1` / `tests/install-linux.sh` | 隔离状态目录下的当前用户生命周期验收。 |
| `.github/workflows/ci.yml` | 运行策略、Go、生命周期、语法和 Dashboard 门禁，不上传二进制 artifact。 |
| `docs/sessions/2026-08-26-ai-installation-implementation.md` | 实际实施、命令、结果、提交与后续外部门禁留痕。 |

## 所有任务共用规则

1. 每个行为任务严格执行 RED → GREEN → 回归；先看到目标测试因缺少行为而失败，再写最小实现。
2. 生产实现只使用标准库，不增加 Go 或 npm 依赖。
3. 所有平台副作用必须通过窄接口注入；单元测试不得写真实 HKCU、真实 `systemd --user` 或用户状态目录。
4. JSON 模式 stdout 只能有每行一个 JSON 对象；字段名、status、phase、code 和验证枚举不随语言变化。
5. 每条命令由统一 runner 发出且仅发出一个终态；命令实现只能发阶段/心跳事件并返回 typed result/error。
6. 人类输出继续从 `internal/cliui/locale.go` 取中英文同构 key；机器枚举不得进入翻译表。
7. 每次提交前运行 `git diff --cached --check`，并确认只暂存本任务文件。
8. 每次提交使用中文 Conventional Commit，并带：

```text
AI-Coding: true
AI-Agent: Codex
AI-Model: GPT-5
```

9. 每个任务的执行者必须在自己的 PowerShell 会话开始先运行以下初始化；本计划后续命令中的 `$Go` 均指该变量，不能假设它从上一个任务或子智能体继承：

```powershell
$Go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $Go) { $Go = Join-Path $env:ProgramFiles 'Go\bin\go.exe' }
if (-not (Test-Path -LiteralPath $Go)) { throw 'Go 1.26+ executable not found' }
& $Go version
```

10. 当前 Windows 若由 Smart App Control 阻止 Go 自动生成的临时 `.test.exe`，先如实记录标准命令被阻止，再使用固定路径测试二进制执行同一测试；编译成功不能写成测试通过。固定路径示例：

```powershell
$Go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $Go) { $Go = Join-Path $env:ProgramFiles 'Go\bin\go.exe' }
New-Item -ItemType Directory -Force -Path 'test-results' | Out-Null
& $Go test -c -o 'test-results/app-phase-a.test.exe' ./internal/app
$TestBinary = (Resolve-Path 'test-results/app-phase-a.test.exe').Path
Push-Location 'internal/app'
& $TestBinary '-test.count=1' '-test.timeout=10m'
Pop-Location
```

11. 不关闭或绕过 Windows 安全功能，不 force push，不删除工作树文件或目录。

### Task 1: 锁死 source-only 策略与发布入口

**Files:**
- Create: `install-policy.json`
- Create: `internal/installpolicy/policy.go`
- Create: `internal/installpolicy/policy_test.go`
- Modify: `.github/workflows/release.yml`

- [ ] **Step 1: 先写失败的策略契约测试**

测试必须从仓库根目录读取 `install-policy.json` 和 `.github/workflows/release.yml`，覆盖：

```go
func TestRepositoryPolicyIsSourceOnly(t *testing.T)
func TestRepositoryPolicyMapsEverySupportedAsset(t *testing.T)
func TestReleaseWorkflowCannotPublishWhileSourceOnly(t *testing.T)
```

断言规范仓库精确为 `edisoncccc/codex-usage`，稳定标签正则为 `^v[0-9]+\.[0-9]+\.[0-9]+$`，四个映射恰好为 Windows/Linux × amd64/arm64；`binary_release_enabled` 为 false，publisher Subject 为 null；不可变 Release、SHA256、Release API digest、Artifact Attestation 和 Windows Authenticode/时间戳要求均为 true；禁止提权、静默源码降级和后台更新。

workflow 断言不得出现 tag trigger、`gh release create`、`upload-artifact` 或构建/上传资产步骤。

- [ ] **Step 2: 运行 RED 测试**

```powershell
$Go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $Go) { $Go = Join-Path $env:ProgramFiles 'Go\bin\go.exe' }
& $Go test ./internal/installpolicy -count=1
```

Expected: 因 package、策略文件或解析函数尚不存在而失败；失败原因必须与本任务一致。

- [ ] **Step 3: 写最小策略实现和根策略文件**

`Policy` 至少暴露以下稳定结构；`Validate` 拒绝重复平台、未知平台/架构、空资产、启用渠道但缺少 publisher，以及任何违反 source-only 默认值的配置：

```go
type Repository struct {
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
	URL   string `json:"url"`
}

type ReleaseRequirements struct {
	Source                     string `json:"source"`
	MustBeImmutable            bool   `json:"must_be_immutable"`
	AllowDraft                 bool   `json:"allow_draft"`
	AllowPrerelease            bool   `json:"allow_prerelease"`
	RequireSHA256Sums          bool   `json:"require_sha256sums"`
	RequireAssetDigest         bool   `json:"require_asset_digest"`
	RequireArtifactAttestation bool   `json:"require_artifact_attestation"`
}

type WindowsRequirements struct {
	RequireAuthenticode bool    `json:"require_authenticode"`
	RequireTimestamp    bool    `json:"require_timestamp"`
	PublisherSubject    *string `json:"publisher_subject"`
}

type PlatformAsset struct {
	OS    string `json:"os"`
	Arch  string `json:"arch"`
	Asset string `json:"asset"`
}

type PlatformInstall struct {
	ProgramPath string `json:"program_path"`
	StatePath   string `json:"state_path"`
	Service     string `json:"service"`
}

type InstallationPolicy struct {
	Scope                     string          `json:"scope"`
	AllowElevation            bool            `json:"allow_elevation"`
	AllowSilentSourceFallback bool            `json:"allow_silent_source_fallback"`
	AutomaticBackgroundUpdate bool            `json:"automatic_background_updates"`
	ListenAddress             string          `json:"listen_address"`
	Windows                   PlatformInstall `json:"windows"`
	Linux                     PlatformInstall `json:"linux"`
}

type Policy struct {
	SchemaVersion       string               `json:"schema_version"`
	CanonicalRepository Repository           `json:"canonical_repository"`
	StableTagPattern    string               `json:"stable_tag_pattern"`
	BinaryReleaseEnabled bool                `json:"binary_release_enabled"`
	Release             ReleaseRequirements  `json:"release"`
	Windows             WindowsRequirements  `json:"windows"`
	Platforms           []PlatformAsset      `json:"platforms"`
	Installation        InstallationPolicy   `json:"installation"`
}

const BinaryReleaseEnabled = false
```

根 JSON 必须使用精确资产名：

```text
codex-usage-windows-amd64.exe
codex-usage-windows-arm64.exe
codex-usage-linux-amd64
codex-usage-linux-arm64
```

安装策略记录 Windows `HKCU` 用户启动项、Linux `systemd --user`、两端当前用户路径和 `127.0.0.1`；这些是声明字符串，不是可执行脚本。

- [ ] **Step 4: 把 Release workflow 改成 fail-closed 哨兵**

仅保留 `workflow_dispatch`、`contents: read`、checkout/setup-go 和策略测试。名称明确标注 source-only；不得监听 `push.tags`，不得运行构建脚本，且不得上传 artifact 或调用 GitHub Release API。

- [ ] **Step 5: 运行 GREEN 与 package 回归**

```powershell
& $Go test ./internal/installpolicy -count=1
& $Go test ./... -run '^$' -count=1
git diff --check
```

Expected: 策略测试通过；全仓库仅编译检查通过；无 whitespace error。

- [ ] **Step 6: 提交策略门禁**

```powershell
git add install-policy.json internal/installpolicy/policy.go internal/installpolicy/policy_test.go .github/workflows/release.yml
git diff --cached --name-only
git diff --cached --check
git commit -m 'feat: 锁定源码发布策略' -m "AI-Coding: true`nAI-Agent: Codex`nAI-Model: GPT-5"
```

Expected staged files: 以上四项且只有以上四项。

### Task 2: 建立机器事件协议与完整构建身份

**Files:**
- Create: `internal/app/events.go`
- Create: `internal/app/events_test.go`
- Create: `internal/app/version.go`
- Create: `internal/app/version_test.go`
- Modify: `internal/app/app.go`
- Modify: `scripts/build.ps1`
- Modify: `scripts/build.sh`
- Modify: `internal/cliui/locale.go`
- Test: `internal/cliui/locale_test.go`

- [ ] **Step 1: 先写事件与版本 RED 测试**

```go
func TestEventEmitterWritesOneJSONObjectPerLine(t *testing.T)
func TestStructuredRunnerEmitsExactlyOneTerminalEvent(t *testing.T)
func TestMachineFieldsDoNotDependOnLocale(t *testing.T)
func TestVersionJSONHasStableBuildIdentity(t *testing.T)
func TestCurrentBuildIdentityRejectsInvalidDirtyValue(t *testing.T)
func TestBuildScriptsKeepCommitAndDirtySeparate(t *testing.T)
func TestVersionJSONRejectsUnknownFlags(t *testing.T)
func TestGlobalVersionRemainsHumanReadable(t *testing.T)
```

逐行解码并断言每个事件都有 `schema_version=1`、`event`、`phase`、`status`、RFC3339 UTC `timestamp`；终态只能是 `result` 或 `error`，且恰好一个。

- [ ] **Step 2: 运行 RED 测试**

```powershell
& $Go test ./internal/app -run 'Test(EventEmitter|StructuredRunner|MachineFields|VersionJSON|CurrentBuildIdentity|BuildScripts|GlobalVersion)' -count=1
```

Expected: 新 API/行为不存在而失败。

- [ ] **Step 3: 实现线程安全 emitter 与统一 runner**

核心类型保持小而显式：

```go
type machineEvent struct {
	SchemaVersion string         `json:"schema_version"`
	Event         string         `json:"event"`
	Phase         string         `json:"phase"`
	Status        string         `json:"status"`
	Timestamp     string         `json:"timestamp"`
	Code          string         `json:"code,omitempty"`
	Message       string         `json:"message,omitempty"`
	Progress      any            `json:"progress,omitempty"`
	Result        any            `json:"result,omitempty"`
}

type verificationStatus string
const (
	verificationVerified      verificationStatus = "verified"
	verificationNotApplicable verificationStatus = "not_applicable"
	verificationNotChecked    verificationStatus = "not_checked"
)
```

`runStructured` 负责检测命令级 `--json`、设置 flag 输出位置、调用 handler、把 typed `codedError` 转成稳定 code/exit code，并用 mutex + terminal guard 保证唯一终态。JSON 模式 stderr 默认保持空，stdout 只由 emitter 写入；现有非结构化命令保持原分派行为。

跨任务不得自行改造的精确接口为：

```go
type commandResult struct {
	Code string
	Data any
}

type codedError struct {
	Code     string
	ExitCode int
	Err      error
	Details  any
}

func (e *codedError) Error() string { return e.Err.Error() }
func (e *codedError) Unwrap() error { return e.Err }

type structuredHandler func(args []string, emitter *eventEmitter) (commandResult, error)

func newEventEmitter(w io.Writer, enabled bool, now func() time.Time) *eventEmitter
func (c CLI) runStructured(phase string, args []string, handler structuredHandler) int
func (e *eventEmitter) Progress(phase, status, code, message string, progress any) error
func (e *eventEmitter) finish(event, phase, status, code, message string, result any) error
```

`runStructured` 是唯一调用 `finish` 的位置；handler 返回成功 `commandResult` 或 `codedError`。所有其他 error 统一映射为 `internal_error`/exit 1；flag/用法错误使用 exit 2。测试用固定 clock 构造 emitter，生产用 `time.Now().UTC`。

`CLI` 在现有 stdout/stderr/locale 基础上新增 `Stdin io.Reader`、`Now func() time.Time` 和 `HeartbeatInterval time.Duration`；零值分别回落到 `os.Stdin`、`time.Now` 和四秒。后续命令测试只能通过这些字段缩短心跳或注入输入，不修改全局时间变量。

- [ ] **Step 4: 实现 `version --json`**

`version` handler 必须解析参数，不再静默忽略 `--json` 以外的 flag；结果为：

```go
type buildIdentity struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Dirty     bool   `json:"dirty"`
	BuildDate string `json:"build_date"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}
```

保留顶层 `--version` 的一行人类文本兼容。两套构建脚本改用完整 `git rev-parse HEAD`；`commit` 保持纯 40 位 SHA，工作树状态通过独立 `dirty` 布尔值和 ldflag 传递，禁止再把 `-dirty` 拼到 SHA。无 Git metadata 的手工构建可以明确返回 `commit=dev, dirty=true`，但未来 trusted Release 必须是 40 位 SHA 且 `dirty=false`。ldflags 路径继续兼容当前 module。

Go `-X` 只设置字符串，因此保留现有 `Version/Commit/BuildDate` 字符串变量，并新增 `BuildDirty = "true"` 字符串变量。`currentBuildIdentity()` 使用 `strconv.ParseBool(BuildDirty)` 严格转为 DTO 的 bool；非法值返回 `build_metadata_invalid`，不能默认为干净构建。脚本只传 `-X ...BuildDirty=true` 或 `false`。

PowerShell 脚本新增显式 `[switch]$SkipTests`，Bash 脚本支持 `SKIP_TESTS=1`；默认仍先执行全部 Go 测试，只有调用方已经在同一提交上取得独立测试证据时才允许跳过。该开关只避免 Smart App Control 让最终四目标纯构建重复触发临时 test EXE，不改变 CI 的测试门禁。

- [ ] **Step 5: 补齐双语帮助并运行 GREEN**

```powershell
& $Go test ./internal/app -run 'Test(EventEmitter|StructuredRunner|MachineFields|VersionJSON|CurrentBuildIdentity|BuildScripts|GlobalVersion)' -count=1
& $Go test ./internal/cliui -count=1
& $Go vet ./internal/app ./internal/cliui
git diff --check
```

- [ ] **Step 6: 提交机器协议基础**

```powershell
git add internal/app/events.go internal/app/events_test.go internal/app/version.go internal/app/version_test.go internal/app/app.go internal/cliui/locale.go scripts/build.ps1 scripts/build.sh
git diff --cached --check
git commit -m 'feat: 增加稳定机器输出协议' -m "AI-Coding: true`nAI-Agent: Codex`nAI-Model: GPT-5"
```

### Task 3: 增加安装记录与幂等决策矩阵

**Files:**
- Create: `internal/install/state.go`
- Create: `internal/install/state_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: 先写安装状态 RED 测试**

```go
func TestAssessFreshInstall(t *testing.T)
func TestAssessSameBinaryIsIdempotent(t *testing.T)
func TestAssessTrustedOlderVersionIsUpgrade(t *testing.T)
func TestAssessRejectsDowngrade(t *testing.T)
func TestAssessRejectsUnrecordedExistingExecutable(t *testing.T)
func TestAssessRejectsDigestMismatch(t *testing.T)
func TestInstallRecordRoundTripIsAtomic(t *testing.T)
func TestResolvePathsIncludesInstallRecord(t *testing.T)
```

所有文件测试使用 `t.TempDir()`；不得执行目标 EXE 来判断它是否可信。

- [ ] **Step 2: 运行 RED 测试**

```powershell
& $Go test ./internal/install ./internal/config -run 'Test(Assess|InstallRecord|ResolvePathsIncludes)' -count=1
```

- [ ] **Step 3: 实现最小安装记录**

```go
type Record struct {
	SchemaVersion    string `json:"schema_version"`
	Product          string `json:"product"`
	Version          string `json:"version"`
	Commit           string `json:"commit"`
	Dirty            bool   `json:"dirty"`
	BuildDate        string `json:"build_date"`
	OS               string `json:"os"`
	Arch             string `json:"arch"`
	ExecutablePath   string `json:"executable_path"`
	ExecutableSHA256 string `json:"executable_sha256"`
	Source           string `json:"source"`
	InstalledAt      string `json:"installed_at"`
}
```

`source` 只允许 `source_build` 或未来的 `trusted_release`。实现 SHA256 流式计算、0600 原子保存、严格 `major.minor.patch` 数字比较，以及 `fresh/same/upgrade/downgrade/untrusted` 决策。已有 EXE 没有记录或记录 digest 与磁盘不一致时一律 `untrusted`；相同版本只有候选 digest 与已安装 digest 完全相同才是 `same`。

`config.Paths` 新增 `InstallRecord`，精确为状态目录下 `install.json`；专用目录白名单加入该文件。

- [ ] **Step 4: 运行 GREEN 与完整 package 回归**

```powershell
& $Go test ./internal/install ./internal/config -count=1
& $Go vet ./internal/install ./internal/config
git diff --check
```

- [ ] **Step 5: 提交安装状态模型**

```powershell
git add internal/install/state.go internal/install/state_test.go internal/config/config.go internal/config/config_test.go
git diff --cached --check
git commit -m 'feat: 记录可信安装状态' -m "AI-Coding: true`nAI-Agent: Codex`nAI-Model: GPT-5"
```

### Task 4: 为扫描器增加单调进度快照

**Files:**
- Create: `internal/usage/progress.go`
- Create: `internal/usage/progress_test.go`
- Modify: `internal/usage/scanner.go`
- Test: `internal/usage/scanner_test.go`

- [ ] **Step 1: 写 observer RED 测试**

```go
func TestScannerObserverReportsDiscoveredAndProcessedFiles(t *testing.T)
func TestScannerObserverProgressIsMonotonic(t *testing.T)
func TestScannerObserverReportsInsertedEventsAndWarnings(t *testing.T)
func TestScanWithoutObserverRemainsCompatible(t *testing.T)
```

进度结构精确包含 `homes_total`、`homes_discovered`、`files_discovered`、`files_processed`、`records_processed`、`events_inserted`、`warnings`。

```go
type ScanProgress struct {
	HomesTotal      int   `json:"homes_total"`
	HomesDiscovered int   `json:"homes_discovered"`
	FilesDiscovered int64 `json:"files_discovered"`
	FilesProcessed  int64 `json:"files_processed"`
	RecordsProcessed int64 `json:"records_processed"`
	EventsInserted  int64 `json:"events_inserted"`
	Warnings        int64 `json:"warnings"`
}

type ProgressObserver func(ScanProgress)
```

- [ ] **Step 2: 运行 RED 测试**

```powershell
& $Go test ./internal/usage -run 'TestScannerObserver|TestScanWithoutObserver' -count=1
```

- [ ] **Step 3: 实现 `ScanWithProgress` 并保留 API**

```go
type ProgressObserver func(ScanProgress)

func (s *Scanner) Scan(ctx context.Context, homes []string, rebuild bool) (ScanResult, error) {
	return s.ScanWithProgress(ctx, homes, rebuild, nil)
}
```

在每个 home 发现完成、每个可处理 JSONL 计入发现、每个文件处理完成和终态更新快照；observer 在 scanner 锁内同步调用，只传值副本，不能回调 scanner。不要改变现有 Token、fork replay、去重或 warning 语义。

- [ ] **Step 4: 运行 GREEN 与 usage 回归**

```powershell
& $Go test ./internal/usage -run 'TestScannerObserver|TestScanWithoutObserver' -count=1
& $Go test ./internal/usage -count=1
& $Go vet ./internal/usage
git diff --check
```

- [ ] **Step 5: 提交扫描进度**

```powershell
git add internal/usage/progress.go internal/usage/progress_test.go internal/usage/scanner.go
git diff --cached --check
git commit -m 'feat: 暴露扫描进度快照' -m "AI-Coding: true`nAI-Agent: Codex`nAI-Model: GPT-5"
```

### Task 5: 让 scan 具备 JSON Lines 与四秒心跳

**Files:**
- Create: `internal/app/progress.go`
- Create: `internal/app/progress_test.go`
- Create: `internal/app/scan.go`
- Create: `internal/app/scan_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/cliui/locale.go`

- [ ] **Step 1: 先写心跳与 scan RED 测试**

```go
func TestHeartbeatRepeatsLatestSnapshotWhileWorkBlocks(t *testing.T)
func TestHeartbeatStopsWithoutWritingAfterCompletion(t *testing.T)
func TestScanJSONEmitsProgressAndSingleTerminalResult(t *testing.T)
func TestScanJSONFailureUsesStableCode(t *testing.T)
func TestScanHumanOutputRemainsLocalized(t *testing.T)
```

测试通过注入 10ms 间隔和阻塞 scanner 验证心跳，不等待生产四秒。

- [ ] **Step 2: 运行 RED 测试**

```powershell
& $Go test ./internal/app -run 'Test(Heartbeat|ScanJSON|ScanHuman)' -count=1
```

- [ ] **Step 3: 实现通用进度 tracker**

app 层 tracker 用 mutex 保存最后快照，独立 ticker 每四秒输出一次；工作开始后立即发阶段事件，结束时先停 ticker、等待 goroutine 退出，再交给统一 runner 发终态。即使 `DiscoverHome` 或单个 JSONL 超过四秒，心跳也必须继续输出完整计数字段。

- [ ] **Step 4: 把 scan handler 移出 `app.go`**

保留 `--rebuild`；`--json` 输出事件序列而非缩进对象，最终 `result.scan` 继续复用 `usage.ScanResult`。人类模式显示发现、处理、事件和 warning 计数；现有非 JSON 行为与退出语义保持兼容。

- [ ] **Step 5: 运行 GREEN 与回归**

```powershell
& $Go test ./internal/app -run 'Test(Heartbeat|ScanJSON|ScanHuman)' -count=1
& $Go test ./internal/app ./internal/usage -count=1
& $Go test ./internal/cliui -count=1
& $Go vet ./internal/app ./internal/usage
git diff --check
```

- [ ] **Step 6: 提交 CLI 扫描进度**

```powershell
git add internal/app/progress.go internal/app/progress_test.go internal/app/scan.go internal/app/scan_test.go internal/app/app.go internal/cliui/locale.go
git diff --cached --check
git commit -m 'feat: 持续报告扫描进度' -m "AI-Coding: true`nAI-Agent: Codex`nAI-Model: GPT-5"
```

### Task 6: 用完整构建身份验证服务与 doctor

**Files:**
- Create: `internal/app/doctor.go`
- Create: `internal/app/doctor_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`
- Modify: `internal/cliui/locale.go`

- [ ] **Step 1: 写 health/doctor RED 测试**

```go
func TestHealthzReturnsCompleteBuildIdentity(t *testing.T)
func TestHealthProbeRejectsVersionOrCommitMismatch(t *testing.T)
func TestDoctorJSONUsesStableSnakeCaseFields(t *testing.T)
func TestDoctorJSONIsLocaleIndependent(t *testing.T)
func TestDoctorJSONErrorCheckReturnsNonZero(t *testing.T)
```

doctor 测试注入临时 state、fake health endpoint 和 homes；不得使用真实后台服务。

- [ ] **Step 2: 运行 RED 测试**

```powershell
& $Go test ./internal/server ./internal/app -run 'Test(Healthz|HealthProbe|DoctorJSON)' -count=1
```

- [ ] **Step 3: 扩展 `/healthz` 身份**

返回 `ok`、`version`、纯 commit、`dirty`、`build_date`、`os`、`arch`。app 的 identity probe 必须解析 JSON 并精确匹配预期；只看到 `ok=true` 不再足够。trusted Release 身份只接受 40 位 commit 且 `dirty=false`。

- [ ] **Step 4: 抽取 typed doctor report**

report 使用显式 JSON tag 的路径 DTO，check 的稳定字段为 `level/name/code`；自然语言 detail 只能是可选说明。总体 `ok/warning/error` 由 check 计算，任何 `error` 使命令返回非零并由统一 runner 输出 `health_check_failed`。移除 JSON 模式强制切换中文的逻辑。

DTO 固定为：

```go
type doctorCheck struct {
	Level  string `json:"level"`
	Name   string `json:"name"`
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

type doctorPaths struct {
	StateDir     string `json:"state_dir"`
	ConfigPath   string `json:"config_path"`
	DatabasePath string `json:"database_path"`
	InstallDir   string `json:"install_dir"`
	Executable   string `json:"executable"`
}

type doctorReport struct {
	Status string        `json:"status"`
	Checks []doctorCheck `json:"checks"`
	Paths  doctorPaths   `json:"paths"`
	Homes  []string      `json:"homes"`
}
```

- [ ] **Step 5: 运行 GREEN 和服务器回归**

```powershell
& $Go test ./internal/server ./internal/app -run 'Test(Healthz|HealthProbe|DoctorJSON)' -count=1
& $Go test ./internal/server ./internal/app ./internal/cliui -count=1
& $Go vet ./internal/server ./internal/app
git diff --check
```

- [ ] **Step 6: 提交身份健康检查**

```powershell
git add internal/app/doctor.go internal/app/doctor_test.go internal/app/app.go internal/server/server.go internal/server/server_test.go internal/cliui/locale.go
git diff --cached --check
git commit -m 'feat: 验证服务构建身份' -m "AI-Coding: true`nAI-Agent: Codex`nAI-Model: GPT-5"
```

### Task 7: 实现事务化安装与失败回滚

**Files:**
- Create: `internal/app/lifecycle.go`
- Create: `internal/app/lifecycle_test.go`
- Modify: `internal/config/migration.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/platform/platform.go`
- Modify: `internal/platform/platform_windows.go`
- Modify: `internal/platform/platform_linux.go`
- Test: `internal/platform/platform_test.go`
- Test: `internal/platform/platform_windows_test.go`

- [ ] **Step 1: 用 fake service 写事务 RED 测试**

```go
func TestLifecycleFreshInstallCommitsAfterIdentityHealth(t *testing.T)
func TestLifecycleSameBinaryRepairsServiceWithoutReplacing(t *testing.T)
func TestLifecycleUpgradeUsesStrictOrder(t *testing.T)
func TestLifecycleCopyFailureLeavesOldInstallUntouched(t *testing.T)
func TestLifecycleFatalPostActivateScanRestoresOldInstall(t *testing.T)
func TestLifecycleMigrationFailureRestoresPreviousStateAndService(t *testing.T)
func TestLifecycleHealthFailureRollsBackPreviousStateMigration(t *testing.T)
func TestLifecycleServiceFailureRestoresOldExecutableAndRecord(t *testing.T)
func TestLifecycleHealthMismatchRestoresOldService(t *testing.T)
func TestLifecycleRejectsDowngradeBeforeStoppingService(t *testing.T)
func TestLifecycleRejectsUntrustedExistingInstall(t *testing.T)
```

严格顺序为：

```text
validate current + inspect previous state → stage candidate
→ stop current service + suspend previous service → begin reversible migration
→ backup old → activate candidate → post-activate scan → install service
→ expected identity health → save new record → commit migration
→ remove previous program/persistence → remove backup
```

- [ ] **Step 2: 运行 RED 测试**

```powershell
& $Go test ./internal/app -run 'TestLifecycle' -count=1
```

- [ ] **Step 3: 实现最小事务和注入缝**

先把现有 `MigratePreviousState` 拆成只读 `InspectPreviousState` 与可回滚事务。只读检查验证 marker、目录边界、数据库冲突并枚举源/目标，不移动或删除任何文件。`BeginPreviousStateMigration` 只在当前/旧服务都停止后执行移动，记录每一对源/目标；开始过程自身失败要逆序恢复。事务在新身份健康且新安装记录保存前不得删除旧 marker、launcher、unit、启动项、旧 EXE 或旧状态目录：

```go
type migrationMove struct {
	source string
	target string
}

type MigrationPlan struct {
	paths    Paths
	previous PreviousPaths
	result   MigrationResult
	moves    []migrationMove
}

type MigrationTransaction struct {
	plan      MigrationPlan
	completed []migrationMove
	committed bool
}

func InspectPreviousState(paths Paths, previous PreviousPaths) (MigrationPlan, MigrationResult, error)
func BeginPreviousStateMigration(plan MigrationPlan) (*MigrationTransaction, error)
func (m *MigrationTransaction) Rollback() error
func (m *MigrationTransaction) Commit() (MigrationResult, error)
```

`Commit` 只在记录保存成功后清理迁移 marker/空旧目录；旧服务持久化和旧 EXE 由 platform commit cleanup 处理。`Rollback` 在任何扫描硬错误、启动、健康或记录保存失败时逆序把 database sidecars、config、backups 和 log 移回旧路径。

平台旧服务同样分为 `SuspendPreviousService`、`ResumePreviousService` 和 `RemovePreviousService`：Windows suspend 只停止进程但保留 HKCU entry/launcher，Linux suspend 只 stop 而不 disable/remove unit；回滚直接恢复旧服务，成功提交后才移除持久化和旧 EXE。

`lifecycleOps` 只注入 service stop/install/uninstall、previous service suspend/resume/remove、identity health、post-activate scan 和 clock。文件替换在目标目录创建暂存文件并 sync；旧 EXE 先 rename 为唯一备份，再激活新文件。备份已存在时拒绝继续，不能删除来源不明的恢复点。

Task 8 必须直接复用以下接口，不得再创建第二套安装状态机：

```go
type installScanOutcome struct {
	Result   usage.ScanResult
	Warnings []string
}

type lifecycleRequest struct {
	CandidatePath     string
	DestinationPath   string
	InstallRecordPath string
	StateDir          string
	ServiceURL        string
	Candidate         buildIdentity
	Source            string
	SkipScan          bool
	Migration         config.MigrationPlan
	PreviousService   platform.PreviousService
}

type lifecycleResult struct {
	Decision        install.Decision
	CandidateSHA256 string
	Service         platform.ServiceResult
	Scan            usage.ScanResult
	ScanWarnings    []string
	DataPreserved   bool
}

type lifecycleOps struct {
	StopService      func(executable, stateDir string) error
	InstallService   func(executable, stateDir string) (platform.ServiceResult, error)
	UninstallService func(executable, stateDir string) error
	SuspendPrevious  func(platform.PreviousService) error
	ResumePrevious   func(platform.PreviousService) error
	RemovePrevious   func(platform.PreviousService) error
	ProbeIdentity    func(context.Context, string, buildIdentity) error
	Scan             func(context.Context, usage.ProgressObserver) (installScanOutcome, error)
	Now              func() time.Time
}

type lifecycleProgress func(phase string, progress any)

func executeLifecycle(
	ctx context.Context,
	request lifecycleRequest,
	ops lifecycleOps,
	report lifecycleProgress,
) (lifecycleResult, error)
```

文件 stage/backup/restore、`install.Load/Save/Assess` 和 SHA256 使用真实标准库实现，不放入 `lifecycleOps`；测试以 `t.TempDir()` 验证实际字节和原子替换，只有 service/health/scan/clock 使用 fake。

post-activate scan 返回两类结果：坏记录、待重建等可恢复扫描问题进入 warning 并继续；无法打开状态库、配置或执行扫描的硬错误返回 error 并触发完整回滚。任一扫描硬错误、启动、健康或记录保存失败时：停止新服务、移开/移除新 EXE、恢复旧 EXE、恢复旧记录、回滚旧状态迁移并 resume 旧服务；回滚失败同时保留原始错误与 rollback detail。fresh install 失败不得留下“成功”记录。相同 digest 只修复 service/health，不替换 EXE，也不重复首扫。健康/记录提交后的旧持久化清理失败作为 warning 保留，不破坏已经健康的新安装。

- [ ] **Step 4: 调整平台结果为稳定枚举**

服务结果区分 `persistent` 与 `detached_fallback`；卸载结果区分 `removed` 与 Windows `scheduled`。Windows 覆盖 HKCU Run 前核对目标是规范安装路径；移除任何“请用管理员身份运行”的建议，改为 `permission_required` 并停止。Linux 保持 `systemd --user` 和现有 fallback，不新增 sudo。

- [ ] **Step 5: 运行 GREEN 与平台回归**

```powershell
& $Go test ./internal/app -run 'TestLifecycle' -count=1
& $Go test ./internal/platform -count=1
& $Go vet ./internal/app ./internal/platform
git diff --check
```

- [ ] **Step 6: 提交安装事务**

```powershell
git add internal/app/lifecycle.go internal/app/lifecycle_test.go internal/config/migration.go internal/config/config_test.go internal/platform/platform.go internal/platform/platform_windows.go internal/platform/platform_linux.go internal/platform/platform_test.go internal/platform/platform_windows_test.go
git diff --cached --check
git commit -m 'feat: 增加安装失败回滚' -m "AI-Coding: true`nAI-Agent: Codex`nAI-Model: GPT-5"
```

### Task 8: 接入 install 的确认、幂等、进度与回执

**Files:**
- Create: `internal/app/install.go`
- Create: `internal/app/install_test.go`
- Create: `internal/app/test_helpers_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_locale_test.go`
- Modify: `internal/cliui/locale.go`

- [ ] **Step 1: 写 install 命令 RED 测试**

```go
func TestInstallJSONRequiresExplicitConfirmation(t *testing.T)
func TestInstallJSONEmitsOrderedPhasesAndReceipt(t *testing.T)
func TestInstallJSONHeartbeatWhileScanBlocks(t *testing.T)
func TestInstallJSONSameBinaryIsIdempotent(t *testing.T)
func TestInstallPreflightRejectsForeignLoopbackService(t *testing.T)
func TestInstallPreflightReportsPermissionRequired(t *testing.T)
func TestInstallMigrationConflictStopsBeforeReplacement(t *testing.T)
func TestInstallJSONFailureEmitsStableTerminalCode(t *testing.T)
func TestInstallJSONStdoutContainsNoHumanProse(t *testing.T)
func TestHumanInstallPromptsAndRemainsLocalized(t *testing.T)
```

泛化测试 helper：捕获 stdin/stdout/stderr、逐行 JSON 解码、终态计数、fake lifecycle/scan；不得触碰真实 HKCU/systemd。

命令层固定使用以下 DTO/依赖缝；测试只替换这五个函数，生产默认值调用现有 config/migration 与 Task 7 lifecycle：

```go
type verificationResult struct {
	Name   string             `json:"name"`
	Status verificationStatus `json:"status"`
	Detail string             `json:"detail,omitempty"`
}

type installReceipt struct {
	Identity          buildIdentity         `json:"identity"`
	InstallPath       string                `json:"install_path"`
	StatePath         string                `json:"state_path"`
	DatabasePath      string                `json:"database_path"`
	ServiceMode       string                `json:"service_mode"`
	DashboardURL      string                `json:"dashboard_url"`
	Scan              usage.ScanResult      `json:"scan"`
	ScanWarnings      []string              `json:"scan_warnings,omitempty"`
	DataPreserved     bool                  `json:"data_preserved"`
	Verifications     []verificationResult  `json:"verifications"`
	UninstallCommand  string                `json:"uninstall_command"`
	PurgeCommand      string                `json:"purge_command"`
}

type installCommandDeps struct {
	ResolvePaths      func() (config.Paths, error)
	Executable        func() (string, error)
	PreflightPort     func(context.Context, string, *install.Record) error
	InspectPrevious   func(config.Paths) (config.MigrationPlan, config.MigrationResult, platform.PreviousService, error)
	RunLifecycle      func(context.Context, lifecycleRequest, lifecycleOps, lifecycleProgress) (lifecycleResult, error)
}
```

- [ ] **Step 2: 运行 RED 测试**

```powershell
& $Go test ./internal/app -run 'Test(InstallJSON|InstallPreflight|InstallMigration|HumanInstall)' -count=1
```

- [ ] **Step 3: 移出并重组 install handler**

新增 `--yes`、`--json`，保留 `--skip-scan`。JSON 模式缺少 `--yes` 时不读 stdin，输出 `confirmation_required` 终态和预检路径后返回非零；人类模式展示 repository/source、安装目录、状态目录、用户服务、loopback、扫描范围与默认保留数据语义，再从 `CLI.Stdin` 读取一次确认。预检先验证安装/状态目录可写、使用 Task 7 `InspectPreviousState` 只读确认旧版迁移无数据库冲突，以及目标端口空闲或正由安装记录对应的旧服务占用；未知 loopback 服务必须在停止服务或替换文件前返回 `existing_install_untrusted`。预检只把 `MigrationPlan` 和 `PreviousService` 交给 lifecycle，绝不直接调用迁移或停止旧服务。

流程发出：`preflight → stop_service → install → scan → start_service → health_check → complete`。这些阶段直接映射 Task 7 的同一事务和 post-activate scan hook，handler 不在事务之外重复扫描或启动服务。下载/Release 验证不属于 CLI Phase A，不得伪造 `download` 或 `signature verified`。

- [ ] **Step 4: 生成诚实的安装回执**

最终 result 至少包含 build identity、绝对安装/状态/数据库路径、服务模式、Dashboard URL、扫描摘要、`data_preserved`、卸载命令和验证明细。source build 的 Release immutable、Attestation、Authenticode 项必须为 `not_applicable`；本地 candidate/copy digest 确实匹配时才能把该项写成 `verified`。

扫描和 30 秒健康等待都由 Task 5 心跳包装，生产最大静默间隔四秒。扫描 warning 可完成安装但必须进入回执；identity health 失败必须触发 Task 7 回滚并返回 `health_check_failed`。

- [ ] **Step 5: 运行 GREEN 和 app/locale 回归**

```powershell
& $Go test ./internal/app -run 'Test(InstallJSON|InstallPreflight|InstallMigration|HumanInstall)' -count=1
& $Go test ./internal/app ./internal/cliui -count=1
& $Go vet ./internal/app
git diff --check
```

- [ ] **Step 6: 提交安装命令**

```powershell
git add internal/app/install.go internal/app/install_test.go internal/app/test_helpers_test.go internal/app/app.go internal/app/app_locale_test.go internal/cliui/locale.go
git diff --cached --check
git commit -m 'feat: 提供 AI 可驱动安装回执' -m "AI-Coding: true`nAI-Agent: Codex`nAI-Model: GPT-5"
```

### Task 9: 补齐显式 update 与安全卸载语义

**Files:**
- Create: `internal/app/update.go`
- Create: `internal/app/update_test.go`
- Create: `internal/app/uninstall.go`
- Create: `internal/app/uninstall_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/cliui/locale.go`
- Modify: `internal/platform/platform.go`
- Modify: `internal/platform/platform_windows.go`
- Modify: `internal/platform/platform_linux.go`

- [ ] **Step 1: 写 update/uninstall RED 测试**

```go
func TestUpdateCheckReturnsReleaseChannelDisabledWithoutNetwork(t *testing.T)
func TestUpdateYesReturnsReleaseChannelDisabledWithoutModification(t *testing.T)
func TestUninstallJSONRequiresConfirmation(t *testing.T)
func TestUninstallKeepsDatabaseAndConfig(t *testing.T)
func TestUninstallPurgeWithoutYesOnlyReportsAbsoluteTarget(t *testing.T)
func TestUninstallPurgeYesUsesValidatedStateDirectory(t *testing.T)
func TestWindowsUninstallReceiptReportsScheduledRemoval(t *testing.T)
```

稳定结果 DTO 为：

```go
type updateReceipt struct {
	ChannelEnabled bool   `json:"channel_enabled"`
	Checked        bool   `json:"checked"`
	Modified       bool   `json:"modified"`
	PolicyPath     string `json:"policy_path"`
}

type uninstallReceipt struct {
	InstallPath      string `json:"install_path"`
	StatePath        string `json:"state_path"`
	DatabasePath     string `json:"database_path"`
	ProgramRemoved   bool   `json:"program_removed"`
	RemovalScheduled bool   `json:"removal_scheduled"`
	DataPreserved    bool   `json:"data_preserved"`
	Purged           bool   `json:"purged"`
}
```

渠道关闭时 `updateReceipt` 可放在 `codedError.Details`，但终态仍是唯一 `error` 事件，code 固定 `release_channel_disabled`。

- [ ] **Step 2: 运行 RED 测试**

```powershell
& $Go test ./internal/app -run 'Test(Update|Uninstall)' -count=1
```

- [ ] **Step 3: 实现 Phase A update 契约**

支持 `update --check --json` 和 `update --yes --json`。两者读取编译期关闭状态，稳定返回 `release_channel_disabled`、非零退出，不创建 HTTP client、不下载、不写文件、不调用 build，也不建议自动源码降级。人类文本解释当前 source-only 和 `INSTALL.md` 路径。

- [ ] **Step 4: 实现卸载确认与准确回执**

普通 `uninstall` 默认保留数据库/config；`--purge` 必须先输出规范状态目录绝对路径。JSON 模式无 `--yes` 返回 `confirmation_required` 且零副作用；人类模式从 stdin 明确确认。`--purge --yes` 继续复用 marker/root/home 防护。

机器调用的规范形式固定为 `codex-usage uninstall --yes --json` 和 `codex-usage uninstall --purge --yes --json`；flag 顺序可以由 Go flag parser 接受，但文档与测试统一使用这两种形式。

正常卸载成功后移除安装记录，保留统计数据。Windows 异步 self-delete 回执为 `removal_scheduled=true`，不能声称路径已经消失；Linux 同步完成则为 false。任何路径验证失败必须在调用删除操作前停止。

- [ ] **Step 5: 运行 GREEN、purge 与平台回归**

```powershell
& $Go test ./internal/app -run 'Test(Update|Uninstall)' -count=1
& $Go test ./internal/platform -run 'Test(ValidatePurge|Uninstall|RemoveInstalled)' -count=1
& $Go test ./internal/app ./internal/platform ./internal/installpolicy -count=1
& $Go vet ./internal/app ./internal/platform
git diff --check
```

- [ ] **Step 6: 提交 lifecycle 命令**

```powershell
git add internal/app/update.go internal/app/update_test.go internal/app/uninstall.go internal/app/uninstall_test.go internal/app/app.go internal/cliui/locale.go internal/platform/platform.go internal/platform/platform_windows.go internal/platform/platform_linux.go
git diff --cached --check
git commit -m 'feat: 明确更新与安全卸载语义' -m "AI-Coding: true`nAI-Agent: Codex`nAI-Model: GPT-5"
```

### Task 10: 写同构 AI/人工安装与签名政策文档

**Files:**
- Create: `INSTALL.md`
- Create: `INSTALL.en.md`
- Create: `CODE_SIGNING.md`
- Create: `CODE_SIGNING.en.md`
- Modify: `README.md`
- Modify: `README.en.md`
- Modify: `CONTRIBUTING.md`
- Modify: `SECURITY.md`
- Modify: `internal/installpolicy/policy_test.go`

- [ ] **Step 1: 先扩展失败的文档契约测试**

增加：

```go
func TestInstallationDocumentsMatchPolicyState(t *testing.T)
func TestInstallationDocumentsDoNotOfferUnsafePipesOrBinaryDownloads(t *testing.T)
func TestBilingualInstallationDocumentsHaveMatchingHeadings(t *testing.T)
```

断言 README 同等可见地链接对应 INSTALL 和 `install-policy.json`；两份 INSTALL 顶部互链、标题层级同构；所有文档明确 source-only；不得出现上游/本 Fork binary download、`curl | sh` 或 `irm | iex`。

- [ ] **Step 2: 运行 RED 测试**

```powershell
& $Go test ./internal/installpolicy -run 'TestInstallationDocuments' -count=1
```

- [ ] **Step 3: 写 INSTALL 中文/英文同构内容**

两份文档按相同顺序覆盖：当前状态、让 AI 安装、人工安装、一次确认清单、Go 1.26+ 源码构建、`install --yes --json`、进度/回执、doctor、update 渠道关闭、默认卸载、purge、路径、网络/隐私、Smart App Control 失败处理和禁止绕过。

AI 流程必须明确：只接受 `https://github.com/edisoncccc/codex-usage`，先读 `install-policy.json`；当前渠道关闭时说明 Go 构建前提并取得确认；预编译验证失败不得自动改走源码。人工流程调用同一个 CLI，不提供远程管道脚本。

- [ ] **Step 4: 写代码签名政策**

中英文同构说明：当前未获 SignPath 批准、无 publisher Subject、无 binary Release；未来由 GitHub Actions 构建、SignPath Foundation 管理私钥/签名、维护者复核变更、发布审批者核对版本说明/签名/哈希/Attestation；签名后重新算 SHA256；失败保持 source-only。不得写任何虚构身份、ID 或“已签名”表述。

- [ ] **Step 5: 精简 README 安装段并同步贡献/安全文档**

README 保留产品首页、合成媒体、Fork/MIT/隐私/费用真相；把冗长源码步骤移到 INSTALL，只留下同等醒目的“让 AI 安装”和“手动安装”入口，以及醒目的 source-only 状态。更新常用命令为新 CLI。CONTRIBUTING 增加安装策略/双语契约测试；SECURITY 增加可信发布门禁和签名问题私密报告边界。

- [ ] **Step 6: 运行 GREEN 和链接检查**

```powershell
& $Go test ./internal/installpolicy -count=1
$required = @('INSTALL.md','INSTALL.en.md','CODE_SIGNING.md','CODE_SIGNING.en.md','install-policy.json','LICENSE','THIRD_PARTY_NOTICES.md')
$required | ForEach-Object { if (-not (Test-Path -LiteralPath $_)) { throw "missing $_" } }
rg -n 'releases/latest/download|zJay26/codex-usage/releases|curl\s+.*\|\s*(sh|bash)|irm\s+.*\|\s*iex' README.md README.en.md INSTALL.md INSTALL.en.md
git diff --check
```

Expected: 测试通过；文件齐全；安全扫描无匹配；中英文链接和结构等价。

- [ ] **Step 7: 提交安装文档**

```powershell
git add INSTALL.md INSTALL.en.md CODE_SIGNING.md CODE_SIGNING.en.md README.md README.en.md CONTRIBUTING.md SECURITY.md internal/installpolicy/policy_test.go
git diff --cached --check
git commit -m 'docs: 增加 AI 与人工安装指南' -m "AI-Coding: true`nAI-Agent: Codex`nAI-Model: GPT-5"
```

### Task 11: 增加仅在一次性 runner 执行的平台生命周期验收

**Files:**
- Create: `tests/install-windows.ps1`
- Create: `tests/install-linux.sh`
- Create: `internal/app/lifecycle_platform_test.go`
- Modify: `.github/workflows/ci.yml`
- Modify: `internal/installpolicy/policy_test.go`

- [ ] **Step 1: 先写真实文件系统回滚 RED 测试**

新增 `TestLifecycleUpgradeAndRollbackOnHostFilesystem`：在 `t.TempDir()` 中使用当前 OS 的真实 copy/rename/sync 权限语义和 fake service/health，依次安装旧身份、升级候选、注入 post-activate scan/health 失败，并断言旧 EXE、旧 digest、旧记录和旧服务全部恢复。该测试由 Windows/Ubuntu unit matrix 同时执行，不触碰真实启动项。

- [ ] **Step 2: 运行平台文件事务 RED 测试**

```powershell
& $Go test ./internal/app -run 'TestLifecycleUpgradeAndRollbackOnHostFilesystem' -count=1
```

Expected: 新平台事务测试因缺少行为或 fixture 而失败。

- [ ] **Step 3: 定义只允许 GitHub 托管 runner 执行的真实服务脚本**

两个脚本第一段必须同时验证 `GITHUB_ACTIONS=true`、`CI=true`、`RUNNER_ENVIRONMENT=github-hosted` 和非空 `RUNNER_TEMP`；任何条件不满足都在创建目录、构建、注册服务或启动进程之前拒绝运行。不得把脚本作为本地或 self-hosted runner 验收命令。

runner 内设置独立的 `CODEX_USAGE_HOME`、`CODEX_HOME`、`XDG_DATA_HOME` 和 `XDG_CONFIG_HOME`（Windows 只使用适用项），所有值都位于 `RUNNER_TEMP`；生成的 Codex fixture 只含合成 metadata/token 记录。禁止读取默认用户 Codex Home。Windows 固定 HKCU Run 和 Linux 固定 user unit 只允许在一次性 runner 账户中使用，正常流程不请求管理员或 sudo。

脚本构建旧/新两个本地身份并执行以下可判定场景：

```text
1. 全新安装旧身份 → doctor 身份一致
2. 同一 binary 重复安装 → digest/记录不变且服务健康
3. 停止测试服务 → 重复安装修复并重启服务
4. 安装更高版本候选 → 数据库/config 保留且身份升级
5. scan --json 处理合成 JSONL → 至少有 progress 和唯一终态
6. 默认卸载 → 数据库/config 保留
7. 若回执为 scheduled，轮询规范 program path 直到消失；超时即失败
8. 重新安装 → uninstall --purge --yes --json
9. Windows 再等待 scheduled self-delete/state purge；Linux 验证同步完成
```

逐行解析 JSON 并断言稳定 schema、唯一终态、绝对路径均位于 runner temp。长时间无变化的四秒心跳由 Task 5 的阻塞 scanner 测试在 Windows/Ubuntu matrix 中证明；真实脚本同时证明扫描事件能端到端输出。失败回滚由本任务真实主机文件系统测试证明，避免为生产 CLI 增加测试后门。

- [ ] **Step 4: 加入 CI 并锁定脚本隔离门禁**

新增 `lifecycle` matrix：Windows/Ubuntu 仅在 GitHub 托管 runner 运行对应脚本。扩展 installpolicy 静态测试，断言两个脚本逐项检查 `GITHUB_ACTIONS`、`CI`、`RUNNER_ENVIRONMENT=github-hosted`、`RUNNER_TEMP`，隔离四类目录，且 CI workflow 只使用 `windows-latest`/`ubuntu-latest`，不使用 self-hosted label。保留现有 unit、cross-build、dashboard jobs；任何 job 都不得调用 `upload-artifact`。在 dashboard job 增加以下语法检查：

```text
node --check internal/web/static/app.js
node --check internal/web/static/i18n.js
node --check tests/dashboard.spec.mjs
node --check tests/demo.spec.mjs
```

Pages workflow 保持手动触发且不参与信任链。

- [ ] **Step 5: 只做本地无副作用 GREEN 与静态检查**

```powershell
& $Go test ./internal/app -run 'Test(LifecycleUpgradeAndRollbackOnHostFilesystem|Heartbeat|InstallJSON)' -count=1
& $Go test ./internal/installpolicy -count=1
node --check internal/web/static/app.js
node --check internal/web/static/i18n.js
node --check tests/dashboard.spec.mjs
node --check tests/demo.spec.mjs
$tokens=$errors=$null
[System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path './tests/install-windows.ps1'), [ref]$tokens, [ref]$errors) | Out-Null
if ($errors.Count) { $errors; throw 'PowerShell lifecycle script has syntax errors' }
rg -n 'upload-artifact|gh release create|tags:' .github/workflows
git diff --check
```

Expected: 真实文件系统回滚、阻塞心跳、安装命令和 runner 隔离契约通过；本地从未执行真实服务脚本。Linux `bash -n` 和两个真实生命周期脚本的 GREEN 证据必须来自 GitHub 托管 Windows/Ubuntu runner。`rg` 不在 release/ci 中发现发布能力；若 Pages 的部署 action 被匹配，人工确认它只在 `workflow_dispatch` 的 Pages workflow 中，且不上传二进制 Release 资产。

- [ ] **Step 6: 提交 CI 生命周期验收**

```powershell
git add tests/install-windows.ps1 tests/install-linux.sh internal/app/lifecycle_platform_test.go internal/installpolicy/policy_test.go .github/workflows/ci.yml
git diff --cached --check
git commit -m 'ci: 验证当前用户安装生命周期' -m "AI-Coding: true`nAI-Agent: Codex`nAI-Model: GPT-5"
```

### Task 12: 完整验证、评审修复与实施留痕

**Files:**
- Create: `docs/sessions/2026-08-26-ai-installation-implementation.md`
- Modify: only files required by specification/quality review findings

- [ ] **Step 1: 运行所有 Go 与静态测试**

```powershell
& $Go test ./... -count=1
& $Go vet ./...
```

Expected: 标准命令通过。若本机 Smart App Control 阻止临时测试 EXE，记录准确错误，按“所有任务共用规则”分别编译固定路径测试二进制并执行所有 package；最终仍必须等待 GitHub Actions Windows + Ubuntu 标准 `go test ./...` 通过，不能以本机编译成功替代。

- [ ] **Step 2: 运行 Dashboard、语法和构建检查**

```powershell
npm ci
npx playwright install chromium
npm test
node --check internal/web/static/app.js
node --check internal/web/static/i18n.js
node --check tests/dashboard.spec.mjs
node --check tests/demo.spec.mjs
& ./scripts/build.ps1 -Version 2.3.5 -SkipTests
```

Expected: Playwright 全部通过；四目标构建成功；只在忽略的 `dist/` 产生本地产物，不上传。

- [ ] **Step 3: 运行策略、隐私、产物与 diff 门禁**

```powershell
& $Go test ./internal/installpolicy -count=1
git diff --check origin/main...HEAD
$privacyNeedles = @($env:USERPROFILE, (Resolve-Path '.').Path, $env:COMPUTERNAME) | Where-Object { $_ }
$privacyMatches = foreach ($needle in $privacyNeedles) { git grep -n -I -F -- $needle -- . }
if ($privacyMatches) { $privacyMatches; throw 'tracked privacy path or machine identifier found' }
git ls-files | rg '(usage\.sqlite|^dist/|^node_modules/|^test-results/|^\.superpowers/|\.exe$)'
git status --short
```

Expected: 无 whitespace error；动态隐私扫描无匹配，因此不会命中仓库内旧计划保存的历史正则文本；意外产物扫描无匹配；工作树干净。仓库中现有的合成 JSONL fixture 继续允许，不能把真实 session 加入 allowlist。

- [ ] **Step 4: 按子智能体流程完成两轮评审**

逐任务已完成规格复核和质量复核后，再做一次跨任务总复核：

- 规格复核逐条映射本计划、设计规格和实际测试。
- 质量复核检查错误路径、并发心跳、唯一终态、回滚、路径边界、平台差异和隐私。
- 发现项必须先复现、修复、加回归测试并单独提交；不能只在留痕中解释。

- [ ] **Step 5: 先推送已本地验证的实施 HEAD**

```powershell
git status --short
git push origin HEAD:main
```

Expected: 工作树干净；不使用 force；远端 `main` fast-forward 到不含最终会话文档的实施 HEAD；没有 tag、Release 或二进制资产。此次 push 是取得真实 Windows/Ubuntu 托管 runner 证据的必要步骤。

- [ ] **Step 6: 等待并核验实施 HEAD 的 CI**

```powershell
$ImplementationHead = git rev-parse HEAD
gh run list --repo edisoncccc/codex-usage --workflow ci.yml --commit $ImplementationHead --limit 5
```

打开与该 SHA 精确对应的 run，等待 unit、lifecycle、cross-build 和 dashboard 全部成功。任何失败都要下载纯文本 log、在本地复现或增加针对性测试、提交修复并再次 fast-forward push；不得把失败 run 写成通过。若 Fork Actions 尚需启用，准确报告外部门禁并完成授权范围内的 Actions 启用后重跑。

- [ ] **Step 7: 有真实 CI 证据后写实施会话文档**

文档使用中文并完整填写：

```markdown
# 2026-08-26 AI 安装协议第一阶段实施会话

## 工作目标
## 执行步骤
## 修改文件
## 运行命令与实际结果
## 规格复核与质量复核
## 关键决策
## 发布与远端结果
## 后续待办
```

记录实际测试数量、Smart App Control 边界、与实施 HEAD 精确对应的 CI run URL/结论、提交 hash、source-only 状态，以及 SignPath/正式 Release 尚未执行的原因。不得记录用户名路径、Token、真实 session 或 Dashboard 数据。

- [ ] **Step 8: 复扫、提交留痕并再次 fast-forward push**

```powershell
git add docs/sessions/2026-08-26-ai-installation-implementation.md
$privacyNeedles = @($env:USERPROFILE, (Resolve-Path '.').Path, $env:COMPUTERNAME) | Where-Object { $_ }
$privacyMatches = foreach ($needle in $privacyNeedles) { git grep -n -I -F -- $needle -- . }
if ($privacyMatches) { $privacyMatches; throw 'tracked privacy path or machine identifier found' }
git diff --cached --check
git commit -m 'docs: 记录 AI 安装协议实施结果' -m "AI-Coding: true`nAI-Agent: Codex`nAI-Model: GPT-5"
git status --short
git push origin HEAD:main
```

Expected: 不使用 force；远端 `main` fast-forward 到最终本地 HEAD；没有 tag、Release 或二进制资产。

- [ ] **Step 9: 等待最终 CI 并验证远端事实**

```powershell
$LocalHead = git rev-parse HEAD
$RemoteHead = gh api repos/edisoncccc/codex-usage/commits/main --jq .sha
if ($LocalHead -ne $RemoteHead) { throw 'remote main does not match local HEAD' }
gh run list --repo edisoncccc/codex-usage --workflow ci.yml --commit $LocalHead --limit 3
gh release list --repo edisoncccc/codex-usage --limit 10
gh api repos/edisoncccc/codex-usage/actions/artifacts --jq '.artifacts | length'
```

Expected: 本地/远端 SHA 一致；最终文档提交对应的 CI 也通过；Release 列表为空；Actions binary artifacts 数量为 0。实施会话文档引用前一个实施 HEAD 的完整平台 run，最终 run 证明加入留痕后仓库仍通过全部门禁。

## 阶段 A 最终验收

1. 规范仓库链接足以让 AI 发现 `INSTALL.md` 和 `install-policy.json`。
2. 当前仍为 source-only，任何 tag 都不能触发发布 workflow。
3. `version/install/scan/doctor/update/uninstall --json` 使用稳定 JSON Lines、唯一终态和语言无关字段。
4. install/scan/health 的长步骤每四秒以内持续输出。
5. 安装只在当前用户范围；没有 admin/sudo 路径。
6. 同一 binary 安全幂等；可信旧记录可升级；降级、未知 EXE、digest 不符会在副作用前停止。
7. 新服务身份不符或健康失败会恢复旧 EXE、旧记录和旧服务。
8. update 渠道关闭时不联网、不修改、不降级。
9. 默认卸载保留 DB/config；purge 需要明确确认和路径防护；Windows 异步删除不虚报同步完成。
10. Go、Playwright、语法、平台生命周期、策略、隐私、产物和 diff 门禁全部有实际通过证据。
11. 远端没有 Release、tag 或二进制 artifact；SignPath 外部申请仍是单独授权事项。
