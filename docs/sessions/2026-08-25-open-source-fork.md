# 2026-08-25 Open-source Fork Session

## 工作目标

将上游 `zJay26/codex-usage` 基线 `cd6d4fdbff54838aed7e38a8bc4edf022c6ce8c7` 上已验证的本地改进整理为公开 GitHub Fork，公开产品展示名使用 **Codex Usage Dashboard**。仓库 slug、命令名继续使用 `codex-usage`，Go module 路径继续使用 `github.com/zJay26/codex-usage`，避免破坏兼容性。

本次发布遵循以下边界：

- 保留完整上游历史、MIT `LICENSE` 与 `THIRD_PARTY_NOTICES.md`，明确致谢 `zJay26/codex-usage`。
- 中文入口为 `README.md`，英文入口为 `README.en.md`，两者结构、视觉和信息权重对等。
- 首屏使用仓库内已有的 `Codex-Usage.png`，下一屏使用仅含合成数据的 `docs/media/codex-usage-demo.gif`。
- 公开说明本地隐私边界、Token 去向、Cached Rate、普通/缓存 Input 分价，以及 Standard API 等价成本不是账单或订阅配额。
- 当前仅发布源码，不创建 GitHub Release，不上传或发布 EXE。
- 原始功能差异来自只读本地参考副本；实施期间该副本保持未修改。

## 执行步骤

1. 在 linked worktree 的独立执行分支上确认上游基线、设计文档和实施计划。
2. 将本地视觉 brainstorm 目录加入忽略规则，避免本机草稿进入公开仓库。
3. 分别移植并测试 Subagent 标题继承、modern fork replay 保护、缓存费用与告警优先级界面改进。
4. 针对代码质量复核意见，补充告警列表 fail-closed、一致性与陈旧响应保护，并增强浏览器测试。
5. 将项目首页改写为同构的中英文产品首页，公开展示名设为 **Codex Usage Dashboard**，保留 `codex-usage` 的仓库与运行时兼容标识。
6. 修正 README 中的平台、隐私和运行命令表述，并完成设计文档发布门禁的空白字符修复。
7. 使用官方 Go 1.26.6、稳定测试二进制、`go vet`、最终服务二进制、Playwright 和 JavaScript 语法检查完成发布前验证。
8. 运行累计差异、隐私、产物、README 资源及禁止下载链接检查，并创建本会话交接。

本文件首次提交前的提交序列如下：

| 提交 | 信息 |
|---|---|
| `a5ad72aa1a982230fcabba3051d61664138ffbf3` | `docs: define public fork README design` |
| `8be24ae513261d684cd7e4fa0f3c7a5973fc0165` | `docs: add public fork implementation plan` |
| `b275463cfa7fba80b096b277e4bb3b498932c130` | `chore: ignore local brainstorm artifacts` |
| `8f2924f2b67b44918cc31c92772392a8f5fe3ca4` | `fix: label subagents from their parent tasks` |
| `16ccb1709c65c84dda51bfa014dda759605fb6e4` | `fix: keep modern fork replay history pending` |
| `519ad9f6a4aee74157c663eb9501c82981b793ff` | `feat: clarify cached usage and warning priority` |
| `fe27742019e5dbb67995ad54bbe90778801c55ed` | `fix: keep warning priority fail-closed` |
| `cf343f356daa25f99412a75fe7778792674f75e4` | `docs: refresh the bilingual project homepage` |
| `e8a21f43eca5e25b27253ae9a7391a132116bf5f` | `docs: correct README platform and privacy claims` |
| `0c7c7a4218ff263d230ffb70507fe3d35d104695` | `docs: refine README runtime examples` |
| `e2ee315dc6e5d6bf83c53b10b873f42ed2aef228` | `docs: fix publication check whitespace` |
| `160f9a26d87a2ade538b8bf5067d0c3352b497c3` | `docs: preserve spec metadata line breaks` |

