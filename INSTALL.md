# Codex Usage Dashboard 安装指南

[简体中文](INSTALL.md) · [English](INSTALL.en.md)

本页是 `codex-usage` 的中文安装规范。人工用户和本地 AI 使用同一个 Go CLI、同一组当前用户路径和同一套确认与回执语义。

## 1. 当前状态

本仓库目前是 **source-only**：[`install-policy.json`](install-policy.json) 中的 `binary_release_enabled` 为 `false`，Windows `publisher_subject` 为 `null`。仓库没有可信二进制 Release，也不提供 EXE 下载。

这意味着当前安装必须在用户明确同意后从源码构建。不要根据资产命名猜测未来下载地址，也不要从上游 Release、镜像站、网盘或搜索结果获取二进制。代码签名的当前状态和未来门禁见 [CODE_SIGNING.md](CODE_SIGNING.md)。

## 2. 支持范围与前置条件

- 规范仓库仅为 <https://github.com/edisoncccc/codex-usage>。
- 支持 Windows 与 Linux 的 `amd64`、`arm64`；当前不支持 macOS。
- 需要 Go 1.26 或更高版本；以 [`go.mod`](go.mod) 为准。
- 获取源码必须使用 Git；Git 不可用时停止，不提供未验证的源码下载替代路径。
- 安装只使用当前用户权限，不请求管理员权限或 `sudo`。
- Windows 和 Linux 都需要对当前用户的程序目录、状态目录和本地临时目录具有写权限。

当前源码构建使用内嵌的纯 Go SQLite 驱动，不需要另装 SQLite、Python、Docker 或 C 编译器。

## 3. 让 AI 安装

把唯一规范仓库链接交给本地 AI，并让它按照本节执行即可。AI 不应仅凭 README 中的自然语言猜测成功，而应读取机器策略和 JSON Lines 终态。

### 3.1 唯一可信入口

AI 必须先完成以下只读检查：

1. 确认用户给出的 URL 精确为 `https://github.com/edisoncccc/codex-usage`；不接受拼写相似仓库、镜像或第三方下载页。
2. 从该仓库根目录读取 [`install-policy.json`](install-policy.json)，确认 `canonical_repository`、当前 OS/架构、安装范围和 `binary_release_enabled`。
3. 当前策略为 `binary_release_enabled=false` 时，明确说明只能从源码构建、需要 Go 1.26+，然后再进入一次确认。
4. 若未来策略启用预编译渠道，但下载、哈希、签名、不可变 Release 或 Attestation 验证失败，必须停止。验证失败不得自动改走源码构建。

### 3.2 AI 执行协议

用户确认后，AI 按以下顺序执行：

1. 在新的专用目录中从规范仓库取得源码；目录已存在、clone 失败、Git remote 不精确匹配或 commit 无法读取时立即停止，不得复用旧同名目录。核验后记录并展示实际 commit。
2. 检查 `go version` 为 1.26+，运行 `go test ./...`，再以 `CGO_ENABLED=0` 构建当前平台程序。
3. 运行 `version --json`，记录版本、commit、dirty、OS 和架构；源码直构可能如实显示 `commit=dev`、`dirty=true`，不得改写成已验证 Release 身份。
4. 只有已经取得本节所述的一次确认后，运行 `install --yes --json`。`--yes` 表示调用方已取得确认，不是跳过确认的许可。
5. 按行解析 stdout；以唯一 `result` 或 `error` 终态及稳定 `code` 判断结果，不匹配中文或英文句子。
6. 安装成功后运行 `doctor --json`，确认服务身份、状态目录、数据库和 `http://127.0.0.1:43189`。
7. 向用户报告实际写入路径、服务模式、验证状态、扫描摘要和卸载命令。任何失败都要保留原始稳定 code，不得声称安装成功。

AI 不得关闭系统安全功能、提权、安装后台自动更新任务，或把网络响应直接交给 shell / PowerShell 表达式执行器。

### 3.3 一次确认清单

正常源码安装只要求一次确认。AI 必须在确认前一次性展示：

- 规范仓库、当前 commit、目标 OS/架构，以及当前是 source-only 源码构建；
- 为取得源码和 Go modules 将访问 GitHub 及本机 Go 配置的模块来源；当前安装器、后台服务和 `update` 不会外联；
- 将运行的测试、构建、`install --yes --json` 和 `doctor --json` 命令；
- Windows 程序/状态路径或 Linux 程序/状态路径；
- Windows `HKCU` 当前用户启动项，或 Linux `systemd --user`（不可用时回执可能为当前用户 detached fallback）；
- 服务只监听 `127.0.0.1:43189`；
- 首次扫描的每个本地 `CODEX_HOME`，以及会读取的 session JSONL 范围；
- 默认卸载保留数据库和配置，只有独立确认的 purge 才删除规范状态目录；
- 不使用管理员权限或 `sudo`。

