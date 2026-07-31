# Codex Usage

`codex-usage` 是一个本地优先、逐电脑统计 Codex 模型 Token 的工具。它是一个 Go 单文件程序，内置：

- 历史 session 增量扫描器
- SQLite 数据库（pure Go、`CGO_ENABLED=0`）
- OTLP/HTTP JSON 指标接收器
- CLI 与 JSON/CSV 导出
- 只绑定 `127.0.0.1` 的离线 Web Dashboard
- Windows 当前用户登录启动项与 Linux `systemd --user` 安装器

它统计的是“运行 Codex 客户端与采集器的当前主机所产生的模型 Token”，不读取 Codex 账号 ID，也不调用账号级 `/usage`。因此一台 Windows 电脑和一台 Linux 服务器使用同一 Codex 账号时，会分别生成各自的 `machine_id`、数据库和 Dashboard。

> “电脑”指运行 Codex 客户端和 `codex-usage` 的主机，而不是 shell/tool 实际执行所在的远程环境。v1 不估算 API 费用或 ChatGPT rate-limit 配额。

## 快速开始

从交付目录选择对应的单文件二进制：

| 系统 | amd64 | arm64 |
|---|---|---|
| Windows | `codex-usage-windows-amd64.exe` | `codex-usage-windows-arm64.exe` |
| Linux | `codex-usage-linux-amd64` | `codex-usage-linux-arm64` |

安装不要求管理员权限：

```powershell
# Windows PowerShell
.\codex-usage-windows-amd64.exe install
```

```bash
# Linux
chmod +x codex-usage-linux-amd64
./codex-usage-linux-amd64 install
```

安装会：

1. 复制程序到用户级目录并生成本机独立 `machine_id`。
2. 初始化仅当前用户可访问的 SQLite 数据库。
3. 扫描本机 `CODEX_HOME` 中已有的 session。
4. 在不覆盖现有 exporter 的前提下，为 Codex 配置本机 OTLP/HTTP JSON endpoint。
5. 启动用户级后台服务。

安装结束后，请自行重启 Codex，让新 OTel 配置对新进程生效。安装器不会强制结束正在运行的任务。

打开 Dashboard：

```text
codex-usage
codex-usage open
```

Linux 无图形环境会输出本机 URL 和 SSH 隧道命令，例如：

```bash
ssh -N -L 43189:127.0.0.1:43189 user@server
```

然后在本地浏览器访问 `http://127.0.0.1:43189`。

## 常用命令

```text
codex-usage summary --since 7d
codex-usage summary --since 30d --json
codex-usage summary --since all --csv
codex-usage scan
codex-usage scan --rebuild
codex-usage doctor
codex-usage config add-home /another/codex-home
codex-usage uninstall
codex-usage uninstall --purge
```

- `scan` 是增量扫描；partial line 会留到下次追加完成后再处理。
- `scan --rebuild` 只重建历史扫描数据，保留已经接收的 OTel 事件。
- `uninstall` 删除服务、程序和工具管理的 Codex 配置，默认保留统计库。
- 只有显式 `uninstall --purge` 才删除统计数据。

## 每台机器的数据位置

默认位置：

| 项目 | Windows | Linux |
|---|---|---|
| Codex Home | `%USERPROFILE%\.codex` | `~/.codex` |
| Codex Usage 状态 | `%LOCALAPPDATA%\codex-usage` | `${XDG_DATA_HOME:-~/.local/share}/codex-usage` |
| 安装程序 | `%LOCALAPPDATA%\Programs\codex-usage\codex-usage.exe` | `~/.local/bin/codex-usage` |
| SQLite | `...\codex-usage\meter.sqlite` | `.../codex-usage/meter.sqlite` |

设置 `CODEX_HOME` 时优先使用该目录。额外目录通过 `config add-home` 添加。用于测试或便携运行时，可以设置 `CODEX_USAGE_HOME` 覆盖工具自己的状态目录。

不要在多台电脑间同步同一个 `CODEX_USAGE_HOME`。如果主动同步同一个 Codex Home，安装前已有 session 不携带可靠的原始机器身份，`doctor` 会给出警告并按 session 去重，但无法把旧历史准确拆回各电脑。

## 核算规则

统一 Token 类型：

```text
input
cached_input
cache_write_input
output
reasoning_output
total
```

- `cached_input` 是 `input` 的子集。
- `reasoning_output` 是 `output` 的子集。
- Dashboard 不把这些子集重复堆叠相加。
- `total` 按 Codex 原始值展示；不能解释为独立生成文字量、费用或账号配额消耗。

### 历史 session

扫描器优先只读最新可识别的 `state_*.sqlite`，获取 canonical rollout 路径、thread 标题、完整项目路径、模型、来源和 `tokens_used`。内部 schema 不兼容时，会退回 `sessions/` 与 `archived_sessions/`。

JSONL 只解码三类小记录：

```text
session_meta
turn_context
event_msg / token_count
```

对 prompt、回复、reasoning、工具输出等无关大记录，只保留最多 16 KiB 的类型探针并流式丢弃，不整行载入内存，也不写入数据库。相关 metadata/token 记录有 8 MiB 硬上限。

每个 session 使用累计 Token 向量计算单调增量：

- 相同累计向量：旧版重复事件，忽略。
- 字段缺失：按旧版兼容，必要时以 `input + output` 补足缺失的 `total`。
- 累计回退：记录可见异常；仅在 `last_token_usage` 有效时作为 `gap_fallback` 计入。
- 损坏 JSON 或超限相关记录：记录 warning，不静默伪造。
- partial line：游标停在该行开头，等待下次追加完成。