本文件首次提交为 `50efb60632f5ac2ff8c45356bb3f4671249da39a`，提交信息为 `docs: record the public fork handoff`；它不属于上表的“首次提交前”序列。

## 修改文件

本次从上游基线累计修改或新增的受跟踪文件：

- 发布设计、计划与留痕：
  - `docs/superpowers/specs/2026-08-25-open-source-readme-design.md`
  - `docs/superpowers/plans/2026-08-25-open-source-fork-readme.md`
  - `docs/sessions/2026-08-25-open-source-fork.md`
- 仓库首页与发布边界：
  - `.gitignore`
  - `README.md`
  - `README.en.md`
- Subagent 标题归属：
  - `internal/store/store.go`
  - `internal/store/store_test.go`
- modern fork replay 保护：
  - `internal/usage/scanner.go`
  - `internal/usage/scanner_test.go`
- Cached Rate、费用分项与告警优先级：
  - `internal/web/static/app.js`
  - `internal/web/static/i18n.js`
  - `internal/web/static/index.html`
  - `internal/web/static/styles.css`
  - `tests/dashboard.spec.mjs`

测试二进制、最终服务 EXE 和 Playwright 输出均位于仓库已忽略的路径，不属于公开提交。

## 运行命令与结果

### Go 基线与最终验证

- 改动前的干净基线曾完成一次标准 `go test ./...`，结果通过。
- 最终严格原命令 `go test ./...` 会在系统临时目录即时生成并加载 `.test.exe`，被 Windows Smart App Control 阻断。按用户明确批准，安全策略保持开启；未使用 `go test -exec`，未关闭或绕过安全策略，也未宣称最终严格原命令通过。
- 使用官方 Go 1.26.6 运行 `go list` 模板，枚举到 12 个 package：9 个含测试文件，3 个无测试文件。无测试文件的包为 `cmd/codex-usage`、`cmd/fixturegen`、`internal/model`。
- 对 9 个含测试的包分别运行 `go test -c -o test-results/task6-<安全包名>-20260825-221746.test.exe <importpath>`，随后在对应 package 目录直接执行稳定二进制，并带 `-test.count=1 -test.timeout=10m`。共列出并执行 82 个顶层测试，9/9 测试 package 均为 PASS。
- `go vet ./...`：exit 0。
- 使用 `CGO_ENABLED=0 go build -trimpath` 构建最终 E2E 服务：`test-results/codex-usage-task6-final-20260825-221845.exe`，SHA256 为 `9347F644F5D21A680C01D2E7929D753E4042199D1F331D78D49C1634F9F25FA3`。

最终测试二进制及 SHA256：

| Package | 仓库相对路径 | SHA256 |
|---|---|---|
| `internal/app` | `test-results/task6-internal-app-20260825-221746.test.exe` | `A7486ECD06F861C919E20AC7B6D9B0C27412B4C0AD05FB5EDC579F96CCE39F83` |
| `internal/cliui` | `test-results/task6-internal-cliui-20260825-221746.test.exe` | `3E20BFDE14318B4C50EAAD28E2187E8D7CDD414C4AE78FF1DC0EC4323D6AD645` |
| `internal/config` | `test-results/task6-internal-config-20260825-221746.test.exe` | `41A59F9223DEBEA7A3BD034E02247712AD177280A3BF7656B3B2EAB1A1A9ADE7` |
| `internal/platform` | `test-results/task6-internal-platform-20260825-221746.test.exe` | `D4AD6A05AAAC9337809DDDD783BDBE05F8E83ABE08A23BCABBAFEF22BCCABAD7` |
| `internal/pricing` | `test-results/task6-internal-pricing-20260825-221746.test.exe` | `21048947DFB7CF92C743B36CEE3F51401FF37CF0F2D012DCBC0725ED2767ADB9` |
| `internal/server` | `test-results/task6-internal-server-20260825-221746.test.exe` | `ECE05D15A2B6A0120D76E2DE30D0DB31DD56C5777C81C4F4E6CEEE5F79D1535F` |
| `internal/store` | `test-results/task6-internal-store-20260825-221746.test.exe` | `17A1B1CBC77687802CAD66755C2BFBA85D96D3C646EFFC880E17ACC11D41518D` |
| `internal/usage` | `test-results/task6-internal-usage-20260825-221746.test.exe` | `7DFD9B09E33F773F0C5E42DB392187595B489DD5586D7F87645ABAF545F0601E` |
| `internal/web` | `test-results/task6-internal-web-20260825-221746.test.exe` | `F4B34F9D64DAAFB22095B7EC8A92D7257C41E13C329347E5C3AAF9FC8546EC4E` |

