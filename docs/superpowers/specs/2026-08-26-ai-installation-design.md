# Codex Usage Dashboard：AI 代装与可信发布设计

- 日期：2026-08-26
- 状态：设计已批准，待实施计划
- 适用仓库：`edisoncccc/codex-usage`
- 产品名：Codex Usage Dashboard
- 仓库与命令名：`codex-usage`

## 1. 背景

Codex Usage Dashboard 面向经常使用 Codex 和其他本地 AI 编程助手的人。该用户群体很可能不会逐行照着 README 手动安装，而是把 GitHub 仓库链接交给本地 AI，并要求 AI 完成安装。

现有项目采用单个 Go 二进制，内含 JSONL 扫描器、SQLite、本地 HTTP API 和静态 Dashboard。现有 `install` 命令已经能够把自身复制到当前用户目录并注册用户级后台服务。问题不在实现语言，而在于公开 Fork 目前只提供源码，尚缺少可信二进制发布、机器可读安装协议、非交互输出和完整的安装后验证。

本设计保留 Go，新增一套由人工用户和 AI 共用的可信安装链。用户只需提供规范仓库链接；AI 不依赖自由推断 README，而是按照仓库声明的策略完成发现、验证、安装和健康检查。

## 2. 目标

### 2.1 用户目标

1. 用户把 `https://github.com/edisoncccc/codex-usage` 交给 Codex、Claude Code 或其他本地 AI，并说“帮我安装”，即可进入确定的安装流程。
2. AI 在执行前一次性展示下载来源、校验方式、写入路径、后台启动项、数据位置、网络行为和卸载方法。
3. 用户确认后，AI完成系统与架构识别、产物下载、可信验证、当前用户安装、服务启动和健康检查。
4. 不使用 AI 的用户可以按照同一份安装规范手动完成完全等价的流程。
5. 长时间扫描必须持续显示进度，不能只留下闪烁光标。

### 2.2 安全目标

1. GitHub 不可变 Release 是唯一二进制发布真源。
2. Windows 产物必须具备公有信任 Authenticode 签名；本项目零预算，首选 SignPath Foundation 免费开源签名。
3. Windows 和 Linux 产物必须带 SHA256，且由 GitHub Actions 生成 Artifact Attestation。
4. 下载、签名或完整性验证失败时必须停止，不能静默降级执行。
5. 安装仅限当前用户，不自动请求管理员或 `sudo` 权限。
6. 更新只能由用户或 AI 明确触发，不进行后台静默更新。

### 2.3 完成标准

当免费签名资格获批并启用二进制渠道后，Windows 与 Linux 的 amd64/arm64 用户均可通过 AI 或人工流程完成一次确认式安装；安装结果包含机器可读回执，`doctor` 能证明服务和 Dashboard 可用。若免费签名未获批准，则仓库继续 source-only，不能发布任何 Windows 或 Linux 二进制 Release。

## 3. 非目标

- 不把项目重写为 Node.js、Python、Rust 或其他语言。
- 不提供需要直接通过网络管道执行的 `curl | sh`、`irm | iex` 等安装方式。
- 首版不以 WinGet、Homebrew、apt、dnf 或 pacman 作为发布真源。
- 不做系统级、多用户或管理员安装。
- 不做后台自动更新、远程遥测或中心服务器。
- 不在本阶段支持 macOS。
- 不购买商业代码签名证书。
- 不因为发布流程变化而改变现有本地隐私、Token 统计或费用估算边界。

## 4. 已锁定决策

1. 产品继续使用 Go 单二进制架构。
2. 首版支持 Windows 与 Linux 的 amd64 和 arm64。
3. 默认安装签名后的预编译二进制；源码构建仅作为用户明确选择的备用路径。
4. 安装只写当前用户范围，正常流程不提权。
5. 用户只需提供仓库链接；专用提示词不是前置条件。
6. 人工与 AI 共享同一个 Go 安装引擎，不维护行为不同的两套安装器。
7. 签名预算为零；优先申请 SignPath Foundation。
8. SignPath 失败时继续 source-only，不发布 Linux-only 或未签名 Windows 版本。
9. GitHub Release 是唯一真源；包管理器未来只能作为入口。

## 5. 总体架构