若 JSONL 总量低于状态库 `tokens_used`，差额进入 `state_fallback / aggregate_only`。它只属于“历史未归属”累计，不会伪造到某一天。

### 实时 OTel

工具配置并接收官方 `turn.token_usage` 指标，支持 OTLP histogram/sum 的 cumulative 与 delta temporality、producer start-time 变化和累计重置。

在已知 OTel 覆盖窗口内，查询以 OTel 为总量来源，并排除同期 session JSONL 总量；覆盖窗口以外由 session 扫描补位。状态库始终只是历史聚合兜底，三路数据不会直接相加。

官方指标默认可能没有 thread ID 或 cwd。此时机器总量仍以 OTel 为准，但项目、thread 和 session 明细使用同期 JSONL 作为“归属视图”；它不会再加到机器总量上。按项目/session 筛选时也明确使用该归属视图。

v1 接收 OTLP/HTTP JSON。工具管理的 Codex 配置类似：

```toml
[otel]
# BEGIN codex-usage managed
metrics_exporter = { otlp-http = { endpoint = "http://127.0.0.1:43189/v1/metrics", protocol = "json" } }
# END codex-usage managed
```

如果已经配置 `otel.metrics_exporter`，安装器绝不覆盖，只保留历史扫描并报告实时采集冲突。

## Dashboard 与本地 API

Dashboard 显示：

- 今天、7 日、30 日与本机累计 Token
- 按小时/日趋势
- 模型、来源、主任务/Subagent/Guardian/Memory、项目与 thread 分解
- 完整本机项目路径与 thread 标题
- `machine_id`、最后扫描、OTel 状态、历史未归属和覆盖警告

API（均为 loopback）：

```text
GET  /api/v1/status
GET  /api/v1/summary
GET  /api/v1/timeseries
GET  /api/v1/breakdown
GET  /api/v1/sessions
GET  /api/v1/warnings
POST /api/v1/rescan
GET  /api/v1/export?format=json
GET  /api/v1/export?format=csv
POST /v1/metrics
GET  /healthz
```

常见参数：`since=7d|30d|today|all`、`model`、`source`、`agent_type`、`project`、`session_id`、`confidence`、`bucket=hour|day`。

## 隐私与安全

- 不打开或解析 `auth.json`。
- 不解析/保存 prompt、回复、reasoning 内容或工具输出。
- 不保存 Codex 账号 ID。
- 不使用 CDN；HTML、CSS、JavaScript 全部嵌入二进制。
- Dashboard 和 OTLP 仅允许 `127.0.0.1`，没有公网监听配置。
- 程序没有外部上报 HTTP client；唯一客户端探测只访问本机 `/healthz`。
- 数据库、配置、备份和日志使用仅当前用户可访问的权限。
- `config.toml` 修改前先做 TOML 语义检查、受限备份和同目录原子写入；Codex CLI 可用时再运行配置加载检查，失败立即回滚。
- 卸载只移除精确匹配的 managed stanza；发现不完整 marker 时拒绝修改。

`doctor` 会检查服务、状态库 schema 回退、已有 exporter、共享历史、覆盖 warning 和隐私 schema。完整本机路径和 thread 标题是用户要求保留的本地明细，因此导出文件也可能包含这些信息。

## 从源码构建

要求 Go 1.26.5 或兼容的 Go 1.26.x：

```bash
go test ./...
CGO_ENABLED=0 go build -trimpath -o codex-usage ./cmd/codex-usage
```

构建四个平台并生成 `SHA256SUMS`：

```powershell
.\scripts\build.ps1
```

```bash
./scripts/build.sh
```

构建脚本执行单元/API 测试，并生成：

```text
dist/codex-usage-windows-amd64.exe
dist/codex-usage-windows-arm64.exe
dist/codex-usage-linux-amd64
dist/codex-usage-linux-arm64
dist/SHA256SUMS
```

浏览器测试使用 Playwright：

```bash
npm install
npx playwright install chromium
CODEX_USAGE_BIN=./codex-usage npm test
```

流式内存验收（会实际生成大型 fixture，默认 10 GiB）：

```bash
./scripts/memory-acceptance.sh ./codex-usage 10
```

## 已知边界

- OTel 默认资源属性不一定包含 thread ID、cwd 或标题，所以未来实时总量可以是 exact，而 thread 明细仍主要由本机 session metadata 提供。
- Codex 的 `state_*.sqlite` 是内部 schema；本工具先探测列，变化时明确回退，不依赖写入该数据库。
- OTel 接收器离线时无法恢复从未落到 session JSONL 或状态库的指标；这种缺口会显示，不会静默估算。
- v1 不提供跨电脑导入、合并或中心同步。要看两台电脑，分别打开各自 Dashboard。

## 官方依据

- [`/usage` 等 Codex slash commands](https://learn.chatgpt.com/docs/developer-commands#built-in-slash-commands)
- [Codex observability / OTel 配置](https://learn.chatgpt.com/docs/config-file/config-advanced#observability-and-telemetry)
- [Codex 本地 history persistence](https://learn.chatgpt.com/docs/config-file/config-advanced#history-persistence)
- [Windows 与 WSL 的 Codex Home 说明](https://learn.chatgpt.com/docs/windows/windows-app#share-config-auth-and-sessions-with-wsl)

## License

MIT。第三方依赖许可见 `THIRD_PARTY_NOTICES.md`。