若发现需要提权、来源不明的已有安装、路径/端口冲突、状态目录身份异常或清除数据请求，停止正常流程并单独报告，不能把它混入一次确认。

## 4. 人工安装

人工安装与 AI 安装调用同一套 CLI。以下命令不会从网络直接执行脚本，而是在当前目录下创建新的专用源码目录；目标已存在时会拒绝复用。

### 4.1 Windows PowerShell

```powershell
$ErrorActionPreference = "Stop"
$Repository = "https://github.com/edisoncccc/codex-usage"
$SourceDir = Join-Path (Get-Location) "codex-usage-source"

if (Test-Path -LiteralPath $SourceDir) {
    throw "拒绝复用已有源码目录：$SourceDir"
}

git clone --origin origin -- $Repository $SourceDir
if ($LASTEXITCODE -ne 0) { throw "git clone 失败，退出码：$LASTEXITCODE" }

Set-Location -LiteralPath $SourceDir
$Origin = git remote get-url origin
if ($LASTEXITCODE -ne 0) { throw "读取 origin 失败，退出码：$LASTEXITCODE" }
if ($Origin -cne $Repository) { throw "origin 不匹配：$Origin" }

$Commit = git rev-parse --verify HEAD
if ($LASTEXITCODE -ne 0) { throw "读取 commit 失败，退出码：$LASTEXITCODE" }
Write-Output "Commit: $Commit"

Get-Content -LiteralPath .\install-policy.json
go version
if ($LASTEXITCODE -ne 0) { throw "go version 失败，退出码：$LASTEXITCODE" }
go test ./...
if ($LASTEXITCODE -ne 0) { throw "go test 失败，退出码：$LASTEXITCODE" }
$env:CGO_ENABLED = "0"
go build -trimpath -o codex-usage.exe ./cmd/codex-usage
if ($LASTEXITCODE -ne 0) { throw "go build 失败，退出码：$LASTEXITCODE" }
& .\codex-usage.exe version --json
if ($LASTEXITCODE -ne 0) { throw "version 失败，退出码：$LASTEXITCODE" }
& .\codex-usage.exe install
if ($LASTEXITCODE -ne 0) { throw "install 失败，退出码：$LASTEXITCODE" }
& .\codex-usage.exe doctor --json
if ($LASTEXITCODE -ne 0) { throw "doctor 失败，退出码：$LASTEXITCODE" }
```

PowerShell 对每个 `git`、`go` 和 `codex-usage.exe` 原生命令都立即检查 `$LASTEXITCODE`；任一步失败都不会继续测试、构建或安装。`install` 会显示完整预检清单并读取一次 `yes` 或 `是`。希望 CLI 使用英文时，可把全局参数放在任意位置，例如 `.\codex-usage.exe --lang en install`。

### 4.2 Linux bash

```bash
set -euo pipefail

repository='https://github.com/edisoncccc/codex-usage'
source_dir="${PWD}/codex-usage-source"

if [[ -e "$source_dir" ]]; then
  printf '拒绝复用已有源码目录：%s\n' "$source_dir" >&2
  exit 1
fi

git clone --origin origin -- "$repository" "$source_dir"
cd -- "$source_dir"

origin="$(git remote get-url origin)"
if [[ "$origin" != "$repository" ]]; then
  printf 'origin 不匹配：%s\n' "$origin" >&2
  exit 1
fi

commit="$(git rev-parse --verify HEAD)"
printf 'Commit: %s\n' "$commit"

cat ./install-policy.json
go version
go test ./...
CGO_ENABLED=0 go build -trimpath -o codex-usage ./cmd/codex-usage
./codex-usage version --json
./codex-usage install
./codex-usage doctor --json
```

`set -euo pipefail` 保证 clone、来源核验、测试、构建或程序执行失败时立即停止。`install` 会显示完整预检清单并读取一次 `yes`。无桌面环境时，程序会提供 SSH 隧道提示；Dashboard 仍只监听服务器本机的回环地址。

## 5. 从源码构建

如果源码已在本机，无需再次 clone。先核对 `git remote get-url origin` 与 `git rev-parse HEAD`，再使用对应平台命令。

### 5.1 Windows 构建

