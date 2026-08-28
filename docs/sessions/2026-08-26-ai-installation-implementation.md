# 2026-08-26 AI 安装协议实施会话

## 工作目标

在已经公开的 `edisoncccc/codex-usage` Fork 上完成已批准的阶段 A：让人工用户和本地 AI 使用同一个 Go CLI，以可确认、可回执、可回滚的方式从源码安装 **Codex Usage Dashboard**，同时继续保持 source-only 发布边界。

完成标准包括：

- 不重写 Go，不增加 Node/Python 运行时依赖。
- Windows 与 Linux 均只安装到当前用户范围，不提权、不绕过系统安全策略。
- `install --yes --json`、`scan --json`、`update --json`、`uninstall --yes --json` 提供稳定 JSON Lines 事件、进度心跳和唯一终态。
- 安装前验证候选程序、已有安装记录和摘要；安装中覆盖服务、程序、配置迁移和记录的事务回滚；安装后验证完整构建身份。
- 默认卸载保留本地配置和数据库，只有显式 `--purge --yes` 才清除产品拥有的数据。
- GitHub Actions 在 Windows/Ubuntu 上执行真实当前用户生命周期；CI、Pages 和 Release 均不得自动上传或发布二进制。
- 全部本地门禁、隐私扫描、代码复核与托管 CI 通过后，才把实施结果视为完成。

## 执行步骤

1. 将 `.github/workflows/release.yml` 收紧为手动触发、只读权限、仅运行 `internal/installpolicy` 的 source-only 哨兵，并以 `install-policy.json` 锁定可信仓库、平台、签名和安装边界。
2. 建立统一机器事件协议：固定 `schema_version/event/phase/status/timestamp/code`，由命令层保证一个最终 `result` 或 `error`，长操作持续输出进度。
3. 增加私有安装记录和 SHA256 信任判断，区分 fresh、same、upgrade、downgrade 与 untrusted；不执行来源不明的旧程序来读取版本。
4. 为扫描器增加同步进度快照、阶段进度和心跳，并把解析警告与数据库/状态写入硬错误分离；扫描分类和游标进度在同一事务中提交。
5. 增加 `/api/health/identity` 与 `doctor --json` 的完整版本、commit、dirty、build date、OS/arch 和服务程序摘要核验。
6. 把程序暂存、旧状态迁移、服务切换、安装记录和健康检查纳入生命周期事务；任何激活前后失败均按拥有关系回滚。
7. 完成 `install` 的一次确认、人类输出和 AI JSON 回执；`--yes` 只表示调用方已完成确认，不代表跳过安全检查。
8. 完成关闭渠道的 `update` 语义和安全 `uninstall`；Windows 正确报告 scheduled self-delete，Linux/Windows 均验证服务与 PID 归属。
9. 编写中英文 `INSTALL`、`CODE_SIGNING`，同步更新 README、CONTRIBUTING 与 SECURITY；明确当前只提供源码，SignPath 尚未申请或获批。
10. 增加仅限 GitHub-hosted runner 的 Windows/Linux 生命周期脚本，覆盖首次安装、幂等重装、停止后修复、升级、扫描、默认卸载、重装和 purge。
11. 根据规格和质量复核继续加固：扫描状态错误传播、原子游标、匿名会话稳定身份、Linux user bus 回退、pidfd、精确进程归属、locale 固定、提交后清理继续、配置写入前拒绝不可信安装、服务状态精确回滚，以及确认前绑定候选程序映像。
12. 启用 Fork Actions，读取每轮原始失败日志，以红测修正托管环境差异，直至 6/6 作业全部通过。

## 修改文件

本阶段修改或新增的受跟踪文件按责任分组如下。

### 发布策略与公开文档