过程事件：第一次批处理已成功编译 `test-results/task6-internal-app-20260825-221610.test.exe`，但 PowerShell 将 `-test.list=.` 错拆为无效的 `-test` 参数，测试程序显示用法并以 exit 2 结束。这是验证编排错误，不是项目测试失败；该唯一、未跟踪产物未覆盖也未删除。随后以新时间戳重新执行全部 9 个测试 package 并通过。

### Playwright 与 JavaScript

- 设置 `CODEX_USAGE_BIN=test-results/codex-usage-task6-final-20260825-221845.exe`、`PLAYWRIGHT_SKIP_BROWSER_GC=1`，并将官方 Go 置于 `PATH` 前部。
- 运行完整 `npm test -- --output node_modules/.cache/playwright-task6-final-20260825-221902`：Dashboard 20 项、Demo 3 项，共 23/23 通过，耗时 36.2 秒。
- `node --check internal/web/static/app.js`：exit 0。
- `node --check internal/web/static/i18n.js`：exit 0。
- `node --check tests/dashboard.spec.mjs`：exit 0。
- `node --check tests/demo.spec.mjs`：exit 0。

过程事件：早期 Playwright RED 尝试中，测试框架自动清理了刚创建的临时测试 EXE。该文件是本任务新建且未跟踪的测试产物，没有删除任何受跟踪文件、既有用户文件或只读参考副本内容。此后统一使用唯一、稳定的测试产物路径，未再重现。

### Git、隐私与发布静态门禁

- `git diff --check cd6d4fdbff54838aed7e38a8bc4edf022c6ce8c7..HEAD`：发布门禁修复后 exit 0。
- 原始隐私 grep：raw 结果恰有 1 项，是实施计划内记录该扫描命令的行自匹配，不是仓库数据泄漏；精确排除这一条计划命令行自身后，refined 结果为 0 个未允许匹配。
- 原始产物扫描：raw 结果恰有 2 项，均为上游基线已有的脱敏合成测试 fixture：
  - `internal/usage/testdata/single-meta-fork-inherited-baseline.jsonl`
  - `internal/usage/testdata/single-meta-fork-zero-baseline.jsonl`
- 两份 fixture 相对上游基线没有变化，内容使用 `redacted` 标识且私密标记扫描无匹配；精确允许这两个受控路径后，refined 结果为 0 个意外产物。fixture 保留且未删除。
- README 必需资源 `Codex-Usage.png`、`docs/media/codex-usage-demo.gif`、`LICENSE`、`THIRD_PARTY_NOTICES.md`、`README.md`、`README.en.md`：6/6 存在。
- README 中上游 Release、`releases/latest/download` 及个人 Fork Release 下载链接扫描：无匹配。
- 创建本文件前 `git status --short`：无输出，工作树干净。
- 本文件暂存后，`git diff --cached --name-only` 仅列出本会话文档，`git diff --cached --check` 为 exit 0；本文件自身的 staged 隐私扫描无匹配。完整 staged 索引的 raw/refined 隐私与产物计数仍分别为 `1/0` 和 `2/0`，与上述已核验的计划命令自匹配及两份合成 fixture 完全一致。

