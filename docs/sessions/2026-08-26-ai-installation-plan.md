# 2026-08-26 AI 安装协议实施计划会话

## 工作目标

把已批准的“AI 代装与可信发布设计”转化为可由子智能体逐任务执行、逐任务规格复核和质量复核的阶段 A 详细计划，同时保持公开 Fork 的 source-only 边界。

## 执行步骤

1. 完整复核已批准的设计规格、公开 Fork 计划、当前 CLI、扫描器、路径、平台服务、健康接口、构建脚本、CI 和 Release workflow。
2. 并行派出三个只读映射任务，分别核对 CLI/进度、平台生命周期和发布/文档边界；映射任务未修改共同工作树。
3. 确认现有 Release workflow 仍会在任意 `v*` tag 上构建并公开未签名二进制，因此把 fail-closed source-only 门禁列为第一实施任务。
4. 把阶段 A 按依赖拆为策略、机器协议、安装记录、扫描进度、服务身份、事务回滚、install、update/uninstall、双语文档、CI 生命周期和最终验证十二项。
5. 为每项写出 RED/GREEN 测试、精确文件、关键类型、命令、预期结果、原子提交和最终远端验证。
6. 明确把 SignPath 申请、publisher 身份、签名 workflow 和正式不可变 Release 排除在当前计划外，避免用未知占位值制造伪实现。

## 修改文件

- `docs/superpowers/plans/2026-08-26-ai-installation-protocol.md`
- `docs/sessions/2026-08-26-ai-installation-plan.md`

本会话不修改 Go、JavaScript、平台服务、构建脚本或 GitHub workflow，不执行远端写操作。

## 运行命令与实际结果

- 使用 `rg --files`、`rg -n`、`Get-Content` 只读定位 app、scanner、config、platform、server、workflow、README、贡献与安全文档。
- 使用 `git status --short --branch` 和 `git log` 确认计划开始时 HEAD 为 AI 安装设计提交，工作树干净且设计尚未推送。
- 三个只读映射均返回具体文件、测试切入点、平台风险和阶段 B/C 门禁，且确认共同工作树未修改。
- 初次规格复核拦截了真实 lifecycle 脚本可能改动本机 HKCU/systemd、事务与扫描顺序冲突、commit/dirty 混写、平台场景不足、端口预检缺失，以及 CI 证据与留痕顺序倒置。
- 初次质量复核进一步拦截了 Smart App Control 下构建脚本重复执行测试、跨子智能体 `$Go` 变量未初始化、ldflag 不能直接设置 bool、旧状态迁移游离于事务、hosted runner guard 不完整，以及 Windows scheduled self-delete 后过早重装。
- 计划已逐项修正：真实服务脚本只在 `RUNNER_ENVIRONMENT=github-hosted` 的一次性 runner 执行；本地只跑真实文件系统和 fake service；旧迁移拆为 inspect/begin/rollback/commit；旧服务拆为 suspend/resume/remove；build commit 与 dirty 分字段；补全 `-SkipTests` 纯构建、平台场景、动态隐私扫描和两阶段 push/CI 留痕顺序。
- 修订后规格复核结果为 PASS，质量复核结果为 PASS。
- 已运行 `git diff --check`、未完成占位扫描、私人路径扫描、设计关键词覆盖检查与代码块平衡检查；均通过，两个 Markdown 文件没有本机路径或未决占位。

## 关键决策

1. 当前可直接实施的是阶段 A；SignPath 外部资格和正式二进制发布不能与可本地验证的代码混成一个计划。
2. Release workflow 必须先失去 tag/build/upload 能力，不能只靠一个未来可能被误改的布尔条件保护现有未签名发布流程。
3. 机器输出由统一 runner 管理唯一终态；各命令只发阶段/心跳并返回 typed result/error。
4. Scanner 提供同步进度值，app 独立 ticker 提供四秒心跳，才能覆盖慢目录发现和单个大文件。
5. 不执行来源不明的旧 EXE来判断版本；使用私有状态目录内的安装记录和磁盘 digest 决定 same/upgrade/downgrade/untrusted。
6. 安装回滚必须同时覆盖 EXE、安装记录和旧服务，健康检查必须匹配完整 commit 身份。
7. Phase A update 只返回渠道关闭，不提前实现不完整的网络下载或可信验证。
8. 人工和 AI 使用同一个 CLI；`--yes` 表示调用方已经完成确认，JSON 模式不会卡在交互 stdin。
9. Windows 异步 self-delete 必须在回执中标记 scheduled，不能把调度成功写成文件已经删除。
10. 所有实现提交保持中文 Conventional Commit 和 AI 元数据；不删除文件、不 force push。
11. Windows 异步卸载后必须等 program path 确实消失再重装；scheduled 只代表删除已安排，不代表完成。

## 后续待办

1. 提交本计划和会话留痕。
2. 使用 `superpowers:subagent-driven-development` 串行执行 Task 1–12；同一时间只允许一个实现者修改共同工作树。
3. 每项实现后先规格复核，再质量复核，再进入下一项。
4. 所有本地与 CI 门禁通过后 fast-forward 推送到公开 Fork。
5. SignPath 申请需要用户另行授权外部提交；获批后再写阶段 B/C 计划。
