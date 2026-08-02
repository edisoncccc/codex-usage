# Codex Usage 验收记录

执行日期：2026-08-02
执行主机：Windows amd64
候选版本：v2.0.0 · JSONL-only accounting

## 自动化检查

- `go test -mod=readonly ./...`：通过。
- `go vet -mod=readonly ./...`：通过。
- Playwright：9/9 通过，覆盖真实编译二进制与 GitHub Pages synthetic Demo。
- Dashboard：概览、每日、明细、筛选、日期下钻、定价覆写、扫描、导出、主题、中英切换和移动端无溢出均通过。
- Demo：正式前端下的所有 `/api/v1/*` 均由 synthetic adapter 接管；无 Cookie、无外部请求，不读取或导出本机数据。

## JSONL-only 统计回归

- 唯一计数源：查询只选择 `session_jsonl`；旧 `otel` / `state_fallback` 行不参与任何总量，`unattributed` 为 0。
- v4 数据迁移：旧解析器事件、游标、session 高水位和 OTel 表会被清除；安装/服务随后从源 JSONL 自动重建。
- fork/subagent：三个子线程共享同一父历史前缀时，父历史只计一次；每个子线程只计继续产生的增量，Session、项目、模型与 Agent 归属保持为子线程 owner。
- 同总量修正：后续快照只修正 Input / Output / Cached Input / Cache Write / Reasoning 分类时，会在同一累计 segment 的既有事件中就近回写；修正量超过最后一条增量或跨增量扫描时仍生效。
- 文件变更：同尺寸重写和截断都会触发全 JSONL 派生索引重建，旧事件不会残留。
- 文件发现：状态库可读且已有路径均有效时，仍与 `sessions/`、`archived_sessions/` 目录取并集，漏行不会隐藏新 JSONL。
- Windows 路径：普通路径与 `\\?\` 扩展路径规范为同一 cursor/origin。
- 日期稳定性：事件入库时保存本地日期与小时；修改查询时的系统时区后，既有历史自然日不漂移。
- 状态库边界：`tokens_used` 只作为 Codex metadata 读取，不再补 Token 总量。

## 安装与升级

- 安装器不再添加 Codex 遥测 exporter 或修改第三方 exporter，也不要求重启 Codex。
- 从旧版升级时，只移除 `# BEGIN/END codex-usage managed` 或旧产品 marker 包围的 exporter；其余 TOML 原样保留。
- 后台服务仍只监听 `127.0.0.1`，启动后先扫描一次，默认每 60 秒增量扫描。
- JSON/CSV 原字段与 `/api/v1/*` 路由保留；状态响应新增 `accounting_mode: "jsonl_only"` 并保留恒为 false 的兼容字段 `otel_active`，timeseries 点新增稳定 `date`。

## 当前电脑隔离实扫

使用 v2.0.0 Windows amd64 候选二进制、独立临时 `CODEX_USAGE_HOME` 对当前电脑源 JSONL 执行 `scan --rebuild`：

- 491 个 JSONL，52,859 个规范增量事件。
- 468 个有用量 Session；14,187 个重复累计快照被忽略。
- 总量 6,709,967,301 Token；`unattributed=0`；0 条扫描 warning。
- 该结果与 2026-08-01 准确性审计的约 65.77 亿修正口径同量级；源历史在两次检查之间继续增长。
- 扫描只读取源 JSONL；正式数据库和已安装服务未改动。

## v2.0.0 构建产物

`scripts/build.ps1 -Version 2.0.0` 成功生成四个无 CGO 单文件二进制与四项 `SHA256SUMS`：

| 平台 | SHA-256 |
|---|---|
| Windows amd64 | `dedac8535d76dca1124c048c5b3877e3f6941608385e2aa9124cfa5d75ed4f0e` |
| Windows arm64 | `50abc0a28b7190ccf181a20359a3fa52c75d7376c744dc741ea027680f2fe503` |
| Linux amd64 | `b20e910c1678164ae18f5346800f3cca7b864a7b5bc3f58a47d971048a80724b` |
| Linux arm64 | `9e3d7f578b651f7d5894a4ac34829f19f1e7bd52fe76c398f6e6fed70edb6fa5` |

Windows amd64 二进制已原生实跑并报告 `codex-usage 2.0.0 (57ae128-dirty, …)`；`-dirty` 明确表示这是提交前本地候选，干净 tag 的 CI 构建会记录实际发布提交。Linux 与 arm64 目标完成交叉构建；对应平台的原生启动仍由 GitHub Actions runner / 实机继续验证。

## 公开素材

- 1280×640 Social Preview 已更新为 `JSONL only · replay-aware`。
- 960×540 / 14 秒 GIF、1280×720 / 20–30 秒中英 MP4、桌面与 390px 移动截图均由 synthetic Demo 重新生成。
- 素材生成脚本的脱敏断言通过；未使用真实 hostname、machine ID、路径、Thread 或 Session ID。
