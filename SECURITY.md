# Security Policy

## Supported versions

安全修复优先覆盖最新发布版本和 `main` 分支。

## Reporting a vulnerability

请不要在公开 Issue 中提交可能泄露本机 Codex 数据、路径或凭据的安全问题。

优先使用 GitHub 仓库的 **Security → Report a vulnerability** 私密报告入口。如果该入口尚未启用，请联系仓库维护者，并仅提供最小化、已脱敏的复现信息。

Codex Usage 不应读取 `auth.json`，不应保存 prompt、回复、reasoning 或工具输出，也不应监听 loopback 以外的网络地址。任何违反这些边界的行为都应按安全问题处理。

费用功能只使用嵌入二进制的 Standard API 价格与本机 Token 事件，不读取 OpenAI 真实账单、账号配额或凭据，也不会在运行时联网抓取价格。定价覆写仅保存在本机 `config.json`，其写接口沿用 loopback Origin 防护。