```text
                     README.md / INSTALL.md（中文）
用户或 AI 打开仓库 ─┤
                     README.en.md / INSTALL.en.md（英文）
                                      │
                                      ▼
                          install-policy.json
                    声明仓库、平台、资产、信任、
                    权限、路径和渠道启用状态
                                      │
                ┌─────────────────────┴─────────────────────┐
                ▼                                           ▼
       可信二进制渠道已启用                         可信二进制渠道未启用
    GitHub 不可变 Release 可用                    SignPath 尚未批准或已失败
                │                                           │
                ▼                                           ▼
      下载、验证、执行安装器                     明确询问是否从源码构建
                │                                  不允许静默降级
                └─────────────────────┬─────────────────────┘
                                      ▼
                               同一个 Go CLI
                   install / doctor / update / uninstall
                                      │
                                      ▼
                          人类输出或 JSON Lines 回执
```

### 5.1 发现层

`README.md` 与 `README.en.md` 在源码安装说明之前增加两个同等可见的入口：

- 让 AI 安装：把当前仓库链接交给本地 AI；AI 必须阅读相应安装规范和机器策略。
- 手动安装：用户按照相同的发现、验证、安装和检查步骤执行。

README 不复制完整安装逻辑，避免与权威安装文档漂移。

### 5.2 说明层

新增同构的 `INSTALL.md` 与 `INSTALL.en.md`，顶部互相切换。两份文档覆盖：

- 支持平台与前置条件；
- AI 执行协议；
- 人工安装命令；
- 可信验证说明；
- 当前用户写入位置；
- 进度和健康检查；
- 升级、卸载与源码备用流程；
- 常见失败及禁止绕过事项。

### 5.3 策略层

根目录新增语言无关的 `install-policy.json`。它只声明规则，不包含可执行脚本。至少包含：

- `schema_version`；
- 规范仓库 `edisoncccc/codex-usage`；
- 稳定标签格式；
- `binary_release_enabled`；
- 支持的 OS/架构和精确资产名称；
- 是否强制不可变 Release；
- Windows 签名发布者策略；
- GitHub Release 与 Artifact Attestation 要求；
- 当前用户安装目录、状态目录与服务类型；
- 禁止提权、禁止静默降级和禁止后台更新。

在 SignPath 尚未批准时，`binary_release_enabled` 必须保持 `false`，Windows 签名身份字段保持不可用状态。不能猜测或预填未来证书 Subject。获得 SignPath 返回的权威身份后，才能通过经过复核的提交同时启用相应策略和 Release workflow。

### 5.4 执行层

现有 Go CLI 扩展为人工与 AI 共用的安装引擎：

```text
codex-usage version --json
codex-usage install --yes --json
codex-usage doctor --json
codex-usage update --check --json
codex-usage update --yes --json
codex-usage uninstall --yes --json
codex-usage uninstall --purge --yes --json
```

不带 `--json` 时输出本地化的人类可读内容；带 `--json` 时 stdout 只输出逐行 JSON 事件，稳定字段名不随语言变化。

## 6. 信任模型

### 6.1 信任根

用户明确提供的 `https://github.com/edisoncccc/codex-usage` 是发现根。AI 不接受搜索结果、拼写相似仓库、镜像站、网盘或第三方下载页替代它。

根目录的安装策略用于约束流程，但不能单独把任意下载地址变成可信来源。二进制必须来自规范仓库的正式不可变 Release。

### 6.2 发布顺序

正式二进制 Release 必须严格执行：

```text
v* 标签指向确定提交
→ 完整 Go、Playwright、隐私与发布门禁
→ 构建 Windows/Linux amd64/arm64
→ SignPath 签署两个 Windows EXE
→ 验证 Authenticode 和时间戳
→ 对最终签名后的文件重新计算 SHA256
→ 为四个最终产物生成 Artifact Attestation
→ 创建 Draft Release
→ 上传完整资产与版本说明
→ 最终复核
→ 发布为 Immutable Release
```

签名会改变 EXE 字节，因此 SHA256 必须在签名之后生成。任何一步失败都不得创建正式 Release。

### 6.3 安装前强制检查

AI 或人工流程必须检查：