隐私 raw 输出固定为 `docs/superpowers/plans/2026-08-25-open-source-fork-readme.md:326:<计划内扫描命令>`。以下 PowerShell 命令通过字符串拼接构造敏感模式，避免检查命令在本文中产生新的自匹配；它同时验证工作树、staged 索引和受跟踪产物。任一 raw 结果偏离精确 allowlist 都会抛出错误：

```powershell
$privacyParts = @(
  ('C:' + '\' + 'Users' + '\' + ('edi' + 'so'))
  ('D:' + '\' + 'Projects' + '\' + 'codex')
  ('Laptop-' + 'Chen')
  ('edi' + 'son' + '_c')
  '01a0[0-9a-f]{4}'
)
$privacyPattern = $privacyParts -join '|'
$privacyRegex = $privacyPattern.Replace('\', '\\')
$allowedPrivacyPrefix = 'docs/superpowers/plans/2026-08-25-open-source-fork-readme.md:326:git grep -n -I -E '
$allowedPrivacyRegexText = $privacyPattern.Replace('\', '\\')
$allowedPrivacyLine = $allowedPrivacyPrefix + "'" + $allowedPrivacyRegexText + "' -- ."

$privacyRaw = @(git grep -n -I -E $privacyRegex -- .)
$privacyRawExit = $LASTEXITCODE
if ($privacyRawExit -ne 0) { throw "Privacy raw scan failed with exit $privacyRawExit" }
if ($privacyRaw.Count -ne 1 -or $privacyRaw[0] -cne $allowedPrivacyLine) {
  throw "Unexpected privacy raw result: $($privacyRaw -join '; ')"
}
$privacyUnexpected = @($privacyRaw | Where-Object { $_ -cne $allowedPrivacyLine })
if ($privacyUnexpected.Count -ne 0) { throw "Unexpected privacy matches: $($privacyUnexpected -join '; ')" }

$privacyStagedRaw = @(git grep --cached -n -I -E $privacyRegex -- .)
$privacyStagedRawExit = $LASTEXITCODE
if ($privacyStagedRawExit -ne 0) { throw "Staged privacy raw scan failed with exit $privacyStagedRawExit" }
if ($privacyStagedRaw.Count -ne 1 -or $privacyStagedRaw[0] -cne $allowedPrivacyLine) {
  throw "Unexpected staged privacy raw result: $($privacyStagedRaw -join '; ')"
}
$privacyStagedUnexpected = @($privacyStagedRaw | Where-Object { $_ -cne $allowedPrivacyLine })
if ($privacyStagedUnexpected.Count -ne 0) {
  throw "Unexpected staged privacy matches: $($privacyStagedUnexpected -join '; ')"
}

$artifactPattern = '(usage\.sqlite|\.jsonl$|^dist/|^node_modules/|^test-results/|^\.superpowers/)'
$artifactRaw = @(git ls-files --cached | rg $artifactPattern)
$artifactRawExit = $LASTEXITCODE
$artifactAllowlist = @(
  'internal/usage/testdata/single-meta-fork-inherited-baseline.jsonl'
  'internal/usage/testdata/single-meta-fork-zero-baseline.jsonl'
)
$artifactUnexpected = @($artifactRaw | Where-Object { $_ -notin $artifactAllowlist })
$artifactMissing = @($artifactAllowlist | Where-Object { $_ -notin $artifactRaw })
if ($artifactRawExit -ne 0 -or $artifactRaw.Count -ne 2) {
  throw "Unexpected artifact raw result: exit=$artifactRawExit count=$($artifactRaw.Count)"
}
if ($artifactUnexpected.Count -ne 0 -or $artifactMissing.Count -ne 0) {
  throw "Artifact allowlist mismatch: unexpected=$($artifactUnexpected -join ',') missing=$($artifactMissing -join ',')"
}

[pscustomobject]@{
  PrivacyRaw = $privacyRaw.Count
  PrivacyUnexpected = $privacyUnexpected.Count
  PrivacyLineExact = $privacyRaw[0] -ceq $allowedPrivacyLine
  PrivacyStagedRaw = $privacyStagedRaw.Count
  PrivacyStagedUnexpected = $privacyStagedUnexpected.Count
  PrivacyStagedLineExact = $privacyStagedRaw[0] -ceq $allowedPrivacyLine
  ArtifactRaw = $artifactRaw.Count
  ArtifactUnexpected = $artifactUnexpected.Count
}
```

