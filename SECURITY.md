# 安全政策

## 支持范围

安全修复优先覆盖 `main` 分支，以及未来明确标记为仍受支持的版本。仓库当前是 **source-only**，没有本项目认可的预编译二进制 Release 或已签名 EXE。

## 私密报告安全问题

请不要在公开 Issue、Discussion 或 PR 中提交可能泄露本机 Codex 数据、路径、凭据、签名资料或可利用细节的安全问题。

优先使用 GitHub 仓库的 **Security → Report a vulnerability** 私密报告入口。如果该入口尚未启用，请联系仓库维护者，并仅提供最小化、已脱敏的复现信息。不要发送 Codex session 原文、`auth.json`、Token、私钥、GitHub/SignPath secret、完整本机路径或真实 Thread 标题。

以下问题应通过私密渠道报告：

- 安装器写出当前用户规范路径、请求管理员或 `sudo`、覆盖来源不明程序，或 purge 触碰 Codex/第三方数据；
- 服务监听 loopback 之外的地址、产生未声明外联、遥测或后台更新；
- 扫描器读取 `auth.json`，或解析/保存 prompt、回复、reasoning、工具输出正文；
- 未来 Release 的 publisher、时间戳、签名、SHA256、asset digest、Attestation、标签或 commit 不一致；
- 签名凭据疑似泄露、Release 资产被替换，或有人冒充本项目分发二进制。

## 运行时隐私边界

Codex Usage 不应读取或解析 `auth.json`，不应解析或保存 prompt、回复、reasoning 或工具输出正文，也不应监听 loopback 以外的网络地址。任何违反这些边界的行为都应按安全问题处理。

费用功能只使用程序内嵌的 Standard API 价格与本机 Token 事件，不读取 OpenAI 真实账单、账号配额或凭据，也不会在运行时联网抓取价格。定价覆写仅保存在本机 `config.json`，其写接口要求与当前服务端口一致的 loopback Origin；无 Origin 的非浏览器客户端仍可使用，浏览器标记为 cross-site 的请求会被拒绝。

## 可信发布门禁

当前 `install-policy.json` 保持 `binary_release_enabled=false`、`publisher_subject=null`；更新命令不联网、不下载、不修改。项目尚未获得 SignPath Foundation 书面批准，任何第三方二进制都不应被视为本项目发布。

未来只有 GitHub Actions 完整构建、SignPath Foundation 托管私钥并签署 Windows 资产、维护者完成变更复核、发布审批者核对版本说明/签名/签名后 SHA256/Attestation，且最终 Release 不可变时，才可能启用二进制渠道。任一门禁失败都必须保持 source-only。完整职责和顺序见 [CODE_SIGNING.md](CODE_SIGNING.md)。