```powershell
$ErrorActionPreference = "Stop"
$Repository = "https://github.com/edisoncccc/codex-usage"
$Origin = git remote get-url origin
if ($LASTEXITCODE -ne 0) { throw "读取 origin 失败，退出码：$LASTEXITCODE" }
if ($Origin -cne $Repository) { throw "origin 不匹配：$Origin" }
$Commit = git rev-parse --verify HEAD
if ($LASTEXITCODE -ne 0) { throw "读取 commit 失败，退出码：$LASTEXITCODE" }
Write-Output "Commit: $Commit"
go version
if ($LASTEXITCODE -ne 0) { throw "go version 失败，退出码：$LASTEXITCODE" }
go test ./...
if ($LASTEXITCODE -ne 0) { throw "go test 失败，退出码：$LASTEXITCODE" }
$env:CGO_ENABLED = "0"
go build -trimpath -o codex-usage.exe ./cmd/codex-usage
if ($LASTEXITCODE -ne 0) { throw "go build 失败，退出码：$LASTEXITCODE" }
& .\codex-usage.exe version --json
if ($LASTEXITCODE -ne 0) { throw "version 失败，退出码：$LASTEXITCODE" }
```

### 5.2 Linux 构建

```bash
set -euo pipefail

repository='https://github.com/edisoncccc/codex-usage'
origin="$(git remote get-url origin)"
if [[ "$origin" != "$repository" ]]; then
  printf 'origin 不匹配：%s\n' "$origin" >&2
  exit 1
fi
commit="$(git rev-parse --verify HEAD)"
printf 'Commit: %s\n' "$commit"
go version
go test ./...
CGO_ENABLED=0 go build -trimpath -o codex-usage ./cmd/codex-usage
./codex-usage version --json
```

测试、构建或首次执行任一步失败，都应停止安装并保留错误。不要把“编译成功”写成“测试通过”。

## 6. 安装、进度与机器回执

### 6.1 人工交互

人工用户运行：

```text
codex-usage install
```

CLI 展示规范仓库、源码来源、安装/状态路径、用户级服务、回环地址、扫描范围和数据保留语义，然后只询问一次。

### 6.2 AI 或自动化调用

取得一次确认后，机器调用的规范形式是：

```text
codex-usage install --yes --json
```

JSON 模式未带 `--yes` 时不会读取 stdin，而是以非零退出和 `confirmation_required` 终态返回预检详情。

### 6.3 进度和终态

`--json` 的 stdout 是 JSON Lines：每行一个完整 JSON 对象。扫描等长步骤最长约 4 秒就会输出新的进度或心跳，包含 homes、文件、记录、事件和 warning 计数，不会只留下闪烁光标。

每次命令尽力只发一个终态：成功为 `event=result`，失败为 `event=error`。安装成功的 `code` 为 `install_complete`，回执包含构建身份、绝对路径、服务模式、Dashboard URL、扫描摘要、数据保留状态、验证三态和规范卸载命令。源码构建的 Release、Attestation 与 Authenticode 项必须是 `not_applicable`；本地候选文件复制后的 SHA256 真正一致时，相关项才是 `verified`。

## 7. 健康检查

人工检查：

```text
codex-usage doctor
```

机器检查：

```text
codex-usage doctor --json
```

`doctor` 检查配置、状态 marker、数据库、Codex Home、回环监听和运行中服务的完整构建身份。机器终态为 `health_check_complete` 才表示检查没有 error；warning 仍会保留在 checks 中。浏览器入口为 <http://127.0.0.1:43189>。

## 8. 更新

当前 Phase A 更新渠道关闭。以下两个命令都会返回非零退出、唯一 `release_channel_disabled` 终态，并保持 `checked=false`、`modified=false`：

```text
codex-usage update --check --json
codex-usage update --yes --json
```

它们不创建网络客户端、不下载、不写文件，也不自动改走源码。需要更新源码构建时，人工取得规范仓库的新 commit，重新运行测试、构建和安装确认流程。

## 9. 卸载与数据保留

### 9.1 默认保留数据

人工用户运行 `codex-usage uninstall` 并确认；已取得确认的机器调用使用：

```text
codex-usage uninstall --yes --json
```

默认卸载停止并移除本项目拥有的当前用户服务与程序，但保留 `usage.sqlite` 和 `config.json`。Windows 正在运行的程序可能返回 `removal_scheduled=true`；这表示退出后才删除，不能声称路径已经消失。Linux 正常同步删除时该值为 `false`。

### 9.2 明确清除数据

purge 会永久删除规范状态目录中的本项目数据库和配置。人工用户先运行 `codex-usage uninstall --purge` 查看绝对目标并单独确认；AI 必须先把同一绝对路径展示给用户，取得独立确认后才能运行：

```text
codex-usage uninstall --purge --yes --json
```

路径、marker、安装记录或可执行文件 SHA256 验证失败时，卸载会在删除前停止。purge 不会触碰 Codex 自身的 session JSONL、`auth.json`、其他程序数据或第三方启动项。

## 10. 默认路径与 CODEX_USAGE_HOME 覆盖

**默认布局（未设置 `CODEX_USAGE_HOME`）**