- `.github/workflows/ci.yml`
- `.github/workflows/release.yml`
- `install-policy.json`
- `README.md`
- `README.en.md`
- `INSTALL.md`
- `INSTALL.en.md`
- `CODE_SIGNING.md`
- `CODE_SIGNING.en.md`
- `CONTRIBUTING.md`
- `SECURITY.md`
- `docs/superpowers/specs/2026-08-26-ai-installation-design.md`
- `docs/superpowers/plans/2026-08-26-ai-installation-protocol.md`
- `docs/sessions/2026-08-26-ai-installation-design.md`
- `docs/sessions/2026-08-26-ai-installation-plan.md`
- `docs/sessions/2026-08-26-ai-installation-implementation.md`

### CLI、安装状态与生命周期

- `internal/app/app.go`、`events.go`、`progress.go`、`scan.go`、`doctor.go`、`install.go`、`lifecycle.go`、`uninstall.go`、`update.go`、`version.go`、`candidate_image.go` 及对应测试。
- `internal/install/state.go`、`state_replace_unix.go`、`state_replace_windows.go` 及测试。
- `internal/installpolicy/policy.go` 及测试。
- `internal/config/config.go`、`migration.go` 及测试。
- `internal/cliui/locale.go`。

### 平台服务、扫描与健康接口

- `internal/platform/platform.go`、`platform_windows.go`、`platform_linux.go` 及 Windows、Linux、安全和通用测试。
- `internal/usage/progress.go`、`scanner.go` 及测试。
- `internal/store/store.go` 及测试。
- `internal/server/server.go` 及测试。

### 构建与真实生命周期

- `scripts/build.ps1`
- `scripts/build.sh`
- `tests/install-windows.ps1`
- `tests/install-linux.sh`

测试二进制、交叉构建产物和 Playwright 输出只存在于已忽略路径，没有进入公开 Git 历史。实施期间没有删除仓库文件，也没有修改只读参考副本。

## 运行命令与实际结果

### 本地 Go、平台和构建验证

- 标准 `go test ./... -count=1` 在本机实际执行；Windows Smart App Control 只阻止临时生成的 `app.test.exe` 和 `config.test.exe` 加载，其余 package 正常运行。安全策略保持开启，没有使用绕过参数，也没有把被阻止写成测试通过。
- 随后对所有含测试的 package 使用已忽略目录中的稳定测试二进制执行完整测试：累计 `596` 个 PASS、`51` 个 SKIP、`0` 个 FAIL；无测试文件的 package 也完成编译检查。
- `go vet ./...`：exit 0。
- Windows 与 Linux 的 `internal/app`、`internal/platform`、`internal/install` 重点回归均通过；Linux 测试由 Windows Go 交叉编译后在 WSL 真实执行，未在 WSL 外部安装额外 Go。
- `scripts/build.ps1 -Version 2.3.5 -SkipTests` 完成 Windows/Linux amd64/arm64 四目标构建并生成 `SHA256SUMS`；WSL `sha256sum -c` 四项均为 OK。
- 四个构建摘要分别为：Linux amd64 `d89ea296…`、Linux arm64 `2737f35d…`、Windows amd64 `a8e35e05…`、Windows arm64 `f86a95c6…`。

### Dashboard 与脚本验证

- `npm ci`：安装 3 个 package，审计 4 个 package，`0 vulnerabilities`。
- `npx playwright install chromium`：exit 0。
- `npm test`：Dashboard 与 Demo 共 `23/23` 通过，完整运行耗时约 46 秒。
- `node --check internal/web/static/app.js`、`i18n.js`、`tests/dashboard.spec.mjs`、`tests/demo.spec.mjs`：全部 exit 0。
- PowerShell AST 解析、只读/常量变量赋值冲突扫描、Windows 注册表缺失值行为检查：通过。
- `bash -n tests/install-linux.sh`：通过。

### 隐私、源码发布与 Git 门禁