1. owner/repo 精确匹配规范仓库；
2. Release 不是 draft 或 prerelease；
3. Release 为 immutable，标签符合稳定 SemVer；
4. 资产名与当前 OS/架构精确匹配；
5. 下载最终地址仍属于 GitHub；
6. 本地 SHA256 同时匹配 GitHub Release API 的资产 digest 与 `SHA256SUMS`；
7. Windows Authenticode 有效、时间戳有效且发布者符合安装策略；
8. 可用时验证 Release 和 Artifact Attestation 来自规范仓库、Release workflow 与同一标签；
9. 首次可信执行后，`version --json` 的版本、完整提交 SHA、OS 和架构与 Release 一致。

`gh` 不是安装前置依赖。若系统已有 GitHub CLI，则自动执行 Release 与 Attestation 验证；若没有，则不静默安装额外工具，而是依靠不可变 Release、双重 SHA256，以及 Windows 上的 Authenticode 完成基础可信链。最终回执必须把每一层记录为 `verified`、`not_applicable` 或 `not_checked`，不得把未检查写成通过。

下载、哈希、不可变状态、资产身份或 Windows 签名任一强制项失败时必须停止。缺少可选增强验证工具不是验证失败，但必须如实进入回执。

## 7. AI 与人工安装流程

### 7.1 预检

安装前检查：

- Windows/Linux 与 amd64/arm64；
- 当前用户目录写权限；
- 是否已有安装及其版本；
- 目标端口和后台服务冲突；
- 二进制渠道是否启用；
- 下载与验证工具可用性；
- 预计写入路径、数据保留和网络请求。

### 7.2 一次确认

正常安装只要求一次用户确认。确认内容必须包含：

- 规范仓库、版本和目标资产；
- 将访问 GitHub 和 SignPath 签名验证链；
- 安装目录与状态目录；
- Windows `HKCU` 用户启动项或 Linux `systemd --user` 服务；
- 只监听 `127.0.0.1:43189`；
- 初次扫描的本地数据范围；
- 默认卸载保留数据库，`--purge` 才删除数据。

若预检发现需要提权、来源不明的已有安装、签名策略冲突或显式清除数据，则正常的一次确认不再适用，流程必须停止并单独报告。

### 7.3 执行

1. 把资产下载到当前用户可写的临时位置。
2. 在执行前完成外部验证。
3. 调用下载产物的 `install --yes --json`。
4. 安装器把自身复制到规范安装目录，迁移受支持的旧状态，注册并启动用户级服务。
5. 初次扫描期间持续输出进度。
6. 执行 `doctor --json`。
7. 返回安装回执和 `http://127.0.0.1:43189`。

### 7.4 源码备用流程

只有以下情况可以提出源码构建：

- `binary_release_enabled=false`；
- 用户明确要求源码构建；
- 当前平台没有受支持的可信二进制。

AI 必须先说明 Go 1.26+ 要求、构建命令和 Windows Smart App Control 风险，再取得明确同意。预编译产物验证失败不能触发自动源码降级。Windows 安全策略阻止构建或运行时，必须停止并说明，不能关闭、绕过或弱化安全策略。

## 8. 进度与机器输出

### 8.1 人类输出

普通 CLI 至少显示以下阶段：

```text
预检
发现版本
下载
验证
停止旧服务
安装或升级
扫描历史
启动服务
健康检查
完成
```

长时间扫描最多五秒必须出现一次阶段更新或心跳，包含已发现文件数、已处理文件数、已写入事件数和警告数。输出不能只剩闪烁光标。

### 8.2 JSON Lines

`--json` 模式 stdout 每行是一个独立 JSON 对象，至少包含：

- `schema_version`；
- `event`；
- `phase`；
- `status`；
- `timestamp`；
- 稳定错误或结果 `code`；
- 可选进度计数；
- 可选本地化 `message`。

成功或失败都必须尽力输出唯一的终态事件。最终成功回执至少包含：

- 版本、完整 commit SHA、OS 与架构；
- 安装路径、状态路径、数据库路径；
- 服务状态和 Dashboard URL；
- 每项验证结果；
- 初次扫描摘要；
- 是否保留旧数据；
- 对应卸载命令。

字段名和枚举值不随语言变化。AI 只能依据稳定字段判断结果，不能通过匹配中文或英文句子推断成功。

## 9. 安装、升级与回滚语义

### 9.1 当前用户范围