实际执行时，允许行与工作树及 staged raw 输出均逐字符相等；两组隐私扫描均为 raw `1`、unexpected `0`。产物扫描的 `rg` 因两项受控命中返回 exit 0，raw 为 `2`、unexpected 为 `0`，整个断言脚本返回 exit 0。

## 关键决策

- 公开身份采用 GitHub 原生 Fork，不将上游成果包装为从零原创；保留 MIT 来源和完整历史。
- 对外产品名使用 **Codex Usage Dashboard**；仓库 URL、命令与 module 继续使用 `codex-usage`。
- 当前只发布源码，不建立 Release，不上传 EXE；二进制签名、校验清单与 Release 属于未来独立决策。
- 只使用仓库已有的合成数据图片，不使用真实 Dashboard、真实路径、Thread 标题或用户数据截图。
- Standard API 等价成本只是按公开价格对本地 Token 记录的折算，不代表 OpenAI 账单、订阅消耗或账户配额。
- Smart App Control 保持开启。最终 Go 验证采用稳定路径的测试二进制等价方案，并明确区分“等价验证通过”和“严格原命令受环境策略阻断”。
- 隐私与产物扫描同时保留 raw 结果及经过逐项核验的 refined 结果，不以删除合成测试 fixture 的方式制造空扫描。

## GitHub 发布结果

### 2026-08-28 最终补录

- 公开仓库仍为 <https://github.com/edisoncccc/codex-usage>，实时核验结果为 `isFork=true`、`isPrivate=false`、父仓库 `zJay26/codex-usage`、默认分支 `main`，Description 仍为 **Codex Usage Dashboard** 的英文产品说明。
- 已由用户在 GitHub Actions 页面显式启用 Fork workflow。启用后以空提交 `f32963d5cb46b562b29e833753af32e09a8b5af7` 触发首次托管验收，并根据真实日志修正 Windows 临时目录、PowerShell JSON 时间戳、Linux systemd 回退及 Windows 生命周期验证脚本。
- 最终实现提交 `c67b952d843479c28ee75a4ff4e63368aeb23134` 对应的 CI run 为 <https://github.com/edisoncccc/codex-usage/actions/runs/33137453013>，`status=completed`、`conclusion=success`、head SHA 精确一致；Windows/Ubuntu 单测与 `go vet`、Windows/Linux 当前用户生命周期、交叉构建和 Dashboard 共 6/6 作业通过。
- 失败过程未隐藏：run `33135758468` 暴露三项托管环境差异；run `33136759221` 暴露 job 级 `runner` 上下文不可用；run `33136931542` 暴露 PowerShell `$HOME` 只读变量冲突；run `33137126198` 暴露预期缺失注册表值的读取方式；每轮均在读取原始日志、补充红测和本地回归后以普通 fast-forward 修正。
- 最终只读核验：Actions artifacts 为 `0`，GitHub Releases 为 `0`，`pages.yml` 与 `release.yml` 的 run 列表均为空；没有创建新标签、Release 或二进制资产。Fork 远端标签集合与上游完全相同，仅包含继承的 `v0.1.0` 至 `v2.3.5`。
- `.github/workflows/ci.yml` 只构建和验证，不上传 artifacts；`.github/workflows/pages.yml` 与 `.github/workflows/release.yml` 仅允许手动触发，其中 Release workflow 是只读 source-only 策略哨兵，不能发布资产。

以下条目保留首次发布当时的时间顺序；若其“尚待验证”描述与本补录冲突，以本补录的最终实时证据为准。