- 动态本机用户名、工作目录、计算机名和会话标识扫描：意外匹配 `0`。
- 受跟踪数据库、构建目录、浏览器输出、临时测试结果和本地 brainstorm 目录扫描：意外匹配 `0`；上游已有的两份脱敏合成 JSONL fixture 仍按既定 allowlist 保留。
- README 资源、MIT `LICENSE`、`THIRD_PARTY_NOTICES.md` 和禁止 binary download 链接检查：通过。
- `git diff --check`、staged diff、提交文件列表和源码-only workflow 契约：通过。
- `release.yml` 只有 `workflow_dispatch`、`contents: read` 和策略测试；`ci.yml` 不含 `actions/upload-artifact`；`pages.yml` 不随 push 自动触发。

### GitHub-hosted CI 过程

Actions 显式启用后，共记录五轮可追溯的托管验收：

| Run | Head SHA | 结果 | 证据与处理 |
|---|---|---|---|
| `33135758468` | `f32963d5cb46b562b29e833753af32e09a8b5af7` | failure | Ubuntu 单测、Dashboard、交叉构建通过；Windows 单测与双平台生命周期暴露短路径、时间戳和 systemd manager 归属差异。 |
| `33136759221` | `f2806afddda4a53ff7e41ac47e00c8f94d769bc6` | failure | workflow 在创建 job 前失败；原因为 job 级 `env` 不允许 `runner.temp`。契约收紧后把变量移到单测 step。 |
| `33136931542` | `5aeb528e74b1773f31dcad4cc5443028d0e09fc4` | failure | 5/6 作业通过；Windows 生命周期发现 `$Home` 与只读 `$HOME` 冲突。 |
| `33137126198` | `d76bc3e4805b8c27f3086a2c82dd4ba17c029f3f` | failure | 5/6 作业通过；Windows 已正确删除 HKCU Run 值，但验证脚本把预期缺失读成终止错误。 |
| `33137453013` | `c67b952d843479c28ee75a4ff4e63368aeb23134` | **success** | 6/6 作业通过，URL：<https://github.com/edisoncccc/codex-usage/actions/runs/33137453013>。 |

成功 run 的作业结果：

- `unit (ubuntu-latest)`：`go test ./...` 与 `go vet ./...` 通过。
- `unit (windows-latest)`：在规范 `runner.temp` 下 `go test ./...` 与 `go vet ./...` 通过。
- `lifecycle (ubuntu-latest)`：真实 Linux 当前用户完整生命周期通过。
- `lifecycle (windows-latest)`：真实 Windows 当前用户完整生命周期通过。
- `cross-build`：四平台源码构建通过，不上传产物。
- `dashboard`：Go 构建、npm、四项 JS 语法检查和 Playwright 全部通过。

## 关键决策

1. 保留 Go 单二进制架构；AI 安装问题通过稳定协议和可信状态机解决，不改写技术栈。
2. 当前阶段严格 source-only。没有 SignPath 批准、publisher 身份和发布演练，就不发布未签名 Windows 或 Linux-only 二进制。
3. 人工用户和 AI 调用相同命令；AI 只负责解释、展示一次确认清单并传入 `--yes`，不获得跳过安全校验的特权。
4. 机器输出固定 JSON Lines 并保持一个最终事件；长扫描由同步进度与独立心跳共同避免“只有光标闪动”。
5. 不信任路径名或旧程序自报版本；使用受保护的安装记录、磁盘 SHA256、当前进程映像句柄和完整构建身份做判断。
6. 候选程序在确认前绑定；确认后即使路径被替换，也只安装已绑定字节；若同一文件被原地修改则安全失败。
7. Linux 回退必须证明 systemd manager 未接管或复用精确归属 PID；Windows 服务、Run 值、launcher 和进程也按精确路径验证。
8. same install 的服务修复先捕获运行与持久化状态，后续失败时恢复原状态，不把“能重新启动”误当成精确回滚。
9. 默认卸载保留数据；purge 只删除当前产品拥有且边界验证通过的数据。Windows scheduled removal 明确区别“已安排”和“已删除”。
10. 托管生命周期脚本在任何副作用前验证真实 GitHub-hosted runner；本地伪造环境变量不能运行真实服务测试。
11. Windows Smart App Control 保持开启。兼容测试方案与原命令结果分别记录，不用关闭安全控制换取绿色输出。
12. 所有推送均在只读核对远端 SHA 后普通 fast-forward；网络 EOF 或 SSH 断开时先确认状态再重试，从未 force push。

