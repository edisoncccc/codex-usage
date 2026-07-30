# Codex Meter v0.1.0 验收记录

执行日期：2026-07-30
执行主机：Windows amd64

## 自动化检查

- `go test -mod=readonly ./...`：通过。
- `go vet -mod=readonly ./...`：通过。
- API 集成测试：通过，覆盖 loopback、CSP、Origin 检查、查询与导出。
- 历史解析测试：通过，覆盖旧版 `info:null`、重复累计事件、cache write、模型切换、partial line、损坏 JSON、超大无关记录、追加扫描和累计重置。
- OTel 测试：通过，覆盖 cumulative/delta、进程重启、延迟批次、接收器恢复和覆盖去重。
- 配置/安装测试：通过，覆盖保留现有 TOML、已有 exporter 不覆盖、失败回滚及精确移除 managed stanza。

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

使用实际编译的 Windows 二进制和合成 session，在应用内 Chromium 中验证：

- 桌面与 390 × 844 移动视口均无页面横向溢出。
- 移动端 session 表格可独立横向滚动。
- Agent 筛选、30 日范围切换、明暗主题切换均有效。
- 浏览器控制台 error 为 0。
- 外部 `script`、`link`、`img` 资源为 0；页面资源全部来自本机二进制。
- OTel 尚未收到数据时明确显示“未收到（历史扫描可用）”。

## 平台构建边界

使用 Go 1.26.5、`CGO_ENABLED=0` 成功生成 Windows/Linux 的 amd64/arm64 四个单文件二进制，并生成 SHA256 校验文件。

本次执行环境只有 Windows amd64，因此 Windows 二进制完成了原生运行与浏览器验收；Linux 二进制完成无 CGO 交叉构建和构建元数据检查，但未在本次本地主机上原生执行。仓库中的 GitHub Actions 工作流包含 Windows 与 Linux 测试/构建任务，可在相应 runner 上继续做原生平台验证。