| 内容 | Windows | Linux |
|---|---|---|
| 程序 | `%LOCALAPPDATA%\Programs\codex-usage\codex-usage.exe` | `~/.local/bin/codex-usage` |
| 状态目录 | `%LOCALAPPDATA%\codex-usage` | `${XDG_DATA_HOME:-~/.local/share}/codex-usage` |
| 数据库 | `%LOCALAPPDATA%\codex-usage\usage.sqlite` | `${XDG_DATA_HOME:-~/.local/share}/codex-usage/usage.sqlite` |
| 配置 | `%LOCALAPPDATA%\codex-usage\config.json` | `${XDG_DATA_HOME:-~/.local/share}/codex-usage/config.json` |
| 安装记录 | `%LOCALAPPDATA%\codex-usage\install.json` | `${XDG_DATA_HOME:-~/.local/share}/codex-usage/install.json` |
| 默认 Codex Home | `%USERPROFILE%\.codex` | `~/.codex` |
| 后台服务 | `HKCU` 当前用户启动项 | `systemd --user`，必要时当前用户 fallback |

**覆盖布局**

设置 `CODEX_USAGE_HOME=<ABS>` 时，状态根是 `<ABS>`，并同时改变安装程序位置；不会在其下再创建额外的 `state` 子目录。

| 内容 | Windows | Linux |
|---|---|---|
| 程序 | `<ABS>/bin/codex-usage.exe` | `<ABS>/bin/codex-usage` |
| 状态根 | `<ABS>` | `<ABS>` |
| 配置 | `<ABS>/config.json` | `<ABS>/config.json` |
| 数据库 | `<ABS>/usage.sqlite` | `<ABS>/usage.sqlite` |
| 安装记录 | `<ABS>/install.json` | `<ABS>/install.json` |
| 备份目录 | `<ABS>/backups` | `<ABS>/backups` |

`<ABS>` 必须是当前用户可写的专用绝对目录，不能是文件系统根目录、用户主目录或含无关文件的目录；实际分隔符遵循操作系统。`CODEX_HOME` 只选择 Codex 数据源，与上述程序/状态布局不同。不要在多台电脑间同步活动状态根。

本文使用裸 `codex-usage` 的命令只在实际程序目录已加入 `PATH` 时成立。否则请调用安装 JSON 终态回执中的绝对 `result.install_path`；设置覆盖后，它就是上表对应的 `<ABS>/bin/...` 路径。

## 11. 网络与隐私边界

- 获取源码和 Go modules 时会访问 GitHub 及本机 Go 配置的模块来源。
- 当前 `install`、`doctor`、后台服务和渠道关闭的 `update` 不进行外部网络请求。
- 后台服务不做遥测、云同步、后台更新或价格抓取，只监听 `127.0.0.1`。
- 扫描器只选择性解析本地 JSONL 中统计所需的 metadata、任务边界和 `token_count`；不解析或持久化 prompt、回复、reasoning 或工具输出正文。
- 程序从不读取或解析 `auth.json`，也不读取真实账单、订阅额度或账号配额。
- 数据库会保留用于归属的本机项目路径和 Thread 标题，导出 JSON/CSV 前请自行脱敏。

## 12. Windows Smart App Control

Windows Smart App Control 或组织应用控制策略可能阻止 Go 在临时目录生成的测试程序、源码构建产物或首次执行。出现系统安全提示、光标长时间无输出或 `operation did not complete successfully` 等阻断时：

1. 停止当前安装流程，并把被阻止的精确命令和路径报告给用户。
2. 不关闭 Smart App Control、Defender、组织策略或签名检查。
3. 不通过改扩展名、复制到特殊目录、加入排除项或其他方式规避控制。
4. 不把只完成编译写成测试或执行已经通过。

在当前 source-only 阶段，如果安全策略不允许本机源码构建和执行，就没有受支持的自动安装替代方案。保持未安装状态，等待未来通过全部签名门禁的可信发布。

## 13. 失败处理与后续动作

- `confirmation_required`：展示详情并取得用户确认；不要自行补 `--yes`。
- `source_build_blocked`：源码构建或候选程序不可用；停止并保留错误。
- `permission_required`：当前用户路径不可写；不要改用管理员或 `sudo`。
- `existing_install_untrusted`：已有程序、记录、端口或路径身份不可信；停止，不覆盖。
- `health_check_failed`：新服务未通过身份/健康检查；安装器会尝试回滚，AI 应报告原始错误与回滚详情。
- `release_channel_disabled`：当前仍是 source-only；不代表网络故障，也不应触发自动下载或自动源码降级。

需要报告安装链、路径边界或签名安全问题时，请按 [SECURITY.md](SECURITY.md) 使用 GitHub 私密漏洞报告入口，并只提交最小化、已脱敏的信息。
