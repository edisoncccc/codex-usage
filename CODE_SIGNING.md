# Codex Usage Dashboard 代码签名政策

[简体中文](CODE_SIGNING.md) · [English](CODE_SIGNING.en.md)

本政策说明 `edisoncccc/codex-usage` 当前的 source-only 状态，以及未来在真实外部门禁满足后才能启用的二进制签名与发布职责。它不是已经完成签名或发布的声明。

## 1. 当前状态

- 项目尚未获得 SignPath Foundation 的书面批准。
- [`install-policy.json`](install-policy.json) 中 `binary_release_enabled=false`，`windows.publisher_subject=null`。
- 仓库没有已签名 Windows 程序、可信二进制 Release 或可供安装的 Linux 二进制。
- 当前 [Release workflow](.github/workflows/release.yml) 只是手动触发的 source-only 守卫，权限为只读，不能构建、上传或发布资产。
- 尚未配置 SignPath organization/project/policy ID、证书 Subject 或签名 secret。本仓库不会用占位值猜测这些身份。

因此，任何声称来自当前项目的已签名 EXE 或二进制下载都不属于本项目认可的发布链。当前唯一受支持的安装方式是用户明确同意后按 [INSTALL.md](INSTALL.md) 从规范仓库源码构建。

## 2. 适用范围与目标

未来可信发布若获批准，覆盖四个最终资产：Windows `amd64`/`arm64` 与 Linux `amd64`/`arm64`。GitHub 不可变 Release 是唯一二进制真源；包管理器以后只能引用同一 Release，不能重新打包或成为版本真源。

Windows 资产必须通过 SignPath Foundation 提供的公有信任 Authenticode 签名和有效时间戳。四个平台最终资产都必须有签名后 SHA256、GitHub Release API asset digest 和 GitHub Artifact Attestation。任何“未检查”都必须如实标为 `not_checked`，不能写成已验证。

## 3. 信任与角色

以下是未来职责定义，不表示已经指定具体个人、SignPath 身份或项目 ID。

### 3.1 GitHub Actions

只有经过复核的 GitHub Actions Release workflow 可以在规范仓库中基于明确的 `vX.Y.Z` 标签构建四个平台资产。workflow 必须在干净提交上运行完整 Go、Playwright、生命周期、隐私和发布门禁；普通 CI 不上传二进制 artifact。

### 3.2 SignPath Foundation

SignPath Foundation 在其平台内托管签名私钥并对两个最终 Windows EXE 执行 Authenticode 签名与时间戳。维护者不得下载、导出或在 GitHub secrets 中保存该私钥。SignPath 的实际发布者 Subject 只能来自获批后的权威结果。

### 3.3 维护者

维护者复核应用源码、依赖、构建脚本、Release workflow、安装策略和双语文档变更；确认提交来源、测试证据、Fork/MIT 归属和隐私边界。任何会启用二进制渠道或改变 publisher 的提交都必须单独审查，不能与无关功能混合。

### 3.4 发布审批者

发布审批者在发布前核对标签指向、版本说明、四个资产、Windows 签名/时间戳/实际 publisher、签名后 SHA256、Release API digest、Attestation 的仓库/工作流/标签/commit，以及 Draft Release 的完整资产清单。只有全部一致，才能批准转为不可变 Release。

## 4. 未来发布门禁

启用二进制渠道前必须同时满足：

1. 用户另行授权对 SignPath Foundation 提交外部申请。
2. SignPath 对本公开 MIT Fork 给出明确书面批准，并返回可验证的真实 publisher 身份。
3. 真实 organization/project/policy 信息和权限通过私密配置完成，仓库不提交私钥或凭据。
4. 测试签名和非发布演练证明两个 Windows 架构的 Authenticode、时间戳与 publisher 验证通过。
5. 经过复核的提交同时更新本政策、安装策略和 Release workflow；不能先启用下载再补文档。
6. 正式版本说明 `docs/releases/vX.Y.Z.md` 已人工复核，所有测试和供应链负面测试通过。

在这些条件全部完成前，`binary_release_enabled` 必须保持 `false`，Release workflow 必须保持不可发布。

## 5. 签名、哈希与 Attestation 顺序

正式发布顺序固定为：

```text
v* 标签指向确定提交
→ 完整测试、隐私和发布门禁
→ GitHub Actions 构建 Windows/Linux amd64/arm64
→ SignPath 签署两个 Windows EXE 并加时间戳
→ 验证 Authenticode、时间戳和实际 publisher
→ 对四个最终文件重新计算 SHA256
→ 为四个最终文件生成 GitHub Artifact Attestation
→ 创建 Draft Release 并附经复核的版本说明
→ 上传最终资产、SHA256SUMS 和所需供应链材料
→ 读取并核对 Release API asset digest，以及完整资产清单、签名、Attestation 和版本说明
→ 发布审批者批准并发布为 Immutable Release
```

签名会改变 EXE 字节，所以 Windows SHA256 必须在签名之后重新计算。`SHA256SUMS` 和 Attestation 必须描述即将上传的最终字节，不能沿用签名前结果；Release API asset digest 只有资产上传到 Draft 后才能读取和核对。最终复核必须同时覆盖 Draft 中的完整资产清单、签名、哈希、Attestation 和版本说明。Attestation 必须绑定规范仓库、受审 workflow、同一标签和同一 commit。

## 6. 失败处理

SignPath 申请被拒绝、条件不满足、签名失败、时间戳失效、publisher 不符、哈希不一致、Attestation 错误、测试失败或版本说明缺失时：

- 不创建或发布正式二进制 Release；
- 不发布 Linux-only 或未签名 Windows 的折中版本；
- 不把 Draft、普通 CI artifact 或第三方镜像当作安装源；
- 不购买商业证书；
- 保持 source-only，并让 `update` 继续返回 `release_channel_disabled`；
- 保存脱敏日志和门禁结果，修复后重新从干净提交开始完整流程。

## 7. 变更控制与安全报告

对发布权限、签名身份、workflow、哈希顺序、Attestation 或 `install-policy.json` 的修改必须经过维护者和发布审批职责的明确复核，并通过 `internal/installpolicy` 文档/策略契约测试。不得在 Issue、PR、日志或文档中提交私钥、Token、SignPath/GitHub secret 或未公开身份资料。

发现伪造签名、publisher 异常、Release 资产被替换、摘要或 Attestation 不一致、签名凭据疑似泄露时，不要公开披露可利用细节。请按 [SECURITY.md](SECURITY.md) 使用 GitHub 私密漏洞报告入口并提供最小化、已脱敏的证据。