- Task 7 的公开 Fork 创建、源码推送与 remote 配置已经完成；截至本次留痕提交前，显式启用后的 CI push 验证尚待下一次正常推送完成。
- 公开仓库：<https://github.com/edisoncccc/codex-usage>，创建时间为 `2026-08-25T15:31:55Z`。
- GitHub 已验证属性：`visibility=PUBLIC`、`isFork=true`、父仓库为 `zJay26/codex-usage`、默认分支为 `main`。
- GitHub Description 已精确设置为 `Codex Usage Dashboard — Local-first Codex token attribution, cache insights, and Standard API-equivalent cost.`；仓库 slug、命令与 Go module 未改名。
- 最终 remote 目标已经规范化：`origin` 的 fetch/push 均为 `git@github.com:edisoncccc/codex-usage.git`，`upstream` 的 fetch/push 均为 `https://github.com/zJay26/codex-usage.git`。
- 首次源码推送以 `619d10b3ca1b82516da03b66f4b48df8a9541062` 为已验证基准，执行 `git push origin HEAD:main`，结果为从上游基线 `cd6d4fd` 正常快进到该提交；首次远端 `main` 与本地基准逐字一致，未使用 force。
- 随后推送 source-only workflow 门禁提交 `ed6a891fe9cb4ef0d03f86529e4fe88702e04083`，结果为 `619d10b..ed6a891` 正常快进。该提交只修改 `.github/workflows/ci.yml` 和 `.github/workflows/pages.yml`：CI 继续构建与测试但不再上传 `dist/` 二进制，Pages 仅保留手动触发，不再随 `main` push 自动运行。
- Fork 创建后的前两次 push 均未产生 workflow run。只读核验显示仓库 Actions 权限为 `enabled=true`、`allowed_actions=all`，三个现有 workflow 的 API 状态均为 `active`；依据 Fork 的显式启用要求，已运行 `gh workflow enable ci.yml --repo edisoncccc/codex-usage`，命令 exit 0，`ci.yml` 的 API 更新时间变为 `2026-08-26T00:09:17+08:00`。未手动 dispatch 任何 workflow，也未启用或触发 Pages/Release。
- 截至本次留痕提交前，CI、Pages 与 Release workflow run 列表均为空，Actions artifacts 为 `total_count=0`，GitHub Release 列表为空；没有上传二进制资产、创建 tag 或发布 Release。下一次正常文档 push 将用于验证已显式启用的 `ci.yml`，实际 run id、URL、head SHA 与 conclusion 只在 GitHub 返回终态后补录。

发布与门禁留痕提交：

| 提交 | 信息与作用 |
|---|---|
| `50efb60632f5ac2ff8c45356bb3f4671249da39a` | `docs: record the public fork handoff`，首次提交会话交接。 |
| `1802838db4574075e0c0e6acc0696dd563075953` | `docs: make publication scans reproducible`，固定可复现的发布扫描。 |
| `9851ecf1533cbfb1cbdf0a16686a4cc8501faf37` | `docs: tighten publication scan allowlist`，收紧隐私与产物 allowlist。 |
| `619d10b3ca1b82516da03b66f4b48df8a9541062` | `docs: restore the public fork URL`，恢复公开 Fork URL 并作为首次推送基准。 |
| `ed6a891fe9cb4ef0d03f86529e4fe88702e04083` | `ci: keep fork publication source-only`，取消 CI 二进制上传并关闭 Pages push 自动触发。 |

## 后续待办

- 公开 Fork、Actions 启用、源码-only 发布边界和双平台托管验收均已完成，没有阻塞性发布待办。
- 非阻塞测试增强：后续可为 Subagent 标题处理补充 malformed JSON、父链 cycle 和 JSON role fallback 等边界测试。
- 若未来决定发布二进制，需另行申请免费开源签名资格、确认 publisher 身份并完成签名、SHA256、Attestation 和不可变 Release 演练；在此之前继续保持 source-only。
