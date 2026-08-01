# Codex Usage 验收记录

执行日期：2026-08-01
执行主机：Windows amd64

## v1.0.0 本地候选版本

- `go test -mod=readonly ./...`：通过。
- `go vet -mod=readonly ./...`：通过。
- Playwright：9/9 通过；保留原 5 项场景，并新增 catalog key 一致性、语言优先级与持久化、英文核心界面、ARIA/日期/数字、Pages 子路径、synthetic API 全接管、无 Cookie/外部请求、定价/扫描/导出及移动端场景。
- CLI：`--lang`、`CODEX_USAGE_LANG`、Windows UI locale / Linux locale 优先级有单元测试；非法显式语言退出码为 2；JSON/CSV schema 不变。Windows amd64 的英文 `summary` 与 `doctor` 完成隔离状态目录实跑。
- Demo：构建过程复用 `internal/web/static` 正式前端，只注入 `scripts/demo-api.js`；API、定价覆写、扫描和导出使用会话内合成数据，刷新复位。
- 素材：1280×640 Social Preview、960×540 / 14 秒 GIF、1280×720 / 20–30 秒中英 MP4 均从 synthetic Demo 自动生成；二进制文本扫描未发现真实用户路径、UUID 或本机用户名形态。
- 构建：`CGO_ENABLED=0` 成功生成 Windows/Linux amd64/arm64 四个二进制，`SHA256SUMS` 恰含四项。Windows amd64 与 WSL Linux amd64 均实跑并报告 `1.0.0`；其余架构完成交叉构建元数据检查。
- 发布前仍需完成 GitHub 侧门槛：推送/合并、`v1.0.0` Release、Pages 在线验证、About/Homepage/Topics/Social Preview、Discussions 与个人主页设置。门槛未完成前不启动外部推广。

## v0.2.0 基线记录

## 自动化检查

- `go test -mod=readonly ./...`：通过。
- `go vet -mod=readonly ./...`：通过。
- API 集成测试：通过，覆盖 loopback、CSP、Origin 检查、既有查询与导出，以及费用估算、价格目录和定价覆写。
- Pricing 单元测试：通过，覆盖 Cached/Cache Write/Reasoning 不重复计费、GPT-5.6 Cache Write 与长上下文倍率、版本快照、未知/累计-only/字段矛盾原因、自定义别名与单价、非法值及大数定点累计。
- Store/配置测试：通过，覆盖本地自然日、排他 `until`、零用量日补齐、OTel/JSONL 规范视图与项目归属，以及只更新 `pricing_overrides` 的原子配置保存。
- 日期准确性回归：通过；同一 Session 跨本地午夜时按每条 `token_count` 的增量与事件时间戳分别落到两天，不使用 Session 最后更新时间归桶。
- 告警迁移：通过；旧库同类同路径记录聚合为一条，并保留首次、最近与累计次数，OTel 防重复信息不计入需复核异常数。
- 历史解析测试：通过，覆盖旧版 `info:null`、重复累计事件、cache write、模型切换、partial line、损坏 JSON、超大无关记录、追加扫描和累计重置。
- OTel 测试：通过，覆盖 cumulative/delta、进程重启、延迟批次、接收器恢复和覆盖去重。
- 配置/安装测试：通过，覆盖保留现有 TOML、已有 exporter 不覆盖、失败回滚及精确移除 managed stanza。
- 升级迁移测试：通过，覆盖旧数据库主文件与 WAL 同步迁移、配置/备份保留、OTel managed stanza 更新及旧状态目录安全清理。

## 真实本机兼容性检查

以只读方式扫描本机现有 Codex Home：

- 识别 454 个 session 文件。
- 首次扫描处理 256,663 条相关记录，生成 32,660 个用量事件。
- 发现并可见记录 7 个累计重置 warning；未静默重复计数。
- SQLite `integrity_check` 返回 `ok`。
- 批量状态库差额核算优化后，增量复扫耗时约 296 ms。

该检查产生的临时统计库未进入源码包或交付目录。

## 10 GiB 流式内存验收

- Fixture 大小：10,737,418,822 bytes。
- 扫描耗时：约 11.65 秒。
- 峰值 working set：12,607,488 bytes（12.02 MiB）。
- 结果：低于 150 MiB 验收上限；1 个合成事件逐字段精确一致；0 个 warning。

大型 fixture 已在验收后删除。

## Dashboard 浏览器验收

使用实际编译的 Windows 二进制与合成统计数据，在 Chromium 中验证：

- “概览 / 每日 / 明细”导航、月份切换、键盘激活、筛选 Chip、价格设置、即时重新估算与扫描反馈均有效。
- 1440px、1024px 与 390 × 844 视口均无整页横向溢出；移动端 Session 使用单列卡片，不依赖表格横向滚动。
- 明暗主题、清晰的 focus 状态和 `prefers-reduced-motion` 均有效。
- 未定价 Token 会降低费用覆盖率，映射保存后无需重启即可重新估算；不会显示为完整费用。
- 浏览器控制台 error 为 0。
- 本机 Microsoft Edge 150 专项运行全部 5 个浏览器场景通过；其中包含计算样式完整性与重复切换不重复请求的断言。HTML 引用内容指纹 CSS/JS，升级后不会混用 Edge 缓存中的旧静态资源。
- 外部脚本、字体、图片和运行时 API 请求为 0；页面资源与价格目录均来自本机二进制。
- OTel 尚未收到数据时明确显示“未收到（历史扫描可用）”。

## 真实规模性能抽查

- 使用当前电脑数据库的只读副本（约 38k 事件）对相同规范费用事件遍历做一次性基准。
- 旧 OFFSET 分页路径约 `27.98s`；单 SQL 流式路径约 `0.362s`，核心遍历约快 `77x`。
- Dashboard 对同一数据版本、同一筛选 URL 复用内存结果；扫描或 OTel 新事件改变数据版本后自动失效，不缓存过期统计。

本轮 Playwright 自动化结果为 **4/4 通过**；桌面与移动端 Dashboard 截图已同步更新。

## 平台构建边界

使用 Go 1.26.5、`CGO_ENABLED=0` 成功生成 Windows/Linux 的 amd64/arm64 四个单文件二进制，并生成 SHA256 校验文件。

本次执行环境只有 Windows amd64，因此 Windows 二进制完成了原生运行与浏览器验收；Linux 二进制完成无 CGO 交叉构建和构建元数据检查，但未在本次本地主机上原生执行。仓库中的 GitHub Actions 工作流包含 Windows 与 Linux 测试/构建任务，可在相应 runner 上继续做原生平台验证。
