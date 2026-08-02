<div align="center">

# codex-usage

**同一个 Codex 账号：哪台电脑、哪个模型、哪个项目和会话用掉了 Token？**

*Local-first Codex usage analytics—from machine attribution to models, projects, sessions, and API-equivalent cost.*

[在线体验](https://zjay26.github.io/codex-usage/?lang=zh-CN) · [Windows x64 下载](https://github.com/zJay26/codex-usage/releases/latest/download/codex-usage-windows-amd64.exe) · [Linux x64 下载](https://github.com/zJay26/codex-usage/releases/latest/download/codex-usage-linux-amd64) · [English](README.en.md) / 简体中文

[![CI](https://github.com/zJay26/codex-usage/actions/workflows/ci.yml/badge.svg)](https://github.com/zJay26/codex-usage/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/zJay26/codex-usage?display_name=tag)](https://github.com/zJay26/codex-usage/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/github/license/zJay26/codex-usage)](LICENSE)

</div>

![Codex Usage 12 秒演示：逐电脑 Token、日期下钻、筛选与等价成本](docs/media/codex-usage-demo.gif)

> 演示与在线 Demo 全部使用合成数据；不读取你的文件、不设 Cookie、无埋点或外部请求。

## 30 秒理解

在每台 Windows、WSL 或 Linux 主机分别运行一个单文件程序。它扫描该机的历史 Codex JSONL，并通过 loopback OTel 接收之后的新用量；两种来源按覆盖时间合并和去重，结果只写入该机的 SQLite。Dashboard 因而回答的是**这台电脑用了多少**，不是整个账号用了多少。

逐电脑归属是 codex-usage 最鲜明的入口，但不是终点。它把本机总量继续拆到模型与 Token 类型、项目、Thread、Session、Agent 和本地自然日，并给出 Standard API 等价成本、定价覆盖率与数据质量记录。

Codex 官方 [`/usage`](https://learn.chatgpt.com/docs/developer-commands.md?surface=cli) 适合查看账号级 daily / weekly / cumulative Token 活动；codex-usage 不替代官方视图，而是补上**这些本机 Token 在哪里产生、由什么构成**的可解释归属层。

分析全程保持本地优先：单文件部署、loopback 服务、本机 SQLite，**从不读取 `auth.json` 或对话内容**。

## 从总量到可解释的使用分析

| 你想知道 | codex-usage 给出的视图 |
|---|---|
| 哪台电脑用了 Token？ | 每台 Windows、WSL 或 Linux 主机独立统计，保留清晰机器边界 |
| 用在了什么模型与 Token 类型？ | 模型及 Input、Cached、Cache Write、Output、Reasoning 构成 |
| 哪项工作驱动了用量？ | 项目、Thread、Session，以及主任务 / Subagent / Guardian / Memory 归属 |
| 什么时候发生？ | 今天、7 日、30 日、全部历史、本地自然日与单日下钻 |
| 如果按 API 价格折算大约是多少？ | Standard API 等价成本与明确的 Token 定价覆盖率 |
| 这些数字能否被复核？ | JSONL / OTel 来源、防重覆盖区间、未归属差额与数据质量记录 |

## 它统计什么 / 不统计什么

| 统计 | 不统计 |
|---|---|
| 当前电脑的 Token、模型、来源、项目、Thread、Session、Agent 和本地自然日 | 账号在其他电脑上的用量 |
| 历史 session JSONL 与未来 `turn.token_usage` OTel 指标 | 账号配额、订阅余额或真实账单 |
| Standard API 文本 Token 的等价成本与定价覆盖率 | prompt、回复、reasoning、工具输出或 `auth.json` |
| 去重记录、覆盖缺口和无法按日期归属的历史差额 | 云同步、远程遥测或第三方分析 |

> “电脑”指运行 Codex 客户端和采集器的主机，不是 shell 或 tool 实际执行的远程环境。费用是等价估算，不是 OpenAI 真实账单。

## 直接安装

Windows amd64 / x64（无需管理员权限）：

```powershell
Invoke-WebRequest https://github.com/zJay26/codex-usage/releases/latest/download/codex-usage-windows-amd64.exe -OutFile codex-usage.exe
.\codex-usage.exe install
```

Linux amd64 / x64：

```bash
curl -fL https://github.com/zJay26/codex-usage/releases/latest/download/codex-usage-linux-amd64 -o codex-usage
chmod +x codex-usage
./codex-usage install
```

需要 arm64？从 [最新 Release](https://github.com/zJay26/codex-usage/releases/latest) 下载 `windows-arm64.exe` 或 `linux-arm64`。下载后可先用同页的 `SHA256SUMS` 校验。英文安装输出使用：

```text
codex-usage --lang en install
```

安装器会创建本机数据库、扫描历史 session、安全添加 loopback OTel endpoint，并启动用户级后台服务；不会覆盖已有第三方 metrics exporter。安装后在方便时重启 Codex，让新进程加载实时采集配置。随后运行 `codex-usage` 即可打开 Dashboard。

Linux 服务器没有桌面环境时，程序会打印 SSH 隧道命令。在自己的电脑执行命令后访问 `http://127.0.0.1:43189`。

## 功能亮点

| 功能 | 你得到什么 |
|---|---|
| 标志性的逐电脑归属 | 每台机器生成独立 `machine_id` 和 SQLite 数据库，不把账号其他电脑混进来 |
| 历史 + 实时 | 首次扫描已有 JSONL，之后接收官方 OTel 指标 |
| 防重 | OTel、JSONL 与状态库按明确覆盖规则合并，不直接相加 |
| 每日下钻 | 连续自然日脉冲带、月历、零用量日与单日模型构成 |
| 多维使用分析 | 按模型、Token 类型、来源、项目、Thread、Session、主任务/Subagent/Guardian/Memory 理解用量 |
| 成本洞察 | 查询时估算 Standard API 等价成本并显示定价覆盖率，未知部分不伪装成零费用 |
| 可审计质量 | 明示来源、防重记录、覆盖缺口和未按日期归属的历史差额 |
| 本地优先 | 只监听 `127.0.0.1`，资源嵌入二进制，无运行时外部请求 |
| 单文件部署 | Windows/Linux、amd64/arm64、无 CGO、无需外部数据库服务 |
| 双语 | Dashboard 与 CLI 支持 `zh-CN` / `en`，URL、按钮、环境变量均可切换 |

<details><summary>查看静态桌面和 390 × 844 移动端截图</summary>

![Codex Usage Dashboard](docs/images/dashboard.png)

![Codex Usage 移动端 Dashboard](docs/images/dashboard-mobile.png)

</details>

## 它是怎么运行的

`codex-usage` 是一个 Go 单文件程序，里面同时包含采集器、SQLite、HTTP API 和 Web Dashboard。安装后，它以用户级后台服务运行。

```mermaid
flowchart LR
    A[Codex session JSONL] -->|历史增量扫描| C[归一化 Token 事件]
    B[Codex OTel 指标] -->|实时 OTLP/HTTP| C
    S[state SQLite] -->|只做历史差额兜底| C
    C --> D[(本机 SQLite)]
    D --> P[查询时费用估算]
    D --> E[127.0.0.1 API]
    P --> E
    E --> F[Dashboard]
    E --> G[CLI / JSON / CSV]
```

### 1. 历史扫描

程序只读当前电脑的 `CODEX_HOME`。它优先从 Codex 状态库取得 session 路径、项目和 Thread 信息，再流式读取 `sessions/` 与 `archived_sessions/` 中的 JSONL。

每个 session 里的 Token 是累计值。程序在**每一条** `token_count` 记录处保存累计向量，用“本次累计值 - 上次累计值”得到这一次的增量，并把增量归到该条记录时间戳对应的本地自然日；不会按 session 的最后更新时间把整段历史塞到同一天。重复扫描仍由稳定事件 ID 与游标去重。超大的 prompt、回复和工具输出记录会被跳过，不会整行载入内存，也不会写进数据库。

状态库中只有累计总量、没有事件时间的差额会保持“未归属日期”，只进入“全部”累计，不会被猜测或平均摊到某一天。OpenAI 的 [`account/usage/read`](https://learn.chatgpt.com/docs/app-server#7-token-usage-chatgpt) 是由 Codex 服务返回的 ChatGPT 账号 Token 活动与可选每日桶；本工具只统计当前电脑的本地来源，两者的范围并不相同，官方文档也没有规定每日桶与本地自然日必须使用相同的时区口径。

### 2. 实时采集

安装器会在不覆盖用户现有 exporter 的前提下，为 Codex 配置本机 OTLP/HTTP JSON endpoint：

```text
http://127.0.0.1:43189/v1/metrics
```

新启动的 Codex 进程把官方 `turn.token_usage` 指标发到这个地址。接收器只监听 loopback，外部机器无法直接访问。

### 3. 合并但不重复计算

- OTel 覆盖到的时间段，以 OTel 为机器总量
- OTel 启用前或离线期间，用 session JSONL 补位
- 状态库里的 `tokens_used` 只用于无法分配日期的历史差额
- 项目、Thread 和 session 明细来自本机 JSONL 归属信息，不会再次加到机器总量

这套规则避免把 OTel、JSONL 和状态库三份数据直接相加。

重复出现的同类数据质量记录会按“类型 + 本地路径”聚合，保留首次、最近时间和累计次数。`state_fallback_suppressed_otel` 表示防重规则成功阻止了状态库差额与 OTel 重复相加，属于审计信息，不计入需复核异常数；累计回退、坏记录或无效时间戳仍会明确保留。

### 4. 展示与服务

后台服务启动时先扫描一次，之后默认每 60 秒增量扫描。Dashboard 和 CLI 都查询同一个本机 SQLite。没有任何中心服务器，也没有跨电脑同步；要看两台电脑，就分别打开两台电脑的 Dashboard。

Dashboard 固定为“概览 / 每日 / 明细”三个一级视图。概览默认显示最近 7 个本地自然日；每日视图补齐零用量日期并支持月历下钻；明细视图一次只展开模型、来源、Agent、项目或 Thread 中的一个维度。

### 5. Standard API 等价成本

费用在查询时流式读取已经过去重、归属规则筛选后的规范事件，不写入 SQLite，也不会改变原有 Token 统计。计算使用定点 nano-USD：Cached Input 与 Cache Write 从 Input 中扣除，Reasoning 已包含在 Output 中，不会重复收费。

内置 Standard 文本价格核对日期为 **2026-07-31**，单位均为 USD / 1M Token：

| 模型 | Input | Cached | Cache Write | Output |
|---|---:|---:|---:|---:|
| [GPT-5.6 Sol](https://developers.openai.com/api/docs/models/gpt-5.6-sol) | 5.00 | 0.50 | 6.25 | 30.00 |
| [GPT-5.6 Terra](https://developers.openai.com/api/docs/models/gpt-5.6-terra) | 2.00 | 0.20 | 2.50 | 12.00 |
| [GPT-5.6 Luna](https://developers.openai.com/api/docs/models/gpt-5.6-luna) | 0.20 | 0.02 | 0.25 | 1.20 |
| [GPT-5.5](https://developers.openai.com/api/docs/models/gpt-5.5) | 5.00 | 0.50 | 未公开 | 30.00 |
| [GPT-5.4](https://developers.openai.com/api/docs/models/gpt-5.4) | 2.50 | 0.25 | 未公开 | 15.00 |
| [GPT-5.4 mini](https://developers.openai.com/api/docs/models/gpt-5.4-mini) | 0.75 | 0.075 | 未公开 | 4.50 |
| [GPT-5.3-Codex](https://developers.openai.com/api/docs/models/gpt-5.3-codex) | 1.75 | 0.175 | 未公开 | 14.00 |
| [GPT-5.2-Codex](https://developers.openai.com/api/docs/models/gpt-5.2-codex) | 1.75 | 0.175 | 未公开 | 14.00 |

GPT-5.6 的 Cache Write 使用官方“普通 Input 的 1.25 倍”规则。GPT-5.4、GPT-5.5 和 GPT-5.6 的单个精确事件在 Input 超过 272K 时应用长上下文倍率；只有累计总量或无法确认请求边界的部分保持未定价。页面始终同时展示费用和 Token 定价覆盖率，未知模型不会被当成零费用。

内部模型可以在 Dashboard 的“定价设置”中映射到一个明确的内置公开模型，或填写自定义单价。等价配置如下，保存后无需重启：

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

本机 API 提供 `GET /api/v1/cost-estimate`、`GET /api/v1/pricing` 和 `PUT /api/v1/pricing/overrides`。价格随二进制嵌入，运行时不会抓取网页。

## 常用命令

```text
codex-usage                         打开 Dashboard
codex-usage summary --since 7d     查看近 7 日摘要
codex-usage summary --since 30d --json
codex-usage summary --since all --csv
codex-usage scan                    增量扫描
codex-usage scan --rebuild          重建历史扫描数据
codex-usage doctor                  检查路径、服务和数据缺口
codex-usage config add-home PATH    添加额外 CODEX_HOME
codex-usage uninstall               卸载程序，保留统计库
codex-usage uninstall --purge       卸载并删除统计数据
```

Dashboard 支持 `?lang=en|zh-CN` 和页头语言按钮；URL 参数优先于已保存语言，其次跟随浏览器。CLI 支持全局 `--lang` 和 `CODEX_USAGE_LANG`，例如 `CODEX_USAGE_LANG=en codex-usage doctor`。`--json` 与 `--csv` 字段不随语言改变。

## 数据存在哪里

| 内容 | Windows | Linux |
|---|---|---|
| Codex Home | `%USERPROFILE%\.codex` | `~/.codex` |
| codex-usage 状态 | `%LOCALAPPDATA%\codex-usage` | `${XDG_DATA_HOME:-~/.local/share}/codex-usage` |
| 安装后的程序 | `%LOCALAPPDATA%\Programs\codex-usage\codex-usage.exe` | `~/.local/bin/codex-usage` |
| SQLite | `...\codex-usage\usage.sqlite` | `.../codex-usage/usage.sqlite` |

设置 `CODEX_USAGE_HOME` 可以覆盖工具自己的状态目录。不要在多台电脑之间同步这个目录，否则逐电脑边界会失真。

`usage.sqlite` 会在首次安装、启动服务、扫描或查询需要打开状态库时自动创建，并持续保存在上述状态目录。程序使用内嵌的纯 Go SQLite 驱动，不要求预装 SQLite、数据库服务、Python、Docker 或 CGO；只需要当前用户对状态目录有读写权限，并有足够磁盘空间。应优先使用本机磁盘，不建议把活动数据库放在网盘、网络共享或多台电脑共同写入的同步目录。

## 隐私边界

`codex-usage` 的边界是刻意收紧的：

- 不读取或解析 `auth.json`
- 不保存 prompt、回复、reasoning 或工具输出
- 不保存 Codex 账号 ID
- 不使用 CDN，页面资源全部离线内嵌
- 不监听 `127.0.0.1` 以外的地址
- 不读取 OpenAI 真实账单或 ChatGPT rate-limit / 账号配额；只提供当前电脑的 Standard API 等价成本估算
- 定价目录随二进制嵌入，运行时不会为费用功能访问外部网络

本机完整项目路径和 Thread 标题会用于归属视图，因此导出的 JSON/CSV 也可能包含这些本机信息。

## 从源码构建

需要 Go 1.26.x：

```bash
go test ./...
CGO_ENABLED=0 go build -trimpath -o codex-usage ./cmd/codex-usage
```

构建全部平台：

```powershell
# Windows
.\scripts\build.ps1
```

```bash
# Linux
./scripts/build.sh
```

Dashboard 测试：

```bash
npm ci
npx playwright install chromium
go build -trimpath -o codex-usage ./cmd/codex-usage
CODEX_USAGE_BIN=./codex-usage npm test
```

更多验收数据见 [ACCEPTANCE.md](ACCEPTANCE.md)。问题反馈前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md)；涉及本机数据或路径的安全问题请按 [SECURITY.md](SECURITY.md) 私密报告。

## 已知边界

- v1 不做跨电脑聚合；每台机器独立查看
- Codex OTel 默认可能不带 Thread ID 或 cwd，此时总量仍准确，归属视图依赖同期 JSONL
- 主动同步同一个 Codex Home 后，安装前的历史无法可靠拆回原始电脑
- `total` 按 Codex 原始值展示，不等于独立生成文字量、真实账单或账号配额；API 等价成本只是按当前价格对本机 Token 的重新折算

## License

[MIT](LICENSE) © Codex Usage contributors
