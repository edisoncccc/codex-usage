# Contributing

感谢你改进 Codex Usage。

可复现缺陷请使用 Issue 模板；安装、统计口径和功能建议请优先使用 GitHub Discussions。公开内容不要包含 hostname、machine ID、本机路径、Thread 标题、Session ID、精确时间或对话内容。

## 开发环境

- Go 1.26.x
- Node.js 24（仅 Dashboard 浏览器测试需要）

提交前请运行：

```bash
go test ./...
go vet ./...
```

Dashboard 改动还应运行：

```bash
npm ci
npx playwright install chromium
npm test
```

## Pull Request

1. 从 `main` 创建短生命周期分支。
2. 保持提交聚焦，并为行为变化补充测试。
3. 不要提交 Codex session、SQLite 数据库、凭据、真实项目路径或构建产物。
4. 确认 Windows 与 Linux 的行为差异已被考虑。
5. 在 PR 中说明统计口径或兼容性变化。

提交消息建议使用简洁的祈使句，例如：

```text
Fix fork replay boundary handling
Add Linux linger diagnostics
```