## 发布结果

- 公开仓库：<https://github.com/edisoncccc/codex-usage>。
- 实时仓库属性：公开、GitHub 原生 Fork、父仓库 `zJay26/codex-usage`、默认分支 `main`，公开产品名为 **Codex Usage Dashboard**。
- 最终实现与托管验收 head：`c67b952d843479c28ee75a4ff4e63368aeb23134`；成功 run：<https://github.com/edisoncccc/codex-usage/actions/runs/33137453013>。
- GitHub Actions artifacts：`0`；GitHub Releases：`0`；Pages workflow runs：`0`；Release workflow runs：`0`。
- 没有创建新标签。Fork 标签集合与上游完全相同，只包含继承的 `v0.1.0`、`v0.2.0`、`v1.0.0`、`v2.0.0` 至 `v2.3.5`。
- 没有创建 Release、上传 EXE、提交 SignPath 申请或改变外部签名状态。
- MIT `LICENSE`、`THIRD_PARTY_NOTICES.md`、上游致谢、中英文 README 和源码-only 提示均保留。

## 主要实施提交

| 提交 | 作用 |
|---|---|
| `ec07cb89b726762150b05dc92ff64b72b8e0b290` | 锁定 source-only 发布策略。 |
| `12c8666a681e168e321c5d6a2b9553ab8a37e8e8` | 增加稳定机器输出协议。 |
| `15264b9a39bd4e2e51970c780a170063d5cb772c` | 记录可信安装状态。 |
| `135d7b1c4989bcd239ffeb93319c88f5bbc2b5fd`、`5c2990156c5493a72fb1be7ca7d1cdb0779c1aec` | 增加扫描进度快照与持续心跳。 |
| `80a6cd9fe29ff30ef236b13292a830f824ddce7c` | 验证服务构建身份。 |
| `f96b19ac1f8a716e0f12e109b48fcf5838766b2c`、`0f6902269cebf8a9bcd79512aef5d619a5b67997` | 增加安装事务回滚和 AI 可驱动回执。 |
| `1dba480cc06d91aa8c36f7f02fa3b39cab05104d`、`5ee57f6c1b57144dde3fc7636846448db3a811d4` | 明确更新/卸载语义并保证状态标记完整。 |
| `7bf1e78787806f9f7f792e745940e56239e78e9a` | 增加 AI 与人工双语安装指南。 |
| `952686ea1fdfb945793365104bfb0a2274ea6449`、`de715f17ac6a66ccaf0bf62e28bcc1af1bf8b2e0`、`642e277d3ef02bf2d9c817440cdf82f0c93b40f7` | 建立并加强双平台托管生命周期。 |
| `008cba0669fbe2412548f2f3d2c3c9530cb95dab` 至 `07f107b8ec56ed8a05e862419443d71afb2e9adf` | 完成扫描、事务、Linux 进程安全、清理、信任前置、服务回滚和候选映像绑定等复核修正。 |
| `f2806afddda4a53ff7e41ac47e00c8f94d769bc6` 至 `c67b952d843479c28ee75a4ff4e63368aeb23134` | 根据真实 hosted CI 日志修正平台与测试脚本差异并取得 6/6 通过。 |

## 后续待办

- 当前阶段没有阻塞性待办；源码 Fork、安装协议和托管验收已完成。
- 如用户未来决定发布二进制，先单独授权并完成 SignPath Foundation 资格申请；只有资格、publisher 身份、测试签名、SHA256、Attestation 和不可变 Release 演练全部通过，才实施阶段 B/C。
- 在二进制发布获批前，保持 `binary_release_enabled=false`、Release 手动只读、Pages 手动、CI 不上传 artifacts。
- 非阻塞增强：可继续增加 PowerShell 生命周期 helper 的独立静态契约，以及 Subagent malformed JSON/父链 cycle 等边界测试。
