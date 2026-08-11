# Codex Usage 验收记录

## v2.3.1 验收记录（2026-08-11）

- 执行主机：Windows amd64
- 候选版本：v2.3.1 · 可导航的逐小时历史、小时成本上下文与统一日期控件
- 范围：Go 单元/集成测试与静态检查、Playwright 真实二进制及 synthetic Demo、Windows/Linux × amd64/arm64 交叉构建、Windows amd64 候选原生版本检查和 SHA-256；Linux 与 arm64 原生运行由 GitHub Actions 和对应平台负责。

主要验收项：

- 每小时历史：支持前一天、后一天、具体日期和“今天”导航，可查看任意本地日期的完整小时并逐点交互。
- 小时上下文：选中小时下方重点显示 Standard API 等价成本，同时显示使用模型、模型 Token/估算费用和定价覆盖率；滑动选点采用防抖并忽略过期请求。
- 每日日期直达：月历可直接选择具体日期，包括零用量日期，并自动跳转到对应月份。
- 日期控件：每日与每小时共享统一组件，年份与月日分层显示，放大字体、日历图形和 50px 导航触控区域；底层保留原生日期面板和键盘焦点。
- 响应式与无障碍：桌面与 390 × 844 移动端四种组合均完成视觉检查，无页面横向溢出；中英文、键盘、主题与 reduced-motion 回归通过。

自动化结果：

- `go test -count=1 -mod=readonly ./...`：通过。
- `go vet -mod=readonly ./...`：通过。
- Playwright Dashboard + synthetic Demo：15/15 通过。
- 四平台交叉构建：Windows/Linux × amd64/arm64 全部生成成功，清单内四项 SHA-256 复算一致。

### v2.3.1 本地候选构建产物

| 平台 | SHA-256 |
|---|---|
| Windows amd64 | `939a23ff819d449bc3f3ded74252aeff39f5490a17ff7e219d4ed4243ced884b` |
| Windows arm64 | `1c2884e37a8b3ce69b73caa92f800efbcf71cf4cc57909bd35322d26a906cb6e` |
| Linux amd64 | `29819108510b2e5c2f49da7a0269b21804701295aa9856f17fbf0a3a1a40883c` |
| Linux arm64 | `503644e2581087175a1d2ff2f97f9da59d874279e835118742b3478a74ca4e25` |

Windows amd64 候选已原生运行并报告 `codex-usage 2.3.1 (9de4556-dirty, …) windows/amd64`。`-dirty` 表示版本准备文件尚未提交时构建；正式 Release 由 tag workflow 从干净提交重新测试和构建，最终哈希应以 Release 附带的 `SHA256SUMS` 为准。

## v2.3.0 验收记录（2026-08-11）

- 执行主机：Windows amd64
- 候选版本：v2.3.0 · 小时级 Token 统计、交互性能与可靠性优化
- 范围：Go 单元测试与静态检查、Playwright 真实二进制与 synthetic Demo、Windows/Linux × amd64/arm64 交叉构建、候选二进制原生启动和 SHA-256；Linux 与 arm64 的原生运行仍由 GitHub Actions 和对应平台负责。

主要验收项：

- 小时统计：使用 JSONL Token 增量事件自身的时间戳按本地小时归属；提供最近 60 分钟汇总，以及最近 24 个完整小时的可交互折线图。
- 趋势界面：概览默认显示“每日Token用量”，同一区域可切换“每小时”；折线与交互点使用同一 180px 坐标系，桌面实测最大偏差约 0.01px。
- 交互性能：视图先切换再异步加载数据；Session 行与费用估算拆分请求，价格和筛选数据使用有界 revision cache，避免首次点击阻塞整页。
- 统计与导出：价格聚合逐事件校验；CSV/JSON 使用稳定单快照遍历；Session metadata 实际变化才推进 `data_revision`。
- 安全与依赖：写接口强制同端口 loopback Origin；SQLite 依赖升级并保留 JSONL-only 正式统计边界。
- 响应式与无障碍：桌面及 390px 移动端无页面横向溢出；小时图可横向滚动，键盘选点、中英文、主题和 reduced-motion 回归通过。

自动化结果：

- `go test ./...`：通过。
- `go vet ./...`：通过。
- Playwright：15/15 通过；CI 时序修复后的移动端 Session 用例此前另行连续运行 5/5 通过。
- 四平台交叉构建：Windows/Linux × amd64/arm64 全部生成成功。

### v2.3.0 构建产物

`scripts/build.ps1 -Version 2.3.0` 生成四个无 CGO 单文件二进制与 `SHA256SUMS`：

| 平台 | SHA-256 |
|---|---|
| Windows amd64 | `23adfd6fbed252c5da07e754ae0453add99fb5bff84432659fa5b6601ba5a2ec` |
| Windows arm64 | `2a0175ff273b97b7d082eb3ee1d0751394ac4e286386c8b7eb30fe449c888faa` |
| Linux amd64 | `1fb4beda28d7a52826c40cbe3c112f14412667ae3cb561aa734f7adce4c2020d` |
| Linux arm64 | `445e0b1c6af431b060224fb42049508110e49413fcd15603e5507b21a7c9fd78` |

