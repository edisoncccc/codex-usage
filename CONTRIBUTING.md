# 贡献指南

感谢你改进 Codex Usage Dashboard。可复现缺陷请使用 Issue 模板；安装、统计口径和功能建议请优先使用 GitHub Discussions。公开内容不要包含 hostname、machine ID、本机路径、Thread 标题、Session ID、精确时间、对话内容或凭据。

## 开发环境

- Go 1.26.x
- Node.js 24（仅 Dashboard 浏览器测试需要）

提交前请运行：

```bash
go test ./...
go vet ./...
go test ./internal/installpolicy -count=1
```

Dashboard 改动还应运行：

```bash
npm ci
npx playwright install chromium
npm test
```

`npm test` 会在临时目录自动构建并启动真实 Go 二进制；如需复用已有产物，可设置 `CODEX_USAGE_BIN`。

## 安装协议与可信发布

仓库当前是 **source-only**：`install-policy.json` 的二进制渠道保持关闭，没有 publisher Subject 或可信二进制 Release。贡献不得加入上游/第三方二进制下载、远程管道安装、管理员安装或后台自动更新。

修改安装行为或文档时必须：

1. 让 `INSTALL.md` / `INSTALL.en.md` 和 `CODE_SIGNING.md` / `CODE_SIGNING.en.md` 保持顶部互链、标题层级与段落顺序同构。
2. 同步 `install-policy.json`、README、贡献/安全说明和对应 Go 契约测试；机器枚举和 JSON 字段不随语言变化。
3. 先增加失败测试，再实现行为，并运行 `go test ./internal/installpolicy -count=1`。
4. 保持当前用户、无提权、无静默源码降级、无后台更新和 loopback-only 边界。
5. 不提交 EXE、Linux 二进制、普通 CI binary artifact、签名凭据或虚构的 SignPath 身份/ID。

SignPath 外部申请、publisher 设置、Release workflow 发布能力和 `binary_release_enabled=true` 都需要独立授权、真实书面批准和专门复核。未来签名流程以 [CODE_SIGNING.md](CODE_SIGNING.md) 为准；失败时继续 source-only。

## Pull Request

1. 从 `main` 创建短生命周期分支。
2. 保持提交聚焦，并为行为变化补充测试。
3. 不要提交 Codex session、SQLite 数据库、凭据、真实项目路径或构建产物。
4. 确认 Windows 与 Linux 的行为差异、卸载数据语义和错误回滚已经覆盖。
5. 在 PR 中说明统计口径、安装策略或兼容性变化，并列出实际测试命令与结果。
6. 若改动供应链或发布边界，说明维护者复核和发布审批者分别核对了什么；不要在公开 PR 中粘贴 secret。

提交消息保持简洁并描述实际变化，例如：

```text
fix: 修正 fork 重放边界
docs: 同步双语安装契约
```