- Windows：`%LOCALAPPDATA%\Programs\codex-usage\codex-usage.exe`，状态位于 `%LOCALAPPDATA%\codex-usage`，使用 `HKCU` 用户启动项。
- Linux：`~/.local/bin/codex-usage`，状态位于 `${XDG_DATA_HOME:-~/.local/share}/codex-usage`，优先使用 `systemd --user`。
- 两端只监听 `127.0.0.1`。
- 正常安装不能写系统级目录、系统服务或全局配置。

### 9.2 幂等安装

- 相同版本重复安装必须安全收敛到同一状态。
- 已有受信任旧版本时转为升级流程。
- 已有更高版本时拒绝隐式降级。
- 来源不明或签名身份不符的同名安装不得自动覆盖。

### 9.3 更新

- 只有显式 `update` 才访问 GitHub。
- `update --check` 只读检查，不下载或修改。
- 更新沿用与首次安装相同的发现和验证规则。
- 更新保留数据库、配置和定价覆盖设置。
- 新版本健康检查失败时自动恢复原可运行版本，并返回失败回执。
- 不增加后台自动更新任务。

### 9.4 卸载

- `uninstall` 停止用户级服务并移除程序，默认保留统计数据库和配置。
- `uninstall --purge` 必须在执行前列出将删除的绝对状态路径，并要求独立明确确认；`--yes` 只代表调用方已经取得该确认。
- 卸载不得触碰 Codex 自身的 session JSONL、`auth.json`、其他程序的数据或第三方启动项。

## 10. 错误处理

错误分为稳定类别，不依赖自然语言：

- `unsupported_platform`；
- `release_channel_disabled`；
- `release_not_immutable`；
- `asset_not_found`；
- `download_failed`；
- `digest_mismatch`；
- `signature_invalid`；
- `publisher_mismatch`；
- `attestation_not_checked`；
- `permission_required`；
- `existing_install_untrusted`；
- `service_start_failed`；
- `health_check_failed`；
- `source_build_blocked`。

强制可信检查失败、需要提权、来源冲突和健康检查失败均返回非零退出码。缺少可选 Attestation 验证工具可以完成安装，但终态回执必须保留 `attestation_not_checked`，以便 AI准确转述。

## 11. SignPath 免费签名门禁

### 11.1 外部条件

SignPath Foundation 的免费证书由基金会作为发布者，私钥由其签名平台托管。项目需要接受基金会显示为 Windows 发布者，并遵守开源许可、维护状态、代码审核、发布审批、仓库可见性和签名政策要求。

本项目是公开 MIT Fork。SignPath 对修改版上游 Fork 有额外条件，而上游现有 Windows 发布说明曾明确标注二进制未签名，因此免费资格不能在本设计中视为已经获得。

### 11.2 处理规则

1. 在仓库补齐代码签名政策、隐私边界和团队角色说明后，才能准备申请材料。
2. 对 SignPath 的外部申请、身份信息提交或通信属于独立外部操作，执行前需要用户授权和必要配合。
3. 申请结果必须以 SignPath 的明确书面答复为准。
4. 获批后先完成测试签名、签名验证和非发布演练，再启用二进制渠道。
5. 被拒绝或条件不满足时，不购买证书，也不发布任何二进制，继续 source-only。

## 12. 发布与渠道

### 12.1 第一阶段

- GitHub 不可变 Release 是唯一真源。
- 正式资产保持现有名称：
  - `codex-usage-windows-amd64.exe`
  - `codex-usage-windows-arm64.exe`
  - `codex-usage-linux-amd64`
  - `codex-usage-linux-arm64`
  - `SHA256SUMS`
- 版本说明来自 `docs/releases/vX.Y.Z.md`，缺失时不能自动生成含义不明的正式说明。
- CI 仍不上传普通二进制 artifacts；只有通过全部门禁的 Release workflow 可以上传正式 Release 资产。
- Pages 保持手动触发，不参与安装信任链。

### 12.2 后续入口

稳定发布验证后，可以另行设计 WinGet 与 Homebrew 元数据。它们只能引用 GitHub 不可变 Release，不能重新打包或成为版本真源。apt、dnf、pacman 等发行版仓库不属于当前范围。

## 13. 隐私与网络边界