Windows amd64 候选已原生运行并报告 `codex-usage 2.3.0 (2fc46a7-dirty, …) windows/amd64`；`-dirty` 表示候选在发布准备提交前构建。GitHub tag 工作流会从干净发布提交重新执行测试与构建，因此正式资产的哈希预计与本地候选不同，发布后须以 Release 附带的 `SHA256SUMS` 为准。

## 2026-08-10 全面优化分支

- 执行主机：Windows amd64
- 分支：`codex/comprehensive-optimization-20260810`
- 范围：源代码、单元/竞态/浏览器测试、依赖漏洞、四平台交叉构建和隔离性能基准；未制作 Release 包，也未验证已安装服务或其他物理平台上的最终运行。

- 正确性：价格聚合不再把 `total_tokens != input_tokens + output_tokens` 的相反误差互相抵消；总览和逐 Session 聚合均与逐事件估算保持一致。
- 导出：CSV/JSON 改为单个 SQLite 读快照和稳定全序遍历，消除 OFFSET 分页在并发写入或相同时间戳下漏行/重复的风险。
- 数据刷新：Session metadata 实际变化会推进 `data_revision`，无变化的 upsert 不会制造多余刷新。
- 安全：写接口只接受与当前端口完全一致的 HTTP loopback Origin，并拒绝无 Origin 但标记为 `Sec-Fetch-Site: cross-site` 的请求；CLI 等非浏览器客户端保持可用。
- 依赖：升级到 [`modernc.org/sqlite v1.56.0`](https://gitlab.com/cznic/sqlite/-/blob/v1.56.0/CHANGELOG.md)，纳入官方记录的 SQLite 3.53.3 journal rollback 数据损坏修复；Playwright 升级到 [1.62.1](https://github.com/microsoft/playwright/releases/tag/v1.62.1)。
- UI：补齐三个对话框入口的 `aria-controls`，并修复警告时间副文本引用不存在颜色变量的问题；保留现有信息架构和视觉方向。
- 开发体验：`npm test` 不再要求手工设置 `CODEX_USAGE_BIN`，会在临时目录自动构建真实二进制。

自动化结果：

- `go test -count=1 -mod=readonly ./...`：通过。
- `go test -race -count=1 -mod=readonly ./...`：通过。
- `go vet -mod=readonly ./...`：通过。
- `govulncheck ./...`：未发现已知漏洞。
- Playwright：12/12 通过，包含自动构建真实二进制、同端口 loopback Origin、ARIA、中英文、移动端、主题、筛选、定价、扫描和 synthetic Demo。
- 交叉构建：Windows/Linux × amd64/arm64 四个目标全部通过。

隔离性能基准使用本机 550 个 JSONL 新建临时数据库，共 67,409 条规范事件；每种实现 3 轮、每轮 5 次：

- OFFSET 分页导出：3.19–3.21 秒/次，约 262.88 MB 分配。
- 单快照导出：0.702–0.712 秒/次，约 159.90 MB 分配。
- 同数据下约快 4.5 倍，单次分配降低约 39%。临时数据库和交叉构建产物已移入回收站，现有统计库未参与测试。

## v2.2.0 验收记录（2026-08-04）

执行日期：2026-08-04
执行主机：Windows amd64
候选版本：v2.2.0 · Session search, per-Session cost, and public-first documentation

## 自动化检查

- `go test -mod=readonly ./...`：通过。
- `go vet -mod=readonly ./...`：通过。
- Playwright：11/11 通过，覆盖真实编译二进制、Session 前端搜索、快捷筛选再次点击取消、筛选项懒加载与 GitHub Pages synthetic Demo。
- 定价：已识别模型统一按 Standard 短上下文单价计算；300K Input 的非 `exact` 回归样例仍完整计价，不再生成 `long_context_uncertain`。
- Dashboard：概览、每日、明细、Session 等价费用、搜索、筛选、日期下钻、定价覆写、扫描、导出、主题、中英切换和移动端无溢出均通过。
- Demo：正式前端下的所有 `/api/v1/*` 均由 synthetic adapter 接管；无 Cookie、无外部请求，不读取或导出本机数据。

## JSONL-only 统计回归

- 唯一计数源：查询只选择 `session_jsonl`；旧 `otel` / `state_fallback` 行不参与任何总量，`unattributed` 为 0。
- v4 → v5 数据迁移：当前实现会先保留旧解析器事件、游标、session 高水位和 OTel 表并提示重建；只有用户在 Dashboard 确认或显式运行 `scan --rebuild` 后才清除派生数据并从仍存在的源 JSONL 重建。
- fork/subagent：三个子线程共享同一父历史前缀时，父历史只计一次；每个子线程只计继续产生的增量，Session、项目、模型与 Agent 归属保持为子线程 owner。
- 同总量修正：后续快照只修正 Input / Output / Cached Input / Cache Write / Reasoning 分类时，会在同一累计 segment 的既有事件中就近回写；修正量超过最后一条增量或跨增量扫描时仍生效。
- 文件变更：同尺寸重写和截断都会触发全 JSONL 派生索引重建，旧事件不会残留。
- 文件发现：状态库可读且已有路径均有效时，仍与 `sessions/`、`archived_sessions/` 目录取并集，漏行不会隐藏新 JSONL。
- Windows 路径：普通路径与 `\\?\` 扩展路径规范为同一 cursor/origin。
- 日期稳定性：事件入库时保存本地日期与小时；修改查询时的系统时区后，既有历史自然日不漂移。
- 状态库边界：`tokens_used` 只作为 Codex metadata 读取，不再补 Token 总量。

## 性能优化回归

基于当前电脑约 498 个 JSONL、5.5 万条事件的只读 SQLite 备份进行隔离测试：

- Dashboard 冷启动请求从约 3.8 秒降至约 1.35 秒；启动阶段不再请求三次全历史 breakdown。
- 全历史费用接口从约 1.41 秒降至约 0.63 秒；聚合遍历的单次内存分配从约 122 MB 降至约 2.35 MB。
- 30 天 Session 查询从约 2.98 秒降至约 1.97 秒；全历史 Timeseries 从约 0.63 秒降至约 0.47 秒。
- 筛选维度首次加载约 1.49 秒，服务端缓存后约 1.7 ms；仅在首次打开筛选面板时请求。
- 无变化 activity probe 每次约 25–28 ms；每 30 秒只检查文件 metadata，完整兜底扫描间隔为 10 分钟。
- SQLite 使用单写连接与两个只读连接；写事务占用时的读取并发测试通过。

## 安装与升级

- 安装器不再添加 Codex 遥测 exporter 或修改第三方 exporter，也不要求重启 Codex。
- 从旧版升级时，只移除 `# BEGIN/END codex-usage managed` 或旧产品 marker 包围的 exporter；其余 TOML 原样保留。
- 后台服务仍只监听 `127.0.0.1`，启动后先扫描一次；每 30 秒轻量检查 JSONL metadata，变化时增量扫描，并每 10 分钟兜底扫描。
- JSON/CSV 原字段与 `/api/v1/*` 路由保留；状态响应新增 `accounting_mode: "jsonl_only"` 并保留恒为 false 的兼容字段 `otel_active`，timeseries 点新增稳定 `date`。

## 当前电脑隔离实扫

以下为 v2.1.0 发布前保留的扫描器回归证据；v2.2.0 未修改 JSONL 扫描与入账规则。使用 v2.1.0 Windows amd64 候选二进制、独立临时 `CODEX_USAGE_HOME` 对当前电脑源 JSONL 执行 `scan --rebuild`：

- 508 个 JSONL，59,269 个规范增量事件。
- 487 个有用量 Session；14,283 个重复累计快照被忽略。
- 总量 7,577,540,264 Token；`unattributed=0`；0 条扫描 warning。
- 该结果与正式服务同一 JSONL 源口径一致；源历史在检查期间仍会继续增长。
- 扫描只读取源 JSONL；正式数据库和已安装服务未改动。

## v2.2.0 构建产物

`scripts/build.ps1 -Version 2.2.0` 成功生成四个无 CGO 单文件二进制与四项 `SHA256SUMS`：

| 平台 | SHA-256 |
|---|---|
| Windows amd64 | `1febb81c24640c1114f16704d766a540a24ea7f2929191ab705af2bb397ab332` |
| Windows arm64 | `ffba31a95bb25bb0605bb678ab4666aef2fd2b7b76b0da94ec2b1ad179d0de18` |
| Linux amd64 | `77515e4a1ce7fcd5ffff8104e6e196e11da9402b0503628da459c35c1003cfba` |
| Linux arm64 | `b1fcaf28112ec79bf2ed44eddeb4c746ec0442f4da4f5f178bdb8f635c6b4e17` |

Windows amd64 二进制已原生实跑并报告 `codex-usage 2.2.0 (d2774bf-dirty, …)`；本地候选的 `-dirty` 标记表示它在提交前构建，干净 tag 的 CI 构建会记录实际发布提交。四项本地校验和已重新计算并逐项验证。Linux 与 arm64 目标完成交叉构建；对应平台的原生启动仍由 GitHub Actions runner / 实机继续验证。

## 公开素材

- 1280×640 Social Preview 已更新为 `JSONL only · replay-aware`。
- 960×540 / 14 秒 GIF、1280×720 / 20–30 秒中英 MP4、桌面与 390px 移动截图均由 synthetic Demo 重新生成。
- 素材生成脚本的脱敏断言通过；未使用真实 hostname、machine ID、路径、Thread 或 Session ID。
