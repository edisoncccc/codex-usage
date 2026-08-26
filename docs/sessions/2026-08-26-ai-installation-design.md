# 2026-08-26 AI 代装与可信发布设计会话

## 工作目标

在现有公开 Fork 和 source-only 发布边界上，分析是否需要放弃 Go，并为“用户把 GitHub 仓库链接交给本地 AI，由 AI 安全安装”设计可信、可验证、人工同样可用的安装与发布方案。

## 执行步骤

1. 复核现有 README、Go module、安装实现、构建脚本、CI/Release workflow、公开 Fork 设计与会话留痕。
2. 确认现有程序是单个 Go 二进制，内含扫描器、SQLite、本地 API 和 Dashboard；`install` 已具备当前用户自复制和后台服务安装基础。
3. 比较保留 Go、改用 Node/Python、脚本安装和包管理器优先等路线。
4. 与用户锁定 Windows+Linux、当前用户范围、一次确认、仓库链接即入口、人工可等价安装和零签名预算。
5. 只读核对 GitHub Immutable Releases、Release API、Artifact Attestations、WinGet、Azure Artifact Signing 与 SignPath Foundation 官方资料。
6. 发现 Azure Public Trust 存在地域资格限制；转为 SignPath Foundation 免费开源签名优先方案。
7. 明确 SignPath 对 Fork 的资格仍需书面确认；未获批时继续 source-only，不发布任何平台二进制。
8. 将完整设计写入正式规格，并进行未完成标记、矛盾、范围和歧义自检。

## 修改文件

- `docs/superpowers/specs/2026-08-26-ai-installation-design.md`
- `docs/sessions/2026-08-26-ai-installation-design.md`

本会话不修改 Go、JavaScript、样式、构建脚本、GitHub workflow 或远端仓库设置。

## 运行命令与只读检查

- 读取 `superpowers:brainstorming`、`gsd-explore`、`git-commit` 说明及其必要引用。
- 使用 `Get-Content` 读取 README、Go module、package、构建脚本、workflow、现有设计、计划和会话留痕。
- 使用 `rg` 检查安装、后台服务、版本信息和路径实现。
- 使用 `git log --oneline --decorate` 与 `git status --short --branch` 核对当前提交和工作树。
- 使用官方网页资料核对 GitHub Release/Attestation、WinGet、Azure Artifact Signing 与 SignPath 条件。
- 规格写入后运行 `git diff --check`、隐私扫描、未完成标记扫描和 staged diff 复核。

## 关键决策

1. 不因安装问题重写 Go；单二进制仍是最低运行时依赖方案。
2. 默认使用免费签名的预编译二进制，源码构建只作为明确备用路径。
3. 首版覆盖 Windows/Linux amd64/arm64，只安装到当前用户范围。
4. 用户只需把规范仓库链接交给 AI，不要求复制专用提示词。
5. AI 与人工用户调用同一个 Go 安装引擎；README、INSTALL 文档和机器策略只是不同入口。
6. GitHub 不可变 Release 是唯一发布真源；WinGet/Homebrew 后续只做入口。
7. 签名预算为零，优先申请 SignPath Foundation；Azure Artifact Signing 和商业证书不作为当前路线。
8. SignPath 未批准或拒绝时，Windows/Linux 都保持 source-only，不发布未签名 Windows 或 Linux-only 二进制。
9. 正常安装一次确认，失败或安全冲突停止；不自动提权、静默降级或绕过 Windows 安全策略。
10. 长时间扫描必须输出人类进度和机器可读 JSON Lines 心跳，解决只有光标闪动的问题。

## 实际结果

- 形成一份覆盖架构、信任链、机器协议、安装状态机、升级回滚、卸载、隐私、签名门禁和测试标准的正式设计规格。
- 设计明确区分产品决策与工程默认值，不再要求用户逐项选择底层验证分支。
- 当前公开 Fork 的 source-only 边界保持不变；没有创建 tag、Release、二进制、外部申请或远端写操作。
- 本次文档提交使用中文 Conventional Commit，并保留 AI-Coding 元数据。

## 后续待办

1. 用户整体审阅正式规格。
2. 规格确认后使用 `superpowers:writing-plans` 编写分任务实施计划。
3. 按计划先实施不依赖 SignPath 的 source-only 安装协议、CLI 输出和测试。
4. SignPath 外部申请在获得用户明确授权和必要配合后单独执行。
5. 只有免费签名资格、测试签名和完整发布演练通过后，才设计并触发首个二进制版本标签。