- 安装和显式更新为了访问 GitHub Release、校验信息及签名链，可以进行与该动作直接相关的网络请求。
- 正常后台服务继续不做遥测、云同步或自动更新。
- 扫描器继续不读取或解析 `auth.json`，不解析或持久化 prompt、回复、reasoning 或工具输出正文。
- 运行时费用功能继续使用内嵌价格，不为费用估算抓取网页。
- Dashboard 继续只监听本机回环地址。

## 14. 测试设计

### 14.1 单元测试

- 安装策略 JSON schema、平台和资产映射；
- `version --json`、进度 JSON Lines 和终态回执；
- 验证状态不会把 `not_checked` 当作 `verified`；
- 相同版本幂等、拒绝隐式降级和拒绝不可信覆盖；
- `--purge` 的确认与路径约束；
- 稳定错误 code 与退出状态。

### 14.2 平台集成测试

Windows 与 Linux 分别覆盖：

- 全新当前用户安装；
- 重复安装；
- 旧版本升级；
- 更新失败回滚；
- 后台服务启动与重启；
- 默认卸载保留数据；
- 显式 purge 只影响规范状态目录；
- `doctor --json` 与 `/healthz`；
- 长扫描持续输出进度。

### 14.3 供应链负面测试

- owner/repo 不符；
- draft、prerelease 或非 immutable Release；
- 错误 OS/架构；
- Release API digest 与 `SHA256SUMS` 不一致；
- 下载后哈希不符；
- Windows 无签名、签名失效、发布者不符或缺少有效时间戳；
- Attestation 指向错误仓库、工作流、标签或提交；
- GitHub CLI 不存在时回执准确标记 `not_checked`；
- 下载重定向到非允许来源。

### 14.4 发布门禁

启用正式 Release 前必须通过：

- `go test ./...` 与 `go vet ./...`；
- Windows/Linux 安装集成测试；
- Playwright Dashboard 测试；
- JavaScript 语法检查；
- README/INSTALL 双语结构与链接检查；
- 隐私和意外产物扫描；
- 签名后 SHA256 顺序检查；
- Authenticode 与 Attestation 实际验证；
- `git diff --check`；
- Draft Release 资产清单复核。

## 15. 实施边界与阶段

### 阶段 A：source-only 安装协议

可以在没有 SignPath 的情况下实施文档、机器策略、CLI JSON/进度、幂等安装、升级框架、健康检查和测试。二进制渠道保持关闭，Release 不发布。

### 阶段 B：SignPath 资格与测试签名

在得到用户对外部申请的授权后准备并提交免费开源签名申请。获批后配置测试签名和签名身份，完成非发布演练。

### 阶段 C：可信二进制发布

只有阶段 B 通过后才能启用 Release workflow、不可变发布和 `binary_release_enabled`，再由明确的 `v*` 版本标签触发首个正式二进制 Release。

## 16. 验收标准

1. README 中 AI 与人工安装入口清晰、同权且中英文同构。
2. AI 只凭规范仓库链接即可发现安装协议，不需要专用提示词。
3. `install-policy.json` 能让 AI 确定平台、资产、可信要求和渠道状态。
4. `--json` 输出可稳定驱动 AI，长步骤持续报告进度。
5. Windows/Linux 都只进行当前用户安装，不请求提权。
6. 安装、重复安装、更新、回滚、卸载和 purge 的数据语义明确并经过测试。
7. 签名或强制完整性验证失败时不会执行产物。
8. 无 SignPath 免费资格时保持 source-only，仓库不产生任何二进制 Release。
9. 有资格后，正式 Windows 资产均为有效 SignPath Authenticode，四个平台资产均有签名后 SHA256 与 Artifact Attestation，Release 为 immutable。
10. 安装回执准确区分已验证、无需验证和未检查项目，不夸大可信程度。

## 17. 官方依据

- GitHub Immutable Releases：<https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases>
- GitHub Release REST API：<https://docs.github.com/en/rest/releases/releases>
- GitHub Artifact Attestations：<https://docs.github.com/en/actions/concepts/security/artifact-attestations>
- GitHub 生成与验证 Attestation：<https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations>
- WinGet 提交和审核流程：<https://learn.microsoft.com/en-us/windows/package-manager/package/repository>
- SignPath Foundation 免费开源签名条件：<https://signpath.org/terms.html>
